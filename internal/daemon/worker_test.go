package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hellbox/internal/config"
	"hellbox/internal/decrypt"
	"hellbox/internal/disc"
	"hellbox/internal/drive"
	"hellbox/internal/makemkv"
	"hellbox/internal/store"
	"hellbox/internal/transcode"
)

// scanTestWorker builds a worker with no store and no real drive. Only the
// scan-retry policy is under test, so nothing here touches hardware.
func scanTestWorker(attempts int, delay time.Duration, log func(level, message string, driveID, jobID *int64)) *Worker {
	cfg := config.Default()
	cfg.ScanAttempts = attempts
	cfg.ScanRetryDelay = config.Duration{Duration: delay}

	if log == nil {
		log = discardLog
	}
	return NewWorker(
		drive.Drive{StableID: "usb-ASUS-1", DevicePath: "/dev/sr0", Vendor: "ASUS", Model: "SDRW-08D2S-U"},
		"top", cfg, (*store.Store)(nil), makemkv.New("/nonexistent", ""), decrypt.New("/nonexistent"), transcode.New("/nonexistent", ""), 0, nil, log, nil, nil)
}

// The case this exists for: a drive answers TEST UNIT READY before MakeMKV can
// open the disc, so the first scan fails on a disc that reads perfectly a
// moment later. Failing on that first error retained a good disc in a closed
// drive waiting for a person.
func TestScanRetriesUntilItSucceeds(t *testing.T) {
	w := scanTestWorker(3, time.Millisecond, nil)

	calls := 0
	want := &makemkv.ScanResult{Raw: "ok"}
	got, err := w.scanWithRetry(context.Background(), nil, func() (*makemkv.ScanResult, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("no titles found on /dev/sr0: Failed to open disc")
		}
		return want, nil
	})

	if err != nil {
		t.Fatalf("scanWithRetry returned %v, want success on the third attempt", err)
	}
	if got != want {
		t.Errorf("scanWithRetry returned %+v, want the successful scan", got)
	}
	if calls != 3 {
		t.Errorf("scanned %d times, want 3 — it must stop as soon as one succeeds", calls)
	}
}

// A disc that never reads has to be given up on. Retrying without a bound would
// hold the drive against a disc that is never going to work.
func TestScanStopsAfterTheConfiguredAttempts(t *testing.T) {
	w := scanTestWorker(3, time.Millisecond, nil)

	calls := 0
	_, err := w.scanWithRetry(context.Background(), nil, func() (*makemkv.ScanResult, error) {
		calls++
		return nil, errors.New("Failed to open disc")
	})

	if err == nil {
		t.Fatal("scanWithRetry succeeded on a scan that always failed")
	}
	if calls != 3 {
		t.Errorf("scanned %d times, want exactly 3", calls)
	}
	// The operator needs the drive's own reason, not just a count.
	if !strings.Contains(err.Error(), "Failed to open disc") {
		t.Errorf("error %q lost the underlying failure", err)
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("error %q does not say how many attempts were made", err)
	}
}

// scan_attempts = 1 is the old behaviour, and must not wait or add a count to
// an error that was never retried.
func TestScanAttemptsOneDoesNotRetry(t *testing.T) {
	w := scanTestWorker(1, time.Hour, nil)

	calls := 0
	start := time.Now()
	_, err := w.scanWithRetry(context.Background(), nil, func() (*makemkv.ScanResult, error) {
		calls++
		return nil, errors.New("Failed to open disc")
	})

	if calls != 1 {
		t.Errorf("scanned %d times, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %s before giving up; the delay must not apply to the last attempt", elapsed)
	}
	if got := err.Error(); got != "Failed to open disc" {
		t.Errorf("error = %q, want the bare failure with no attempt count", got)
	}
}

// Shutdown, or a disc pulled mid-scan, cancels the context. Sitting out the
// retry delay would stall the daemon's shutdown for no gain.
func TestScanStopsPromptlyWhenCancelled(t *testing.T) {
	w := scanTestWorker(5, time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan struct{})
	var err error

	go func() {
		defer close(done)
		_, err = w.scanWithRetry(ctx, nil, func() (*makemkv.ScanResult, error) {
			calls++
			cancel()
			return nil, errors.New("Failed to open disc")
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("scanWithRetry did not return after its context was cancelled")
	}

	if err == nil {
		t.Fatal("scanWithRetry succeeded despite cancellation")
	}
	if calls != 1 {
		t.Errorf("scanned %d times after cancellation, want 1", calls)
	}
}

// A retried scan is worth saying out loud: a disc that needed three attempts is
// a drive worth watching, and silence would hide that.
func TestScanLogsEachFailedAttempt(t *testing.T) {
	var logged []string
	w := scanTestWorker(3, time.Millisecond, func(level, message string, driveID, jobID *int64) {
		logged = append(logged, level+": "+message)
	})

	calls := 0
	if _, err := w.scanWithRetry(context.Background(), nil, func() (*makemkv.ScanResult, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("Failed to open disc")
		}
		return &makemkv.ScanResult{}, nil
	}); err != nil {
		t.Fatalf("scanWithRetry: %v", err)
	}

	var warns, infos int
	for _, l := range logged {
		if strings.HasPrefix(l, "warn: ") && strings.Contains(l, "scan attempt") {
			warns++
		}
		if strings.HasPrefix(l, "info: ") && strings.Contains(l, "scan succeeded on attempt 3") {
			infos++
		}
	}
	if warns != 2 {
		t.Errorf("logged %d retry warnings, want 2 (one per failed attempt): %v", warns, logged)
	}
	if infos != 1 {
		t.Errorf("did not report which attempt finally succeeded: %v", logged)
	}
}

// A disc that has left is not a disc that failed. Without this the drive was
// asked three times about a disc it had already reported gone, and then told
// the operator it was "retained in the drive" — with the tray open and nothing
// in it.
func TestScanAbandonsWhenTheDiscLeaves(t *testing.T) {
	w := scanTestWorker(3, time.Hour, nil)

	calls := 0
	_, err := w.scanWithRetry(context.Background(),
		func() (bool, error) { return true, nil },
		func() (*makemkv.ScanResult, error) {
			calls++
			return nil, errors.New("MEDIUM NOT PRESENT - TRAY OPEN")
		})

	if !errors.Is(err, errDiscGone) {
		t.Errorf("error = %v, want errDiscGone so the drive returns to rest", err)
	}
	if calls != 1 {
		t.Errorf("scanned %d times after the disc left, want 1", calls)
	}
}

// A drive that will not answer must not stop the retries: the scan might still
// succeed, and refusing to try on a failed diagnostic would be worse.
func TestScanKeepsRetryingWhenTheDriveWillNotAnswer(t *testing.T) {
	w := scanTestWorker(3, time.Millisecond, nil)

	calls := 0
	_, err := w.scanWithRetry(context.Background(),
		func() (bool, error) { return false, errors.New("no such device") },
		func() (*makemkv.ScanResult, error) {
			calls++
			return nil, errors.New("Failed to open disc")
		})

	if errors.Is(err, errDiscGone) {
		t.Error("an unreadable drive was treated as a disc that left")
	}
	if calls != 3 {
		t.Errorf("scanned %d times, want all 3 attempts", calls)
	}
}

// A disc still present goes on being retried, which is the ordinary spin-up case.
func TestScanRetriesWhileTheDiscIsStillThere(t *testing.T) {
	w := scanTestWorker(3, time.Millisecond, nil)

	calls := 0
	res, err := w.scanWithRetry(context.Background(),
		func() (bool, error) { return false, nil },
		func() (*makemkv.ScanResult, error) {
			calls++
			if calls < 3 {
				return nil, errors.New("Failed to open disc")
			}
			return &makemkv.ScanResult{}, nil
		})

	if err != nil || res == nil {
		t.Fatalf("scanWithRetry = %v, %v; want success on the third attempt", res, err)
	}
	if calls != 3 {
		t.Errorf("scanned %d times, want 3", calls)
	}
}

// Shutting down mid-scan is not a disc that failed. Reported as one, a daemon
// restarted during a scan left the drive insisting the disc was "retained in
// the drive" and needed dealing with, when nothing had happened to it at all.
func TestScanCancellationIsNotAFailure(t *testing.T) {
	w := scanTestWorker(3, time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := w.scanWithRetry(ctx, nil, func() (*makemkv.ScanResult, error) {
		cancel()
		return nil, errors.New("context canceled")
	})

	if !errors.Is(err, errCancelled) {
		t.Errorf("error = %v, want errCancelled so the disc is not reported as failed", err)
	}
}

// One disc at a time across every drive. A rip is bound by its drive and a
// decrypt by its disc, but both share one set of disks with the transcode
// queue, so two at once makes each slower without finishing any sooner.
func TestDiscSlotAdmitsOneWorkerAtATime(t *testing.T) {
	slot := make(chan struct{}, 1)
	a, b := scanTestWorker(1, 0, nil), scanTestWorker(1, 0, nil)
	a.discSlot, b.discSlot = slot, slot

	if !a.takeDiscSlot(context.Background()) {
		t.Fatal("the first worker could not take a free slot")
	}

	// The second must not get in while the first holds it.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if b.takeDiscSlot(ctx) {
		t.Error("two workers held the slot at once")
	}
	if got := b.Snapshot().State; got != StateQueued {
		t.Errorf("waiting drive shows %q, want %q so it does not look hung", got, StateQueued)
	}

	// Once the first is done the second gets in.
	a.releaseDiscSlot()
	if !b.takeDiscSlot(context.Background()) {
		t.Error("the slot was not released")
	}
}

// Releasing a slot that was never held must not block or panic, because the
// release runs from a defer on every path out of handling a disc.
func TestReleasingAnUnheldSlotIsSafe(t *testing.T) {
	w := scanTestWorker(1, 0, nil)
	w.discSlot = make(chan struct{}, 1)
	w.releaseDiscSlot()
	w.releaseDiscSlot()

	if !w.takeDiscSlot(context.Background()) {
		t.Error("the slot became unusable after spurious releases")
	}
}

// A worker with no slot runs unimpeded, which is what keeps one usable in a
// test — and on a machine with a single drive there is nothing to serialise.
func TestNoSlotMeansNoWaiting(t *testing.T) {
	w := scanTestWorker(1, 0, nil)
	if !w.takeDiscSlot(context.Background()) {
		t.Error("a worker with no slot was blocked")
	}
	w.releaseDiscSlot()
}

// The bug this exists for: the whole Blu-ray path was written, compiled, and
// committed while nothing called it. Go is happy to build an unreachable
// method, so the first sign was a Blu-ray being handed to MakeMKV — the one
// program the path exists to avoid.
func TestBluRayDiscsSkipTheDVDPipeline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   drive.DiscKind
		err    error
		wantBD bool
	}{
		{"blu-ray goes down the Blu-ray path", drive.DiscBluRay, nil, true},
		{"a DVD does not", drive.DiscDVD, nil, false},
		// A drive that will not answer must not silently take the Blu-ray path:
		// encoding a DVD from bluray: would fail after the disc was read.
		{"an unreadable answer falls back to DVD", drive.DiscUnknown, errors.New("no such device"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := scanTestWorker(1, time.Millisecond, nil)

			bluRayPath := false
			w.discKind = func(string) (drive.DiscKind, uint16, error) { return tc.kind, 0, tc.err }
			w.bluRay = func(context.Context) { bluRayPath = true }

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // stop the DVD path immediately; only the branch is under test
			w.handleDisc(ctx)

			if bluRayPath != tc.wantBD {
				t.Errorf("Blu-ray path taken = %v, want %v", bluRayPath, tc.wantBD)
			}
		})
	}
}

// The bug this exists for: a cancelled job was recorded through the very
// context whose cancellation was being recorded, so the write never reached
// the database. The job stayed as it was, AttemptsForDisc counted it — it only
// excludes jobs actually marked cancelled — and two interruptions exhausted
// max_rip_attempts on a disc that had never failed at anything.
func TestCancellingDoesNotUseUpAnAttempt(t *testing.T) {
	d := testDaemon(t)
	ctx := context.Background()

	discID, err := d.st.SaveDisc(ctx, disc.Disc{
		Fingerprint: "fp-cancel", VolumeLabel: "Hackers", Type: disc.TypeBluRay,
		Titles: []disc.Title{{Index: 0, DurationSecs: 6322}},
	}, "")
	if err != nil {
		t.Fatalf("SaveDisc: %v", err)
	}
	driveID, err := d.st.UpsertDrive(ctx, "usb-test-1", "/dev/sr1", "BD", "HL-DT-ST BD-RE BT10N")
	if err != nil {
		t.Fatalf("UpsertDrive: %v", err)
	}
	jobID, err := d.st.CreateJob(ctx, discID, driveID, 1)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// Exactly what a cancelled rip does: the work context is already dead by
	// the time the outcome is written.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	done, stop := afterCancel(cancelled)
	if err := d.st.SetJobState(done, jobID, store.JobCancelled, "cancelled"); err != nil {
		t.Fatalf("recording the cancellation failed: %v", err)
	}
	stop()

	got, err := d.st.AttemptsForDisc(ctx, discID)
	if err != nil {
		t.Fatalf("AttemptsForDisc: %v", err)
	}
	if got != 0 {
		t.Errorf("a cancelled job counted %d attempts, want 0 — an interruption is not a failure", got)
	}
}

// A cancelled scan was reported as "scan failed", at error level, followed by
// "the tray stays closed until this is dealt with" — telling a person who had
// just cancelled that there was something wrong with the disc. The sixth place
// an interruption was dressed up as a failure.
func TestACancelledScanIsNotAFailure(t *testing.T) {
	var logged []string
	w := scanTestWorker(1, time.Millisecond, func(level, message string, driveID, jobID *int64) {
		logged = append(logged, level+" "+message)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := w.scanWithRetry(ctx, nil, func() (*makemkv.ScanResult, error) {
		return nil, context.Canceled
	})
	if err == nil {
		t.Fatal("a cancelled scan returned no error")
	}
	if !errors.Is(err, errCancelled) && !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled scan returned %v, want something handleDisc recognises as a cancellation", err)
	}
	for _, l := range logged {
		if strings.HasPrefix(l, "error") {
			t.Errorf("a cancellation logged at error level: %q", l)
		}
	}
}

// Both discs here are real, and they are the two that bracket the problem.
//
// National Treasure's drive reported 21 titles including its feature twice;
// its copy reported 16, with the duplicate resolved and three short extras the
// drive had never mentioned. Refusing on title count would have refused it,
// which is why the test is running time.
//
// The Star Trek disc reported four episodes at the drive and one in the copy.
// hellbox ripped the one, filed it, and ejected the disc as done.
func TestPartialCopyIsJudgedByRunningTime(t *testing.T) {
	titles := func(secs ...int) []disc.Title {
		out := make([]disc.Title, len(secs))
		for i, s := range secs {
			out[i] = disc.Title{Index: i, DurationSecs: s}
		}
		return out
	}

	nationalTreasureDrive := titles(62, 63, 66, 72, 101, 107, 125, 126, 143, 153,
		156, 169, 199, 300, 364, 471, 496, 515, 679, 7843, 7843)
	nationalTreasureCopy := titles(7844, 213, 496, 156, 125, 62, 153, 132, 679,
		471, 107, 364, 169, 143, 109, 63)

	// Four ~45 minute episodes totalling 3h1m35s, against the single episode
	// the copy exposed.
	trekDrive := titles(2726, 2730, 2719, 2720)
	trekCopy := titles(2726)

	for _, tc := range []struct {
		name         string
		drive, copy  []disc.Title
		wantAccepted bool
	}{
		{"a copy that resolves a duplicated feature is accepted", nationalTreasureDrive, nationalTreasureCopy, true},
		{"a copy missing three episodes of four is refused", trekDrive, trekCopy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, want := totalSecs(tc.copy), dedupedSecs(tc.drive)
			pct := kept * 100 / want
			accepted := kept*100 >= want*minKeptPercent
			t.Logf("kept %ds of %ds (%d%%)", kept, want, pct)
			if accepted != tc.wantAccepted {
				t.Errorf("accepted = %v at %d%% kept, want %v", accepted, pct, tc.wantAccepted)
			}
		})
	}
}
