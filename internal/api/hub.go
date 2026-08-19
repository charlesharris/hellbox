// Package api serves hellboxd over local HTTP.
//
// This replaces v1's unix socket. The socket existed to feed a terminal client
// on the same machine; HTTP feeds that, a browser, and the Rails catalog with
// less ceremony and no hand-rolled framing.
//
// Nothing here authenticates anything. The daemon binds to loopback on a
// single-user appliance, and inventing a credential scheme for it would add a
// failure mode without removing one.
package api

import (
	"sync"
	"time"
)

// Event is one thing that happened, as pushed to subscribers.
//
// The ID is monotonic across the daemon's lifetime and is what makes recovery
// work: a client that reconnects says which id it last saw, and gets everything
// after it. Without that, a Rails restart mid-rip would silently lose the
// events that landed while it was down, and the catalog would disagree with the
// filesystem in a way nothing would notice.
type Event struct {
	ID   uint64    `json:"id"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	Data any       `json:"data,omitempty"`
}

// Event kinds.
const (
	EventDrives = "drives" // a drive changed state or made progress
	EventLog    = "log"    // a human-readable line
	EventDisc   = "disc"   // a disc was enumerated, ripped, or finished
	EventHealth = "health" // a health check result changed
	EventHello  = "hello"  // sent once on connect, carrying the current id
)

// ringSize is how many events are kept for replay.
//
// This is the whole recovery budget. A client away for longer than this cannot
// catch up from the stream and must reconcile against the filesystem instead —
// which is always correct, because the rips tree is self-describing, but is far
// more expensive. A few thousand events covers a Rails restart, a deploy, or a
// laptop lid closing, and costs a megabyte or so of memory.
const ringSize = 4096

// Hub fans events out to subscribers and keeps recent ones for replay.
type Hub struct {
	mu sync.RWMutex

	nextID uint64
	ring   []Event // circular; len grows to ringSize then wraps
	start  int     // index of the oldest event once wrapped
	full   bool

	subs map[chan Event]struct{}
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{
		ring: make([]Event, 0, ringSize),
		subs: map[chan Event]struct{}{},
	}
}

// Publish records an event and delivers it to every subscriber.
//
// Delivery is non-blocking. A subscriber that cannot keep up drops events
// rather than stalling the daemon, because the daemon is ripping a disc and a
// slow browser tab must never be able to interfere with that. A dropped event
// is recoverable: the subscriber's next reconnect replays from its last id, and
// a subscriber so far behind that the ring has wrapped falls back to a full
// reconcile.
// Fanout happens while the lock is still held, which looks wrong and is not.
// The obvious version — copy the subscriber list, unlock, then send — races
// against a client disconnecting: release() removes the channel and closes it,
// and a send already in flight panics on a closed channel. The race detector
// caught exactly that.
//
// Holding the lock is safe here only because every send is non-blocking. A
// subscriber cannot stall the publisher whether the lock is held or not, so the
// lock costs a few microseconds of fanout across a handful of local clients and
// buys mutual exclusion with release().
func (h *Hub) Publish(kind string, data any) Event {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	ev := Event{ID: h.nextID, Kind: kind, At: time.Now(), Data: data}
	h.append(ev)

	for c := range h.subs {
		select {
		case c <- ev:
		default: // slow subscriber; it will catch up on reconnect
		}
	}
	return ev
}

// append adds to the ring, wrapping once it is full.
func (h *Hub) append(ev Event) {
	if len(h.ring) < ringSize {
		h.ring = append(h.ring, ev)
		return
	}
	h.full = true
	h.ring[h.start] = ev
	h.start = (h.start + 1) % ringSize
}

// LastID returns the most recent event id, or 0 if nothing has happened.
func (h *Hub) LastID() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nextID
}

// Since returns the events after id, oldest first, and whether the caller can
// trust that it missed nothing.
//
// complete is false when id is older than anything still held, which means the
// ring wrapped while the caller was away. That is not an error and must not be
// treated as one — it is the signal to stop replaying and reconcile against the
// filesystem, which is always authoritative.
func (h *Hub) Since(id uint64) (events []Event, complete bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	ordered := h.orderedLocked()
	if len(ordered) == 0 {
		return nil, id == 0 || id == h.nextID
	}

	oldest := ordered[0].ID
	// complete when the very next event the caller wants is still held, or when
	// the caller is already up to date.
	complete = id+1 >= oldest || id >= h.nextID

	for _, ev := range ordered {
		if ev.ID > id {
			events = append(events, ev)
		}
	}
	return events, complete
}

// orderedLocked returns the ring oldest-first. Caller holds the lock.
func (h *Hub) orderedLocked() []Event {
	if !h.full {
		return h.ring
	}
	out := make([]Event, 0, len(h.ring))
	out = append(out, h.ring[h.start:]...)
	out = append(out, h.ring[:h.start]...)
	return out
}

// Subscribe returns a channel of future events and a function to release it.
//
// The buffer absorbs a burst — per-title progress during a rip arrives faster
// than a browser repaints — without making the publisher wait.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)

	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// Subscribers reports how many clients are attached, for the health view.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
