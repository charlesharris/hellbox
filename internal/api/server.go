package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Version is the protocol version, carried on every response so a client can
// refuse a daemon it does not understand rather than misreading it.
const Version = 2

// Backend is what the daemon exposes to the API.
//
// The read models are `any` deliberately. The daemon owns their shape and
// serialises them straight to JSON; restating those types here would mean
// changing two files to add a field and would tempt this package into having
// opinions about disc state, which belong upstream.
type Backend interface {
	Drives(ctx context.Context) (any, error)
	Health(ctx context.Context) (any, error)
	Disc(ctx context.Context, fingerprint string) (any, error)

	Eject(ctx context.Context, drive string) error
	Cancel(ctx context.Context, drive string) error
	Rescan(ctx context.Context) error
	Forget(ctx context.Context, fingerprint string) error
}

// ErrNotFound is returned by a Backend for a drive or disc that does not exist,
// so the API can answer 404 rather than 500.
var ErrNotFound = errors.New("not found")

// Server serves the daemon over HTTP.
type Server struct {
	backend Backend
	hub     *Hub

	// KeepAlive is how often an idle SSE stream emits a comment frame.
	//
	// Without it an idle connection is indistinguishable from a dead one, and
	// something in the middle eventually closes it. A rip can run for an hour
	// between interesting events, so this is not a corner case.
	KeepAlive time.Duration
}

// NewServer wires a Backend and a Hub into an http.Handler.
func NewServer(b Backend, h *Hub) *Server {
	return &Server{backend: b, hub: h, KeepAlive: 20 * time.Second}
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/drives", s.read(func(ctx context.Context, _ *http.Request) (any, error) {
		return s.backend.Drives(ctx)
	}))
	mux.HandleFunc("GET /v1/health", s.read(func(ctx context.Context, _ *http.Request) (any, error) {
		return s.backend.Health(ctx)
	}))
	mux.HandleFunc("GET /v1/discs/{fingerprint}", s.read(func(ctx context.Context, r *http.Request) (any, error) {
		return s.backend.Disc(ctx, r.PathValue("fingerprint"))
	}))

	mux.HandleFunc("POST /v1/drives/{id}/eject", s.act(func(ctx context.Context, r *http.Request) error {
		return s.backend.Eject(ctx, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /v1/drives/{id}/cancel", s.act(func(ctx context.Context, r *http.Request) error {
		return s.backend.Cancel(ctx, r.PathValue("id"))
	}))
	mux.HandleFunc("POST /v1/rescan", s.act(func(ctx context.Context, _ *http.Request) error {
		return s.backend.Rescan(ctx)
	}))
	mux.HandleFunc("POST /v1/discs/{fingerprint}/forget", s.act(func(ctx context.Context, r *http.Request) error {
		return s.backend.Forget(ctx, r.PathValue("fingerprint"))
	}))

	mux.HandleFunc("GET /v1/events", s.events)

	return mux
}

// envelope wraps every JSON response.
type envelope struct {
	Version int    `json:"version"`
	OK      bool   `json:"ok"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`

	// LastEventID lets a client that has just fetched state know exactly where
	// to resume the stream. Without it there is a window between reading state
	// and subscribing in which an event can be missed with nothing to detect it.
	LastEventID uint64 `json:"last_event_id"`
}

func (s *Server) read(fn func(context.Context, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Captured before the read, never after. An event that lands while the
		// handler runs must be replayed to the client rather than assumed
		// already reflected in what it was handed.
		last := s.hub.LastID()

		data, err := fn(r.Context(), r)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, envelope{Version: Version, OK: true, Data: data, LastEventID: last})
	}
}

func (s *Server) act(fn func(context.Context, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r.Context(), r); err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, envelope{Version: Version, OK: true, LastEventID: s.hub.LastID()})
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, ErrNotFound) {
		code = http.StatusNotFound
	}
	s.write(w, code, envelope{Version: Version, OK: false, Error: err.Error(), LastEventID: s.hub.LastID()})
}

func (s *Server) write(w http.ResponseWriter, code int, e envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(e)
}

// events streams state changes as Server-Sent Events.
//
// SSE rather than a WebSocket because the traffic is entirely one-way and SSE
// carries reconnection in the protocol: a client sends Last-Event-ID and the
// server resumes from it. Rebuilding that over a WebSocket would mean
// hand-rolling the same thing worse.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxy buffering would hold events until a buffer filled, which for a
	// stream that is idle for minutes at a time means delivering them far too
	// late to be worth anything.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Subscribe before replaying, so an event arriving between the two is
	// queued rather than lost. It may be delivered twice; ids make that
	// harmless, where a gap would not be.
	ch, release := s.hub.Subscribe()
	defer release()

	from := lastEventID(r)
	missed, complete := s.hub.Since(from)

	// A client that has fallen further behind than the ring holds is told so
	// explicitly. Silently replaying what survives would hand it a gap it has
	// no way to detect, and its catalog would drift from the filesystem with
	// nothing to notice.
	if !complete {
		writeFrame(w, "", "reconcile", map[string]any{
			"reason":        "the event buffer wrapped while you were away",
			"last_event_id": s.hub.LastID(),
		})
	}

	writeFrame(w, "", EventHello, map[string]any{
		"version":       Version,
		"last_event_id": s.hub.LastID(),
	})

	sent := from
	for _, ev := range missed {
		writeEvent(w, ev)
		sent = ev.ID
	}
	flusher.Flush()

	keep := time.NewTicker(s.keepAlive())
	defer keep.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			// Replay and the live stream overlap by design; skip what already
			// went out rather than sending it twice.
			if ev.ID <= sent {
				continue
			}
			writeEvent(w, ev)
			sent = ev.ID
			flusher.Flush()
		case <-keep.C:
			// A comment frame. Clients ignore it; the network does not.
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) keepAlive() time.Duration {
	if s.KeepAlive <= 0 {
		return 20 * time.Second
	}
	return s.KeepAlive
}

// lastEventID reads the resume point from the SSE header, falling back to a
// query parameter so the stream can be resumed by hand with curl.
func lastEventID(r *http.Request) uint64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func writeEvent(w http.ResponseWriter, ev Event) {
	writeFrame(w, strconv.FormatUint(ev.ID, 10), ev.Kind, ev)
}

// writeFrame emits one SSE frame.
func writeFrame(w http.ResponseWriter, id, kind string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	fmt.Fprintf(w, "event: %s\n", kind)
	// SSE is newline-delimited, so a payload containing one would end the frame
	// early. JSON encodes newlines as \n inside strings, so this cannot happen —
	// but the split is cheap insurance against a future payload that is not JSON.
	for _, line := range strings.Split(string(body), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}
