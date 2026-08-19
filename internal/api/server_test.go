package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	drives  any
	health  any
	disc    any
	discErr error
	ejected []string
	rescans int
	forgot  []string
	actErr  error
}

func (f *fakeBackend) Drives(context.Context) (any, error) { return f.drives, nil }
func (f *fakeBackend) Health(context.Context) (any, error) { return f.health, nil }
func (f *fakeBackend) Disc(_ context.Context, fp string) (any, error) {
	if f.discErr != nil {
		return nil, f.discErr
	}
	return f.disc, nil
}
func (f *fakeBackend) Eject(_ context.Context, d string) error {
	f.ejected = append(f.ejected, d)
	return f.actErr
}
func (f *fakeBackend) Cancel(context.Context, string) error { return f.actErr }
func (f *fakeBackend) Rescan(context.Context) error         { f.rescans++; return f.actErr }
func (f *fakeBackend) Forget(_ context.Context, fp string) error {
	f.forgot = append(f.forgot, fp)
	return f.actErr
}

func newTestServer(t *testing.T) (*fakeBackend, *Hub, http.Handler) {
	t.Helper()
	b := &fakeBackend{drives: []string{"sr1"}, health: map[string]any{"ok": true}}
	h := NewHub()
	s := NewServer(b, h)
	s.KeepAlive = 50 * time.Millisecond
	return b, h, s.Handler()
}

func decode(t *testing.T, body string) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return e
}

func TestReadEndpointsReturnEnvelopedData(t *testing.T) {
	_, _, h := newTestServer(t)

	for _, path := range []string{"/v1/drives", "/v1/health"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: status %d", path, rr.Code)
		}
		e := decode(t, rr.Body.String())
		if !e.OK || e.Version != Version {
			t.Errorf("%s: envelope = %+v", path, e)
		}
		if e.Data == nil {
			t.Errorf("%s: no data", path)
		}
	}
}

// A client reads state and then subscribes. If the id were captured after the
// read, an event landing during the handler would be missed with nothing to
// detect it.
func TestReadsReportAResumePointTakenBeforeTheRead(t *testing.T) {
	_, hub, h := newTestServer(t)
	hub.Publish(EventLog, "before")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/drives", nil))

	if got := decode(t, rr.Body.String()).LastEventID; got != 1 {
		t.Errorf("LastEventID = %d, want 1", got)
	}
}

func TestUnknownDiscIs404NotAServerError(t *testing.T) {
	b, _, h := newTestServer(t)
	b.discErr = fmt.Errorf("disc abc: %w", ErrNotFound)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/discs/abc", nil))

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
	if e := decode(t, rr.Body.String()); e.OK || e.Error == "" {
		t.Errorf("envelope = %+v", e)
	}
}

func TestBackendFailureIs500(t *testing.T) {
	b, _, h := newTestServer(t)
	b.discErr = errors.New("the database is on fire")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/discs/abc", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestActionsRouteToTheBackendWithTheirPathValues(t *testing.T) {
	b, _, h := newTestServer(t)

	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/drives/sr1/eject"},
		{"POST", "/v1/rescan"},
		{"POST", "/v1/discs/deadbeef/forget"},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(c.method, c.path, nil))
		if rr.Code != 200 {
			t.Errorf("%s %s: status %d", c.method, c.path, rr.Code)
		}
	}
	if len(b.ejected) != 1 || b.ejected[0] != "sr1" {
		t.Errorf("ejected = %v, want [sr1]", b.ejected)
	}
	if b.rescans != 1 {
		t.Errorf("rescans = %d, want 1", b.rescans)
	}
	if len(b.forgot) != 1 || b.forgot[0] != "deadbeef" {
		t.Errorf("forgot = %v, want [deadbeef]", b.forgot)
	}
}

// A GET must not be able to eject a disc.
func TestActionsRejectTheWrongMethod(t *testing.T) {
	b, _, h := newTestServer(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/v1/drives/sr1/eject", nil))

	if rr.Code == 200 {
		t.Error("GET reached a mutating route")
	}
	if len(b.ejected) != 0 {
		t.Error("a GET ejected a drive")
	}
}

// ---------- SSE ----------

// waitForSubscriber blocks until the SSE handler has attached to the hub.
func waitForSubscriber(t *testing.T, h *Hub) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.Subscribers() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the event handler never subscribed")
}

// readFrames consumes an SSE stream until it has seen want frames or the
// deadline passes, returning the raw text.
func readFrames(t *testing.T, body *bufio.Reader, want int, deadline time.Duration) string {
	t.Helper()
	var sb strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		blanks := 0
		for {
			line, err := body.ReadString('\n')
			if line != "" {
				sb.WriteString(line)
				if strings.TrimSpace(line) == "" {
					blanks++
					if blanks >= want {
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
	}
	return sb.String()
}

func TestEventStreamSendsHelloThenLiveEvents(t *testing.T) {
	_, hub, h := newTestServer(t)

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	// Wait for the handler to actually subscribe rather than sleeping and
	// hoping. Do() returns as soon as headers arrive, which is before the
	// handler reaches Subscribe, so a publish here can land in the gap and be
	// delivered to nobody.
	waitForSubscriber(t, hub)
	hub.Publish(EventDrives, map[string]string{"drive": "sr1"})

	got := readFrames(t, br, 2, 2*time.Second)
	if !strings.Contains(got, "event: "+EventHello) {
		t.Errorf("no hello frame in:\n%s", got)
	}
	if !strings.Contains(got, "event: "+EventDrives) {
		t.Errorf("live event not delivered:\n%s", got)
	}
	if !strings.Contains(got, "id: 1") {
		t.Errorf("event carried no id, so it cannot be resumed:\n%s", got)
	}
}

// The recovery path that makes a Rails restart survivable.
func TestLastEventIDReplaysWhatWasMissed(t *testing.T) {
	_, hub, h := newTestServer(t)
	for i := 0; i < 4; i++ {
		hub.Publish(EventLog, i)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got := readFrames(t, bufio.NewReader(resp.Body), 3, 2*time.Second)
	for _, want := range []string{"id: 3", "id: 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in replay:\n%s", want, got)
		}
	}
	if strings.Contains(got, "id: 1\n") || strings.Contains(got, "id: 2\n") {
		t.Errorf("replayed events the client already had:\n%s", got)
	}
}

// A client further behind than the ring must be told, not quietly given a
// partial replay it cannot tell is partial.
func TestAWrappedRingTellsTheClientToReconcile(t *testing.T) {
	_, hub, h := newTestServer(t)
	for i := 0; i < ringSize+10; i++ {
		hub.Publish(EventLog, i)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got := readFrames(t, bufio.NewReader(resp.Body), 1, 2*time.Second)
	if !strings.Contains(got, "event: reconcile") {
		t.Errorf("expected a reconcile instruction:\n%s", got[:min(len(got), 400)])
	}
}

func TestKeepAliveIsSentOnAnIdleStream(t *testing.T) {
	_, _, h := newTestServer(t) // KeepAlive is 50ms in tests

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got := readFrames(t, bufio.NewReader(resp.Body), 4, 2*time.Second)
	if !strings.Contains(got, ": keepalive") {
		t.Errorf("an idle stream must not look dead:\n%s", got)
	}
}

func TestQueryParamResumeWorksForCurl(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/events?last_event_id=42", nil)
	if got := lastEventID(r); got != 42 {
		t.Errorf("lastEventID = %d, want 42", got)
	}
	// The header wins when both are present.
	r.Header.Set("Last-Event-ID", "7")
	if got := lastEventID(r); got != 7 {
		t.Errorf("lastEventID = %d, want the header to win", got)
	}
	if got := lastEventID(httptest.NewRequest("GET", "/v1/events", nil)); got != 0 {
		t.Errorf("lastEventID = %d, want 0", got)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
