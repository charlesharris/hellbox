package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hellbox/internal/config"
	"hellbox/internal/decrypt"
	"hellbox/internal/disc"
	"hellbox/internal/drive"
	"hellbox/internal/makemkv"
	"hellbox/internal/store"
	"hellbox/internal/transcode"
)

// Worker owns exactly one optical drive: its device, its polling, and any rip
// running on it. Nothing else in the daemon touches the drive, which is what
// makes running several drives concurrently safe without a global lock.
type Worker struct {
	drv     drive.Drive
	label   string
	cfg     config.Config
	st      *store.Store
	mk      *makemkv.Runner
	dec     *decrypt.Decrypter
	enc     *transcode.Encoder
	status  *driveStatus
	driveID int64

	log func(level, message string, driveID *int64, jobID *int64)

	// onRipped is called once a disc is completely ripped and verified. It is a
	// callback rather than a direct call into the daemon so a worker stays
	// testable without one, and so what happens next — today, queueing a
	// transcode — is the daemon's business rather than the drive's.
	onRipped func(ctx context.Context, discID int64, ripDir string, titles []disc.Title)

	// onBluRayDone files a Blu-ray's single output. A Blu-ray produces no raw
	// rip and so never enters the transcode queue, so filing is handed on
	// directly rather than through it.
	onBluRayDone func(ctx context.Context, discID int64, titleIndex int, output string)

	// publish mirrors durable facts onto the HTTP event stream. Set after
	// construction rather than passed in, because NewWorker already carries
	// three callbacks and a fourth positional argument helps nobody.
	//
	// Nil is safe: a worker built by a test publishes nowhere.
	publish func(kind string, data any)

	// discKind asks the drive what kind of disc is loaded. A field rather than
	// a direct call so the routing it drives can be tested without a drive:
	// the Blu-ray path was written unreachable once and compiled perfectly.
	discKind func(devicePath string) (drive.DiscKind, uint16, error)

	// bluRay is the Blu-ray pipeline, indirected for the same reason.
	bluRay func(ctx context.Context)

	// discSlot is held for the whole of a disc's processing, and is shared by
	// every worker. Only one disc is worked on at a time across all drives: a
	// rip is bound by its drive and a decrypt by its disc, but both share one
	// set of disks with the transcode queue, and running two at once makes each
	// slower without finishing any sooner.
	discSlot chan struct{}

	// cancelWork aborts whatever the drive is doing — a scan, a decrypt, or a
	// rip. It was previously set only once ripping began, which left a scan
	// that hung with no way to stop it short of ejecting the disc or
	// restarting the daemon.
	cancelWork context.CancelFunc
}

// NewWorker creates a worker for a drive.
func NewWorker(d drive.Drive, label string, cfg config.Config, st *store.Store, mk *makemkv.Runner,
	dec *decrypt.Decrypter, enc *transcode.Encoder, driveID int64, discSlot chan struct{},
	log func(level, message string, driveID, jobID *int64),
	onRipped func(ctx context.Context, discID int64, ripDir string, titles []disc.Title),
	onBluRayDone func(ctx context.Context, discID int64, titleIndex int, output string)) *Worker {

	if label == "" {
		label = d.StableID
	}
	model := strings.TrimSpace(d.Vendor + " " + d.Model)
	w := &Worker{
		drv:          d,
		label:        label,
		cfg:          cfg,
		st:           st,
		mk:           mk,
		dec:          dec,
		enc:          enc,
		driveID:      driveID,
		discSlot:     discSlot,
		status:       newDriveStatus(d.StableID, label, d.DevicePath, model),
		log:          log,
		onRipped:     onRipped,
		onBluRayDone: onBluRayDone,
		discKind:     drive.ReadDiscKind,
	}
	w.bluRay = w.handleBluRay
	return w
}

// Snapshot returns the drive's current externally visible state.
func (w *Worker) Snapshot() DriveSnapshot { return w.status.Snapshot() }

// StableID identifies the drive this worker owns.
func (w *Worker) StableID() string { return w.drv.StableID }

// Eject opens the tray. It refuses while a rip is running, because ejecting
// mid-rip produces a partial file that looks like a successful one.
func (w *Worker) Eject() error {
	if w.status.Snapshot().State.Busy() {
		return fmt.Errorf("%s is busy; cancel the rip first", w.label)
	}
	return w.drv.Eject()
}

// Cancel aborts a rip in progress.
func (w *Worker) Cancel() error {
	if w.cancelWork == nil {
		return fmt.Errorf("%s is not doing anything to cancel", w.label)
	}
	w.cancelWork()
	return nil
}

// Run watches the drive until ctx is cancelled.
//
// Disc handling runs inline rather than in its own goroutine. A drive can only
// do one thing at a time, so serialising here removes any possibility of two
// rips racing on one device. The monitor's event channel is buffered, and
// transitions only occur when the tray actually changes, so nothing is lost
// while a rip is underway.
func (w *Worker) Run(ctx context.Context) {
	mon := drive.NewMonitor(w.drv, w.cfg.PollInterval.Duration)
	events := mon.Run(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			w.handleEvent(ctx, ev)
		}
	}
}

func (w *Worker) handleEvent(ctx context.Context, ev drive.Event) {
	if ev.Err != nil {
		if w.status.Snapshot().State != StateAbsent {
			w.logf("warn", "%s is unreadable: %v", w.label, ev.Err)
		}
		w.status.setState(StateAbsent)
		return
	}

	switch ev.To {
	case drive.StatusTrayOpen:
		w.status.setState(StateTrayOpen)
	case drive.StatusNoDisc:
		w.status.setState(StateEmpty)
	case drive.StatusNotReady:
		w.status.setState(StateLoading)
	case drive.StatusIncompatible:
		w.handleIncompatible()
	case drive.StatusDiscOK:
		w.handleDisc(ctx)
	}
}

// afterCancel gives the bookkeeping that follows a cancellation a context of
// its own.
//
// Recording "cancelled" through the context that did the cancelling writes
// nothing at all: the query fails before it reaches the database, the job stays
// in whatever state it was, and AttemptsForDisc — which excludes cancelled jobs
// precisely so an interruption is free — counts it as a used attempt. Two
// restarts were enough to exhaust max_rip_attempts on a disc that had never
// failed at anything.
//
// The timeout is short because this runs while the daemon is shutting down as
// often as not, and a bookkeeping write must never be what holds up an exit.
func afterCancel(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// handleIncompatible reports a disc this drive cannot read.
//
// The tray stays shut. An open tray means the disc is done and safe to
// reshelve, and this one has not been read at all — handing it back would say
// the opposite of what happened. Nothing is retried either: no amount of
// re-reading turns a Blu-ray into something a DVD drive can open, and a person
// has to swap the disc before anything can proceed.
func (w *Worker) handleIncompatible() {
	const msg = "a disc is loaded that this drive cannot read; a Blu-ray in a DVD drive is the usual cause"
	w.status.update(func(s *DriveSnapshot) { s.Error = msg })
	w.status.setState(StateIncompatible)
	w.logf("warn", "%s: %s", w.label, msg)
}

// handleDisc takes a disc from insertion to an open tray.
func (w *Worker) handleDisc(ctx context.Context) {
	w.logf("info", "%s: disc detected", w.label)

	// One disc at a time across every drive. Waiting here rather than at the
	// rip means a second drive does not spend half an hour decrypting while the
	// first is mid-rip, competing for the same disks.
	if !w.takeDiscSlot(ctx) {
		return
	}
	defer w.releaseDiscSlot()

	// Which pipeline this disc goes down. A Blu-ray is decrypted in place by
	// libaacs and encoded straight from the drive; a DVD is decrypted to disk
	// and ripped. Asked of the drive rather than guessed from what is on the
	// disc, which would mean mounting it first.
	if kind, profile, err := w.discKind(w.drv.DevicePath); err != nil {
		w.logf("warn", "%s: could not read the disc type (%v); treating it as a DVD", w.label, err)
	} else if kind == drive.DiscBluRay {
		w.logf("info", "%s: Blu-ray (profile 0x%04x)", w.label, profile)
		w.bluRay(ctx)
		return
	}

	w.status.setState(StateScanning)

	// Cancellable from the moment there is something to cancel, rather than
	// from the moment ripping starts. Everything before that — the scan, and
	// the decrypt that can precede a rip — takes far longer than the rip does.
	ctx, cancel := context.WithCancel(ctx)
	w.cancelWork = cancel
	defer func() { w.cancelWork = nil; cancel() }()

	scan, err := w.scanDisc(ctx)
	if err != nil {
		if errors.Is(err, errDiscGone) {
			// The monitor's next event sets whatever the drive is now. Marking
			// it here would race with that and could leave a stale state.
			w.logf("info", "%s: the disc left before it could be read", w.label)
			return
		}
		// A cancelled scan is not a failed one. Reported as an error it read as
		// a bad disc, and "retained in the drive; the tray stays closed until
		// this is dealt with" said there was something to deal with when a
		// person had just asked for the scan to stop.
		if errors.Is(err, errCancelled) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			w.status.setState(StateCancelled)
			w.logf("info", "%s: cancelled before the disc was read; it is untouched", w.label)
			return
		}
		w.fail(ctx, 0, "scan failed: %v", err)
		return
	}

	d := scan.Disc
	w.status.update(func(s *DriveSnapshot) {
		s.DiscLabel = d.VolumeLabel
		s.Fingerprint = d.Fingerprint
		s.TitleCount = len(d.Titles)
	})
	w.logf("info", "%s: %q — %d titles, %s, %s",
		w.label, displayLabel(d), len(d.Titles), humanBytes(d.TotalBytes), d.TotalDuration().Round(time.Second))

	// Published before anything is ripped, and deliberately so. A disc that
	// cannot be read is still worth cataloguing completely, and enumeration
	// needs no key on either format — so the catalog learns what a BD+ Blu-ray
	// holds even though nothing can decrypt it.
	w.publishDisc("enumerated", d, "", false, "")

	// Recognising an already-ripped disc before doing any work is the whole
	// point of the fingerprint: an unattended operator who loses track of which
	// discs are done gets a twenty-second answer instead of a wasted hour.
	existing, err := w.st.FindDisc(ctx, d.Fingerprint)
	if err != nil {
		w.fail(ctx, 0, "could not check for a previous rip: %v", err)
		return
	}
	if existing != nil && existing.Ripped() {
		w.status.update(func(s *DriveSnapshot) { s.RipDir = existing.RipDir })
		w.status.setState(StateDuplicate)
		w.logf("info", "%s: already ripped to %s — ejecting without re-reading", w.label, existing.RipDir)
		w.ejectNow()
		return
	}

	discID, err := w.st.SaveDisc(ctx, d, scan.Raw)
	if err != nil {
		w.fail(ctx, 0, "could not record disc: %v", err)
		return
	}

	attempts, err := w.st.AttemptsForDisc(ctx, discID)
	if err != nil {
		attempts = 0
	}
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

	ripped, err := w.rip(ctx, jobID, discID, d, scan.Raw)
	if err != nil {
		if errors.Is(err, errCancelled) {
			done, stop := afterCancel(ctx)
			_ = w.st.SetJobState(done, jobID, store.JobCancelled, "cancelled")
			stop()
			w.status.setState(StateCancelled)
			w.logf("info", "%s: cancelled — the disc is untouched; eject and reinsert to try again", w.label)
			return
		}
		_ = w.st.SetJobState(ctx, jobID, store.JobFailed, err.Error())
		w.fail(ctx, jobID, "%v", err)
		return
	}

	_ = w.st.SetJobState(ctx, jobID, store.JobComplete, "")
	w.status.setState(StateComplete)
	w.logf("info", "%s: complete — %q", w.label, displayLabel(d))

	// Published again now there is a rip directory to point at. The catalog
	// keys on fingerprint, so this updates the record enumeration created
	// rather than making a second one.
	w.publishDisc("ripped", d, w.status.Snapshot().RipDir, false, "")

	// Handed on before the tray opens, so the next disc can go in while the
	// first is still encoding.
	if w.onRipped != nil {
		w.onRipped(ctx, discID, w.status.Snapshot().RipDir, ripped.Titles)
	}
	w.ejectNow()
}

// takeDiscSlot waits for the shared slot, reporting false if the wait was
// interrupted. A worker with no slot configured proceeds immediately, which is
// what keeps a Worker usable in tests without one.
func (w *Worker) takeDiscSlot(ctx context.Context) bool {
	if w.discSlot == nil {
		return true
	}
	select {
	case w.discSlot <- struct{}{}:
		return true
	default:
	}

	// Something else has it. Say so, because a drive sitting at QUEUED with no
	// explanation looks like a drive that has hung.
	w.status.setState(StateQueued)
	w.logf("info", "%s: waiting for another drive to finish", w.label)
	select {
	case w.discSlot <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (w *Worker) releaseDiscSlot() {
	if w.discSlot == nil {
		return
	}
	select {
	case <-w.discSlot:
	default:
	}
}

// errCancelled reports that a person stopped the work. Like errDiscGone it is
// not a failure: nothing is wrong with the disc and nothing needs attention.
var errCancelled = errors.New("cancelled")

// errDiscGone reports that the disc left before it could be read. It is not a
// failure: nothing went wrong and nothing needs a person, so the drive returns
// to rest rather than to FAILED.
var errDiscGone = errors.New("the disc was removed before it could be read")

// discGone reports whether the drive no longer holds a readable disc.
func (w *Worker) discGone() (bool, error) {
	st, err := w.drv.Status()
	if err != nil {
		return false, err
	}
	return st != drive.StatusDiscOK, nil
}

// rescanCopy reads the titles from a decrypted copy, keeping the disc's
// identity from the drive.
//
// The copy is the authority on what is on the disc: it is a complete image,
// where the drive's own read may be partial. The volume label and fingerprint
// stay as the drive reported them, because a folder source carries no label and
// the fingerprint has to keep identifying the same disc across rips.
func (w *Worker) rescanCopy(ctx context.Context, src makemkv.Source, d disc.Disc) (disc.Disc, string, error) {
	res, err := w.mk.Scan(ctx, src, w.cfg.MinTitleSeconds)
	if err != nil {
		return d, "", err
	}
	if len(res.Disc.Titles) == 0 {
		return d, "", fmt.Errorf("the decrypted copy reported no titles")
	}

	out := d
	out.Titles = res.Disc.Titles
	out.TotalBytes = res.Disc.TotalBytes
	return out, res.Raw, nil
}

// scanDisc scans the disc in this drive.
func (w *Worker) scanDisc(ctx context.Context) (*makemkv.ScanResult, error) {
	return w.scanWithRetry(ctx, w.discGone, func() (*makemkv.ScanResult, error) {
		// The retry wrapper is kept for both paths. It exists because a drive
		// answers TEST UNIT READY before MakeMKV can open the disc; whether
		// libdvdread needs the same grace is unknown, and a retry costs nothing
		// on a scan that succeeds first time.
		if w.cfg.NativeDVD {
			attemptCtx := ctx
			if w.cfg.ScanTimeout.Duration > 0 {
				var cancel context.CancelFunc
				attemptCtx, cancel = context.WithTimeout(ctx, w.cfg.ScanTimeout.Duration)
				defer cancel()
			}
			res, err := w.nativeScan(attemptCtx)
			if err != nil && attemptCtx.Err() != nil && ctx.Err() == nil {
				return nil, fmt.Errorf("scan did not finish within %s", w.cfg.ScanTimeout)
			}
			return res, err
		}

		// Each attempt gets its own deadline. Without one a scan that hangs
		// hangs forever, and since the attempt never returns the retries never
		// come round either — the drive simply stops, mid-scan, indefinitely.
		attemptCtx := ctx
		if w.cfg.ScanTimeout.Duration > 0 {
			var cancel context.CancelFunc
			attemptCtx, cancel = context.WithTimeout(ctx, w.cfg.ScanTimeout.Duration)
			defer cancel()
		}
		// Always scanned from the drive, never from a decrypted copy: a folder
		// source reports no volume label, and both the fingerprint and the rip
		// directory name derive from it.
		res, err := w.mk.Scan(attemptCtx, makemkv.DeviceSource(w.drv.DevicePath), w.cfg.MinTitleSeconds)
		if err != nil && attemptCtx.Err() != nil && ctx.Err() == nil {
			// The deadline, not the caller. Reported as its own thing so a
			// retry still happens and the operator is told the scan was stopped
			// rather than that the disc failed.
			return nil, fmt.Errorf("scan did not finish within %s", w.cfg.ScanTimeout)
		}
		return res, err
	})
}

// scanWithRetry runs scan until it succeeds or the attempts are used up.
//
// A drive answers TEST UNIT READY before MakeMKV can necessarily open the disc,
// so the first scan after insertion can fail on a disc that reads perfectly
// moments later. Treating that first failure as final retained the disc in a
// closed drive waiting for a person, which is the one thing the daemon is meant
// never to need.
//
// Retries are not conditioned on the error text. MakeMKV's messages are
// informal and have changed between releases, so matching on them would rot
// quietly; a bounded retry instead costs a genuinely unreadable disc a further
// ScanAttempts-1 delays before it is given up on, and gets that decision right
// without depending on wording.
func (w *Worker) scanWithRetry(ctx context.Context, gone func() (bool, error),
	scan func() (*makemkv.ScanResult, error)) (*makemkv.ScanResult, error) {

	var lastErr error
	for attempt := 1; attempt <= w.cfg.ScanAttempts; attempt++ {
		res, err := scan()
		if err == nil {
			if attempt > 1 {
				w.logf("info", "%s: scan succeeded on attempt %d of %d",
					w.label, attempt, w.cfg.ScanAttempts)
			}
			return res, nil
		}
		lastErr = err

		// A cancelled context means shutdown or an ejected disc. Neither is a
		// scan worth repeating, and neither is a failure: reported as one, a
		// daemon restarted mid-scan left the drive claiming the disc was
		// "retained in the drive" and needed dealing with.
		if ctx.Err() != nil {
			return nil, errCancelled
		}

		// A disc that has left is not a disc that failed. Without this the
		// drive was asked three times about a disc it had already reported
		// gone, and then told the operator it was "retained in the drive" with
		// the tray open and nothing in it.
		//
		// The drive is asked rather than the error text read: the same SCSI
		// status the detector already trusts, rather than matching on messages
		// MakeMKV rewords between releases.
		if gone != nil {
			if left, err := gone(); left {
				return nil, errDiscGone
			} else if err != nil {
				w.logf("warn", "%s: could not re-check the drive between scans: %v", w.label, err)
			}
		}

		if attempt == w.cfg.ScanAttempts {
			break
		}

		w.logf("warn", "%s: scan attempt %d of %d failed: %v; retrying in %s",
			w.label, attempt, w.cfg.ScanAttempts, err, w.cfg.ScanRetryDelay)
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(w.cfg.ScanRetryDelay.Duration):
		}
	}
	if w.cfg.ScanAttempts > 1 {
		return nil, fmt.Errorf("%w (%d attempts)", lastErr, w.cfg.ScanAttempts)
	}
	return nil, lastErr
}

// rip writes every title, verifies each, and records the disc as ripped.
// rip writes every title of a disc, returning the titles it actually used.
//
// The return matters because a decrypted copy can disagree with the drive about
// what is on the disc, and the rescan that resolves that happens in here. d
// arrives by value, so an updated title list died at this function's edge: the
// caller went on to queue transcodes for the drive's four titles when the copy
// held one, and three encodes then failed on files that had never been written.
func (w *Worker) rip(ctx context.Context, jobID, discID int64, d disc.Disc, rawInfo string) (disc.Disc, error) {
	ripDir, err := w.makeRipDir(d)
	if err != nil {
		return d, err
	}
	w.status.update(func(s *DriveSnapshot) { s.RipDir = ripDir })

	ripLog, err := os.Create(filepath.Join(ripDir, "rip.log"))
	if err != nil {
		return d, fmt.Errorf("create rip.log: %w", err)
	}
	defer ripLog.Close()

	ripCtx := ctx

	// Where the titles are read from. Normally the drive; a disc the drive
	// cannot decrypt is copied to disk first and read from there.
	src, discard, err := w.ripSource(ripCtx, ripLog, d)
	if err != nil {
		return d, err
	}

	// A decrypted copy is rescanned, and what it says wins.
	//
	// MakeMKV numbers titles within its own filtered list, so a scan of the
	// drive and a rip from a copy only agree while both see the same titles.
	// They do not always: a drive that cannot decrypt a disc may offer only
	// part of it, and one here offered a single three-minute trailer for a disc
	// whose copy holds a two-hour-fifty-three feature as well. Ripping index 0
	// then meant the trailer to the scan and the feature to the copy — it wrote
	// the right file for the wrong reason, and recorded the trailer's runtime
	// and size against it.
	//
	// Identity stays with the drive: a folder source reports no volume label,
	// and the fingerprint has to keep meaning the same disc.
	if src.IsFolder() {
		if rescanned, raw, err := w.rescanCopy(ripCtx, src, d); err != nil {
			w.logf("warn", "%s: could not rescan the decrypted copy (%v); "+
				"ripping what the drive reported", w.label, err)
		} else {
			// Refused when the copy holds substantially less of the disc than
			// the drive described.
			//
			// Counting titles is the obvious test and it is wrong. National
			// Treasure's drive reported 21 titles including its feature twice,
			// where the copy reported 16 with the duplicate resolved and three
			// short extras the drive had never mentioned. The lists are not
			// subsets of one another, and refusing on the count would refuse a
			// disc whose copy was the better description.
			//
			// Running time is the test that separates them. That same disc
			// keeps 92% of its content in the copy; the Star Trek disc that
			// lost three of its four episodes kept 25%. Duplicate durations are
			// collapsed on the drive's side first, so a feature listed twice is
			// not counted as content the copy is missing.
			if kept, want := totalSecs(rescanned.Titles), dedupedSecs(d.Titles); want > 0 && kept*100 < want*minKeptPercent {
				return d, fmt.Errorf(
					"the decrypted copy holds %s of the %s the drive described (%d%%); "+
						"refusing to rip a partial disc",
					durationOf(kept), durationOf(want), kept*100/want)
			}
			if len(rescanned.Titles) != len(d.Titles) {
				w.logf("warn", "%s: the drive offered %d title%s but the decrypted copy holds %d; "+
					"using the copy", w.label, len(d.Titles), plural(len(d.Titles)), len(rescanned.Titles))
			}
			d.Titles = rescanned.Titles
			rawInfo = raw
			w.status.update(func(s *DriveSnapshot) { s.TitleCount = len(d.Titles) })
			if _, err := w.st.SaveDisc(ctx, d, rawInfo); err != nil {
				w.logf("warn", "%s: could not record the rescanned titles: %v", w.label, err)
			}
		}
	}

	// Written before ripping a single byte, and only now that the titles are
	// the ones actually about to be ripped. If the rip is interrupted, what the
	// disc contained is still on disk, which is what lets later phases work
	// without the disc.
	if err := writeDiscJSON(ripDir, d); err != nil {
		return d, err
	}
	if err := os.WriteFile(filepath.Join(ripDir, scanFileName(w.cfg.NativeDVD)), []byte(rawInfo), 0o644); err != nil {
		return d, fmt.Errorf("write makemkv-info.txt: %w", err)
	}

	var totalSecs int
	for _, t := range d.Titles {
		totalSecs += t.DurationSecs
	}

	w.status.setState(StateRipping)
	w.status.beginRip(totalSecs)
	_ = w.st.SetJobProgress(ctx, jobID, 0, len(d.Titles))
	_ = w.st.SetJobState(ctx, jobID, store.JobRipping, "")

	for i, t := range d.Titles {
		w.status.setProgress(t.Index, 0, fmt.Sprintf("title %d of %d", i+1, len(d.Titles)))

		onProgress := func(p makemkv.RipProgress) {
			w.status.setProgress(p.TitleIndex, p.Fraction, p.Operation)
		}
		var res *makemkv.RipResult
		var err error
		if w.cfg.NativeDVD {
			res, err = w.nativeRipTitle(ripCtx, t, ripDir, onProgress)
		} else {
			res, err = w.mk.RipTitle(ripCtx, src, t.Index, ripDir, w.cfg.MinTitleSeconds, onProgress)
		}
		if res != nil {
			fmt.Fprintf(ripLog, "===== title %d (%s) =====\n%s\n", t.Index, t.Duration(), res.Raw)
		}
		if err != nil {
			if ripCtx.Err() != nil {
				done, stop := afterCancel(ctx)
				_ = w.st.SetJobState(done, jobID, store.JobCancelled, "cancelled")
				stop()
				return d, fmt.Errorf("cancelled during title %d", t.Index)
			}
			return d, fmt.Errorf("title %d: %w", t.Index, err)
		}

		w.status.setState(StateVerifying)
		if err := verifyOutput(res.Path, w.cfg.MinOutputBytes); err != nil {
			return d, fmt.Errorf("title %d failed verification: %w", t.Index, err)
		}
		// The native path also cross-checks the measured duration against what
		// enumeration claimed. That is the check that catches a rip which is
		// present, large, structurally valid Matroska, and three-quarters
		// missing -- the failure that silently cost three episodes under v1.
		if w.cfg.NativeDVD {
			if err := w.verifyNativeTitle(ctx, res.Path, t); err != nil {
				return d, fmt.Errorf("title %d failed verification: %w", t.Index, err)
			}
		}
		w.status.setState(StateRipping)

		if err := w.st.MarkTitleRipped(ctx, discID, t.Index, res.SizeBytes, true); err != nil {
			return d, fmt.Errorf("record title %d: %w", t.Index, err)
		}

		w.status.titleDone(t.DurationSecs)
		_ = w.st.SetJobProgress(ctx, jobID, i+1, len(d.Titles))
		w.logf("info", "%s: title %d of %d written (%s in %s)",
			w.label, i+1, len(d.Titles), humanBytes(res.SizeBytes), res.Elapsed.Round(time.Second))
	}

	// The disc is only marked ripped once every title is written and verified.
	// A partially ripped disc must not be recognised as done on reinsertion.
	if err := w.st.SetDiscRipDir(ctx, discID, ripDir); err != nil {
		return d, fmt.Errorf("record rip directory: %w", err)
	}

	// Only now is the decrypted copy scratch. Until the rip is recorded it is
	// the expensive half of a retry.
	discard()
	return d, nil
}

// minKeptPercent is how much of the disc's running time a decrypted copy has to
// account for before it is trusted.
//
// Set from the two discs that bracket the problem: a copy that corrected a
// duplicated feature kept 92%, and one that silently dropped three episodes of
// four kept 25%. Anywhere in between is a guess, so it sits nearer the failure
// than the success — a refused disc is retained and can be looked at, where one
// ejected as done cannot.
const minKeptPercent = 60

// totalSecs is the running time of a title list.
func totalSecs(titles []disc.Title) int {
	var n int
	for _, t := range titles {
		n += t.DurationSecs
	}
	return n
}

// dedupedSecs is the running time of a title list counting each distinct
// duration once.
//
// A DVD that lists its feature twice — which drives that cannot decrypt the
// disc do — would otherwise make the copy look like it had lost half the disc.
func dedupedSecs(titles []disc.Title) int {
	seen := map[int]bool{}
	var n int
	for _, t := range titles {
		if seen[t.DurationSecs] {
			continue
		}
		seen[t.DurationSecs] = true
		n += t.DurationSecs
	}
	return n
}

// durationOf renders seconds for a message.
func durationOf(secs int) string {
	return (time.Duration(secs) * time.Second).String()
}

// ripSource decides where titles are read from, decrypting the disc to disk
// first when the drive cannot decrypt it itself.
//
// The returned discard removes the decrypted copy and is called only once the
// rip has succeeded. A failed rip deliberately leaves the copy behind: it costs
// half an hour to produce, the disc is still the same disc, and re-reading it
// is what every part of hellbox exists to avoid. The next attempt reuses it.
func (w *Worker) ripSource(ctx context.Context, ripLog io.Writer, d disc.Disc) (makemkv.Source, func(), error) {
	fromDrive := makemkv.DeviceSource(w.drv.DevicePath)
	noDiscard := func() {}

	// The native path reads the disc in place whatever the drive's region says.
	// libdvdcss derives a title key from the disc itself when the drive refuses
	// to hand one over, which was verified end to end against a region 1/3/4
	// CSS disc in an RPC-2 drive with no region set.
	//
	// This is the reversal that matters most in the swap. v1 sent every
	// region-blocked disc through a half-hour dvdbackup copy it did not need,
	// because MakeMKV could not read those discs. The copy is now for discs
	// that actually fail, not discs that look like they will.
	if w.cfg.NativeDVD {
		if reason, needed := w.decryptNeeded(); needed {
			w.logf("info", "%s: %s — reading it in place anyway, libdvdcss does not need the drive's key",
				w.label, reason)
		}
		return fromDrive, noDiscard, nil
	}

	reason, needed := w.decryptNeeded()
	if !needed {
		return fromDrive, noDiscard, nil
	}
	if !w.cfg.DecryptFallback {
		return "", noDiscard, fmt.Errorf("%s, and decrypt_fallback is off", reason)
	}
	if err := w.dec.Available(); err != nil {
		return "", noDiscard, fmt.Errorf("%s, and the disc cannot be decrypted to disk either: %w", reason, err)
	}

	// Keyed by fingerprint so the copy belongs to this disc and no other, and
	// so a retry finds the one the last attempt made.
	key := disc.Short(d.Fingerprint)

	discard := func(res *decrypt.Result) func() {
		return func() {
			if err := decrypt.Discard(res); err != nil {
				w.logf("warn", "%s: could not remove the decrypted copy at %s: %v", w.label, res.Root, err)
			}
		}
	}

	if res, ok := decrypt.Existing(w.cfg.WorkDir, key); ok {
		w.logf("info", "%s: reusing the %s already decrypted for this disc", w.label, humanBytes(res.Bytes))
		return makemkv.FolderSource(res.Folder), discard(res), nil
	}

	w.logf("info", "%s: %s — copying it to disk to decrypt it first", w.label, reason)
	w.status.setState(StateDecrypting)
	w.status.setProgress(0, 0, "decrypting")

	res, err := w.dec.Mirror(ctx, w.drv.DevicePath, w.cfg.WorkDir, key, func(p decrypt.Progress) {
		w.status.setProgress(0, p.Fraction, p.Operation)
	})
	if err != nil {
		// A cancelled decrypt is a person stopping it, not a disc that failed.
		// Reported as a failure it retained a perfectly good disc and told the
		// operator to deal with it.
		if ctx.Err() != nil {
			return "", noDiscard, errCancelled
		}
		return "", noDiscard, fmt.Errorf("decrypting the disc to disk: %w", err)
	}

	fmt.Fprintf(ripLog, "===== decrypt =====\n%s\n", res.Raw)
	w.logf("info", "%s: decrypted %s to disk", w.label, humanBytes(res.Bytes))

	return makemkv.FolderSource(res.Folder), discard(res), nil
}

// decryptNeeded reports whether the drive can read this disc's encrypted
// content, and if not, why.
//
// It fails open. A drive that will not answer these questions is more likely to
// be an unusual drive than a region-locked one, and refusing to rip on a failed
// diagnostic would be worse than trying — the stall watchdog is the backstop
// either way.
func (w *Worker) decryptNeeded() (string, bool) {
	region, err := drive.ReadRegion(w.drv.DevicePath)
	if err != nil {
		w.logf("warn", "%s: could not read the drive's region (%v); ripping from the drive", w.label, err)
		return "", false
	}
	prot, err := drive.ReadProtection(w.drv.DevicePath)
	if err != nil {
		w.logf("warn", "%s: could not read the disc's protection (%v); ripping from the drive", w.label, err)
		return "", false
	}
	if prot.PlayableIn(region) {
		return "", false
	}
	return fmt.Sprintf("this disc is %s and the drive is %s", prot, region), true
}

// makeRipDir creates the disc's directory, keeping the name unique if one
// already exists from a previous attempt.
func (w *Worker) makeRipDir(d disc.Disc) (string, error) {
	base := filepath.Join(w.cfg.RipsDir, d.DirName())
	candidate := base
	for i := 2; ; i++ {
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("create rip directory %s: %w", candidate, err)
		}
		if empty, _ := dirIsEmpty(candidate); empty {
			return candidate, nil
		}
		if i > 50 {
			return "", fmt.Errorf("too many existing rip directories for %s", base)
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func dirIsEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// writeDiscJSON writes the disc description atomically, so a crash cannot leave
// a half-written file that later phases would read as truth.
func writeDiscJSON(dir string, d disc.Disc) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("encode disc.json: %w", err)
	}
	tmp := filepath.Join(dir, ".disc.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write disc.json: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "disc.json")); err != nil {
		return fmt.Errorf("finalise disc.json: %w", err)
	}
	return nil
}

// fail records a failure and retains the disc.
//
// The tray deliberately stays closed. An open tray is the signal that a disc is
// done and safe to reshelve; opening it on failure would hand back a disc that
// did not rip, with nothing to distinguish it from one that did.
func (w *Worker) fail(ctx context.Context, jobID int64, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	w.status.update(func(s *DriveSnapshot) { s.Error = msg })
	w.status.setState(StateFailed)

	var jp *int64
	if jobID != 0 {
		jp = &jobID
	}
	w.log("error", fmt.Sprintf("%s: %s", w.label, msg), &w.driveID, jp)

	if w.cfg.EjectOnFailure {
		w.ejectNow()
	} else {
		w.logf("warn", "%s: disc retained in the drive; the tray stays closed until this is dealt with", w.label)
	}
	_ = ctx
}

// ejectNow opens the tray, if configured to.
func (w *Worker) ejectNow() {
	if !w.cfg.EjectOnSuccess {
		return
	}
	w.status.setState(StateEjecting)
	if err := w.drv.Eject(); err != nil {
		w.logf("warn", "%s: could not open the tray: %v", w.label, err)
		return
	}
	w.status.setState(StateTrayOpen)
}

func (w *Worker) logf(level, format string, args ...any) {
	w.log(level, fmt.Sprintf(format, args...), &w.driveID, nil)
}

// displayLabel gives a disc a readable name even when its volume label is
// missing or one of the generic strings authoring tools emit.
func displayLabel(d disc.Disc) string {
	if l := strings.TrimSpace(d.VolumeLabel); l != "" {
		return l
	}
	return "unlabelled disc " + disc.Short(d.Fingerprint)
}
