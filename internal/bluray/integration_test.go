package bluray

import (
	"context"
	"testing"
	"time"

	"hellbox/internal/library"
	"hellbox/internal/testsource"
)

// TestEnumerateRealDisc runs the enumerator against real media. It is skipped
// unless HELLBOX_BD_SOURCE names some, so the ordinary suite still needs no
// hardware:
//
//	HELLBOX_BD_SOURCE=/dev/sr1          go test ./internal/bluray/ -run Real -v
//	HELLBOX_BD_SOURCE=~/discs/firefly1  go test ./internal/bluray/ -run Real -v
//
// A device, a directory holding BDMV, or an ISO: libbluray's tools take all
// three. Only /dev/sr1 reads Blu-ray on this hardware, so a copied BDMV tree
// is the only way to exercise this package anywhere else.
//
// It asserts almost nothing about the disc, because it cannot know which disc
// is in the drive. What it proves is that the tools run, the parsers cope with
// their real output, and enumeration survives a disc that cannot be decrypted —
// which is the property the whole package exists for.
func TestEnumerateRealDisc(t *testing.T) {
	dev := testsource.Path(t, "HELLBOX_BD_SOURCE", "HELLBOX_BD_DEVICE")

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
