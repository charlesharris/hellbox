package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"hellbox/internal/api"
	"hellbox/internal/proto"
	"hellbox/internal/store"
)

// serve accepts client connections on the unix socket until ctx is cancelled.
func (d *Daemon) serve(ctx context.Context) error {
	// A socket left behind by a crashed daemon would otherwise block the bind.
	if err := os.Remove(d.cfg.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear stale socket %s: %w", d.cfg.SocketPath, err)
	}

	ln, err := net.Listen("unix", d.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", d.cfg.SocketPath, err)
	}
	defer ln.Close()
	defer os.Remove(d.cfg.SocketPath)

	// Group-readable so a client running as the same user, or in the same
	// group, can connect without being root.
	if err := os.Chmod(d.cfg.SocketPath, 0o660); err != nil {
		return fmt.Errorf("set socket permissions: %w", err)
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				wg.Wait()
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.handleConn(ctx, conn)
		}()
	}
}

// handleConn serves one client until it disconnects.
func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Writes are serialised through one goroutine so that a push and a response
	// can never interleave mid-line on the wire.
	out := make(chan any, 64)
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		enc := json.NewEncoder(conn)
		for {
			select {
			case <-connCtx.Done():
				return
			case msg, ok := <-out:
				if !ok {
					return
				}
				if err := enc.Encode(msg); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	var (
		pushes    chan proto.Push
		pushesMu  sync.Mutex
		pumpGroup sync.WaitGroup
	)
	defer func() {
		pushesMu.Lock()
		if pushes != nil {
			d.unsubscribe(pushes)
		}
		pushesMu.Unlock()
		pumpGroup.Wait()
		close(out)
		writerDone.Wait()
	}()

	send := func(msg any) {
		select {
		case out <- msg:
		case <-connCtx.Done():
		}
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 16*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		var req proto.Request
		if err := json.Unmarshal(line, &req); err != nil {
			send(proto.Response{OK: false, Error: fmt.Sprintf("malformed request: %v", err)})
			continue
		}

		if req.Method == proto.MethodSubscribe {
			pushesMu.Lock()
			if pushes == nil {
				pushes = d.subscribe()
				pumpGroup.Add(1)
				go func(ch chan proto.Push) {
					defer pumpGroup.Done()
					for {
						select {
						case <-connCtx.Done():
							return
						case p, ok := <-ch:
							if !ok {
								return
							}
							send(p)
						}
					}
				}(pushes)
			}
			pushesMu.Unlock()
			send(proto.Response{ID: req.ID, OK: true})
			d.sendStatusTo(ctx, send)
			continue
		}

		result, err := d.handle(ctx, req)
		if err != nil {
			send(proto.Response{ID: req.ID, OK: false, Error: err.Error()})
			continue
		}
		send(proto.Response{ID: req.ID, OK: true, Result: result})
	}
}

// handle dispatches one request.
func (d *Daemon) handle(ctx context.Context, req proto.Request) (json.RawMessage, error) {
	switch req.Method {
	case proto.MethodStatus:
		return marshal(d.status(ctx))

	case proto.MethodHistory:
		var p proto.LimitParams
		_ = json.Unmarshal(req.Params, &p)
		jobs, err := d.st.RecentJobs(ctx, p.Limit)
		if err != nil {
			return nil, err
		}
		return marshal(jobs)

	case proto.MethodEvents:
		var p proto.LimitParams
		_ = json.Unmarshal(req.Params, &p)
		events, err := d.st.RecentEvents(ctx, p.Limit)
		if err != nil {
			return nil, err
		}
		return marshal(events)

	case proto.MethodEject:
		var p proto.DriveParams
		_ = json.Unmarshal(req.Params, &p)
		w, err := d.workerFor(p.Drive)
		if err != nil {
			return nil, err
		}
		if err := w.Eject(); err != nil {
			return nil, err
		}
		return marshal(map[string]string{"ejected": w.label})

	case proto.MethodCancel:
		var p proto.DriveParams
		_ = json.Unmarshal(req.Params, &p)
		w, err := d.workerFor(p.Drive)
		if err != nil {
			return nil, err
		}
		if err := w.Cancel(); err != nil {
			return nil, err
		}
		return marshal(map[string]string{"cancelled": w.label})

	case proto.MethodForget:
		var p proto.FingerprintParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("forget needs a fingerprint")
		}
		if p.Fingerprint == "" {
			return nil, fmt.Errorf("forget needs a fingerprint")
		}
		if err := d.st.ForgetDisc(ctx, p.Fingerprint); err != nil {
			return nil, err
		}
		d.logEvent("info", "disc "+p.Fingerprint[:min(12, len(p.Fingerprint))]+" forgotten; it will be ripped again", nil, nil)
		return marshal(map[string]string{"forgot": p.Fingerprint})

	case proto.MethodDisc:
		var p proto.FingerprintParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Fingerprint == "" {
			return nil, fmt.Errorf("disc.get needs a fingerprint")
		}
		rec, err := d.st.FindDisc(ctx, p.Fingerprint)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, fmt.Errorf("no disc with fingerprint %s", p.Fingerprint)
		}
		return marshal(rec)

	case proto.MethodRescan:
		return d.rescan(ctx)

	case proto.MethodFailures:
		var p proto.LimitParams
		_ = json.Unmarshal(req.Params, &p)
		failures, err := d.st.Failures(ctx, p.Limit)
		if err != nil {
			return nil, err
		}
		return marshal(failures)

	case proto.MethodQueue:
		var p proto.LimitParams
		_ = json.Unmarshal(req.Params, &p)
		jobs, err := d.st.TranscodeJobs(ctx, p.Limit)
		if err != nil {
			return nil, err
		}
		return marshal(jobs)

	case proto.MethodQueueCancel:
		if err := d.CancelTranscode(); err != nil {
			return nil, err
		}
		return marshal(map[string]string{"cancelled": "the transcode in progress"})

	case proto.MethodQueueRetry:
		var p proto.JobParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.ID == 0 {
			return nil, fmt.Errorf("retry needs a job id")
		}
		job, err := d.st.TranscodeJobByID(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if job == nil {
			return nil, fmt.Errorf("no queued job %d", p.ID)
		}
		if job.State == store.TranscodeRunning {
			return nil, fmt.Errorf("job %d is running; cancel it first", p.ID)
		}
		// Attempts are cleared as well as the state, because asking for a retry
		// is a person overriding the budget that stopped it.
		if err := d.st.ResetTranscodeAttempts(ctx, p.ID); err != nil {
			return nil, err
		}
		d.refreshTranscodeCounts(ctx)
		d.nudgeTranscoder()
		return marshal(map[string]string{"requeued": fmt.Sprintf("title %d", job.TitleIndex)})

	case proto.MethodTranscode:
		var p proto.FingerprintParams
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Fingerprint == "" {
			return nil, fmt.Errorf("transcode needs a fingerprint")
		}
		n, name, err := d.requeueDisc(ctx, p.Fingerprint)
		if err != nil {
			return nil, err
		}
		return marshal(map[string]string{
			"queued": fmt.Sprintf("%d title%s of %s", n, plural(n), name),
		})

	case proto.MethodRetry:
		return nil, fmt.Errorf("retry is not implemented yet; eject and reinsert the disc")

	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}

// rescan re-detects drives and re-runs the health checks.
//
// It previously did only the latter, which meant the one command named for
// re-detection could not detect anything: a drive plugged in after startup
// stayed invisible until the 30-second discovery tick came round, and the
// operator had no way to hurry it.
func (d *Daemon) rescan(ctx context.Context) (json.RawMessage, error) {
	before := len(d.snapshotWorkers())

	// wg is nil when no daemon is running — `hellboxd -check` reaches this code
	// through the same health path. There is nothing to start workers onto, so
	// discovery is skipped rather than crashing.
	if d.wg != nil {
		if err := d.syncDrives(ctx, d.wg); err != nil {
			return nil, fmt.Errorf("drive discovery failed: %w", err)
		}
	}
	d.runHealthChecks(ctx)

	after := len(d.snapshotWorkers())
	detail := fmt.Sprintf("%d drive%s", after, plural(after))
	if added := after - before; added > 0 {
		detail += fmt.Sprintf(", %d newly found", added)
	}
	return marshal(map[string]string{"rescanned": detail})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// status assembles the full daemon snapshot.
func (d *Daemon) status(ctx context.Context) proto.Status {
	workers := d.snapshotWorkers()
	drives := make([]json.RawMessage, 0, len(workers))
	for _, w := range workers {
		if b, err := json.Marshal(w.Snapshot()); err == nil {
			drives = append(drives, b)
		}
	}

	d.mu.RLock()
	health := append([]proto.Health(nil), d.health...)
	d.mu.RUnlock()

	ripped, _ := d.st.CompletedCount(ctx)
	free, _ := freeBytes(d.cfg.RipsDir)

	return proto.Status{
		Version:         1,
		ProtocolVersion: proto.Version,
		StartedAt:       d.startedAt,
		Now:             time.Now(),
		Drives:          drives,
		Health:          health,
		Transcode:       d.transcodes.Snapshot(),
		DiscsRipped:     ripped,
		RipsDir:         d.cfg.RipsDir,
		FreeBytes:       free,
	}
}

func (d *Daemon) sendStatusTo(ctx context.Context, send func(any)) {
	b, err := json.Marshal(d.status(ctx))
	if err != nil {
		return
	}
	send(proto.Push{Event: proto.EventStatus, Data: b})
}

func (d *Daemon) broadcastStatus(ctx context.Context) {
	// The HTTP stream is fed first and unconditionally. The socket's early
	// return below is an optimisation for having no socket clients, and letting
	// it also skip the hub would mean the web client silently received nothing
	// whenever slay happened not to be running.
	st := d.status(ctx)
	d.publishEvent(api.EventDrives, st.Drives)

	d.subsMu.Lock()
	n := len(d.subs)
	d.subsMu.Unlock()
	if n == 0 {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	d.publishRaw(proto.Push{Event: proto.EventStatus, Data: b})
}

func (d *Daemon) subscribe() chan proto.Push {
	ch := make(chan proto.Push, 64)
	d.subsMu.Lock()
	d.subs[ch] = struct{}{}
	d.subsMu.Unlock()
	return ch
}

func (d *Daemon) unsubscribe(ch chan proto.Push) {
	d.subsMu.Lock()
	if _, ok := d.subs[ch]; ok {
		delete(d.subs, ch)
		close(ch)
	}
	d.subsMu.Unlock()
}

func (d *Daemon) publish(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	d.publishRaw(proto.Push{Event: event, Data: b})
}

// publishRaw fans a message out to subscribers, dropping it for any client that
// is not keeping up. A slow client must never stall a rip.
func (d *Daemon) publishRaw(p proto.Push) {
	d.subsMu.Lock()
	defer d.subsMu.Unlock()
	for ch := range d.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func marshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
