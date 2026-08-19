package proto

import "time"

// DriveState is where a drive is in the rip lifecycle. It is the state machine
// from the Phase 1 specification, and it is what the TUI renders.
type DriveState string

const (
	// StateAbsent means the drive could not be read at all — unplugged, or
	// dropped by the kernel.
	StateAbsent DriveState = "absent"

	// StateEmpty means the drive is closed and holds no disc.
	StateEmpty DriveState = "empty"

	// StateTrayOpen means the tray is open, waiting to be fed. Following the
	// convention that an open tray means the previous disc succeeded, this is
	// the resting state between discs.
	StateTrayOpen DriveState = "tray_open"

	// StateLoading means the tray is closing or the disc is spinning up.
	StateLoading DriveState = "loading"

	// StateIncompatible means a disc is loaded that this drive cannot read —
	// a Blu-ray in a DVD-only drive, most often.
	//
	// Not a failure: nothing was attempted and nothing went wrong. It needs a
	// person to swap the disc, so it is reported and then left alone rather
	// than retried.
	StateIncompatible DriveState = "incompatible"

	// StateQueued means a disc is waiting for another drive to finish.
	//
	// Only one disc is worked on at a time across every drive. A rip is bound
	// by the drive, a decrypt by the disc, and a transcode by the GPU — but
	// they all share one set of disks, and two at once makes each slower
	// without finishing any sooner.
	StateQueued DriveState = "queued"

	// StateScanning means the disc structure is being read.
	StateScanning DriveState = "scanning"

	// StateDecrypting means the disc is being copied to disk and decrypted
	// before it can be ripped.
	//
	// This is not the normal path. It happens when the drive cannot decrypt the
	// disc itself — an RPC-2 drive with no region set — and takes far longer
	// than the rip that follows, so it is shown as what it is rather than
	// folded into ripping.
	StateDecrypting DriveState = "decrypting"

	// StateRipping means titles are being written.
	StateRipping DriveState = "ripping"

	// StateVerifying means output files are being checked.
	StateVerifying DriveState = "verifying"

	// StateComplete means the rip finished and verified.
	StateComplete DriveState = "complete"

	// StateDuplicate means the disc was already ripped, so nothing was done.
	StateDuplicate DriveState = "duplicate"

	// StateFailed means the rip failed and the disc has been retained. The tray
	// stays closed: an open tray must only ever mean success, so that a disc
	// handed back can be reshelved without checking anything.
	StateFailed DriveState = "failed"

	// StateCancelled means a person stopped the work. The disc is untouched and
	// still in the drive.
	//
	// Distinct from FAILED because nothing is wrong with the disc, and distinct
	// from the resting states because there is still a disc in there. The tray
	// stays shut either way: an open tray means success, and this is not one.
	StateCancelled DriveState = "cancelled"

	// StateEjecting means the tray is being opened.
	StateEjecting DriveState = "ejecting"
)

// Terminal reports whether a state ends a disc's processing.
func (s DriveState) Terminal() bool {
	switch s {
	case StateComplete, StateDuplicate, StateFailed, StateIncompatible, StateCancelled:
		return true
	}
	return false
}

// Busy reports whether the drive is actively working on a disc.
func (s DriveState) Busy() bool {
	switch s {
	case StateQueued, StateScanning, StateDecrypting, StateRipping, StateVerifying, StateEjecting:
		return true
	}
	return false
}

// DriveSnapshot is the externally visible state of one drive. It is what the
// socket serves and the TUI renders, and it carries no pointers into daemon
// internals so it can be copied and serialised freely.
type DriveSnapshot struct {
	StableID   string     `json:"stable_id"`
	Label      string     `json:"label"`
	DevicePath string     `json:"device_path"`
	Model      string     `json:"model"`
	State      DriveState `json:"state"`
	Since      time.Time  `json:"since"`

	DiscLabel   string `json:"disc_label,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	RipDir      string `json:"rip_dir,omitempty"`

	TitleCount   int     `json:"title_count,omitempty"`
	TitlesDone   int     `json:"titles_done,omitempty"`
	CurrentTitle int     `json:"current_title,omitempty"`
	Fraction     float64 `json:"fraction,omitempty"`
	Operation    string  `json:"operation,omitempty"`

	// ETA is an estimate for the whole disc, derived from titles already
	// written. It is omitted until at least one title has completed, because an
	// estimate from no data is worse than none.
	ETASeconds int `json:"eta_seconds,omitempty"`

	Error   string `json:"error,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
}

// TranscodeSnapshot is the state of the transcode queue.
//
// Transcoding is not a property of any drive — it reads files and needs none —
// so it is reported separately rather than folded into a drive's state.
type TranscodeSnapshot struct {
	Running    bool    `json:"running"`
	Disc       string  `json:"disc,omitempty"`
	TitleIndex int     `json:"title_index,omitempty"`
	Fraction   float64 `json:"fraction,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	Hardware   bool    `json:"hardware,omitempty"`

	// Pending is jobs still to run; Failed is jobs given up on and waiting for
	// a person.
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// TranscodeJobSummary is one entry in the transcode queue, as the socket serves
// it and the client renders it.
type TranscodeJobSummary struct {
	ID         int64  `json:"id"`
	DiscID     int64  `json:"disc_id"`
	Disc       string `json:"disc"`
	TitleIndex int    `json:"title_index"`
	State      string `json:"state"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Attempt    int    `json:"attempt"`
	Hardware   bool   `json:"hardware,omitempty"`
	Error      string `json:"error,omitempty"`
	OutputPath string `json:"output_path,omitempty"`

	// Filed reports whether the result reached the library, which is the only
	// way to tell a transcode that finished from one that finished and arrived.
	Filed bool `json:"filed"`
}

// Failure is a disc that did not make it through, with why.
type Failure struct {
	When        time.Time `json:"when"`
	VolumeLabel string    `json:"volume_label"`
	Fingerprint string    `json:"fingerprint"`
	Kind        string    `json:"kind"`
	Advice      string    `json:"advice,omitempty"`
	Error       string    `json:"error"`
	Attempt     int       `json:"attempt"`
}

// Event is an entry in the activity log.
type Event struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	JobID   *int64    `json:"job_id,omitempty"`
	DriveID *int64    `json:"drive_id,omitempty"`
}

// JobSummary is a job joined with the disc and drive it concerns.
type JobSummary struct {
	ID            int64      `json:"id"`
	State         string     `json:"state"`
	Attempt       int        `json:"attempt"`
	CreatedAt     time.Time  `json:"created_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	TitlesDone    int        `json:"titles_done"`
	TitlesTotal   int        `json:"titles_total"`
	VolumeLabel   string     `json:"volume_label"`
	Fingerprint   string     `json:"fingerprint"`
	RipDir        string     `json:"rip_dir,omitempty"`
	DriveLabel    string     `json:"drive_label"`
	DriveStableID string     `json:"drive_stable_id"`
}
