package makemkv

import (
	"sync"
	"time"
)

// stallTimer reports when a rip has stopped making forward progress.
//
// A rip that hangs is not hypothetical: a drive with no region set will
// enumerate a disc correctly and then stop partway through the first title,
// sleeping in MakeMKV's own retry loop rather than failing. Without this the
// daemon waits for it indefinitely — one such rip sat for seventeen hours —
// which breaks the promise that unattended operation needs no human.
//
// Progress is measured by MakeMKV's own progress values changing, not by output
// arriving. A stalled rip keeps emitting messages: the one that hung repeated
// "trying to work around..." thirty times while reading nothing at all, so a
// timer reset by any output would never have fired.
type stallTimer struct {
	timeout time.Duration

	mu     sync.Mutex
	last   time.Time
	fired  bool
	marker [3]int
	seen   bool
}

// newStallTimer returns a timer that expires after timeout without progress. A
// timeout of zero disables it.
func newStallTimer(timeout time.Duration, now time.Time) *stallTimer {
	return &stallTimer{timeout: timeout, last: now}
}

// observe records a progress reading, restarting the clock if it differs from
// the last one seen. Repeating the same values is not progress.
func (s *stallTimer) observe(value, total, max int, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m := [3]int{value, total, max}
	if s.seen && m == s.marker {
		return
	}
	s.marker, s.seen, s.last = m, true, now
}

// expired reports whether the timeout has elapsed with no progress. It reports
// true only once, so the caller acts on a stall a single time.
func (s *stallTimer) expired(now time.Time) bool {
	if s.timeout <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.fired || now.Sub(s.last) < s.timeout {
		return false
	}
	s.fired = true
	return true
}

// stalled reports whether this timer is what ended the rip, which is how a
// stall is told apart from an ordinary cancellation.
func (s *stallTimer) stalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fired
}

// checkInterval is how often a stall is looked for. Checking far more often
// than the timeout keeps the reported stall close to when it actually began
// without polling pointlessly on a long timeout.
func (s *stallTimer) checkInterval() time.Duration {
	if s.timeout <= 0 {
		return 0
	}
	if d := s.timeout / 10; d < 30*time.Second {
		if d < time.Second {
			return time.Second
		}
		return d
	}
	return 30 * time.Second
}
