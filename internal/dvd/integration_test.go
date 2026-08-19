package dvd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"hellbox/internal/library"
	"hellbox/internal/testsource"
	"hellbox/internal/verify"
)

// These run against real media, and are skipped unless HELLBOX_DVD_SOURCE
// names some:
//
//	HELLBOX_DVD_SOURCE=/dev/sr1        go test ./internal/dvd/ -run Real -v -timeout 60m
//	HELLBOX_DVD_SOURCE=~/discs/kk.iso  go test ./internal/dvd/ -run Real -v -timeout 60m
//
// A device, a directory holding VIDEO_TS, or an ISO: libdvdread reads all
// three and nothing below this cares which it was given. An image is the
// better default — a drive takes hours, exists on one machine, and gives a
// different answer if somebody swaps the disc.
//
// Extraction is slow enough that it needs an explicit timeout well above the
// Go default. A DVD reads at a few times realtime at best.

func realSource(t *testing.T) string {
	return testsource.Path(t, "HELLBOX_DVD_SOURCE", "HELLBOX_DVD_DEVICE")
}

// TestRealEnumerate walks a disc and reports what it found.
//
// It asserts only what must be true of any DVD, because it cannot know which
// disc is loaded. The numbers are logged so they can be compared against a
// previous run on other hardware — which is the actual point, since the drive
// this was first measured on has since been removed.
func TestRealEnumerate(t *testing.T) {
	dev := realSource(t)

	e := NewEnumerator()
	if err := e.Available(); err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	started := time.Now()
	d, err := e.Enumerate(ctx, dev)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	t.Logf("enumerated %d titles in %s", len(d.Titles), time.Since(started).Round(time.Second))

	var total int
	for _, tt := range d.Titles {
		total += tt.DurationSecs
		var a, s int
		for _, st := range tt.Streams {
			switch st.Kind {
			case "audio":
				a++
			case "subtitle":
				s++
			}
		}
		t.Logf("  t%02d %-9s %2d ch  a=%d s=%d", tt.Index, tt.Duration(), tt.Chapters, a, s)
	}
	t.Logf("total runtime %s, PAL=%v", (time.Duration(total) * time.Second), PAL(d.Titles))

	filtered := e.Filtered(d.Titles)
	t.Logf("above %ds: %d titles; classified as %s",
		e.MinSeconds, len(filtered), library.Classify(filtered))

	if len(d.Titles) == 0 {
		t.Fatal("no titles found on a disc the drive can read")
	}
	if total == 0 {
		t.Error("every title reported zero duration")
	}
	// Enumeration must be deterministic, or the fingerprint is worthless and a
	// disc would be re-ripped every time it went in.
	again, err := e.Enumerate(ctx, dev)
	if err != nil {
		t.Fatalf("second enumerate: %v", err)
	}
	if len(again.Titles) != len(d.Titles) {
		t.Errorf("enumeration is not stable: %d titles then %d", len(d.Titles), len(again.Titles))
	}
}

// TestRealExtractShortestTitle rips the shortest title over the threshold and
// verifies it, which is the whole pipeline in miniature: enumerate, extract,
// check the output against what the disc claimed.
//
// The shortest title is chosen so this costs a minute rather than an hour. It
// exercises exactly the same code path a feature does.
func TestRealExtractShortestTitle(t *testing.T) {
	dev := realSource(t)

	e := NewEnumerator()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	d, err := e.Enumerate(ctx, dev)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	titles := e.Filtered(d.Titles)
	if len(titles) == 0 {
		t.Skip("no titles above the threshold")
	}

	shortest := titles[0]
	for _, tt := range titles {
		if tt.DurationSecs < shortest.DurationSecs {
			shortest = tt
		}
	}
	t.Logf("extracting t%02d (%s)", shortest.Index, shortest.Duration())

	dir := t.TempDir()
	out := filepath.Join(dir, "title.mkv")

	var lastPct int
	started := time.Now()
	res, err := NewExtractor().Extract(ctx, dev, shortest, out, func(p Progress) {
		if pct := int(p.Fraction * 100); pct >= lastPct+25 {
			lastPct = pct
			t.Logf("  %d%% at %.1fx", pct, p.Speed)
		}
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	mbps := float64(res.SizeBytes) / (1 << 20) / res.Elapsed.Seconds()
	t.Logf("wrote %.1f MB in %s (%.2f MB/s, %.1fx realtime)",
		float64(res.SizeBytes)/(1<<20), res.Elapsed.Round(time.Second), mbps,
		float64(shortest.DurationSecs)/res.Elapsed.Seconds())
	_ = started

	// The output must survive the same verification a real rip gets.
	v := verify.New()
	vr, err := v.Title(ctx, out, verify.Expectation{
		DurationSecs: shortest.DurationSecs,
		MinBytes:     1 << 20,
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.OK {
		t.Errorf("the extracted title failed verification: %s", vr.Error())
	}
	t.Logf("verified: %ds measured against %ds claimed", vr.DurationSecs, shortest.DurationSecs)

	if got := os.Getenv("HELLBOX_KEEP_OUTPUT"); got != "" {
		dst := filepath.Join(got, "title_"+strconv.Itoa(shortest.Index)+".mkv")
		if err := os.Rename(out, dst); err == nil {
			t.Logf("kept output at %s", dst)
		}
	}
}
