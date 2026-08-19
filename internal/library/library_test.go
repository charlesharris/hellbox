package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hellbox/internal/disc"
)

func titles(durations ...int) []disc.Title {
	out := make([]disc.Title, 0, len(durations))
	for i, d := range durations {
		out = append(out, disc.Title{Index: i, DurationSecs: d})
	}
	return out
}

// Both cases are the real discs: Roman Holiday's runtimes and Still Game's, as
// MakeMKV reported them. A disc says nothing about what it holds, so its shape
// is all there is — and getting this wrong files a film as six episodes.
func TestClassifyRealDiscs(t *testing.T) {
	tests := []struct {
		name string
		in   []disc.Title
		want Kind
	}{
		{
			// ROMAN_HOLIDAY: a feature and six extras.
			"film with extras",
			titles(7068, 1528, 411, 110, 134, 150, 821),
			KindMovie,
		},
		{
			// STILL_GAME disc 1: six episodes of much the same length.
			"six episodes",
			titles(1748, 1740, 1752, 1745, 1750, 1739),
			KindTV,
		},
		{
			// STILL_GAME disc 2: three episodes. Fewer, same shape.
			"three episodes",
			titles(1745, 1748, 1743),
			KindTV,
		},
		{"single feature", titles(6000), KindMovie},

		// A lone short title is neither, and guessing would file it wrongly.
		{"single short title", titles(400), KindUnknown},
		{"nothing", nil, KindUnknown},

		// A long episode must not read as a feature just for being long.
		{"long episodes", titles(3300, 3250, 3280), KindTV},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in); got != tc.want {
				t.Errorf("Classify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Volume labels are upper case with underscores, and carry markers that stop a
// title matching. What is left is what Jellyfin searches on.
func TestName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ROMAN_HOLIDAY", "Roman Holiday"},
		{"STILL_GAME", "Still Game"},
		{"STILL_GAME_DISC_1", "Still Game"},
		{"THE_MATRIX_DVD_2", "The Matrix"},
		{"BLADE.RUNNER", "Blade Runner"},
		{"  spaced  out  ", "spaced out"},
		{"", ""},
	} {
		if got := Name(tc.in); got != tc.want {
			t.Errorf("Name(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A film's feature gets Jellyfin's exact convention; its extras go where
// Jellyfin will not offer them as alternative versions of the film.
func TestPlanFilm(t *testing.T) {
	d := disc.Disc{VolumeLabel: "ROMAN_HOLIDAY", Fingerprint: strings.Repeat("a", 64)}
	d.Titles = titles(7068, 1528, 411)

	got := Plan("/lib", d, KindMovie, nil)
	if len(got) != 3 {
		t.Fatalf("Plan returned %d placements, want 3", len(got))
	}

	var features int
	for _, p := range got {
		if p.Feature {
			features++
			if p.Path != "/lib/Movies/Roman Holiday/Roman Holiday.mkv" {
				t.Errorf("feature at %q", p.Path)
			}
			continue
		}
		if !strings.Contains(p.Path, "/extras/") {
			t.Errorf("extra at %q, want it under extras/", p.Path)
		}
	}
	if features != 1 {
		t.Errorf("%d titles marked as the feature, want exactly 1", features)
	}
}

// The feature is the longest title, not the first. Title order is MakeMKV's,
// and nothing guarantees the feature comes first.
func TestPlanFilmPicksTheLongestTitle(t *testing.T) {
	d := disc.Disc{VolumeLabel: "X", Fingerprint: strings.Repeat("b", 64)}
	d.Titles = titles(600, 7000, 500)

	for _, p := range Plan("/lib", d, KindMovie, nil) {
		if p.Feature && p.TitleIndex != 1 {
			t.Errorf("feature is title %d, want the longest (1)", p.TitleIndex)
		}
	}
}

// Episodes are filed unnumbered, because a disc does not carry episode numbers
// — both Still Game discs are labelled STILL_GAME with nothing to tell them
// apart. The fingerprint is what makes a file traceable to its rip.
func TestPlanEpisodesCarryTheirSource(t *testing.T) {
	fp := strings.Repeat("c", 64)
	d := disc.Disc{VolumeLabel: "STILL_GAME", Fingerprint: fp}
	d.Titles = titles(1748, 1740)

	got := Plan("/lib", d, KindTV, nil)
	for _, p := range got {
		if !strings.HasPrefix(p.Path, "/lib/TV/Still Game/") {
			t.Errorf("episode at %q, want it under the show", p.Path)
		}
		if !strings.Contains(p.Path, disc.Short(fp)) {
			t.Errorf("episode %q does not name the rip it came from", p.Path)
		}
		if p.Feature {
			t.Errorf("episode %d marked as a feature", p.TitleIndex)
		}
	}
}

// A disc with no usable label still has to go somewhere findable.
func TestPlanUnlabelledDisc(t *testing.T) {
	d := disc.Disc{Fingerprint: strings.Repeat("d", 64)}
	d.Titles = titles(1748, 1740)

	got := Plan("/lib", d, KindTV, nil)
	if len(got) == 0 || !strings.Contains(got[0].Path, disc.Short(d.Fingerprint)) {
		t.Errorf("unlabelled disc filed as %q", got[0].Path)
	}
}

func TestLinkSharesTheBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	if err := os.WriteFile(src, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "lib", "TV", "Show", "out.mkv")

	if err := Link(src, dst); err != nil {
		t.Fatalf("Link: %v", err)
	}
	si, _ := os.Stat(src)
	di, _ := os.Stat(dst)
	if !os.SameFile(si, di) {
		t.Error("Link copied instead of hardlinking")
	}
}

// A file already in place may be one someone renamed or replaced by hand.
// Overwriting it would lose work hellbox did not do.
func TestLinkRefusesToReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Link(src, dst); err != ErrExists {
		t.Errorf("Link over an existing file = %v, want ErrExists", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "theirs" {
		t.Error("Link overwrote a file that was already there")
	}
}

// Format markers say how a film was framed for a 4:3 television, not what the
// film is. Left in they produce a title no metadata provider will match:
// SPACEBALLS_LB is not a film called "Spaceballs Lb".
func TestNameDropsFormatMarkers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SPACEBALLS_LB", "Spaceballs"},
		{"THE_MATRIX_WS", "The Matrix"},
		{"ALIEN_FS", "Alien"},
		{"TOP_GUN_PS", "Top Gun"},
		{"STILL_GAME_DISC_2_WS", "Still Game"},
	} {
		if got := Name(tc.in); got != tc.want {
			t.Errorf("Name(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Edition words are frequently part of the real title, and dropping them would
// break more matches than it fixed.
func TestNameKeepsEditionWords(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"BLADE_RUNNER_FINAL_CUT", "Blade Runner Final Cut"},
		{"ALIEN_SPECIAL_EDITION", "Alien Special Edition"},
	} {
		if got := Name(tc.in); got != tc.want {
			t.Errorf("Name(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A DVD routinely carries its feature twice — widescreen and fullscreen, or the
// variants of a seamless-branching disc. Comparing the two longest titles then
// finds them equal and calls the most film-shaped disc imaginable neither a film
// nor a set of episodes, filing twenty-one titles as television.
func TestClassifyHandlesDuplicatedFeatures(t *testing.T) {
	// NATIONAL_TREASURE, as MakeMKV reported it: the feature twice, then extras.
	nt := titles(7860, 7860, 660, 540, 480, 480, 360, 300, 180, 180, 180, 180,
		120, 120, 120, 120, 120, 60, 60, 60, 60)
	if got := Classify(nt); got != KindMovie {
		t.Errorf("Classify() = %q for a film carrying its feature twice, want %q", got, KindMovie)
	}

	// Three variants of the same feature is still a film.
	if got := Classify(titles(7860, 7860, 7860, 600, 300, 180)); got != KindMovie {
		t.Errorf("Classify() = %q for three variants of one feature, want %q", got, KindMovie)
	}
}

// The discs already through must keep classifying as they did.
func TestClassifyStillAgreesWithEveryRealDisc(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []disc.Title
		want Kind
	}{
		{"ROMAN_HOLIDAY", titles(7068, 1528, 411, 110, 134, 150, 821), KindMovie},
		{"SPACEBALLS_LB", titles(5770, 700, 190), KindMovie},
		{"STILL_GAME 6ep", titles(1748, 1740, 1752, 1745, 1750, 1739), KindTV},
		{"STILL_GAME 3ep", titles(1745, 1748, 1743), KindTV},
		{"EARTHSEA", titles(10372, 190), KindMovie},
	} {
		if got := Classify(tc.in); got != tc.want {
			t.Errorf("%s classified as %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A television disc can be lopsided too. A Star Trek disc of one 90-minute
// two-parter and two 45-minute episodes has a longest title exactly twice its
// median — the definition of a film disc — and was filed as a film.
//
// What tells them apart is the remainder: take the longest title away and an
// episode disc still looks like episodes, while a film disc is left with a
// scatter of unrelated extras.
func TestClassifyTVWithADoubleLengthEpisode(t *testing.T) {
	// NEXTGEN, as MakeMKV reported it.
	if got := Classify(titles(5453, 2725, 2720)); got != KindTV {
		t.Errorf("Classify() = %q for a two-parter beside two episodes, want %q", got, KindTV)
	}
	// A feature-length opener beside a full disc of episodes.
	if got := Classify(titles(5400, 2700, 2700, 2700, 2700)); got != KindTV {
		t.Errorf("Classify() = %q for a long opener beside episodes, want %q", got, KindTV)
	}
}

// The remainder test must not drag films into television. A film's extras do
// not look like episodes, and this is what stops the check overreaching.
func TestClassifyKeepsFilmsWithLongExtras(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []disc.Title
	}{
		{"feature with one long documentary", titles(7200, 3600, 300, 180, 120)},
		{"feature with two unrelated extras", titles(7068, 1528, 821, 411, 150, 134, 110)},
		{"feature carried twice", titles(7860, 7860, 660, 540, 480, 300, 180, 120)},
	} {
		if got := Classify(tc.in); got != KindMovie {
			t.Errorf("%s classified as %q, want %q", tc.name, got, KindMovie)
		}
	}
}
