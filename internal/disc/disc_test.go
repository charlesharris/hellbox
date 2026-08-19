package disc

import (
	"strings"
	"testing"
	"time"
)

func TestFingerprintIsOrderIndependent(t *testing.T) {
	// MakeMKV's title enumeration order is not guaranteed to be identical
	// between reads, so the same disc must fingerprint the same either way.
	a := []Title{
		{Index: 0, DurationSecs: 1748, SizeBytes: 890109157},
		{Index: 1, DurationSecs: 1744, SizeBytes: 932509440},
		{Index: 2, DurationSecs: 1739, SizeBytes: 866609002},
	}
	b := []Title{
		{Index: 0, DurationSecs: 1739, SizeBytes: 866609002},
		{Index: 1, DurationSecs: 1748, SizeBytes: 890109157},
		{Index: 2, DurationSecs: 1744, SizeBytes: 932509440},
	}

	if ComputeFingerprint("STILL_GAME_S1D1", a) != ComputeFingerprint("STILL_GAME_S1D1", b) {
		t.Error("fingerprint changed when title order changed")
	}
}

func TestFingerprintDistinguishesDiscs(t *testing.T) {
	base := []Title{{DurationSecs: 1748, SizeBytes: 890109157}}

	tests := []struct {
		name   string
		label  string
		titles []Title
	}{
		{"different label", "OTHER_DISC", base},
		{"different duration", "DISC", []Title{{DurationSecs: 1749, SizeBytes: 890109157}}},
		{"different size", "DISC", []Title{{DurationSecs: 1748, SizeBytes: 890109158}}},
		{"extra title", "DISC", append(append([]Title{}, base...), Title{DurationSecs: 60, SizeBytes: 1000})},
	}

	reference := ComputeFingerprint("DISC", base)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if ComputeFingerprint(tc.label, tc.titles) == reference {
				t.Error("fingerprint collided with a different disc")
			}
		})
	}
}

func TestFingerprintIgnoresSurroundingWhitespace(t *testing.T) {
	titles := []Title{{DurationSecs: 100, SizeBytes: 200}}
	if ComputeFingerprint("  DISC  ", titles) != ComputeFingerprint("DISC", titles) {
		t.Error("whitespace in the volume label changed the fingerprint")
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"STILL GAME - SERIES 1 DISC 1", "still-game-series-1-disc-1"},
		{"Harry Potter & the Prisoner of Azkaban", "harry-potter-the-prisoner-of-azkaban"},
		{"  padded  ", "padded"},
		{"___", ""},
		{"", ""},
		{"a/b\\c:d", "a-b-c-d"},
	}
	for _, tc := range tests {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugIsTruncatedCleanly(t *testing.T) {
	got := Slug(strings.Repeat("abcde ", 20))
	if len(got) > 40 {
		t.Errorf("slug is %d characters, want at most 40", len(got))
	}
	if strings.HasSuffix(got, "-") || strings.HasPrefix(got, "-") {
		t.Errorf("slug %q has a dangling separator", got)
	}
}

func TestDirName(t *testing.T) {
	when := time.Date(2026, 7, 27, 14, 30, 0, 0, time.UTC)

	d := Disc{
		VolumeLabel: "STILL GAME SERIES 1 DISC 1",
		Fingerprint: "a3f9c2e10b47deadbeef",
		ScannedAt:   when,
	}
	if got, want := d.DirName(), "2026-07-27--still-game-series-1-disc-1--a3f9c2e10b47"; got != want {
		t.Errorf("DirName() = %q, want %q", got, want)
	}
}

func TestDirNameFallsBackForUninformativeLabels(t *testing.T) {
	when := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

	// Authoring tools emit these constantly; they must not become directory
	// names, or every disc would collide on the human-readable part.
	for _, label := range []string{"", "DVD_VIDEO", "dvd_video", "LOGICAL_VOLUME_ID", "!!!"} {
		d := Disc{VolumeLabel: label, Fingerprint: "abcdef123456xyz", ScannedAt: when}
		if got, want := d.DirName(), "2026-07-27--unlabeled--abcdef123456"; got != want {
			t.Errorf("DirName() for label %q = %q, want %q", label, got, want)
		}
	}
}

func TestTitleFileName(t *testing.T) {
	tests := []struct {
		index int
		want  string
	}{
		{0, "title_00.mkv"},
		{4, "title_04.mkv"},
		{12, "title_12.mkv"},
		{100, "title_100.mkv"},
	}
	for _, tc := range tests {
		if got := TitleFileName(tc.index); got != tc.want {
			t.Errorf("TitleFileName(%d) = %q, want %q", tc.index, got, tc.want)
		}
	}
}

func TestTitleDuration(t *testing.T) {
	tests := []struct {
		secs int
		want string
	}{
		{1748, "0:29:08"},
		{5025, "1:23:45"},
		{0, "0:00:00"},
	}
	for _, tc := range tests {
		got := Title{DurationSecs: tc.secs}.Duration()
		if got != tc.want {
			t.Errorf("Title{%d}.Duration() = %q, want %q", tc.secs, got, tc.want)
		}
	}
}

func TestShort(t *testing.T) {
	if got := Short("a3f9c2e10b47deadbeef"); got != "a3f9c2e10b47" {
		t.Errorf("Short() = %q", got)
	}
	if got := Short("abc"); got != "abc" {
		t.Errorf("Short() on a short input = %q, want it returned unchanged", got)
	}
}
