package dvd

import (
	"strings"
	"testing"
	"time"

	"hellbox/internal/disc"
)

func TestArgsAreRemuxNotReencode(t *testing.T) {
	e := NewExtractor()
	got := strings.Join(e.args("/dev/sr1", disc.Title{Index: 0}, "/out/title_00.mkv"), " ")

	// The raw tree is the system of record. A re-encode here would make every
	// later stage read from something already lossy.
	if !strings.Contains(got, "-c copy") {
		t.Errorf("a rip must be a remux: %s", got)
	}
	if !strings.Contains(got, "-map 0") {
		t.Errorf("a rip must keep every stream: %s", got)
	}
	if !strings.Contains(got, "-region 0") {
		t.Errorf("extraction must be region-free: %s", got)
	}
	if !strings.Contains(got, "-progress pipe:1") {
		t.Errorf("progress must be machine-readable, not scraped from prose: %s", got)
	}
}

// hellbox numbers titles from 0; the demuxer numbers them from 1. Getting this
// wrong rips the neighbouring title, which looks entirely plausible.
func TestTitleNumberIsOffsetForTheDemuxer(t *testing.T) {
	e := NewExtractor()
	args := e.args("/dev/sr1", disc.Title{Index: 0}, "/out/x.mkv")
	if !argPairPresent(args, "-title", "1") {
		t.Errorf("title 0 must be requested as -title 1, got: %v", args)
	}
	args = e.args("/dev/sr1", disc.Title{Index: 6}, "/out/x.mkv")
	if !argPairPresent(args, "-title", "7") {
		t.Errorf("title 6 must be requested as -title 7, got: %v", args)
	}
}

// Measured: chapters arrive without preindex, and it moved one mark by 124ms
// at the cost of reading the title a second time.
func TestPreindexIsOffByDefault(t *testing.T) {
	if NewExtractor().Preindex {
		t.Error("preindex doubles the read for ~124ms of chapter accuracy")
	}
	args := NewExtractor().args("/dev/sr1", disc.Title{Index: 0}, "/out/x.mkv")
	if !argPairPresent(args, "-preindex", "0") {
		t.Errorf("expected -preindex 0, got: %v", args)
	}

	e := NewExtractor()
	e.Preindex = true
	if !argPairPresent(e.args("/dev/sr1", disc.Title{Index: 0}, "/out/x.mkv"), "-preindex", "1") {
		t.Error("preindex should be requestable")
	}
}

func TestStallTimeoutHasASaneDefault(t *testing.T) {
	if got := NewExtractor().StallTimeout; got != 10*time.Minute {
		t.Errorf("StallTimeout = %v, want 10m", got)
	}
}

func TestParseSpeed(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{
		{"1.23x", 1.23}, {" 4x ", 4}, {"0.5x", 0.5}, {"N/A", 0}, {"", 0},
	} {
		if got := parseSpeed(c.in); got != c.want {
			t.Errorf("parseSpeed(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func argPairPresent(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1] == value
		}
	}
	return false
}
