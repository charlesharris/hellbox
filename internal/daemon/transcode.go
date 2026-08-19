package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hellbox/internal/disc"
	"hellbox/internal/proto"
	"hellbox/internal/store"
	"hellbox/internal/transcode"
)

// maxTranscodeAttempts bounds how many times a title is encoded before it is
// left alone. Unlike a rip there is no disc to lose, so the only cost of giving
// up is a file that has to be re-queued by hand — and the only cost of not
// giving up is a queue that spins on a file that will never encode.
const maxTranscodeAttempts = 2

// transcodeStatus is what the socket reports about the queue.
type transcodeStatus struct {
	mu sync.RWMutex

	snap proto.TranscodeSnapshot
}

func (t *transcodeStatus) Snapshot() proto.TranscodeSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snap
}

func (t *transcodeStatus) update(fn func(*proto.TranscodeSnapshot)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&t.snap)
}

// clearCurrent forgets the job that just ended, leaving the queue counts alone.
func (t *transcodeStatus) clearCurrent() {
	t.update(func(s *proto.TranscodeSnapshot) {
		s.Disc, s.TitleIndex, s.Fraction, s.Speed, s.Hardware = "", 0, 0, 0, false
		s.Running = false
	})
}

// runTranscodes drains the transcode queue until ctx is cancelled.
//
// It runs in the daemon rather than in a drive's worker, which is the whole
// point: a transcode reads a file and needs no drive, so making a worker wait
// for one would hold the tray shut and stop the next disc going in while the
// GPU worked. The drive is free the moment the rip verifies.
//
// One job at a time. There is a single GPU and four cores, so a second
// concurrent encode would take roughly twice as long as each rather than adding
// throughput.
func (d *Daemon) runTranscodes(ctx context.Context) {
	if !d.cfg.Transcode {
		return
	}

	// A daemon stopped mid-encode leaves its job claimed, and nothing else would
	// ever release it.
	if n, err := d.st.ReclaimRunningTranscodes(ctx); err != nil {
		// Shutting down before the queue ever started is not a problem worth
		// reporting; it just means there was nothing to do yet.
		if ctx.Err() == nil {
			d.logEvent("warn", fmt.Sprintf("could not reclaim interrupted transcodes: %v", err), nil, nil)
		}
	} else if n > 0 {
		d.logEvent("info", fmt.Sprintf("%d transcode%s interrupted by a restart returned to the queue",
			n, plural(n)), nil, nil)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		// Drain everything available before waiting again, so a disc with seven
		// titles does not trickle through at one title per tick.
		for {
			job, err := d.st.NextTranscode(ctx, maxTranscodeAttempts)
			if err != nil {
				if ctx.Err() == nil {
					d.logEvent("warn", fmt.Sprintf("could not read the transcode queue: %v", err), nil, nil)
				}
				break
			}
			if job == nil {
				break
			}
			d.transcodeOne(ctx, job)
			if ctx.Err() != nil {
				return
			}
		}
		d.refreshTranscodeCounts(ctx)

		// Catch up on anything the library has no link for, including titles
		// encoded before filing existed.
		d.fileUnfiled(ctx)

		select {
		case <-ctx.Done():
			return
		case <-d.transcodeWake:
		case <-ticker.C:
		}
	}
}

// CancelTranscode stops the encode in flight.
func (d *Daemon) CancelTranscode() error {
	d.transcodeMu.Lock()
	cancel := d.cancelTranscode
	d.transcodeMu.Unlock()
	if cancel == nil {
		return fmt.Errorf("nothing is being transcoded")
	}
	cancel()
	return nil
}

// transcodeOne encodes a single title.
func (d *Daemon) transcodeOne(ctx context.Context, job *store.TranscodeJob) {
	out := d.transcodeOutputPath(job)
	label := job.VolumeLabel
	if label == "" {
		label = fmt.Sprintf("disc %d", job.DiscID)
	}

	d.transcodes.update(func(s *proto.TranscodeSnapshot) {
		s.Running, s.Disc, s.TitleIndex = true, label, job.TitleIndex
		s.Fraction, s.Speed = 0, 0
		s.Hardware = d.enc.Hardware()
	})
	defer d.transcodes.clearCurrent()

	// Held so the socket can stop this one encode without disturbing the queue
	// behind it.
	encCtx, cancel := context.WithCancel(ctx)
	d.transcodeMu.Lock()
	d.cancelTranscode = cancel
	d.transcodeMu.Unlock()
	defer func() {
		d.transcodeMu.Lock()
		d.cancelTranscode = nil
		d.transcodeMu.Unlock()
		cancel()
	}()

	// The file is probed rather than trusted to the database, because whether
	// it is interlaced decides how it is encoded and only the file knows.
	// Duration comes from the same read; the scan's figure is the fallback.
	src, err := d.enc.Probe(encCtx, job.SourcePath)
	if err != nil {
		d.logEvent("warn", fmt.Sprintf("could not probe %s: %v; encoding it as progressive",
			job.SourcePath, err), nil, nil)
		src = &transcode.Source{}
	}
	duration := src.Duration
	if duration <= 0 {
		if secs, err := d.st.TitleDuration(ctx, job.DiscID, job.TitleIndex); err == nil {
			duration = time.Duration(secs) * time.Second
		}
	}
	if src.Interlaced {
		d.logEvent("info", fmt.Sprintf("%s title %d is interlaced (%s) and will be deinterlaced",
			label, job.TitleIndex, src.FieldOrder), nil, nil)
	}

	res, err := d.enc.Transcode(encCtx, job.SourcePath, out,
		transcode.Profile{
			Name:         job.Profile,
			Quality:      d.cfg.TranscodeQuality,
			MaxKbps:      d.cfg.TranscodeMaxKbps,
			MaxHeight:    d.cfg.TranscodeMaxHeight,
			SourceHeight: src.Height,
			Streams:      src.Streams,
			Language:     d.cfg.PreferredLanguage,
			AudioKbps:    d.cfg.AudioKbps,
			Preset:       d.cfg.SoftwarePreset,
			Interlaced:   src.Interlaced,
		},
		duration,
		func(p transcode.Progress) {
			d.transcodes.update(func(s *proto.TranscodeSnapshot) {
				s.Fraction, s.Speed = p.Fraction, p.Speed
			})
		})

	if encCtx.Err() != nil {
		// Cancelled, by shutdown or by a person. Either way the job goes back to
		// the queue rather than counting an attempt against it: nothing has been
		// learned about whether it can be encoded.
		_ = d.st.RequeueTranscode(context.WithoutCancel(ctx), job.ID)
		if ctx.Err() == nil {
			d.logEvent("info", fmt.Sprintf("transcode of %s title %d cancelled; it stays in the queue",
				label, job.TitleIndex), nil, nil)
		}
		return
	}
	if err != nil {
		_ = d.st.FailTranscode(ctx, job.ID, err.Error())
		d.logEvent("error", fmt.Sprintf("transcode of %s title %d failed: %v",
			label, job.TitleIndex, err), nil, nil)
		return
	}

	if err := d.st.FinishTranscode(ctx, job.ID, res.Path, res.SizeBytes, res.Hardware); err != nil {
		d.logEvent("warn", fmt.Sprintf("could not record the transcode of %s title %d: %v",
			label, job.TitleIndex, err), nil, nil)
	}

	// Filed as soon as it exists, so a long disc appears in the library as it
	// goes rather than all at once at the end.
	d.fileTitle(ctx, job.DiscID, job.TitleIndex, res.Path)

	how := "software"
	if res.Hardware {
		how = "hardware"
	}
	d.logEvent("info", fmt.Sprintf("transcoded %s title %d — %s in %s (%s)",
		label, job.TitleIndex, humanBytes(res.SizeBytes), res.Elapsed.Round(time.Second), how), nil, nil)
	logStreams(func(level, format string, args ...any) {
		d.logEvent(level, fmt.Sprintf(format, args...), nil, nil)
	}, fmt.Sprintf("%s title %d", label, job.TitleIndex), res.Streams)
}

// transcodeOutputPath mirrors the rip's directory under transcoded_dir.
//
// Mirroring rather than inventing a name keeps the two trees walkable side by
// side, and leaves the disc's own directory name — which already carries date,
// label and fingerprint — as the thing that identifies both. Phase 3 renames
// for Jellyfin; until then the name says exactly what the file came from.
func (d *Daemon) transcodeOutputPath(job *store.TranscodeJob) string {
	return filepath.Join(d.cfg.TranscodedDir,
		filepath.Base(filepath.Dir(job.SourcePath)),
		disc.TitleFileName(job.TitleIndex))
}

// logStreams records which tracks reached the output and which did not.
//
// The choice was made and thrown away before, so a stereo file gave no way to
// tell whether a 5.1 track had been passed over or had never been on the disc.
// One line each, and only when there is something to say.
func logStreams(logf func(level, format string, args ...any), label string, sel transcode.Selection) {
	if len(sel.Kept) > 0 {
		logf("info", "%s: kept %s", label, strings.Join(sel.Kept, ", "))
	}
	if len(sel.Dropped) > 0 {
		logf("info", "%s: dropped %s", label, strings.Join(sel.Dropped, ", "))
	}
}

// queueTranscodes enqueues every verified title of a ripped disc.
func (d *Daemon) queueTranscodes(ctx context.Context, discID int64, ripDir string, titles []disc.Title) {
	if !d.cfg.Transcode {
		return
	}
	profile := "default"
	var queued int
	for _, t := range titles {
		src := filepath.Join(ripDir, disc.TitleFileName(t.Index))
		if err := d.st.QueueTranscode(ctx, discID, t.Index, profile, src); err != nil {
			d.logEvent("warn", fmt.Sprintf("could not queue title %d for transcoding: %v", t.Index, err), nil, nil)
			continue
		}
		queued++
	}
	if queued == 0 {
		return
	}
	d.logEvent("info", fmt.Sprintf("queued %d title%s for transcoding", queued, plural(queued)), nil, nil)
	d.refreshTranscodeCounts(ctx)
	d.nudgeTranscoder()
}

// requeueDisc queues every ripped title of a disc for transcoding again.
//
// This is what makes the raw rips worth keeping. A transcode redone after a
// settings change, or after a bug, costs minutes from a file already on disk —
// where the alternative is finding the disc and reading it again. The first
// television disc needed exactly this: six episodes encoded before it was clear
// that one quality setting could not serve both film and video.
func (d *Daemon) requeueDisc(ctx context.Context, fingerprint string) (int, string, error) {
	if !d.cfg.Transcode {
		return 0, "", fmt.Errorf("transcoding is off; set transcode = true to use this")
	}

	rec, err := d.st.FindDisc(ctx, fingerprint)
	if err != nil {
		return 0, "", err
	}
	if rec == nil {
		return 0, "", fmt.Errorf("no disc with fingerprint %s", fingerprint)
	}
	if !rec.Ripped() {
		return 0, "", fmt.Errorf("%s has no completed rip to transcode from", displayName(rec.VolumeLabel, fingerprint))
	}

	indices, err := d.st.RippedTitles(ctx, rec.ID)
	if err != nil {
		return 0, "", err
	}
	if len(indices) == 0 {
		return 0, "", fmt.Errorf("%s has no verified titles", displayName(rec.VolumeLabel, fingerprint))
	}

	var queued int
	for _, idx := range indices {
		src := filepath.Join(rec.RipDir, disc.TitleFileName(idx))

		// The rip is the source of truth, and it may have been moved or removed
		// since. Queuing a job for a file that is not there would fail later,
		// slowly, one title at a time.
		if _, err := os.Stat(src); err != nil {
			d.logEvent("warn", fmt.Sprintf("title %d of %s is missing from %s; skipping it",
				idx, displayName(rec.VolumeLabel, fingerprint), rec.RipDir), nil, nil)
			continue
		}
		if err := d.st.QueueTranscode(ctx, rec.ID, idx, "default", src); err != nil {
			return queued, "", err
		}
		queued++
	}
	if queued == 0 {
		return 0, "", fmt.Errorf("none of the titles of %s are still on disk under %s",
			displayName(rec.VolumeLabel, fingerprint), rec.RipDir)
	}

	name := displayName(rec.VolumeLabel, fingerprint)
	d.logEvent("info", fmt.Sprintf("queued %d title%s of %s for transcoding again",
		queued, plural(queued), name), nil, nil)
	d.refreshTranscodeCounts(ctx)
	d.nudgeTranscoder()
	return queued, name, nil
}

// displayName names a disc for a message, falling back to its fingerprint when
// the disc carried no label.
func displayName(label, fingerprint string) string {
	if l := strings.TrimSpace(label); l != "" {
		return l
	}
	return disc.Short(fingerprint)
}

// nudgeTranscoder wakes the runner without blocking if it is already awake.
func (d *Daemon) nudgeTranscoder() {
	select {
	case d.transcodeWake <- struct{}{}:
	default:
	}
}

func (d *Daemon) refreshTranscodeCounts(ctx context.Context) {
	pending, failed, err := d.st.TranscodeQueue(ctx, maxTranscodeAttempts)
	if err != nil {
		return
	}
	d.transcodes.update(func(s *proto.TranscodeSnapshot) {
		s.Pending, s.Failed = pending, failed
	})
}
