package identify

import (
	"context"
	"testing"
)

// Every label here is one hellbox has actually seen, taken from the rips on the
// machine it runs on. Inventing labels is how the failure classifier came to
// recognise seven kinds of failure that had never occurred while missing all
// seven that had.
func TestLabelNetOnRealDiscs(t *testing.T) {
	for _, tc := range []struct {
		label  string
		title  string
		season int
		disc   int
		kind   Kind
		// weak marks labels the net must not be confident about, because a
		// stronger net has to be able to overrule them.
		weak bool
	}{
		{label: "ROMAN_HOLIDAY", title: "Roman Holiday"},
		{label: "NATIONAL_TREASURE", title: "National Treasure"},
		{label: "Earthsea", title: "Earthsea"},

		// Already mixed case: typed by a person, so left as typed.
		{label: "Hackers", title: "Hackers"},

		// LB is letterboxed — a description of the transfer, not the film.
		{label: "SPACEBALLS_LB", title: "Spaceballs"},

		// Six physically different discs carry this one label. The net can
		// only ever offer the series name, which is the point.
		{label: "STILL_GAME", title: "Still Game"},

		// The five schemes one television series arrived under.
		{label: "STNGD3", title: "Stng", disc: 3, weak: true},
		{label: "STNGD5", title: "Stng", disc: 5, weak: true},
		{label: "STNG4", title: "Stng", disc: 4, weak: true},
		{label: "NEXTGEN", title: "Nextgen", weak: true},
		{label: "NEXTGEN2", title: "Nextgen", disc: 2, weak: true},

		// Explicit forms, which are the only ones worth any confidence.
		{label: "STILL_GAME_S3D2", title: "Still Game", season: 3, disc: 2, kind: KindSeries},
		{label: "STILL_GAME_SEASON_3", title: "Still Game", season: 3, kind: KindSeries},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got, err := LabelNet{}.Identify(context.Background(), Input{VolumeLabel: tc.label})
			if err != nil {
				t.Fatalf("Identify: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
			}
			c := got[0]
			if c.Title != tc.title {
				t.Errorf("title = %q, want %q", c.Title, tc.title)
			}
			if c.Season != tc.season {
				t.Errorf("season = %d, want %d", c.Season, tc.season)
			}
			if c.Disc != tc.disc {
				t.Errorf("disc = %d, want %d", c.Disc, tc.disc)
			}
			if tc.kind != KindUnknown && c.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", c.Kind, tc.kind)
			}
			if tc.weak && c.Confidence > 0.4 {
				t.Errorf("confidence %.2f is too high for %q; a better net must be able to overrule it",
					c.Confidence, tc.label)
			}
			if !tc.weak && c.Confidence < 0.4 {
				t.Errorf("confidence %.2f is too low for %q, which reads as a real title",
					c.Confidence, tc.label)
			}
		})
	}
}

// A label that identifies nothing must produce nothing, rather than a film
// called "Dvd Video".
func TestLabelNetAbstainsOnJunk(t *testing.T) {
	for _, label := range []string{"", "  ", "DVD_VIDEO", "DVDVIDEO", "UNTITLED", "LOGICAL_VOLUME_ID"} {
		got, err := LabelNet{}.Identify(context.Background(), Input{VolumeLabel: label})
		if err != nil {
			t.Fatalf("Identify(%q): %v", label, err)
		}
		for _, c := range got {
			if c.Confidence > 0.4 {
				t.Errorf("label %q produced %q at confidence %.2f; it identifies nothing",
					label, c.Title, c.Confidence)
			}
		}
	}
}
