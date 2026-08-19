package drive

import (
	"context"
	"time"
)

// Event reports a change in a drive's state.
type Event struct {
	Drive Drive
	From  Status
	To    Status
	At    time.Time
	Err   error // set when the drive could not be read at all
}

// Monitor polls a drive and emits an Event on every state transition.
//
// Polling is used rather than a udev subscription deliberately. It is a few
// dozen lines with no external dependency, and — more importantly — it cannot
// miss a transition that happened while the daemon was restarting. A
// subscription would silently start from an unknown baseline; a poller always
// reads ground truth on its first tick.
type Monitor struct {
	drive    Drive
	interval time.Duration

	// settle suppresses transitions through the transient states a drive
	// passes through while a tray closes and a disc spins up.
	settle time.Duration
}

// NewMonitor creates a monitor for a drive. An interval of zero selects two
// seconds, which is responsive enough for a human feeding discs by hand and
// cheap enough to run per drive indefinitely.
func NewMonitor(d Drive, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Monitor{drive: d, interval: interval, settle: 3 * time.Second}
}

// Run polls until ctx is cancelled, sending events on the returned channel. The
// channel is closed when polling stops.
//
// The first reading always produces an event, so a caller that starts with a
// disc already in the drive sees it rather than waiting for the next physical
// change.
func (m *Monitor) Run(ctx context.Context) <-chan Event {
	out := make(chan Event, 8)

	go func() {
		defer close(out)

		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		var (
			current   = StatusUnknown
			pending   = StatusUnknown
			pendingAt time.Time
			started   bool
		)

		emit := func(to Status, err error) {
			ev := Event{Drive: m.drive, From: current, To: to, At: time.Now(), Err: err}
			current = to
			select {
			case out <- ev:
			case <-ctx.Done():
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			status, err := m.drive.Status()
			if err != nil {
				// The drive vanished — unplugged, or the kernel dropped it.
				// Report once and keep polling so it can come back.
				if current != StatusUnknown || !started {
					started = true
					emit(StatusUnknown, err)
				}
				pending = StatusUnknown
				continue
			}

			if !started {
				started = true
				current = StatusUnknown
				emit(status, nil)
				pending = status
				continue
			}

			if status == current {
				pending = status
				continue
			}

			// A drive reports NotReady while the tray closes and the disc spins
			// up, and can flicker between NoDisc and NotReady for several
			// seconds. Require a transient state to persist before believing
			// it; settled states are reported immediately.
			if status == StatusNotReady || status == StatusUnknown {
				if pending != status {
					pending, pendingAt = status, time.Now()
					continue
				}
				if time.Since(pendingAt) < m.settle {
					continue
				}
			}

			pending = status
			emit(status, nil)
		}
	}()

	return out
}
