package api

import (
	"sync"
	"testing"
)

func TestPublishAssignsMonotonicIDs(t *testing.T) {
	h := NewHub()
	a := h.Publish(EventLog, "one")
	b := h.Publish(EventLog, "two")

	if a.ID != 1 || b.ID != 2 {
		t.Errorf("ids = %d,%d; want 1,2", a.ID, b.ID)
	}
	if h.LastID() != 2 {
		t.Errorf("LastID = %d, want 2", h.LastID())
	}
	if a.At.IsZero() {
		t.Error("events must be timestamped")
	}
}

// The recovery path: a client says what it last saw and gets the rest.
func TestSinceReplaysWhatWasMissed(t *testing.T) {
	h := NewHub()
	for i := 0; i < 5; i++ {
		h.Publish(EventLog, i)
	}

	got, complete := h.Since(2)
	if !complete {
		t.Error("nothing has wrapped; replay must be complete")
	}
	if len(got) != 3 {
		t.Fatalf("replayed %d events, want 3", len(got))
	}
	if got[0].ID != 3 || got[2].ID != 5 {
		t.Errorf("replayed ids %d..%d, want 3..5", got[0].ID, got[2].ID)
	}
}

func TestSinceFromZeroReplaysEverything(t *testing.T) {
	h := NewHub()
	for i := 0; i < 3; i++ {
		h.Publish(EventLog, i)
	}
	got, complete := h.Since(0)
	if !complete || len(got) != 3 {
		t.Errorf("got %d events complete=%v, want 3 and true", len(got), complete)
	}
}

func TestSinceWhenAlreadyCurrentReturnsNothing(t *testing.T) {
	h := NewHub()
	h.Publish(EventLog, "x")
	got, complete := h.Since(1)
	if len(got) != 0 {
		t.Errorf("got %d events, want none", len(got))
	}
	if !complete {
		t.Error("a caller that is up to date has missed nothing")
	}
}

func TestSinceOnAnEmptyHub(t *testing.T) {
	h := NewHub()
	got, complete := h.Since(0)
	if len(got) != 0 || !complete {
		t.Errorf("got %d complete=%v; want 0 and true", len(got), complete)
	}
}

// The case that must not be silently wrong: a client away so long the ring
// wrapped. Replaying what is left would hand it a gap it could not detect, and
// the catalog would disagree with the filesystem with nothing to notice.
func TestSinceReportsIncompleteOnceTheRingWraps(t *testing.T) {
	h := NewHub()
	for i := 0; i < ringSize+50; i++ {
		h.Publish(EventLog, i)
	}

	_, complete := h.Since(1)
	if complete {
		t.Error("id 1 has been evicted; the caller must be told to reconcile instead")
	}

	// A recent caller is still fine.
	got, complete := h.Since(h.LastID() - 10)
	if !complete {
		t.Error("a recent caller should still be replayable")
	}
	if len(got) != 10 {
		t.Errorf("replayed %d, want 10", len(got))
	}
}

func TestRingKeepsTheNewestEventsAfterWrapping(t *testing.T) {
	h := NewHub()
	total := ringSize + 100
	for i := 0; i < total; i++ {
		h.Publish(EventLog, i)
	}

	all, _ := h.Since(0)
	if len(all) != ringSize {
		t.Fatalf("ring holds %d, want %d", len(all), ringSize)
	}
	// Oldest first, newest last, no duplicates or gaps.
	for i := 1; i < len(all); i++ {
		if all[i].ID != all[i-1].ID+1 {
			t.Fatalf("ids not contiguous at %d: %d then %d", i, all[i-1].ID, all[i].ID)
		}
	}
	if all[len(all)-1].ID != uint64(total) {
		t.Errorf("newest id = %d, want %d", all[len(all)-1].ID, total)
	}
}

func TestSubscribeReceivesFutureEvents(t *testing.T) {
	h := NewHub()
	ch, release := h.Subscribe()
	defer release()

	h.Publish(EventDrives, "state")
	select {
	case ev := <-ch:
		if ev.Kind != EventDrives {
			t.Errorf("kind = %q", ev.Kind)
		}
	default:
		t.Fatal("subscriber received nothing")
	}
}

// A browser tab that stops reading must never be able to stall a rip.
func TestASlowSubscriberDoesNotBlockPublishing(t *testing.T) {
	h := NewHub()
	_, release := h.Subscribe() // never drained
	defer release()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			h.Publish(EventLog, i)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-make(chan struct{}):
		t.Fatal("publishing blocked on a subscriber that stopped reading")
	}
	if h.LastID() != 5000 {
		t.Errorf("published %d, want 5000", h.LastID())
	}
}

func TestReleaseIsIdempotentAndUnsubscribes(t *testing.T) {
	h := NewHub()
	_, release := h.Subscribe()
	if h.Subscribers() != 1 {
		t.Fatalf("Subscribers = %d, want 1", h.Subscribers())
	}
	release()
	release() // must not panic on a double close
	if h.Subscribers() != 0 {
		t.Errorf("Subscribers = %d, want 0", h.Subscribers())
	}
}

// The daemon publishes from drive workers while clients attach and detach.
func TestHubIsRaceFree(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				h.Publish(EventDrives, i)
			}
		}()
	}
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, release := h.Subscribe()
			for i := 0; i < 50; i++ {
				select {
				case <-ch:
				default:
				}
				h.Since(uint64(i))
				h.LastID()
			}
			release()
		}()
	}
	wg.Wait()

	if h.LastID() != 800 {
		t.Errorf("LastID = %d, want 800", h.LastID())
	}
}
