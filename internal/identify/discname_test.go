package identify

import (
	"context"
	"strings"
	"testing"
)

func run(t *testing.T, name, source string) []Candidate {
	t.Helper()
	c, err := DiscNameNet{}.Identify(context.Background(),
		Input{DiscName: name, DiscNameSource: source})
	if err != nil {
		t.Fatalf("Identify(%q): %v", name, err)
	}
	return c
}

// The specimen: read off a disc whose volume label is DVD_VIDEO.
func TestReadsARealDVDTitleAndDropsTheEdition(t *testing.T) {
	got := run(t, "The Karate Kid (Special Edition)", "the DVD text data manager")
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	c := got[0]
	// "(Special Edition)" describes the release, not the work, and leaving it on
	// costs a provider match.
	if c.Title != "The Karate Kid" {
		t.Errorf("Title = %q, want %q", c.Title, "The Karate Kid")
	}
	if c.Confidence < 0.7 {
		t.Errorf("Confidence = %v; a name a person typed should outrank a label", c.Confidence)
	}
	if !strings.Contains(c.Why, "text data manager") {
		t.Errorf("Why = %q; should name the source", c.Why)
	}
	if !strings.Contains(c.Why, "Special Edition") {
		t.Errorf("Why = %q; should record what it removed", c.Why)
	}
}

// The Blu-ray specimen, from a disc that cannot be decrypted at all.
func TestReadsABluRayNameAndItsDiscNumber(t *testing.T) {
	got := run(t, "FIREFLY: DISC 1", "bdmt_eng.xml")
	if len(got) != 1 {
		t.Fatalf("got %d candidates", len(got))
	}
	c := got[0]
	if c.Title != "Firefly" {
		t.Errorf("Title = %q, want Firefly (an all-caps name is a shout, not a style)", c.Title)
	}
	if c.Disc != 1 {
		t.Errorf("Disc = %d, want 1", c.Disc)
	}
	if c.Kind != KindSeries {
		t.Errorf("Kind = %q; a disc number implies a set", c.Kind)
	}
}

func TestExtractsSeasonAndDisc(t *testing.T) {
	c := run(t, "Still Game Season 3 Disc 2", "bdmt_eng.xml")[0]
	if c.Title != "Still Game" || c.Season != 3 || c.Disc != 2 {
		t.Errorf("got title=%q season=%d disc=%d", c.Title, c.Season, c.Disc)
	}
}

func TestATrailingYearIsKeptAsAYear(t *testing.T) {
	c := run(t, "Roman Holiday (1953)", "the DVD text data manager")[0]
	if c.Title != "Roman Holiday" || c.Year != 1953 {
		t.Errorf("got title=%q year=%d", c.Title, c.Year)
	}
}

// A parenthetical that is neither a year nor an edition is part of the title.
// Removing it would break more matches than it fixed.
func TestAMeaningfulParentheticalSurvives(t *testing.T) {
	for _, name := range []string{
		"Fanny (Hill)",
		"Alien (Anthology Volume)",
	} {
		c := run(t, name, "x")
		if len(c) == 0 {
			t.Fatalf("%q produced nothing", name)
		}
		if !strings.Contains(c[0].Title, "(") {
			t.Errorf("%q -> %q; an unrecognised parenthetical must be kept", name, c[0].Title)
		}
	}
}

// Mixed case was written the way someone wanted it.
func TestMixedCaseIsLeftAlone(t *testing.T) {
	c := run(t, "eXistenZ", "x")[0]
	if c.Title != "eXistenZ" {
		t.Errorf("Title = %q, want eXistenZ", c.Title)
	}
}

func TestAbstainsOnNothingAndOnJunk(t *testing.T) {
	for _, name := range []string{"", "   ", "DVD_VIDEO", "dvd video", "UNTITLED"} {
		if got := run(t, name, "x"); len(got) != 0 {
			t.Errorf("%q produced %d candidates, want abstention", name, len(got))
		}
	}
}

// The net must beat LabelNet on the same disc, or wiring it in changes nothing.
func TestOutranksTheLabelOnTheSameDisc(t *testing.T) {
	in := Input{
		VolumeLabel:    "DVD_VIDEO",
		DiscName:       "The Karate Kid (Special Edition)",
		DiscNameSource: "the DVD text data manager",
	}
	res, err := Run(context.Background(), []Net{LabelNet{}, DiscNameNet{}}, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Best.Title != "The Karate Kid" {
		t.Errorf("Best = %q from net %q; want the disc's own name to win",
			res.Best.Title, res.Best.Net)
	}
	// LabelNet correctly abstains on DVD_VIDEO, so there is nothing to contest.
	if res.Contested {
		t.Error("nothing disagreed; this should not be contested")
	}
}
