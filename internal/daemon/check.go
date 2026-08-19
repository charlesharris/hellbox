package daemon

import (
	"context"
	"fmt"
	"io"

	"hellbox/internal/drive"
	"hellbox/internal/proto"
)

// Check runs the startup health checks and reports them, without starting the
// daemon. This is what `hellboxd -check` runs, and it is the first thing to
// reach for when a rip fails for no apparent reason.
func (d *Daemon) Check(ctx context.Context, out io.Writer) error {
	if err := d.prepareDirs(); err != nil {
		return err
	}

	// Register drives before running the checks so the drive count reports the
	// truth. No workers are started: Check never touches a disc.
	found, err := drive.Discover()
	if err != nil {
		fmt.Fprintf(out, "  FAIL  drives: %v\n", err)
	}
	for _, dr := range found {
		state := "unreadable"
		if status, err := dr.Status(); err == nil {
			state = status.String()
		} else {
			state += ": " + err.Error()
		}

		d.mu.Lock()
		d.workers[dr.StableID] = NewWorker(dr, defaultLabel(dr), d.cfg, d.st, d.mk, d.dec, d.enc, 0, nil, discardLog, nil, nil)
		d.mu.Unlock()

		fmt.Fprintf(out, "  drive  %s\n         stable id: %s\n         state: %s\n",
			dr.Describe(), dr.StableID, state)
	}
	if len(found) == 0 {
		fmt.Fprintln(out, "  drive  none detected")
	}
	fmt.Fprintln(out)

	d.runHealthChecks(ctx)

	d.mu.RLock()
	health := append([]proto.Health(nil), d.health...)
	d.mu.RUnlock()

	var fatal bool
	for _, h := range health {
		mark := "ok  "
		switch {
		case h.OK:
		case h.Fatal:
			mark, fatal = "FAIL", true
		default:
			mark = "warn"
		}
		fmt.Fprintf(out, "  %-4s  %-16s %s\n", mark, h.Name, h.Detail)
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "  rips dir    %s\n", d.cfg.RipsDir)
	fmt.Fprintf(out, "  state db    %s\n", d.cfg.StatePath)
	fmt.Fprintf(out, "  socket      %s\n", d.cfg.SocketPath)

	if fatal {
		return fmt.Errorf("startup checks failed")
	}
	return nil
}

// discardLog satisfies the worker's logger where events are not wanted.
func discardLog(level, message string, driveID, jobID *int64) {}
