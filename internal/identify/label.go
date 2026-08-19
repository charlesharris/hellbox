package identify

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// LabelNet reads what it can out of the volume label.
//
// It is the weakest net and it is written to know that. A volume label is at
// most 32 characters on UDF and 11 on the ISO9660 filesystem most DVDs carry,
// upper-cased, chosen by an authoring house with no interest in being parsed.
// This collection alone contains STNGD3, STNGD5, STNG4, NEXTGEN and NEXTGEN2 —
// five schemes for one television series — and six different Still Game discs
// that are all called STILL_GAME.
//
// So its job is to extract structure, not to be clever. It will say that
// STNGD3 is something called "STNG", disc 3, and say it with low confidence. It
// will not expand STNG into Star Trek: The Next Generation, because doing that
// requires a lookup table of abbreviations that would be wrong for the first
// disc nobody thought of, and because a net that guesses confidently is worse
// than one that abstains: the whole design assumes a stronger net can overrule
// this one, and it cannot overrule a fabrication that arrived with high
// confidence.
type LabelNet struct{}

func (LabelNet) Name() string { return "label" }

// junkLabels are labels that identify nothing. Authoring software writes these
// by default and a good few discs never get past them.
var junkLabels = map[string]bool{
	"dvd_video": true, "dvdvideo": true, "dvd": true, "video_ts": true,
	"bluray": true, "blu_ray": true, "bd_rom": true, "bdrom": true,
	"untitled": true, "unnamed": true, "new_volume": true, "disc": true,
	"movie": true, "video": true, "cdrom": true, "default": true,
	"logical_volume_id": true, "volume": true, "my_disc": true, "dvd_rom": true,
}

// formatMarkers are suffixes describing the transfer rather than the work.
// SPACEBALLS_LB is Spaceballs, letterboxed — the LB is not part of the title.
var formatMarkers = map[string]bool{
	"lb": true, "ws": true, "fs": true, "sf": true, // letterbox, widescreen, fullscreen
	"se": true, "ce": true, "de": true, "ee": true, // special/collector's/deluxe/extended edition
	"uncut": true, "unrated": true, "remastered": true, "restored": true,
	"ntsc": true, "pal": true, "r1": true, "r2": true,
	"dvd": true, "bd": true, "disc": true, "side": true,
	"16x9": true, "4x3": true,
}

var (
	// discSuffix matches a trailing disc number: STNGD3, STILL_GAME_D2.
	discSuffix = regexp.MustCompile(`(?i)^(.*?)[ _-]*d(?:isc|isk)?[ _-]*(\d{1,2})$`)

	// seasonSuffix matches an explicit season: STILL_GAME_S3, FOO_SEASON_2.
	seasonSuffix = regexp.MustCompile(`(?i)^(.*?)[ _-]*s(?:eason|sn)?[ _-]*(\d{1,2})$`)

	// seasonDisc matches both together, the least ambiguous form there is:
	// STILL_GAME_S3D2.
	seasonDisc = regexp.MustCompile(`(?i)^(.*?)[ _-]*s(?:eason)?[ _-]*(\d{1,2})[ _-]*d(?:isc|isk)?[ _-]*(\d{1,2})$`)

	// yearSuffix matches a trailing four-digit year, which a few discs carry.
	yearSuffix = regexp.MustCompile(`^(.*?)[ _-]*(19\d{2}|20\d{2})$`)

	// trailingDigits matches a bare number stuck on the end: NEXTGEN2, STNG4.
	// What it means is genuinely unknowable — disc 2, season 2, or part of the
	// name — which is why it is handled separately and costs confidence.
	trailingDigits = regexp.MustCompile(`^(.*?[a-z])[ _-]*(\d{1,2})$`)
)

// Identify reads the label.
func (n LabelNet) Identify(_ context.Context, in Input) ([]Candidate, error) {
	raw := strings.TrimSpace(in.VolumeLabel)
	if raw == "" {
		return nil, nil
	}
	if junkLabels[strings.ToLower(strings.ReplaceAll(raw, " ", "_"))] {
		return nil, nil
	}

	c := Candidate{Net: n.Name(), Confidence: 0.5, Why: "read from the volume label"}
	work := raw

	// Season and disc first, longest pattern first so STILL_GAME_S3D2 is not
	// read as a disc number with the season left glued to the title.
	switch {
	case seasonDisc.MatchString(work):
		m := seasonDisc.FindStringSubmatch(work)
		work = m[1]
		c.Season, _ = strconv.Atoi(m[2])
		c.Disc, _ = strconv.Atoi(m[3])
		c.Kind = KindSeries
		c.Confidence = 0.65
		c.Why = "label gives an explicit season and disc"
	case seasonSuffix.MatchString(work):
		m := seasonSuffix.FindStringSubmatch(work)
		work = m[1]
		c.Season, _ = strconv.Atoi(m[2])
		c.Kind = KindSeries
		c.Confidence = 0.6
		c.Why = "label gives an explicit season"
	case discSuffix.MatchString(work):
		m := discSuffix.FindStringSubmatch(work)
		work = m[1]
		c.Disc, _ = strconv.Atoi(m[2])
		c.Confidence = 0.55
		c.Why = "label gives an explicit disc number"
	}

	if c.Season == 0 && c.Disc == 0 {
		if m := yearSuffix.FindStringSubmatch(work); m != nil {
			work = m[1]
			c.Year, _ = strconv.Atoi(m[2])
			c.Confidence = 0.6
			c.Why = "label carries a year"
		}
	}

	work, dropped := stripMarkers(work)

	// A bare trailing number, only after markers are gone so DVD2 does not
	// become a title of "DVD". It could be a disc number, a season, or part of
	// the name — NEXTGEN2 gives no way to tell — so it is recorded as a disc
	// number, which is the commonest, and the uncertainty is paid for in
	// confidence.
	if c.Season == 0 && c.Disc == 0 && c.Year == 0 {
		if m := trailingDigits.FindStringSubmatch(strings.ToLower(work)); m != nil {
			n, _ := strconv.Atoi(m[2])
			if n > 0 && n <= 20 {
				work = work[:len(m[1])]
				c.Disc = n
				c.Confidence = 0.35
				c.Why = "label ends in a bare number, which may be a disc or a season"
			}
		}
	}

	title := titleCase(work)
	if title == "" {
		return nil, nil
	}
	c.Title = title

	// A label that survives as a single short token is an abbreviation, not a
	// title: STNG, NEXTGEN. Naming the library after it would be worse than
	// leaving it to another net, so it is reported at a confidence that any
	// real evidence beats.
	if looksAbbreviated(title, raw == strings.ToUpper(raw)) {
		c.Confidence = 0.15
		c.Why = "label looks like an abbreviation rather than a title"
	}

	if len(dropped) > 0 {
		c.Why += " (ignored " + strings.Join(dropped, ", ") + ")"
	}
	return []Candidate{c}, nil
}

// stripMarkers removes format words, returning what it took.
func stripMarkers(s string) (string, []string) {
	parts := splitWords(s)
	var kept, dropped []string
	for _, p := range parts {
		if formatMarkers[strings.ToLower(p)] {
			dropped = append(dropped, p)
			continue
		}
		kept = append(kept, p)
	}
	// Never strip everything: a disc labelled DISC_2 is better named "Disc"
	// than left with no name at all, and the confidence already says so.
	if len(kept) == 0 {
		return s, nil
	}
	return strings.Join(kept, " "), dropped
}

// splitWords breaks a label on the separators labels actually use.
func splitWords(s string) []string {
	f := func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == ' ' || r == '+' }
	return strings.FieldsFunc(s, f)
}

// titleCase renders a label as a title.
//
// Labels are usually upper-case because ISO9660 requires it, so lower-casing
// and re-capitalising reads better than leaving ROMAN_HOLIDAY shouting. A label
// that is already mixed case was typed by a person and is left alone: "Earthsea"
// is what someone meant.
func titleCase(s string) string {
	words := splitWords(s)
	if len(words) == 0 {
		return ""
	}
	mixed := s != strings.ToUpper(s)
	out := make([]string, 0, len(words))
	for _, w := range words {
		if mixed {
			out = append(out, w)
			continue
		}
		lower := strings.ToLower(w)
		out = append(out, strings.ToUpper(lower[:1])+lower[1:])
	}
	return strings.Join(out, " ")
}

// looksAbbreviated reports whether a title is probably an abbreviation.
//
// A single short word from an all-upper-case label, with no separator anywhere:
// STNG, NEXTGEN. The upper-case part matters, because a label somebody typed by
// hand is a name they meant — Earthsea and Hackers are both short single words
// and both are real titles.
//
// A genuinely short all-caps film title, ALIEN or SEVEN, is marked weak by this
// and that is accepted. It still produces the right name; it just produces it at
// a confidence any real evidence can beat, and with nothing to contradict it the
// answer stands regardless. The reverse mistake is the expensive one: a
// confident "Stng" cannot be overruled by the net that knows better.
func looksAbbreviated(title string, wasUpper bool) bool {
	if !wasUpper {
		return false
	}
	if strings.Contains(title, " ") {
		return false
	}
	return len(title) <= 8
}
