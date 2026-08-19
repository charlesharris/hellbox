package daemon

import (
	"sync"
	"time"

	"hellbox/internal/proto"
)

// The drive state machine and its snapshot are wire types: the socket serves
// them and every client renders them. They live in proto so that a client can
// depend on the protocol alone, rather than on the daemon and everything it
// links — SQLite included. Aliased here so daemon code reads unchanged.
type (
	DriveState    = proto.DriveState
	DriveSnapshot = proto.DriveSnapshot
)

const (
	StateAbsent       = proto.StateAbsent
	StateEmpty        = proto.StateEmpty
	StateTrayOpen     = proto.StateTrayOpen
	StateLoading      = proto.StateLoading
	StateIncompatible = proto.StateIncompatible
	StateQueued       = proto.StateQueued
	StateScanning     = proto.StateScanning
	StateDecrypting   = proto.StateDecrypting
	StateRipping      = proto.StateRipping
	StateVerifying    = proto.StateVerifying
	StateComplete     = proto.StateComplete
	StateDuplicate    = proto.StateDuplicate
	StateFailed       = proto.StateFailed
	StateCancelled    = proto.StateCancelled
	StateEjecting     = proto.StateEjecting
)

// driveStatus is the mutable state a worker maintains, guarded for concurrent
// reads by the socket server.
type driveStatus struct {
	mu   sync.RWMutex
	snap DriveSnapshot

	// ripStarted and bytesDone drive the ETA estimate.
	ripStarted time.Time
	secsDone   int
	secsTotal  int
}

func newDriveStatus(stableID, label, devicePath, model string) *driveStatus {
	return &driveStatus{
		snap: DriveSnapshot{
			StableID:   stableID,
			Label:      label,
			DevicePath: devicePath,
			Model:      model,
			State:      StateEmpty,
			Since:      time.Now(),
		},
	}
}

// Snapshot returns a copy safe to hand to another goroutine.
func (d *driveStatus) Snapshot() DriveSnapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.snap
}

// setState moves to a new state, clearing per-disc fields when returning to
// rest so stale progress never lingers on screen.
func (d *driveStatus) setState(s DriveState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.snap.State == s {
		return
	}
	d.snap.State = s
	d.snap.Since = time.Now()

	switch s {
	case StateEmpty, StateTrayOpen, StateAbsent:
		d.snap.DiscLabel = ""
		d.snap.Fingerprint = ""
		d.snap.RipDir = ""
		d.snap.TitleCount = 0
		d.snap.TitlesDone = 0
		d.snap.CurrentTitle = 0
		d.snap.Fraction = 0
		d.snap.Operation = ""
		d.snap.ETASeconds = 0
		d.snap.Error = ""
		d.snap.Attempt = 0
		d.secsDone, d.secsTotal = 0, 0
	}
}

func (d *driveStatus) update(fn func(*DriveSnapshot)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(&d.snap)
}

// beginRip starts the ETA clock. secsTotal is the summed runtime of every title
// to be written, which is a far better predictor of rip time than file count:
// discs vary enormously in how many titles they carry and how long each runs.
func (d *driveStatus) beginRip(secsTotal int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ripStarted = time.Now()
	d.secsTotal = secsTotal
	d.secsDone = 0
}

// titleDone advances the ETA estimate after a title is written.
func (d *driveStatus) titleDone(durationSecs int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.secsDone += durationSecs
	d.snap.TitlesDone++
	d.snap.Fraction = 0
	d.snap.Operation = ""

	if d.secsDone <= 0 || d.secsTotal <= 0 || d.ripStarted.IsZero() {
		return
	}
	elapsed := time.Since(d.ripStarted).Seconds()
	rate := float64(d.secsDone) / elapsed // seconds of content per second of wall clock
	if rate <= 0 {
		return
	}
	remaining := float64(d.secsTotal-d.secsDone) / rate
	if remaining < 0 {
		remaining = 0
	}
	d.snap.ETASeconds = int(remaining)
}

// setProgress records within-title progress from makemkvcon.
func (d *driveStatus) setProgress(titleIndex int, fraction float64, operation string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snap.CurrentTitle = titleIndex
	d.snap.Fraction = fraction
	d.snap.Operation = operation
}
