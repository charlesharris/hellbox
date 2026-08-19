package identify

import "testing"

// realOCR is verbatim tesseract output from the National Treasure bonus menu on
// this machine's own disc — 720x480, ornate small-caps over parchment artwork,
// which is close to the worst a DVD menu gets. Cleaning it up before testing
// would test a menu that does not exist.
const realOCR = `ta Xf yh ce * fi
, =» Bonv> TREASURE HUN? Je Siege I
wad ye i He On Location (x1:17) | Rd, Ge Fipes fief jalcal ef Oy A
yes” DELETED ScENES 7) ey a es le pads
P % De i ° if fie -* A ~ -~4 = 4 lait bay fea vp ;
my ** ; : AnIMATIC (2:21) a Th oe ea ~ fs pany aank aasho et él
os due ALTERNATE ENDIN® (1:01) Wee aoe! “Ye ha in`

func TestParseMenuLinesOnRealOCR(t *testing.T) {
	got := ParseMenuLines(realOCR)

	// Three, not four. The disc prints four running times and OCR lost the
	// colon on one of them — "DELETED ScENES 7)" was 7:47. That is the real
	// yield on this menu and the test says so rather than describing a menu
	// that reads cleanly.
	if len(got) != 3 {
		for _, e := range got {
			t.Logf("  %q -> %ds  (raw %q)", e.Name, e.Seconds, e.Raw)
		}
		t.Fatalf("parsed %d entries, want 3 — the times OCR left intact", len(got))
	}

	for _, e := range got {
		t.Logf("%-40q %ds", e.Name, e.Seconds)
	}

	// Two of the three times are also misread: 11:17 came out as 1:17 and 1:11
	// as 2:21, because this disc renders numerals in a decorative face that
	// tesseract reads wrong the same way every time. Deinterlacing and
	// averaging twenty frames changed nothing, so it is the glyphs and not the
	// noise. A wrong time simply fails to match any title, which is the
	// behaviour wanted: it drops the entry rather than misfiling it.

	// The exactly-read one has to survive intact.
	var found bool
	for _, e := range got {
		if e.Seconds == 61 {
			found = true
			if e.Name == "" {
				t.Errorf("the 1:01 entry lost its name; raw was %q", e.Raw)
			}
		}
	}
	if !found {
		t.Error("the 1:01 entry was not parsed; it is the one OCR read correctly")
	}
}

// Navigation items must never become titles. "Play", "Scene Selection" and
// "Main Menu" name a place to go, not a thing on the disc, and filing one as an
// episode is the failure this drops them to avoid.
func TestNavigationItemsAreNotEntries(t *testing.T) {
	got := ParseMenuLines("PLAY\nSCENE SELECTION\nSET UP\nSNEAK PEEKS\nMain Menu\nBONUS TREASURE HUNT")
	if len(got) != 0 {
		t.Errorf("parsed %+v, want nothing: no line carries a running time", got)
	}
}

func TestAssignByDurationMatchesTitles(t *testing.T) {
	entries := []MenuEntry{
		{Name: "National Treasure On Location", Seconds: 677}, // 11:17
		{Name: "Deleted Scenes", Seconds: 467},                // 7:47
		{Name: "Alternate Ending", Seconds: 61},               // 1:01
	}
	// The durations the scan reported, in disc order, with the feature first.
	titles := []int{7412, 470, 61, 679}

	got := AssignByDuration(entries, titles, 5)

	if got[3].Name != "National Treasure On Location" {
		t.Errorf("title 3 (679s) got %q, want the 11:17 entry", got[3].Name)
	}
	if got[1].Name != "Deleted Scenes" {
		t.Errorf("title 1 (470s) got %q, want the 7:47 entry", got[1].Name)
	}
	if got[2].Name != "Alternate Ending" {
		t.Errorf("title 2 (61s) got %q, want the 1:01 entry", got[2].Name)
	}
	if _, assigned := got[0]; assigned {
		t.Errorf("the feature got %q; nothing on the menu matched its length", got[0].Name)
	}
}

// The case that makes a loose tolerance dangerous: a disc of episodes that are
// all about the same length. Assigning a name to whichever is nearest would be
// a coin toss filed as a fact, so an ambiguous match is dropped.
func TestAmbiguousDurationsAreNotGuessed(t *testing.T) {
	entries := []MenuEntry{{Name: "Code of Honor", Seconds: 2730}}
	titles := []int{2728, 2732} // both within tolerance, equally far

	got := AssignByDuration(entries, titles, 10)
	if len(got) != 0 {
		t.Errorf("assigned %+v, want nothing: two titles are equally close", got)
	}
}

// A time with unreadable text before it must not produce a title named after
// artwork.
func TestUnreadableNamesAreDropped(t *testing.T) {
	got := ParseMenuLines(`~*^ ,, ;; (4:20)
%%% ' " . (2:05)`)
	for _, e := range got {
		if e.Name != "" {
			t.Errorf("salvaged %q from artwork; want no name", e.Name)
		}
	}
	assigned := AssignByDuration(got, []int{260, 125}, 5)
	if len(assigned) != 0 {
		t.Errorf("assigned %+v from nameless entries", assigned)
	}
}
