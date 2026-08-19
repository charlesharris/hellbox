// Package proto defines the wire format between hellboxd and its clients.
//
// The transport is newline-delimited JSON over a unix socket. That is enough
// for a request/response exchange plus server-pushed events, streams naturally,
// and needs no RPC framework. Clients never open the state database directly:
// everything goes through here, which keeps the daemon the single writer and
// leaves room for a remote client later.
package proto

import (
	"encoding/json"
	"time"
)

// Version is the protocol version. Clients check it on connect so a daemon and
// a client from different builds fail loudly instead of misinterpreting fields.
const Version = 1

// Method names understood by the daemon.
const (
	MethodStatus    = "status"
	MethodSubscribe = "subscribe"
	MethodHistory   = "history"
	MethodEvents    = "events"
	MethodDisc      = "disc.get"
	MethodEject     = "eject"
	MethodRetry     = "retry"
	MethodCancel    = "cancel"
	MethodForget    = "forget"
	MethodRescan    = "rescan"
	MethodTranscode = "transcode"

	// Queue methods. Transcoding is not a property of any drive, so these take
	// a job id rather than a drive.
	MethodQueue       = "queue"
	MethodQueueCancel = "queue.cancel"
	MethodQueueRetry  = "queue.retry"
	MethodFailures    = "failures"
)

// Request is a client call.
type Request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the daemon's reply to a Request.
type Response struct {
	ID     int             `json:"id"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Push is an unsolicited message sent to subscribed clients. It carries no id.
type Push struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Push event names.
const (
	EventStatus = "status"
	EventLog    = "log"
)

// DriveParams selects a drive by stable id or by its operator-assigned label.
type DriveParams struct {
	Drive string `json:"drive"`
}

// JobParams selects one queued job.
type JobParams struct {
	ID int64 `json:"id"`
}

// FingerprintParams selects a disc.
type FingerprintParams struct {
	Fingerprint string `json:"fingerprint"`
}

// LimitParams bounds a listing.
type LimitParams struct {
	Limit int `json:"limit"`
}

// Health is one startup or periodic check.
//
// These are reported rather than merely logged because the failure they most
// often describe — a lapsed MakeMKV registration key — presents as an
// inexplicable rip failure. Naming the real cause is a large part of being
// easier to live with than the tool this replaces.
type Health struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`

	// Fatal marks a check that prevents ripping entirely, as opposed to a
	// warning worth showing but not worth stopping for.
	Fatal bool `json:"fatal"`
}

// Status is the full daemon snapshot.
type Status struct {
	Version         int       `json:"version"`
	ProtocolVersion int       `json:"protocol_version"`
	StartedAt       time.Time `json:"started_at"`
	Now             time.Time `json:"now"`

	Drives []json.RawMessage `json:"drives"`
	Health []Health          `json:"health"`

	Transcode TranscodeSnapshot `json:"transcode"`

	DiscsRipped int    `json:"discs_ripped"`
	RipsDir     string `json:"rips_dir"`
	FreeBytes   uint64 `json:"free_bytes"`
}

// Healthy reports whether nothing fatal is wrong.
func (s Status) Healthy() bool {
	for _, h := range s.Health {
		if !h.OK && h.Fatal {
			return false
		}
	}
	return true
}
