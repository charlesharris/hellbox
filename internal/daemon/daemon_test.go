package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hellbox/internal/config"
	"hellbox/internal/decrypt"
	"hellbox/internal/disc"
	"hellbox/internal/drive"
	"hellbox/internal/makemkv"
	"hellbox/internal/proto"
	"hellbox/internal/store"
	"hellbox/internal/transcode"
)

// testDaemon builds a daemon backed by a throwaway database, with everything
// pointed inside a temporary directory. No drive is touched.
func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Default()
	cfg.RipsDir = filepath.Join(dir, "rips")
	cfg.WorkDir = filepath.Join(dir, "work")
	cfg.StatePath = filepath.Join(dir, "state.db")
	cfg.SocketPath = filepath.Join(dir, "hellbox.sock")
	cfg.MakeMKVSettingsPath = filepath.Join(dir, "mk", ".MakeMKV", "settings.conf")
	cfg.AutoRefreshKey = false

	// Pointed at nothing on purpose. The health checks otherwise invoke the
	// real makemkvcon, which makes these tests slow and, worse, dependent on
	// whether MakeMKV happens to be installed on the machine running them.
	cfg.MakeMKVBin = filepath.Join(dir, "no-such-makemkvcon")

	st, err := store.Open(cfg.StatePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	d := New(cfg, st)
	if err := d.prepareDirs(); err != nil {
		t.Fatalf("prepareDirs: %v", err)
	}
	return d
}

func addWorker(d *Daemon, stableID, label, devPath string) *Worker {
	w := NewWorker(
		drive.Drive{StableID: stableID, DevicePath: devPath, Vendor: "ASUS", Model: "SDRW-08D2S-U"},
		label, d.cfg, d.st, makemkv.New("/nonexistent", ""), decrypt.New("/nonexistent"), transcode.New("/nonexistent", ""), 0, nil, discardLog, nil, nil)
	d.workers[stableID] = w
	return w
}

func TestWorkerFor(t *testing.T) {
	d := testDaemon(t)
	addWorker(d, "usb-ASUS-1", "top", "/dev/sr0")

	t.Run("by stable id", func(t *testing.T) {
		if w, err := d.workerFor("usb-ASUS-1"); err != nil || w.label != "top" {
			t.Errorf("workerFor(stable id) = %v, %v", w, err)
		}
	})

	t.Run("by label", func(t *testing.T) {
		if w, err := d.workerFor("top"); err != nil || w.label != "top" {
			t.Errorf("workerFor(label) = %v, %v", w, err)
		}
	})

	// Labels are what a person types, and they type them however they like.
	t.Run("by label, any case", func(t *testing.T) {
		if _, err := d.workerFor("TOP"); err != nil {
			t.Errorf("workerFor(\"TOP\"): %v", err)
		}
	})

	// With one drive, naming it is pointless ceremony.
	t.Run("empty name with a single drive", func(t *testing.T) {
		if w, err := d.workerFor(""); err != nil || w.label != "top" {
			t.Errorf("workerFor(\"\") = %v, %v", w, err)
		}
	})

	t.Run("unknown name is an error", func(t *testing.T) {
		if _, err := d.workerFor("nope"); err == nil {
			t.Error("an unknown drive name resolved to a worker")
		}
	})
}

// With several drives, an unnamed command must not silently pick one — ejecting
// or cancelling the wrong drive is exactly the surprise to avoid.
func TestWorkerForRefusesToGuessBetweenDrives(t *testing.T) {
	d := testDaemon(t)
	addWorker(d, "usb-ASUS-1", "top", "/dev/sr0")
	addWorker(d, "usb-ASUS-2", "side", "/dev/sr1")

	if w, err := d.workerFor(""); err == nil {
		t.Errorf("an unnamed command picked %q out of two drives", w.label)
	}
}

func TestDefaultLabel(t *testing.T) {
	if got := defaultLabel(drive.Drive{Model: "SDRW-08D2S-U", StableID: "usb-x"}); got != "SDRW-08D2S-U" {
		t.Errorf("defaultLabel = %q, want the model", got)
	}
	// A drive whose model the kernel does not report still needs a name.
	if got := defaultLabel(drive.Drive{StableID: "usb-x"}); got != "usb-x" {
		t.Errorf("defaultLabel = %q, want the stable id", got)
	}
	if got := defaultLabel(drive.Drive{Model: "   ", StableID: "usb-x"}); got != "usb-x" {
		t.Errorf("defaultLabel = %q for a blank model, want the stable id", got)
	}
}

func testDisc() disc.Disc {
	return disc.Disc{
		Fingerprint: "a3f9c2e10b47deadbeef",
		VolumeLabel: "STILL_GAME_S1D1",
		Titles: []disc.Title{
			{Index: 0, DurationSecs: 1500, SizeBytes: 2 << 30},
			{Index: 1, DurationSecs: 1500, SizeBytes: 2 << 30},
		},
	}
}

func TestMakeRipDir(t *testing.T) {
	d := testDaemon(t)
	w := addWorker(d, "usb-ASUS-1", "top", "/dev/sr0")

	first, err := w.makeRipDir(testDisc())
	if err != nil {
		t.Fatalf("makeRipDir: %v", err)
	}
	if fi, err := os.Stat(first); err != nil || !fi.IsDir() {
		t.Fatalf("rip directory not created: %v", err)
	}
	if !strings.Contains(filepath.Base(first), "a3f9c2e10b47") {
		t.Errorf("directory name %q does not carry the fingerprint", filepath.Base(first))
	}

	// A directory left empty by an attempt that failed before writing anything
	// is reused rather than accumulating numbered siblings.
	second, err := w.makeRipDir(testDisc())
	if err != nil {
		t.Fatalf("makeRipDir: %v", err)
	}
	if second != first {
		t.Errorf("an empty directory was not reused: %q then %q", first, second)
	}

	// One that holds output must never be written into again — rips are
	// immutable, and clobbering one would destroy a disc already read.
	if err := os.WriteFile(filepath.Join(first, "title_00.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := w.makeRipDir(testDisc())
	if err != nil {
		t.Fatalf("makeRipDir: %v", err)
	}
	if third == first {
		t.Fatal("a rip directory containing output was reused")
	}
	if _, err := os.Stat(filepath.Join(first, "title_00.mkv")); err != nil {
		t.Errorf("existing output disturbed: %v", err)
	}
}

func TestWriteDiscJSON(t *testing.T) {
	dir := t.TempDir()
	if err := writeDiscJSON(dir, testDisc()); err != nil {
		t.Fatalf("writeDiscJSON: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "disc.json"))
	if err != nil {
		t.Fatalf("read disc.json: %v", err)
	}
	var got disc.Disc
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("disc.json is not valid JSON: %v", err)
	}
	if got.Fingerprint != testDisc().Fingerprint || len(got.Titles) != 2 {
		t.Errorf("disc.json lost information: %+v", got)
	}

	// The write goes through a temporary file; leaving one behind would put a
	// stray dotfile in a directory that is meant to be self-describing.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

// rescan is reachable before Run has started anything — `hellboxd -check` uses
// the same health path — so it must not assume a wait group exists.
func TestRescanWithoutRunningDaemon(t *testing.T) {
	d := testDaemon(t)

	raw, err := d.rescan(context.Background())
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	var ack map[string]string
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("rescan result is not an acknowledgement: %v", err)
	}
	if ack["rescanned"] == "" {
		t.Errorf("rescan said nothing about what it found: %v", ack)
	}

	// It must also have refreshed the health checks, which is the other half of
	// what it is for.
	d.mu.RLock()
	n := len(d.health)
	d.mu.RUnlock()
	if n == 0 {
		t.Error("rescan did not run the health checks")
	}
}

func TestRescanReportsDriveCount(t *testing.T) {
	d := testDaemon(t)
	addWorker(d, "usb-ASUS-1", "top", "/dev/sr0")

	raw, err := d.rescan(context.Background())
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	var ack map[string]string
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ack["rescanned"], "1 drive") {
		t.Errorf("rescanned = %q, want it to report one drive", ack["rescanned"])
	}
}

// A rip must never start on a drive that is already busy, and eject must refuse
// mid-rip: ejecting then would leave a partial file that looks finished.
func TestEjectRefusedWhileBusy(t *testing.T) {
	d := testDaemon(t)
	w := addWorker(d, "usb-ASUS-1", "top", "/dev/sr0")

	for _, s := range []DriveState{StateScanning, StateRipping, StateVerifying} {
		w.status.setState(s)
		if err := w.Eject(); err == nil {
			t.Errorf("eject was allowed while %s", s)
		} else if !strings.Contains(err.Error(), "busy") {
			t.Errorf("eject refusal does not explain why: %v", err)
		}
	}
}

// Cancelling when nothing is running must say so rather than appear to work.
func TestCancelWithNoRip(t *testing.T) {
	d := testDaemon(t)
	w := addWorker(d, "usb-ASUS-1", "top", "/dev/sr0")

	if err := w.Cancel(); err == nil {
		t.Error("cancel reported success with no rip in progress")
	}
}

func TestHealthChecksReportDriveCount(t *testing.T) {
	d := testDaemon(t)
	d.runHealthChecks(context.Background())

	d.mu.RLock()
	health := append([]proto.Health(nil), d.health...)
	d.mu.RUnlock()

	byName := map[string]proto.Health{}
	for _, h := range health {
		byName[h.Name] = h
	}

	drives, ok := byName["drives"]
	if !ok {
		t.Fatal("no drive count among the health checks")
	}
	if drives.OK {
		t.Errorf("drives reported OK with none registered: %q", drives.Detail)
	}

	// A missing makemkvcon must be fatal and must say what to do about it.
	// Reporting it as a warning would let the daemon look healthy while being
	// unable to rip anything at all.
	mk, ok := byName["makemkv"]
	if !ok {
		t.Fatal("no makemkv check")
	}
	if mk.OK || !mk.Fatal {
		t.Errorf("a missing makemkvcon was not reported as a fatal problem: %+v", mk)
	}
	if !strings.Contains(mk.Detail, "install") {
		t.Errorf("makemkv failure does not say how to fix it: %q", mk.Detail)
	}
}
