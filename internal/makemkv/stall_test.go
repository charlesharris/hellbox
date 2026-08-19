package makemkv

import (
	"testing"
	"time"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// The case this exists for. A rip that hangs must be stopped, not waited on:
// one sat for seventeen hours on a disc the drive would not decrypt.
func TestStallExpiresAfterTheTimeout(t *testing.T) {
	s := newStallTimer(10*time.Minute, epoch)

	if s.expired(epoch.Add(9 * time.Minute)) {
		t.Error("expired before the timeout elapsed")
	}
	if !s.expired(epoch.Add(10 * time.Minute)) {
		t.Error("did not expire once the timeout elapsed")
	}
	if !s.stalled() {
		t.Error("stalled() does not report the stall that fired")
	}
}

// Progress restarts the clock, or every long rip would be killed mid-way.
func TestProgressPostponesTheStall(t *testing.T) {
	s := newStallTimer(10*time.Minute, epoch)

	for i := 1; i <= 10; i++ {
		at := epoch.Add(time.Duration(i) * 9 * time.Minute)
		s.observe(i, 100, 100, at)
		if s.expired(at) {
			t.Fatalf("expired at %s despite progress every 9 minutes", at.Sub(epoch))
		}
	}
	if !s.expired(epoch.Add(90*time.Minute + 10*time.Minute)) {
		t.Error("did not expire once progress actually stopped")
	}
}

// The distinction the whole watchdog turns on. The rip that hung kept emitting
// output — thirty "trying to work around..." messages — while reading nothing,
// so repeating the same progress values must not count as progress.
func TestRepeatedIdenticalProgressIsNotProgress(t *testing.T) {
	s := newStallTimer(10*time.Minute, epoch)

	for i := 1; i <= 30; i++ {
		s.observe(4096, 65536, 65536, epoch.Add(time.Duration(i)*time.Minute))
	}
	if !s.expired(epoch.Add(11 * time.Minute)) {
		t.Error("thirty identical progress readings postponed the stall; " +
			"a rip repeating itself is exactly the hang this must catch")
	}
}

// Any change counts, including one that moves backwards: MakeMKV resets its
// counters between operations within a title, and that is still a live rip.
func TestAnyChangeInProgressCounts(t *testing.T) {
	s := newStallTimer(10*time.Minute, epoch)

	s.observe(50000, 65536, 65536, epoch.Add(time.Minute))
	s.observe(10, 65536, 65536, epoch.Add(9*time.Minute)) // counter reset
	if s.expired(epoch.Add(15 * time.Minute)) {
		t.Error("a reset counter was treated as no progress")
	}
}

// Acting on one stall twice would cancel a command that has already ended.
func TestStallFiresOnlyOnce(t *testing.T) {
	s := newStallTimer(time.Minute, epoch)

	if !s.expired(epoch.Add(2 * time.Minute)) {
		t.Fatal("did not expire")
	}
	if s.expired(epoch.Add(3 * time.Minute)) {
		t.Error("expired a second time; the caller would cancel twice")
	}
}

// Zero restores the old behaviour of waiting indefinitely, which has to remain
// available for anyone who would rather a slow disc finished than be stopped.
func TestZeroTimeoutNeverExpires(t *testing.T) {
	s := newStallTimer(0, epoch)

	if s.expired(epoch.Add(24 * time.Hour)) {
		t.Error("expired despite the watchdog being disabled")
	}
	if s.stalled() {
		t.Error("reported a stall despite the watchdog being disabled")
	}
	if iv := s.checkInterval(); iv != 0 {
		t.Errorf("checkInterval() = %s, want 0 so no watchdog goroutine starts", iv)
	}
}

// The check interval has to be short enough that the reported stall is close to
// when it began, without polling pointlessly on a long timeout.
func TestCheckIntervalIsBounded(t *testing.T) {
	for _, tc := range []struct {
		timeout time.Duration
		want    time.Duration
	}{
		{10 * time.Minute, 30 * time.Second},
		{time.Hour, 30 * time.Second},
		{2 * time.Minute, 12 * time.Second},
		{10 * time.Second, time.Second},
	} {
		if got := newStallTimer(tc.timeout, epoch).checkInterval(); got != tc.want {
			t.Errorf("checkInterval() for %s = %s, want %s", tc.timeout, got, tc.want)
		}
	}
}
