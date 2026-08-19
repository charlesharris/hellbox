package daemon

import (
	"testing"
	"time"
)

func newTestStatus() *driveStatus {
	return newDriveStatus("usb-ASUS_SDRW", "top", "/dev/sr0", "ASUS SDRW-08D2S-U")
}

// Returning to rest must clear everything about the disc that has left. A drive
// still showing the last disc's label and a half-finished progress bar reads as
// a rip in trouble rather than an empty drive.
func TestSetStateClearsStaleDiscFields(t *testing.T) {
	for _, rest := range []DriveState{StateEmpty, StateTrayOpen, StateAbsent} {
		t.Run(string(rest), func(t *testing.T) {
			d := newTestStatus()
			d.setState(StateRipping)
			d.update(func(s *DriveSnapshot) {
				s.DiscLabel = "STILL GAME SERIES 1 DISC 1"
				s.Fingerprint = "a3f9c2e10b47"
				s.RipDir = "/srv/media/rips/x"
				s.TitleCount = 6
				s.Error = "something went wrong"
				s.Attempt = 2
			})
			d.beginRip(3600)
			d.setProgress(3, 0.58, "Saving title 4")
			d.titleDone(600)

			d.setState(rest)

			got := d.Snapshot()
			if got.DiscLabel != "" || got.Fingerprint != "" || got.RipDir != "" {
				t.Errorf("disc identity survived the disc leaving: %+v", got)
			}
			if got.TitleCount != 0 || got.TitlesDone != 0 || got.CurrentTitle != 0 {
				t.Errorf("title counters survived: %+v", got)
			}
			if got.Fraction != 0 || got.Operation != "" || got.ETASeconds != 0 {
				t.Errorf("progress survived: %+v", got)
			}
			if got.Error != "" || got.Attempt != 0 {
				t.Errorf("error state survived: %+v", got)
			}
		})
	}
}

// A failed disc stays in the drive and its error must stay on screen with it.
// Clearing it would leave a drive that is stuck for no visible reason.
func TestSetStateKeepsErrorWhileDiscIsRetained(t *testing.T) {
	d := newTestStatus()
	d.setState(StateScanning)
	d.update(func(s *DriveSnapshot) { s.DiscLabel = "SOME DISC"; s.Error = "scan failed" })
	d.setState(StateFailed)

	got := d.Snapshot()
	if got.Error != "scan failed" {
		t.Errorf("error cleared on failure: %q", got.Error)
	}
	if got.DiscLabel != "SOME DISC" {
		t.Errorf("disc label cleared while the disc is still in the drive: %q", got.DiscLabel)
	}
}

// The incompatible state is reached with a disc physically loaded, so it must
// keep its explanation too — it is the only thing telling the operator to swap
// the disc.
func TestIncompatibleKeepsItsExplanation(t *testing.T) {
	d := newTestStatus()
	d.update(func(s *DriveSnapshot) { s.Error = "a disc this drive cannot read" })
	d.setState(StateIncompatible)

	if got := d.Snapshot(); got.Error == "" {
		t.Error("the reason the drive is idle was cleared")
	}
}

func TestSetStateIsIdempotent(t *testing.T) {
	d := newTestStatus()
	d.setState(StateRipping)
	first := d.Snapshot().Since

	time.Sleep(2 * time.Millisecond)
	d.setState(StateRipping)

	if got := d.Snapshot().Since; !got.Equal(first) {
		// Re-stamping on every poll would make "how long has it been ripping"
		// permanently read as a few seconds.
		t.Error("re-entering the same state restarted its clock")
	}
}

func TestETAFromCompletedTitles(t *testing.T) {
	d := newTestStatus()
	d.beginRip(3600) // one hour of content

	// Pretend the rip began a while ago so the rate is measurable.
	d.mu.Lock()
	d.ripStarted = time.Now().Add(-60 * time.Second)
	d.mu.Unlock()

	d.titleDone(900) // 15 minutes of content in 60 seconds of wall clock

	got := d.Snapshot()
	if got.TitlesDone != 1 {
		t.Errorf("TitlesDone = %d, want 1", got.TitlesDone)
	}
	// 2700 seconds remaining at 15 s of content per wall second ≈ 180 s.
	if got.ETASeconds < 150 || got.ETASeconds > 220 {
		t.Errorf("ETASeconds = %d, want roughly 180", got.ETASeconds)
	}
}

// An estimate from no data is worse than none: it would show a confident and
// wildly wrong figure for the whole of the first title.
func TestNoETABeforeATitleCompletes(t *testing.T) {
	d := newTestStatus()
	d.beginRip(3600)
	d.setProgress(0, 0.4, "Saving title 1")

	if got := d.Snapshot().ETASeconds; got != 0 {
		t.Errorf("ETASeconds = %d before any title finished, want 0", got)
	}
}

// A rip of titles with no known duration must not divide by zero or report a
// nonsense estimate.
func TestETAWithNoDurationInformation(t *testing.T) {
	d := newTestStatus()
	d.beginRip(0)
	d.titleDone(0)

	if got := d.Snapshot().ETASeconds; got != 0 {
		t.Errorf("ETASeconds = %d with no duration data, want 0", got)
	}
}

// beginRip must reset the counters, or a second disc in the same drive inherits
// the first one's progress.
func TestBeginRipResetsProgress(t *testing.T) {
	d := newTestStatus()
	d.beginRip(1800)
	d.titleDone(600)
	d.beginRip(3600)

	if d.secsDone != 0 {
		t.Errorf("secsDone = %d after a new rip began, want 0", d.secsDone)
	}
	if d.secsTotal != 3600 {
		t.Errorf("secsTotal = %d, want 3600", d.secsTotal)
	}
}

func TestBusyAndTerminal(t *testing.T) {
	busy := map[DriveState]bool{
		StateScanning: true, StateRipping: true, StateVerifying: true, StateEjecting: true,
		StateEmpty: false, StateTrayOpen: false, StateFailed: false,
		StateComplete: false, StateIncompatible: false, StateAbsent: false,
	}
	for s, want := range busy {
		if got := s.Busy(); got != want {
			t.Errorf("%s.Busy() = %v, want %v", s, got, want)
		}
	}

	terminal := map[DriveState]bool{
		StateComplete: true, StateDuplicate: true, StateFailed: true, StateIncompatible: true,
		StateEmpty: false, StateRipping: false, StateScanning: false, StateLoading: false,
	}
	for s, want := range terminal {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}

// The socket serves snapshots while a worker is mutating them; the race
// detector should find any unguarded access here.
func TestConcurrentSnapshotAndUpdate(t *testing.T) {
	d := newTestStatus()
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			d.setProgress(i%8, float64(i%100)/100, "Saving")
			d.setState(StateRipping)
			d.setState(StateVerifying)
		}
	}()
	for i := 0; i < 500; i++ {
		_ = d.Snapshot()
	}
	<-done
}
