package bluray

import (
	"context"
	"os"
	"testing"
	"time"

	"hellbox/internal/library"
)

// TestEnumerateRealDisc runs the enumerator against a disc that is actually in
// a drive. It is skipped unless HELLBOX_BD_DEVICE names one, so the ordinary
// suite still needs no hardware:
//
//	HELLBOX_BD_DEVICE=/dev/sr1 go test ./internal/bluray/ -run Real -v
//
// It asserts almost nothing about the disc, because it cannot know which disc
// is in the drive. What it proves is that the tools run, the parsers cope with
// their real output, and enumeration survives a disc that cannot be decrypted —
// which is the property the whole package exists for.
func TestEnumerateRealDisc(t *testing.T) {
	dev := os.Getenv("HELLBOX_BD_DEVICE")
	if dev == "" {
		t.Skip("set HELLBOX_BD_DEVICE to run against real hardware")
	}

	r := NewReader()
	if err := r.Available(); err != nil {
		t.Skipf("libbluray tools unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	info, err := r.Enumerate(ctx, dev)
	if info == nil {
		t.Fatalf("no info returned: %v", err)
	}
	if err != nil {
		// Not fatal: a disc whose playlists could not be listed should still
		// have yielded a name and a protection verdict.
		t.Logf("enumerate reported: %v", err)
	}

	d := info.Disc(60)
	t.Logf("label      : %s", info.VolumeLabel)
	t.Logf("disc name  : %s", info.DiscName)
	t.Logf("protection : %s (needs MakeMKV: %v)", info.Protection.Describe(), info.Protection.NeedsMakeMKV())
	t.Logf("disc id    : %s", info.Protection.DiscID)
	t.Logf("playlists  : %d total, %d content", len(info.Playlists), len(d.Titles))
	for _, tt := range d.Titles {
		t.Logf("  t%02d %-9s %2d ch  %s", tt.Index, tt.Duration(), tt.Chapters, tt.SourceFile)
	}
	t.Logf("classified : %s", library.Classify(d.Titles))
	t.Logf("dir name   : %s", d.DirName())

	if info.VolumeLabel == "" {
		t.Error("a mounted Blu-ray should always report a volume label")
	}
	if len(info.Playlists) == 0 {
		t.Error("no playlists enumerated; enumeration must work even on an undecryptable disc")
	}
	if d.Fingerprint == "" {
		t.Error("no fingerprint computed")
	}
}
