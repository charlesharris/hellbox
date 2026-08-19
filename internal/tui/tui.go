// Package tui renders the hellbox terminal client.
//
// The client is stateless and holds nothing the daemon does not: it may be
// started, killed and restarted freely while a rip runs. Everything on screen
// comes from the socket.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"hellbox/internal/client"
	"hellbox/internal/proto"
)

// view is which panel is on screen.
type view int

const (
	viewDrives view = iota
	viewHistory
	viewLog
	viewQueue
	viewFailures
)

// Model is the Bubble Tea model for `slay`.
type Model struct {
	cli     *client.Client
	version string

	status   proto.Status
	drives   []proto.DriveSnapshot
	jobs     []proto.JobSummary
	queue    []proto.TranscodeJobSummary
	failures []proto.Failure
	events   []proto.Event
	haveAny  bool // a status has arrived at least once

	view      view
	selected  int
	connected bool
	quitting  bool

	// pending maps an in-flight request id to the method that sent it, so its
	// reply can be decoded as the right type.
	pending map[int]string

	// flash is a transient line for the result of a command, cleared on the
	// next one. Errors from the daemon are shown here rather than swallowed.
	flash    string
	flashErr bool

	width, height int
	lastErr       string
}

// New creates the model. The client must not be running yet.
func New(cli *client.Client, version string) Model {
	return Model{cli: cli, version: version, width: 80, height: 24}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(waitFor(m.cli), tick())
}

// message types delivered into the Bubble Tea loop.
type (
	daemonMsg client.Message
	tickMsg   time.Time
)

// waitFor turns the client's channel into a Bubble Tea command. Each received
// message schedules the next wait, which is the idiomatic way to bridge a
// channel into the update loop.
func waitFor(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-c.Messages()
		if !ok {
			return tea.Quit()
		}
		return daemonMsg(msg)
	}
}

// tick drives the relative-time display ("3m ago") so it stays honest between
// pushes. The daemon pushes status on its own schedule during a rip; this only
// keeps the clock moving when nothing is happening.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tick()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case daemonMsg:
		m = m.handleDaemon(client.Message(msg))
		return m, waitFor(m.cli)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		// Marked before closing so the disconnect the close itself causes is
		// not painted as a lost connection on the way out.
		m.quitting = true
		m.cli.Close()
		return m, tea.Quit

	case "d":
		m.view = viewDrives
	case "h":
		m.view = viewHistory
		m.request(proto.MethodHistory, proto.LimitParams{Limit: 50})
	case "l":
		m.view = viewLog
		m.request(proto.MethodEvents, proto.LimitParams{Limit: 200})
	case "x":
		m.view = viewFailures
		m.request(proto.MethodFailures, proto.LimitParams{Limit: 100})
	case "t":
		m.view = viewQueue
		m.request(proto.MethodQueue, proto.LimitParams{Limit: 100})

	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected < m.rowCount()-1 {
			m.selected++
		}

	case "e":
		if d, ok := m.current(); ok {
			m.request(proto.MethodEject, proto.DriveParams{Drive: d.StableID})
		}
	case "r":
		if m.view == viewQueue {
			if j, ok := m.currentJob(); ok {
				m.request(proto.MethodQueueRetry, proto.JobParams{ID: j.ID})
			}
			break
		}
		if d, ok := m.current(); ok {
			m.request(proto.MethodRetry, proto.DriveParams{Drive: d.StableID})
		}
	case "c":
		// In the queue, cancel means the encode; on a drive, the rip. Each view
		// acts on what it is showing.
		if m.view == viewQueue {
			m.request(proto.MethodQueueCancel, nil)
			break
		}
		if d, ok := m.current(); ok {
			m.request(proto.MethodCancel, proto.DriveParams{Drive: d.StableID})
		}
	case "T":
		// Re-encode the disc in this drive from its rip. The point of keeping
		// raw rips is that this costs minutes and no disc.
		if d, ok := m.current(); ok {
			if d.Fingerprint == "" {
				m.flash, m.flashErr = "no disc in "+d.Label+" to transcode", true
			} else {
				m.request(proto.MethodTranscode, proto.FingerprintParams{Fingerprint: d.Fingerprint})
			}
		}

	case "f":
		if fingerprint, refusal := m.forgetTarget(); refusal != "" {
			m.flash, m.flashErr = refusal, true
		} else if fingerprint != "" {
			m.request(proto.MethodForget, proto.FingerprintParams{Fingerprint: fingerprint})
		}
	case "R":
		m.request(proto.MethodRescan, nil)
	}
	return m, nil
}

// request sends a command and remembers what it was, so the reply can be
// decoded as the right type. A transport failure is reported in the flash line
// rather than discarded.
func (m *Model) request(method string, params any) {
	id, err := m.cli.Send(method, params)
	if err != nil {
		m.flash, m.flashErr = err.Error(), true
		return
	}
	if m.pending == nil {
		m.pending = map[int]string{}
	}
	m.pending[id] = method
}

// forgetTarget decides what pressing `f` should forget: the fingerprint to
// send, or a refusal to show instead.
//
// Forgetting is the only way to rip a disc that has already been ripped, or one
// that has used up max_rip_attempts. It was reachable over the socket and
// nowhere else, which left hand-writing JSON to a unix socket as the only way
// to do something the client is otherwise built for.
//
// Split out from the key handler so the decision can be tested without a
// daemon: everything interesting about it is in these three cases.
func (m Model) forgetTarget() (fingerprint, refusal string) {
	d, ok := m.current()
	if !ok {
		return "", ""
	}
	switch {
	case d.Fingerprint == "":
		return "", "no disc in " + d.Label + " to forget"
	case d.State.Busy():
		// Forgetting mid-rip would clear the attempt count of the very rip
		// still running, and the disc is going to be recorded again when it
		// finishes — so it would do nothing except mislead.
		return "", d.Label + " is busy; cancel it first"
	default:
		return d.Fingerprint, ""
	}
}

// rowCount is how many rows the current view can select between.
func (m Model) rowCount() int {
	if m.view == viewQueue {
		return len(m.queue)
	}
	return len(m.drives)
}

// currentJob returns the selected queue entry.
func (m Model) currentJob() (proto.TranscodeJobSummary, bool) {
	if m.selected < 0 || m.selected >= len(m.queue) {
		return proto.TranscodeJobSummary{}, false
	}
	return m.queue[m.selected], true
}

func (m Model) current() (proto.DriveSnapshot, bool) {
	if m.selected < 0 || m.selected >= len(m.drives) {
		return proto.DriveSnapshot{}, false
	}
	return m.drives[m.selected], true
}

func (m Model) handleDaemon(msg client.Message) Model {
	if m.quitting {
		return m
	}
	switch {
	case msg.Connected:
		m.connected, m.lastErr = true, ""
		m.flash, m.flashErr = "connected", false

	case msg.Disconnected:
		m.connected = false
		m.flash, m.flashErr = "lost the connection to hellboxd — reconnecting", true

	case msg.Err != nil:
		m.lastErr = msg.Err.Error()

	case msg.Push != nil:
		if msg.Push.Event == proto.EventStatus {
			m = m.applyStatus(msg.Push.Data)
		}

	case msg.Response != nil:
		r := msg.Response
		method := m.pending[r.ID]
		delete(m.pending, r.ID)
		switch {
		case !r.OK:
			m.flash, m.flashErr = r.Error, true
		case len(r.Result) > 0:
			m = m.applyResult(method, r.Result)
		}
	}
	return m
}

// applyResult decodes a response as whatever its request asked for.
//
// Routing by payload shape does not work here: a log entry and a job summary
// both carry an "id" field, and each decodes cleanly into the other's type, so
// the log view silently filled with jobs. The request id is the only reliable
// discriminator.
func (m Model) applyResult(method string, raw json.RawMessage) Model {
	switch method {
	case proto.MethodHistory:
		var jobs []proto.JobSummary
		if err := json.Unmarshal(raw, &jobs); err != nil {
			m.lastErr = "could not read history: " + err.Error()
			return m
		}
		m.jobs = jobs

	case proto.MethodEvents:
		var events []proto.Event
		if err := json.Unmarshal(raw, &events); err != nil {
			m.lastErr = "could not read the log: " + err.Error()
			return m
		}
		m.events = events

	case proto.MethodFailures:
		var fs []proto.Failure
		if err := json.Unmarshal(raw, &fs); err != nil {
			m.lastErr = "could not read failures: " + err.Error()
			return m
		}
		m.failures = fs

	case proto.MethodQueue:
		var jobs []proto.TranscodeJobSummary
		if err := json.Unmarshal(raw, &jobs); err != nil {
			m.lastErr = "could not read the queue: " + err.Error()
			return m
		}
		m.queue = jobs
		if m.selected >= len(jobs) {
			m.selected = 0
		}

	case proto.MethodTranscode:
		var ack map[string]string
		if err := json.Unmarshal(raw, &ack); err == nil {
			m.flash, m.flashErr = "queued "+ack["queued"]+" for transcoding", false
		}

	case proto.MethodForget:
		// The ack carries the whole fingerprint, which is 64 characters of hex
		// and tells the operator nothing. What they need to know is that the
		// disc will be read again.
		var ack map[string]string
		if err := json.Unmarshal(raw, &ack); err == nil {
			m.flash, m.flashErr = "forgotten — this disc will be ripped again", false
		}

	default:
		// eject, retry, cancel and rescan all answer with a one-entry object
		// describing what they did.
		var ack map[string]string
		if err := json.Unmarshal(raw, &ack); err == nil {
			for k, v := range ack {
				m.flash, m.flashErr = fmt.Sprintf("%s %s", k, v), false
			}
		}
	}
	return m
}

func (m Model) applyStatus(raw json.RawMessage) Model {
	var s proto.Status
	if err := json.Unmarshal(raw, &s); err != nil {
		m.lastErr = "could not read status: " + err.Error()
		return m
	}
	m.status = s
	m.haveAny = true

	drives := make([]proto.DriveSnapshot, 0, len(s.Drives))
	for _, d := range s.Drives {
		var snap proto.DriveSnapshot
		if err := json.Unmarshal(d, &snap); err == nil {
			drives = append(drives, snap)
		}
	}
	m.drives = drives
	if m.selected >= len(m.drives) {
		m.selected = max(0, len(m.drives)-1)
	}
	return m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Run starts the client and the terminal program together.
func Run(socketPath, version string) error {
	cli := client.New(socketPath)
	go cli.Run()
	defer cli.Close()

	p := tea.NewProgram(New(cli, version), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// truncate shortens s to n display columns, marking that it was cut.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(r))
}
