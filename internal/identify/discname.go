package identify

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// DiscNameNet reads the name the disc gives itself.
//
// This is the strongest evidence available before anything is ripped, and both
// formats carry it in their own way:
//
//   - A DVD keeps it in the Text Data Manager inside VIDEO_TS.IFO. The Karate
//     Kid, whose volume label is DVD_VIDEO, holds "The Karate Kid (Special
//     Edition)" there along with a sort title.
//   - A Blu-ray keeps it in BDMV/META/DL/bdmt_eng.xml. Firefly disc 1, whose
//     volume label is FIREFLYUS_D1, holds "FIREFLY: DISC 1".
//
// Neither is encrypted, so both are readable with no key and no decryption,
// even on a disc that cannot be ripped at all.
//
// The design treated these as two nets, IfoNet and BdmtNet. They are one: the
// evidence is identical in kind — prose a person typed rather than eleven
// upper-case characters a filesystem allowed — and splitting them would mean
// two implementations of the same reasoning that could drift apart. Where the
// name came from is recorded in Why, which is what anyone tracing a wrong name
// actually needs.
type DiscNameNet struct{}

func (DiscNameNet) Name() string { return "discname" }

// editionWords mark a trailing parenthetical as describing the release rather
// than naming the work.
//
// Only parentheticals are considered. LabelNet deliberately leaves bare edition
// words alone because they are so often part of a real title, and that rule
// still holds — but "(Special Edition)" appended to a name is a different
// construction, and leaving it on costs a provider match.
var editionWords = []string{
	"special edition", "collector's edition", "collectors edition",
	"deluxe edition", "extended edition", "director's cut", "directors cut",
	"unrated", "uncut", "remastered", "restored", "anniversary edition",
	"limited edition", "ultimate edition", "widescreen", "fullscreen",
	"full screen", "wide screen", "theatrical cut", "theatrical version",
	"2-disc set", "two-disc set", "disc 1", "disc 2", "bonus disc",
}

var (
	// trailingParen matches a parenthetical at the end: "Foo (Special Edition)".
	trailingParen = regexp.MustCompile(`^(.*?)\s*\(([^()]{1,40})\)\s*$`)

	// yearParen matches a year in parentheses, which is a gift rather than noise.
	yearParen = regexp.MustCompile(`^(19\d{2}|20\d{2})$`)

	// discSuffixInName matches "FIREFLY: DISC 1" and similar.
	discInName = regexp.MustCompile(`(?i)^(.*?)[\s:,-]+dis[ck]\s*(\d{1,2})\s*$`)

	// seasonInName matches an explicit season written into the name.
	seasonInName = regexp.MustCompile(`(?i)^(.*?)[\s:,-]+(?:season|series)\s*(\d{1,2})\s*$`)
)

// Identify reads Input.DiscName.
func (n DiscNameNet) Identify(_ context.Context, in Input) ([]Candidate, error) {
	raw := strings.TrimSpace(in.DiscName)
	if raw == "" {
		return nil, nil
	}
	if junkLabels[strings.ToLower(strings.ReplaceAll(raw, " ", "_"))] {
		return nil, nil
	}

	source := strings.TrimSpace(in.DiscNameSource)
	if source == "" {
		source = "the disc"
	}

	c := Candidate{
		Net: n.Name(),
		// High, but never certain. It is a name someone typed, which is not the
		// same as the name a metadata provider files the work under — and it is
		// occasionally the studio, the disc's marketing name, or a box-set label.
		Confidence: 0.8,
		Why:        "read from " + source,
	}

	work := raw
	var notes []string

	// Both patterns anchor at the end of the string, so whichever marker comes
	// last has to be stripped first: "Season 3 Disc 2" only yields its season
	// once "Disc 2" is gone. Rather than depend on discs writing them in a
	// consistent order, peel markers off the tail until none is left.
	for peeled := true; peeled; {
		peeled = false
		if m := discInName.FindStringSubmatch(work); m != nil && c.Disc == 0 {
			work = m[1]
			c.Disc, _ = strconv.Atoi(m[2])
			// A disc number says a set, which says episodes far more often than
			// not — but not always, so it informs Kind only when nothing else has.
			if c.Kind == KindUnknown {
				c.Kind = KindSeries
			}
			notes = append(notes, "disc "+m[2])
			peeled = true
			continue
		}
		if m := seasonInName.FindStringSubmatch(work); m != nil && c.Season == 0 {
			work = m[1]
			c.Season, _ = strconv.Atoi(m[2])
			c.Kind = KindSeries
			notes = append(notes, "season "+m[2])
			peeled = true
		}
	}

	// A trailing parenthetical is either a year, an edition, or part of the
	// title. Only the first two are removed.
	if m := trailingParen.FindStringSubmatch(work); m != nil {
		inner := strings.TrimSpace(m[2])
		switch {
		case yearParen.MatchString(inner):
			c.Year, _ = strconv.Atoi(inner)
			work = m[1]
			notes = append(notes, "year "+inner)
		case isEdition(inner):
			work = m[1]
			notes = append(notes, "edition "+strconv.Quote(inner))
		}
	}

	title := tidyDiscName(work)
	if title == "" {
		return nil, nil
	}
	c.Title = title

	if len(notes) > 0 {
		c.Why += " (" + strings.Join(notes, ", ") + ")"
	}
	return []Candidate{c}, nil
}

func isEdition(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, w := range editionWords {
		if l == w || strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// tidyDiscName cleans a name without rewriting it.
//
// Deliberately light. Unlike a volume label, this text was typed by a person
// and mostly wants leaving alone; the one transformation worth making is
// case, because some discs shout.
func tidyDiscName(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ":,-–—"))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}

	// All upper case with no lower anywhere is a shout, not a style choice:
	// "FIREFLY" becomes "Firefly". Anything with mixed case was written the way
	// someone wanted it and is left exactly as found.
	if s == strings.ToUpper(s) && strings.ToUpper(s) != strings.ToLower(s) {
		words := strings.Fields(strings.ToLower(s))
		for i, w := range words {
			r := []rune(w)
			r[0] = []rune(strings.ToUpper(string(r[0])))[0]
			words[i] = string(r)
		}
		s = strings.Join(words, " ")
	}
	return s
}
