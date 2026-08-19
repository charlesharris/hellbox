package decrypt

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every line below is verbatim from a real dvdbackup run, so the parsing cannot
// drift from what the tool actually prints.
func TestParseProgressFromRealOutput(t *testing.T) {
	tests := []struct {
		line     string
		ok       bool
		fraction float64
		op       string
	}{
		{"Copying Title, part 1/3: 47% done (482/1024 MiB)", true, (0 + 0.47) / 3, "Copying Title, part 1/3"},
		{"Copying Title, part 2/3: 94% done (958/1024 MiB)", true, (1 + 0.94) / 3, "Copying Title, part 2/3"},
		{"Copying Title, part 6/6: 100% done (12/12 MiB)", true, 1, "Copying Title, part 6/6"},

		// Menus carry no part, and inventing a disc-wide figure for them would
		// send the bar to 100% before the titles had started.
		{"Copying menu: 100% done (1/1 MiB)", false, 0, ""},

		{"libdvdread: Attempting to retrieve all CSS keys", false, 0, ""},
		{"", false, 0, ""},
	}

	for _, tc := range tests {
		got, ok := parseProgress(tc.line)
		if ok != tc.ok {
			t.Errorf("parseProgress(%q) ok = %v, want %v", tc.line, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if diff := got.Fraction - tc.fraction; diff > 0.0001 || diff < -0.0001 {
			t.Errorf("parseProgress(%q) fraction = %v, want %v", tc.line, got.Fraction, tc.fraction)
		}
		if got.Operation != tc.op {
			t.Errorf("parseProgress(%q) operation = %q, want %q", tc.line, got.Operation, tc.op)
		}
	}
}

// dvdbackup redraws progress with a bare carriage return and emits no newline
// until the copy ends. A scanner splitting on newlines alone reads a whole
// thirty-five minute decrypt as one line and reports nothing until it is over.
func TestScannerSplitsOnCarriageReturns(t *testing.T) {
	// One newline, the rest carriage returns — the shape of the real stream.
	stream := "libdvdread: Attempting to retrieve all CSS keys\n" +
		"Copying Title, part 1/2: 10% done (1/10 MiB)\r" +
		"Copying Title, part 1/2: 50% done (5/10 MiB)\r" +
		"Copying Title, part 2/2: 100% done (10/10 MiB)\r"

	sc := bufio.NewScanner(strings.NewReader(stream))
	sc.Split(scanLinesOrReturns)

	var lines, progress int
	for sc.Scan() {
		lines++
		if _, ok := parseProgress(sc.Text()); ok {
			progress++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != 4 {
		t.Errorf("read %d lines, want 4", lines)
	}
	if progress != 3 {
		t.Errorf("found %d progress readings, want 3; splitting on newlines alone finds 0", progress)
	}
}

// A part number out of range must not produce a fraction outside 0..1, which
// would render as a bar past its own end.
func TestProgressIsClamped(t *testing.T) {
	for _, line := range []string{
		"Copying Title, part 0/3: 50% done (1/2 MiB)",
		"Copying Title, part 3/0: 50% done (1/2 MiB)",
	} {
		if got, ok := parseProgress(line); ok && (got.Fraction < 0 || got.Fraction > 1) {
			t.Errorf("parseProgress(%q) fraction = %v, outside 0..1", line, got.Fraction)
		}
	}
}

// A copy is only reusable once dvdbackup has finished. The dangerous case is a
// daemon killed mid-decrypt: the directory holds most of a disc and looks
// entirely plausible, and reusing it would rip a truncated film that then
// verifies as fine, because verification checks the output file, not the disc.
func TestExistingRejectsAnUnfinishedCopy(t *testing.T) {
	root := t.TempDir()
	const key = "5d7fd5a1507a"

	// A copy with all the structure and no completion marker.
	folder := filepath.Join(Path(root, key), outputName, "VIDEO_TS")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "VTS_01_1.VOB"), make([]byte, 2048), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, ok := Existing(root, key); ok {
		t.Fatal("an unfinished copy was offered for reuse")
	}

	// The same copy, marked complete.
	if err := os.WriteFile(filepath.Join(Path(root, key), completeMarker), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	res, ok := Existing(root, key)
	if !ok {
		t.Fatal("a finished copy was not offered for reuse")
	}
	if res.Bytes != 2048 {
		t.Errorf("Bytes = %d, want 2048", res.Bytes)
	}
	if res.Folder != filepath.Join(Path(root, key), outputName) {
		t.Errorf("Folder = %q, want the directory containing VIDEO_TS", res.Folder)
	}
}

// A copy without VIDEO_TS is not a disc, marker or not.
func TestExistingRejectsACopyWithNoVideoTS(t *testing.T) {
	root := t.TempDir()
	const key = "abc123abc123"
	if err := os.MkdirAll(Path(root, key), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(Path(root, key), completeMarker), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if _, ok := Existing(root, key); ok {
		t.Error("a copy with no VIDEO_TS was offered for reuse")
	}
}

// Nothing on disk is the ordinary case and must not be an error.
func TestExistingOnAnEmptyWorkDir(t *testing.T) {
	if _, ok := Existing(t.TempDir(), "5d7fd5a1507a"); ok {
		t.Error("Existing found a copy in an empty directory")
	}
}

// The path has to be the same every time or a retry decrypts the disc again,
// and different per disc or one disc's copy is ripped as another's.
func TestPathIsStablePerDisc(t *testing.T) {
	if a, b := Path("/w", "aaaa"), Path("/w", "aaaa"); a != b {
		t.Errorf("Path is not stable: %q vs %q", a, b)
	}
	if a, b := Path("/w", "aaaa"), Path("/w", "bbbb"); a == b {
		t.Errorf("two discs share the path %q", a)
	}
}

func TestDiscardRemovesTheCopy(t *testing.T) {
	root := t.TempDir()
	const key = "5d7fd5a1507a"
	if err := os.MkdirAll(filepath.Join(Path(root, key), outputName, "VIDEO_TS"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := Discard(&Result{Root: Path(root, key)}); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(Path(root, key)); !os.IsNotExist(err) {
		t.Error("the copy survived Discard")
	}
	// Discarding nothing is what a rip from the drive does on success.
	if err := Discard(nil); err != nil {
		t.Errorf("Discard(nil) = %v, want nil", err)
	}
}
