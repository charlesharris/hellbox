package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The files these tests probe are generated with ffmpeg rather than mocked.
// The whole point of this package is that it disbelieves what a program says
// about its own output, so testing it against a fake prober would test nothing.

func ffmpegAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
}

// makeMKV writes a real Matroska file of the given length with one video and
// one audio stream.
func makeMKV(t *testing.T, dir, name string, secs int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-t", itoa(secs), "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate %s: %v\n%s", name, err, out)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestGoodTitlePasses(t *testing.T) {
	ffmpegAvailable(t)
	dir := t.TempDir()
	path := makeMKV(t, dir, "title_00.mkv", 10)

	v := New()
	res, err := v.Title(context.Background(), path, Expectation{
		DurationSecs: 10, Video: 1, Audio: 1, MinBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Title: %v", err)
	}
	if !res.OK {
		t.Errorf("a correct file failed: %s", res.Error())
	}
	if res.DurationSecs != 10 {
		t.Errorf("measured %ds, want 10", res.DurationSecs)
	}
}

// The check v1 lacked. A file three-quarters missing passes every one of v1's
// tests: it exists, it is large, it has Matroska magic, ffmpeg exited zero.
func TestTruncatedRipIsCaught(t *testing.T) {
	ffmpegAvailable(t)
	dir := t.TempDir()
	// The disc said 40 seconds; only 10 arrived — the Star Trek shape, where
	// three of four episodes went missing.
	path := makeMKV(t, dir, "title_00.mkv", 10)

	v := New()
	res, _ := v.Title(context.Background(), path, Expectation{
		DurationSecs: 40, MinBytes: 1024,
	})
	if res.OK {
		t.Fatal("a rip missing three quarters of its content passed verification")
	}
	if !strings.Contains(res.Error(), "truncated") {
		t.Errorf("problem should name the truncation, got: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "75") {
		t.Errorf("problem should quantify the loss, got: %s", res.Error())
	}
}

// Remuxing is not frame-exact against a disc's own arithmetic, so a second or
// two either way must not fail a good rip.
func TestSmallDurationDriftIsTolerated(t *testing.T) {
	ffmpegAvailable(t)
	dir := t.TempDir()
	path := makeMKV(t, dir, "title_00.mkv", 100)

	v := New()
	res, _ := v.Title(context.Background(), path, Expectation{
		DurationSecs: 101, MinBytes: 1024,
	})
	if !res.OK {
		t.Errorf("one second of drift on a 100s title should pass, got: %s", res.Error())
	}
}

// Two per cent of a short extra is under a second, which is inside the noise of
// where a cell begins. The floor stops that failing good rips.
func TestShortTitlesGetAnAbsoluteFloor(t *testing.T) {
	if got := durationProblem(62, 64, DefaultTolerance); got != "" {
		t.Errorf("2s drift on a 64s title should pass via the floor, got: %s", got)
	}
	// The floor must not become a licence to lose a whole short title.
	if got := durationProblem(20, 64, DefaultTolerance); got == "" {
		t.Error("a 64s title arriving as 20s must fail")
	}
}

func TestMissingFileIsReportedPlainly(t *testing.T) {
	v := New()
	res, err := v.Title(context.Background(), filepath.Join(t.TempDir(), "absent.mkv"),
		Expectation{DurationSecs: 60})
	if err != nil {
		t.Fatalf("a missing file is a verdict, not an error: %v", err)
	}
	if res.OK || !strings.Contains(res.Error(), "no output file") {
		t.Errorf("got: %s", res.Error())
	}
}

func TestUndersizedOutputIsAFailedRip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.mkv")
	if err := os.WriteFile(path, []byte{0x1A, 0x45, 0xDF, 0xA3, 0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}

	v := New()
	res, _ := v.Title(context.Background(), path, Expectation{
		DurationSecs: 60, MinBytes: 10 << 20,
	})
	if res.OK {
		t.Error("a 6-byte file passed")
	}
	if !strings.Contains(res.Error(), "failed rip") {
		t.Errorf("should distinguish a failed rip from a short title, got: %s", res.Error())
	}
}

func TestNonMatroskaIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notmkv.mkv")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}

	v := New()
	res, _ := v.Title(context.Background(), path, Expectation{MinBytes: 100})
	if res.OK || !strings.Contains(res.Error(), "magic") {
		t.Errorf("got: %s", res.Error())
	}
}

func TestMissingStreamIsCaught(t *testing.T) {
	ffmpegAvailable(t)
	dir := t.TempDir()
	path := makeMKV(t, dir, "title_00.mkv", 5)

	v := New()
	// The file has one audio track; claim the disc offered five.
	res, _ := v.Title(context.Background(), path, Expectation{
		DurationSecs: 5, Audio: 5, MinBytes: 1024,
	})
	if res.OK {
		t.Error("a file missing four audio tracks passed")
	}
	if !strings.Contains(res.Error(), "audio") {
		t.Errorf("got: %s", res.Error())
	}
}

// Zero means "do not check", because a deliberate stream selection legitimately
// produces fewer tracks than the disc offered.
func TestZeroStreamCountsAreNotChecked(t *testing.T) {
	ffmpegAvailable(t)
	dir := t.TempDir()
	path := makeMKV(t, dir, "title_00.mkv", 5)

	v := New()
	res, _ := v.Title(context.Background(), path, Expectation{DurationSecs: 5, MinBytes: 1024})
	if !res.OK {
		t.Errorf("unchecked stream counts should not fail: %s", res.Error())
	}
}

// A count of files hides the loss that actually happened: every expected file
// present, one of them short.
func TestDiscTotalCatchesALostEpisodeWithNoMissingFile(t *testing.T) {
	v := New()
	results := []Result{
		{DurationSecs: 2563}, {DurationSecs: 2634},
		{DurationSecs: 2638}, {DurationSecs: 60}, // this one came back short
	}
	problems := v.Disc(results, 2563+2634+2638+2600)

	if len(problems) == 0 {
		t.Fatal("a disc missing 40 minutes passed with the right number of files")
	}
	if !strings.Contains(problems[0], "whole disc") {
		t.Errorf("got: %v", problems)
	}
}

func TestDiscTotalPassesWhenComplete(t *testing.T) {
	v := New()
	results := []Result{{DurationSecs: 2563}, {DurationSecs: 2634}}
	if problems := v.Disc(results, 2563+2634); len(problems) != 0 {
		t.Errorf("a complete disc failed: %v", problems)
	}
}
