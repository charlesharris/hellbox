package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"hellbox/internal/api"
)

// apiBackend adapts the daemon to the HTTP API.
//
// A separate type rather than methods on Daemon, so that the API's interface
// cannot quietly widen into "whatever the daemon happens to expose". Adding an
// endpoint should require deciding to add one.
type apiBackend struct{ d *Daemon }

func (b apiBackend) Drives(ctx context.Context) (any, error) {
	return b.d.status(ctx).Drives, nil
}

func (b apiBackend) Health(ctx context.Context) (any, error) {
	return b.d.status(ctx).Health, nil
}

func (b apiBackend) Disc(ctx context.Context, fingerprint string) (any, error) {
	if fingerprint == "" {
		return nil, fmt.Errorf("a fingerprint is required")
	}
	rec, err := b.d.st.FindDisc(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		// Distinguished from a failure so the API answers 404 rather than 500.
		return nil, fmt.Errorf("no disc with fingerprint %s: %w", fingerprint, api.ErrNotFound)
	}
	return rec, nil
}

func (b apiBackend) Eject(_ context.Context, drive string) error {
	w, err := b.d.workerFor(drive)
	if err != nil {
		return fmt.Errorf("%v: %w", err, api.ErrNotFound)
	}
	return w.Eject()
}

func (b apiBackend) Cancel(_ context.Context, drive string) error {
	w, err := b.d.workerFor(drive)
	if err != nil {
		return fmt.Errorf("%v: %w", err, api.ErrNotFound)
	}
	return w.Cancel()
}

func (b apiBackend) Rescan(ctx context.Context) error {
	_, err := b.d.rescan(ctx)
	return err
}

func (b apiBackend) Forget(ctx context.Context, fingerprint string) error {
	if fingerprint == "" {
		return fmt.Errorf("a fingerprint is required")
	}
	if err := b.d.st.ForgetDisc(ctx, fingerprint); err != nil {
		return err
	}
	short := fingerprint
	if len(short) > 12 {
		short = short[:12]
	}
	b.d.logEvent("info", "disc "+short+" forgotten; it will be ripped again", nil, nil)
	return nil
}

// serveHTTP runs the HTTP API until ctx is cancelled.
//
// It runs beside the unix socket rather than replacing it, so `slay` keeps
// working while the web client is built. The socket goes when nothing needs it.
//
// Binding is to loopback only, and deliberately not configurable to anything
// else. This is an appliance with no authentication; the one thing standing
// between it and the network is the bind address, so it is not a setting.
func (d *Daemon) serveHTTP(ctx context.Context, addr string) error {
	if addr == "" {
		return nil
	}

	handler := api.NewServer(apiBackend{d}, d.hub).Handler()
	srv := &http.Server{
		Handler: handler,
		// No write timeout: the event stream is long-lived by design and a
		// deadline here would sever it mid-rip. Read timeouts are safe because
		// every request body is small or absent.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	d.logEvent("info", "http api listening on "+ln.Addr().String(), nil, nil)

	go func() {
		<-ctx.Done()
		// A brief grace period so an in-flight response completes; event streams
		// are closed by their own context, not waited on.
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// publishEvent mirrors a daemon event onto the HTTP event stream.
//
// The hub is separate from the socket's own subscriber list because the two
// have different delivery contracts: a socket client gets a full status
// snapshot, while an HTTP client gets an identified, replayable delta. Feeding
// one from the other would force the weaker guarantee on both.
func (d *Daemon) publishEvent(kind string, data any) {
	if d.hub != nil {
		d.hub.Publish(kind, data)
	}
}
