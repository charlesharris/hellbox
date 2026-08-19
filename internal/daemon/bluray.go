package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"hellbox/internal/disc"
	"hellbox/internal/drive"
	"hellbox/internal/store"
	"hellbox/internal/transcode"
)

// handleBluRay takes a Blu-ray from insertion to an open tray.
//
// It encodes straight from the disc, where a DVD is ripped to a raw file first.
// The reason is size: a DVD title is about a gigabyte and keeping it costs
// little, while a Blu-ray runs twenty-four megabits a second and its raw would
// be eighteen gigabytes — enough that a few dozen discs would fill the machine.
//
// That trades away the guarantee the rips tree gives everywhere else: a
// Blu-ray re-encoded under different settings has to be read again. It is a
// cheaper trade here than it looks, because Blu-ray has no region problem and
// so no decrypt step — reading one again costs minutes, where a DVD costs the
// forty-minute decrypt it took the first time.
//
// libaacs decrypts in place as ffmpeg reads, so there is no separate decrypt
// stage at all, and no MakeMKV: the key that gates Blu-ray in MakeMKV is not
// involved.
func (w *Worker) handleBluRay(ctx context.Context) {
	label, err := drive.VolumeLabel(ctx, w.drv.DevicePath)
	if err != nil {
		w.logf("warn", "%s: could not read the volume label: %v", w.label, err)
	}

	// libaacs decrypts in place as ffmpeg reads, so the drive is the input:
	// there is no copy to disk and no separate decrypt stage.
	src := "bluray:" + w.drv.DevicePath
	w.status.setState(StateScanning)

	probe, err := w.enc.Probe(ctx, src)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		w.fail(ctx, 0, "could not read the Blu-ray: %v", err)
		return
	}
	if probe.Duration <= 0 {
		w.fail(ctx, 0, "the Blu-ray reported no playable title; it may not be in the AACS key database")
		return
	}

	// One title: whatever libaacs considers the disc's main playlist. A disc of
	// episodes has more, and reaching them needs the playlist list, which needs
	// the disc mounted — so for now a Blu-ray yields its feature and says so.
	d := disc.Disc{
		VolumeLabel:    label,
		ScannedAt:      time.Now(),
		DriveModel:     w.drv.Describe(),
		Type:           disc.TypeBluRay,
		MakeMKVVersion: "",
		Titles: []disc.Title{{
			Index:        0,
			DurationSecs: int(probe.Duration.Seconds()),
			OutputFile:   disc.TitleFileName(0),
		}},
	}
	d.Fingerprint = disc.ComputeFingerprint(d.VolumeLabel, d.Titles)
	d.TotalBytes = 0

	w.status.update(func(s *DriveSnapshot) {
		s.DiscLabel = d.VolumeLabel
		s.Fingerprint = d.Fingerprint
		s.TitleCount = 1
	})
	w.logf("info", "%s: %q — Blu-ray, %s, %dx%d",
		w.label, displayLabel(d), probe.Duration.Round(time.Second), probe.Width, probe.Height)

	existing, err := w.st.FindDisc(ctx, d.Fingerprint)
	if err != nil {
		w.fail(ctx, 0, "could not check for a previous rip: %v", err)
		return
	}
	if existing != nil && existing.Ripped() {
		w.status.update(func(s *DriveSnapshot) { s.RipDir = existing.RipDir })
		w.status.setState(StateDuplicate)
		w.logf("info", "%s: already done — ejecting without re-reading", w.label)
		w.ejectNow()
		return
	}

	discID, err := w.st.SaveDisc(ctx, d, "")
	if err != nil {
		w.fail(ctx, 0, "could not record disc: %v", err)
		return
	}

	attempts, _ := w.st.AttemptsForDisc(ctx, discID)
	if attempts >= w.cfg.MaxRipAttempts {
		w.status.update(func(s *DriveSnapshot) { s.Attempt = attempts })
		w.fail(ctx, 0, "already failed %d times; not retrying automatically", attempts)
		return
	}
	jobID, err := w.st.CreateJob(ctx, discID, w.driveID, attempts+1)
	if err != nil {
		w.fail(ctx, 0, "could not open a job: %v", err)
		return
	}
	w.status.update(func(s *DriveSnapshot) { s.Attempt = attempts + 1 })

	out := filepath.Join(w.cfg.TranscodedDir, d.DirName(), disc.TitleFileName(0))
	res, err := w.encodeBluRay(ctx, src, out, probe)
	if err != nil {
		if errors.Is(err, errCancelled) {
			done, stop := afterCancel(ctx)
			_ = w.st.SetJobState(done, jobID, store.JobCancelled, "cancelled")
			stop()
			w.status.setState(StateCancelled)
			w.logf("info", "%s: cancelled — the disc is untouched", w.label)
			return
		}
		_ = w.st.SetJobState(ctx, jobID, store.JobFailed, err.Error())
		w.fail(ctx, jobID, "%v", err)
		return
	}

	// Recorded against the transcoded directory rather than a rips directory,
	// because for a Blu-ray there is no raw and this is the only copy.
	if err := w.st.MarkTitleRipped(ctx, discID, 0, 0, true); err != nil {
		w.logf("warn", "%s: could not record the title: %v", w.label, err)
	}
	if err := w.st.SetDiscRipDir(ctx, discID, filepath.Dir(out)); err != nil {
		w.logf("warn", "%s: could not record the output directory: %v", w.label, err)
	}
	_ = w.st.SetJobState(ctx, jobID, store.JobComplete, "")

	// Recorded even though it never went through the transcode queue, so the
	// reconciliation that files what the library is missing can see it. Without
	// this the single onBluRayDone call below is the only chance the disc ever
	// gets to be filed.
	if err := w.st.RecordTranscode(ctx, discID, 0, "default", src, out, res.SizeBytes, res.Hardware); err != nil {
		w.logf("warn", "%s: could not record the encode: %v", w.label, err)
	}

	w.status.setState(StateComplete)
	w.logf("info", "%s: complete — %q", w.label, displayLabel(d))

	if w.onBluRayDone != nil {
		w.onBluRayDone(ctx, discID, 0, out)
	}
	w.ejectNow()
}

// encodeBluRay runs the single pass from disc to library file.
func (w *Worker) encodeBluRay(ctx context.Context, src, out string, probe *transcode.Source) (*transcode.Result, error) {
	w.status.setState(StateRipping)
	w.status.beginRip(int(probe.Duration.Seconds()))

	res, err := w.enc.Transcode(ctx, src, out,
		transcode.Profile{
			Name:         "default",
			Quality:      w.cfg.TranscodeQuality,
			MaxKbps:      w.cfg.TranscodeMaxKbps,
			MaxHeight:    w.cfg.TranscodeMaxHeight,
			SourceHeight: probe.Height,
			Streams:      probe.Streams,
			Language:     w.cfg.PreferredLanguage,
			AudioKbps:    w.cfg.AudioKbps,
			Preset:       w.cfg.SoftwarePreset,
			Interlaced:   probe.Interlaced,
		},
		probe.Duration,
		func(p transcode.Progress) {
			w.status.setProgress(0, p.Fraction, fmt.Sprintf("%.0fx", p.Speed))
		})
	if err != nil {
		if ctx.Err() != nil {
			return nil, errCancelled
		}
		return nil, err
	}

	how := "software"
	if res.Hardware {
		how = "hardware"
	}
	w.logf("info", "%s: encoded %s in %s (%s)",
		w.label, humanBytes(res.SizeBytes), res.Elapsed.Round(time.Second), how)
	logStreams(w.logf, w.label, res.Streams)
	return res, nil
}
