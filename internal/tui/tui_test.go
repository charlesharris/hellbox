package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hellbox/internal/proto"
)

// The bug this guards against: a log entry and a job summary both carry an
// "id" field and each decodes cleanly into the other's type, so routing a
// response by payload shape silently filled the log view with jobs.
func TestApplyResultRoutesByMethod(t *testing.T) {
	events, err := json.Marshal([]proto.Event{
		{ID: 1, At: time.Now(), Level: "warn", Message: "a disc this drive cannot read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := json.Marshal([]proto.JobSummary{
		{ID: 1, State: "complete", VolumeLabel: "STILL GAME S1D1", TitlesDone: 6, TitlesTotal: 6},
	})
	if err != nil {
		t.Fatal(err)
	}

	m := Model{}
	m = m.applyResult(proto.MethodEvents, events)
	if len(m.events) != 1 || m.events[0].Message == "" {
		t.Errorf("events did not land in the log: %+v", m.events)
	}
	if len(m.jobs) != 0 {
		t.Errorf("events leaked into history: %+v", m.jobs)
	}

	m = m.applyResult(proto.MethodHistory, jobs)
	if len(m.jobs) != 1 || m.jobs[0].VolumeLabel == "" {
		t.Errorf("jobs did not land in history: %+v", m.jobs)
	}
	if len(m.events) != 1 {
		t.Errorf("history overwrote the log: %+v", m.events)
	}
}

func TestApplyResultAcknowledgement(t *testing.T) {
	m := Model{}
	m = m.applyResult(proto.MethodEject, json.RawMessage(`{"ejected":"top"}`))
	if !strings.Contains(m.flash, "top") {
		t.Errorf("flash = %q, want it to mention the drive", m.flash)
	}
	if m.flashErr {
		t.Error("a successful eject was reported as an error")
	}
}

func TestApplyStatusParsesDrives(t *testing.T) {
	drive, _ := json.Marshal(proto.DriveSnapshot{
		StableID: "usb-ASUS", Label: "top", DevicePath: "/dev/sr0",
		State: proto.StateRipping, TitleCount: 6, TitlesDone: 3, Fraction: 0.5,
	})
	status, _ := json.Marshal(proto.Status{
		Drives:      []json.RawMessage{drive},
		Health:      []proto.Health{{Name: "makemkv key", OK: true}},
		DiscsRipped: 41,
	})

	m := Model{}.applyStatus(status)
	if !m.haveAny {
		t.Error("status was not marked as received")
	}
	if len(m.drives) != 1 || m.drives[0].Label != "top" {
		t.Fatalf("drives = %+v", m.drives)
	}
	if m.drives[0].State != proto.StateRipping {
		t.Errorf("state = %q", m.drives[0].State)
	}
}

// A selection must not survive the drive it pointed at going away.
func TestApplyStatusClampsSelection(t *testing.T) {
	two := func(n int) json.RawMessage {
		drives := make([]json.RawMessage, n)
		for i := range drives {
			drives[i], _ = json.Marshal(proto.DriveSnapshot{StableID: string(rune('a' + i))})
		}
		b, _ := json.Marshal(proto.Status{Drives: drives})
		return b
	}

	m := Model{selected: 1}.applyStatus(two(2))
	if m.selected != 1 {
		t.Errorf("selection moved unnecessarily: %d", m.selected)
	}
	m = m.applyStatus(two(1))
	if m.selected != 0 {
		t.Errorf("selection = %d after the drive count fell to 1, want 0", m.selected)
	}
	m = m.applyStatus(two(0))
	if m.selected != 0 {
		t.Errorf("selection = %d with no drives, want 0", m.selected)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in, want string
		n        int
	}{
		{"hello", "hello", 5},
		// The ellipsis occupies one of the n columns, so the result is never
		// wider than asked for — the whole point, since these are laid out
		// against a fixed terminal width.
		{"hello", "hel…", 4},
		{"hello", "", 0},
		{"hello", "h", 1},
		{"", "", 5},
	}
	for _, tt := range tests {
		got := truncate(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
		if len([]rune(got)) > tt.n {
			t.Errorf("truncate(%q, %d) = %q, wider than %d columns", tt.in, tt.n, got, tt.n)
		}
	}
}

func TestPad(t *testing.T) {
	if got := pad("ab", 5); got != "ab   " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("abcdef", 3); got != "abcdef" {
		t.Errorf("pad must never shorten: %q", got)
	}
}

// humanBytes must agree with the daemon exactly; the same number shown two
// ways would read as a bug in one of them.
func TestHumanBytesMatchesDaemon(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KB"},
		{897_000_000_000, "835.4 GB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.in); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProgressBar(t *testing.T) {
	// Out-of-range fractions must not produce a negative repeat count, which
	// would panic rather than merely look wrong.
	for _, f := range []float64{-1, 0, 0.5, 1, 2} {
		_ = progressBar(f, 20)
	}
}

// Every state must render something, and a resting drive must not read as a
// stuck one.
func TestStateHintsCoverEveryState(t *testing.T) {
	states := []proto.DriveState{
		proto.StateAbsent, proto.StateEmpty, proto.StateTrayOpen, proto.StateLoading,
		proto.StateIncompatible, proto.StateScanning, proto.StateRipping,
		proto.StateVerifying, proto.StateComplete, proto.StateDuplicate,
		proto.StateFailed, proto.StateEjecting,
	}
	for _, s := range states {
		if discPlaceholder(s) == "" {
			t.Errorf("state %q has no disc placeholder", s)
		}
	}

	// The states a drive rests in must all explain themselves.
	for _, s := range []proto.DriveState{
		proto.StateEmpty, proto.StateTrayOpen, proto.StateComplete,
		proto.StateDuplicate, proto.StateIncompatible, proto.StateFailed,
	} {
		if stateHint(s) == "" {
			t.Errorf("resting state %q gives the operator no hint", s)
		}
	}
}

// The view must render at any size without panicking, including sizes too
// small to fit anything.
func TestViewRendersAtAnySize(t *testing.T) {
	drive, _ := json.Marshal(proto.DriveSnapshot{
		StableID: "usb-ASUS", Label: "top", DevicePath: "/dev/sr0",
		State: proto.StateRipping, TitleCount: 6, TitlesDone: 3,
		Fraction: 0.58, ETASeconds: 720, Operation: "Saving title 4",
	})
	status, _ := json.Marshal(proto.Status{
		Drives: []json.RawMessage{drive},
		Health: []proto.Health{
			{Name: "makemkv key", OK: false, Fatal: true, Detail: "registration key has expired"},
		},
		DiscsRipped: 41, FreeBytes: 897_000_000_000,
	})

	base := Model{version: "0.1.0", connected: true}.applyStatus(status)
	base.events = []proto.Event{{At: time.Now(), Level: "warn", Message: strings.Repeat("long ", 60)}}
	base.jobs = []proto.JobSummary{{ID: 1, State: "failed", VolumeLabel: strings.Repeat("x", 90), Error: "boom"}}

	for _, size := range [][2]int{{100, 30}, {80, 24}, {40, 10}, {20, 6}, {1, 1}} {
		for _, v := range []view{viewDrives, viewHistory, viewLog} {
			m := base
			m.width, m.height, m.view = size[0], size[1], v
			out := m.View()
			if out == "" {
				t.Errorf("view %d at %dx%d rendered nothing", v, size[0], size[1])
			}
		}
	}
}

// A fatal health problem must be visible on every screen, not just the drives
// panel — an expired key is the likeliest cause of a mystery failure.
func TestHealthBannerAlwaysVisible(t *testing.T) {
	status, _ := json.Marshal(proto.Status{
		Health: []proto.Health{
			{Name: "makemkv key", OK: false, Fatal: true, Detail: "registration key has expired"},
		},
	})
	base := Model{version: "0.1.0", width: 100, height: 30}.applyStatus(status)

	for _, v := range []view{viewDrives, viewHistory, viewLog} {
		m := base
		m.view = v
		if !strings.Contains(m.View(), "registration key has expired") {
			t.Errorf("view %d hid a fatal health problem", v)
		}
	}
}

// Forgetting was reachable over the socket and nowhere else, which left
// hand-writing JSON to a unix socket as the only way to re-rip a disc.
func TestForgetTargetUsesTheDiscInTheDrive(t *testing.T) {
	const fp = "5d7fd5a1507a4dc06e88c7f50d90975dea2999551ff50612ff1c3a8010df8980"
	m := Model{drives: []proto.DriveSnapshot{{
		StableID:    "usb-ASUS-1",
		Label:       "top",
		State:       proto.StateFailed,
		DiscLabel:   "ROMAN_HOLIDAY",
		Fingerprint: fp,
	}}}

	got, refusal := m.forgetTarget()
	if refusal != "" {
		t.Fatalf("refused a failed disc: %q", refusal)
	}
	if got != fp {
		t.Errorf("forgetTarget() = %q, want the disc's own fingerprint", got)
	}
}

// A drive with no disc has no fingerprint, and sending an empty one would draw
// an error from the daemon rather than an answer.
func TestForgetTargetRefusesAnEmptyDrive(t *testing.T) {
	m := Model{drives: []proto.DriveSnapshot{{StableID: "usb-ASUS-1", Label: "top", State: proto.StateEmpty}}}

	got, refusal := m.forgetTarget()
	if got != "" {
		t.Errorf("forgetTarget() = %q for an empty drive", got)
	}
	if !strings.Contains(refusal, "top") {
		t.Errorf("refusal %q does not name the drive", refusal)
	}
}

// Forgetting mid-rip would clear the attempt count of the rip still running,
// which is then recorded again when it finishes — misleading and pointless.
func TestForgetTargetRefusesABusyDrive(t *testing.T) {
	for _, st := range []proto.DriveState{
		proto.StateScanning, proto.StateDecrypting, proto.StateRipping, proto.StateVerifying,
	} {
		m := Model{drives: []proto.DriveSnapshot{{
			StableID: "usb-ASUS-1", Label: "top", State: st, Fingerprint: "abc123",
		}}}
		got, refusal := m.forgetTarget()
		if got != "" {
			t.Errorf("forgetTarget() = %q during %s", got, st)
		}
		if refusal == "" {
			t.Errorf("no refusal shown for %s", st)
		}
	}
}

// No drives at all is not an error, just nothing to do.
func TestForgetTargetWithNoDrives(t *testing.T) {
	got, refusal := Model{}.forgetTarget()
	if got != "" || refusal != "" {
		t.Errorf("forgetTarget() = %q, %q; want both empty", got, refusal)
	}
}

// The ack carries 64 characters of hex, which tells the operator nothing.
func TestForgetAcknowledgementIsLegible(t *testing.T) {
	m := Model{}.applyResult(proto.MethodForget,
		json.RawMessage(`{"forgot":"5d7fd5a1507a4dc06e88c7f50d90975dea2999551ff50612ff1c3a8010df8980"}`))

	if m.flashErr {
		t.Error("a successful forget was reported as an error")
	}
	if strings.Contains(m.flash, "5d7fd5a1507a") {
		t.Errorf("flash %q shows the raw fingerprint", m.flash)
	}
	if !strings.Contains(m.flash, "ripped again") {
		t.Errorf("flash %q does not say what forgetting achieved", m.flash)
	}
}

// An idle queue must cost no space. Most of the time there is nothing to
// transcode, and a permanent empty line would push the drives down the screen
// for no reason.
func TestTranscodeLineIsEmptyWhenIdle(t *testing.T) {
	m := Model{status: proto.Status{Transcode: proto.TranscodeSnapshot{}}}
	if got := m.transcodeLine(); got != "" {
		t.Errorf("transcodeLine() = %q for an idle queue, want empty", got)
	}
}

func TestTranscodeLineReportsWork(t *testing.T) {
	m := Model{status: proto.Status{Transcode: proto.TranscodeSnapshot{
		Running: true, Disc: "ROMAN_HOLIDAY", TitleIndex: 3,
		Fraction: 0.42, Speed: 23.4, Hardware: true, Pending: 4,
	}}}

	got := m.transcodeLine()
	for _, want := range []string{"ROMAN_HOLIDAY", "title 3", "42%", "23x", "gpu", "4 queued"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcodeLine() missing %q:\n%s", want, got)
		}
	}
}

// A queue that has given up on something has to say so, or the files silently
// never appear and nothing explains why.
func TestTranscodeLineReportsFailuresWhenNotRunning(t *testing.T) {
	m := Model{status: proto.Status{Transcode: proto.TranscodeSnapshot{Failed: 2}}}
	if got := m.transcodeLine(); !strings.Contains(got, "2 failed") {
		t.Errorf("transcodeLine() = %q, want it to report the failures", got)
	}
}

// Software encoding is several times slower, so which one is running is worth
// showing rather than leaving to be inferred from the speed.
func TestTranscodeLineDistinguishesSoftware(t *testing.T) {
	m := Model{status: proto.Status{Transcode: proto.TranscodeSnapshot{
		Running: true, Disc: "X", Fraction: 0.1, Speed: 4.8, Hardware: false,
	}}}
	got := m.transcodeLine()
	if !strings.Contains(got, "sw") || strings.Contains(got, "gpu") {
		t.Errorf("transcodeLine() = %q, want it to show software encoding", got)
	}
}

// The queue view is the answer to "what is left", so waiting and running work
// has to be visible without hunting for it.
func TestQueueViewListsJobs(t *testing.T) {
	m := Model{view: viewQueue, width: 100, queue: []proto.TranscodeJobSummary{
		{ID: 1, Disc: "STILL_GAME", TitleIndex: 0, State: "running", Attempt: 1},
		{ID: 2, Disc: "STILL_GAME", TitleIndex: 1, State: "pending"},
		{ID: 3, Disc: "ROMAN_HOLIDAY", TitleIndex: 0, State: "complete", SizeBytes: 1 << 30, Filed: true},
	}}

	got := m.queueView()
	for _, want := range []string{"STILL_GAME", "ROMAN_HOLIDAY", "t00", "t01", "running", "pending", "complete"} {
		if !strings.Contains(got, want) {
			t.Errorf("queueView() missing %q:\n%s", want, got)
		}
	}
}

// A transcode that finished but never reached the library is the case that
// would otherwise be invisible: the file simply never appears and nothing
// explains why. It was exactly this that hid a film from Jellyfin.
func TestQueueViewFlagsFinishedButUnfiled(t *testing.T) {
	m := Model{view: viewQueue, width: 100, queue: []proto.TranscodeJobSummary{
		{ID: 1, Disc: "ROMAN_HOLIDAY", TitleIndex: 0, State: "complete", SizeBytes: 1 << 30, Filed: false},
	}}

	got := m.queueView()
	if !strings.Contains(got, "not filed") {
		t.Errorf("a finished-but-unfiled transcode is shown as ordinary:\n%s", got)
	}
}

// The error is why a job failed, and is the only thing that makes a failure
// actionable.
func TestQueueViewShowsTheErrorForTheSelectedJob(t *testing.T) {
	m := Model{view: viewQueue, width: 120, selected: 0, queue: []proto.TranscodeJobSummary{
		{ID: 1, Disc: "X", State: "failed", Error: "ffmpeg: no such file"},
	}}
	if got := m.queueView(); !strings.Contains(got, "no such file") {
		t.Errorf("selected failure does not show its reason:\n%s", got)
	}
}

func TestQueueViewWhenEmpty(t *testing.T) {
	m := Model{view: viewQueue, width: 80}
	if got := m.queueView(); !strings.Contains(got, "empty") {
		t.Errorf("queueView() = %q for an empty queue", got)
	}
}

// Selection has to follow whichever view is showing, or arrow keys move a
// hidden cursor through the drives while the queue is on screen.
func TestRowCountFollowsTheView(t *testing.T) {
	m := Model{
		drives: []proto.DriveSnapshot{{StableID: "a"}, {StableID: "b"}},
		queue:  []proto.TranscodeJobSummary{{ID: 1}, {ID: 2}, {ID: 3}},
	}
	if got := m.rowCount(); got != 2 {
		t.Errorf("rowCount() on the drives view = %d, want 2", got)
	}
	m.view = viewQueue
	if got := m.rowCount(); got != 3 {
		t.Errorf("rowCount() on the queue view = %d, want 3", got)
	}
}

// The count is the point of grouping: "three discs failed" says nothing to act
// on, while "three were not in the AACS key database" says to update it.
func TestFailuresViewGroupsByKind(t *testing.T) {
	m := Model{view: viewFailures, width: 110, height: 30, failures: []proto.Failure{
		{Kind: "aacs-key", VolumeLabel: "SOME BLURAY", Error: "no valid processing key",
			Advice: "the disc is not in the AACS key database; try a newer KEYDB.cfg"},
		{Kind: "aacs-key", VolumeLabel: "ANOTHER", Error: "no valid processing key"},
		{Kind: "cancelled", VolumeLabel: "STILL_GAME", Error: "cancelled",
			Advice: "stopped deliberately; nothing is wrong with the disc"},
	}}

	got := m.failuresView()
	if !strings.Contains(got, "aacs-key") || !strings.Contains(got, "KEYDB.cfg") {
		t.Errorf("failuresView() does not group and advise:\n%s", got)
	}
	// The commonest group leads, because that is the pattern worth acting on.
	if strings.Index(got, "aacs-key") > strings.Index(got, "cancelled") {
		t.Errorf("groups not ordered by count:\n%s", got)
	}
}

func TestFailuresViewWhenNothingFailed(t *testing.T) {
	m := Model{view: viewFailures, width: 80, height: 24}
	if got := m.failuresView(); !strings.Contains(got, "nothing has failed") {
		t.Errorf("failuresView() = %q for an empty list", got)
	}
}

// A cancellation is not a failure. Shown, because it arrives through the same
// field and omitting it would make the list quietly incomplete — but it must
// not read as a problem.
func TestFailuresViewDoesNotAlarmAboutCancellations(t *testing.T) {
	m := Model{view: viewFailures, width: 100, height: 24, failures: []proto.Failure{
		{Kind: "cancelled", VolumeLabel: "X", Error: "cancelled",
			Advice: "stopped deliberately; nothing is wrong with the disc"},
	}}
	got := m.failuresView()
	if !strings.Contains(got, "nothing is wrong") {
		t.Errorf("a cancellation is not explained as harmless:\n%s", got)
	}
}
