package identify

import (
	"regexp"
	"strconv"
	"strings"
)

// MenuEntry is one line read off a disc menu: a name, and the running time
// printed beside it.
//
// The running time is what makes this worth doing. OCR of a DVD menu is not
// clean — 720x480, ornate fonts, artwork behind the text — and on a real disc
// it produced "On Location (x1:17)" and "ALTERNATE ENDIN® (1:01)". A name read
// that badly is not something to file a library by on its own.
//
// But the disc already told us, in the scan, exactly how long each title is. A
// printed time next to a name is therefore a checksum: it says which title the
// name belongs to. The name only has to be legible enough to be worth reading,
// not perfect, and the pairing is what carries the confidence.
type MenuEntry struct {
	Name string

	// Seconds is the running time printed beside the name.
	Seconds int

	// Raw is the OCR line it came from, kept because a wrong name is much
	// easier to understand next to what was actually read.
	Raw string
}

// timePattern matches a running time printed on a menu: (11:17), [7:47], 1:31:04.
//
// Deliberately loose about the brackets, which discs render in every style
// there is, and about surrounding punctuation, which OCR invents freely.
var timePattern = regexp.MustCompile(`[\(\[\{]?\s*(\d{1,2})\s*[:;.]\s*(\d{2})(?:\s*[:;.]\s*(\d{2}))?\s*[\)\]\}]?`)

// ParseMenuLines pulls name-and-time pairs out of OCR output.
//
// Lines with no time on them are dropped. That throws away real menu items —
// "Play", "Scene Selection", "Main Menu" — which is correct: those name a
// navigation target rather than a title, and are exactly the lines that would
// otherwise be filed as episode names.
func ParseMenuLines(text string) []MenuEntry {
	var out []MenuEntry
	for _, line := range strings.Split(text, "\n") {
		loc := timePattern.FindStringSubmatchIndex(line)
		if loc == nil {
			continue
		}
		m := timePattern.FindStringSubmatch(line)

		secs := parseClock(m[1], m[2], m[3])
		if secs <= 0 {
			continue
		}

		name := cleanMenuName(line[:loc[0]])
		if name == "" {
			// A time with nothing readable before it is still worth keeping:
			// the time alone can confirm a title exists, and a later pass may
			// recover the name from another frame of the same menu.
			name = ""
		}
		out = append(out, MenuEntry{Name: name, Seconds: secs, Raw: strings.TrimSpace(line)})
	}
	return out
}

// parseClock turns matched digits into seconds, treating a three-part match as
// hours:minutes:seconds and a two-part one as minutes:seconds.
func parseClock(a, b, c string) int {
	x, _ := strconv.Atoi(a)
	y, _ := strconv.Atoi(b)
	if c != "" {
		z, _ := strconv.Atoi(c)
		if y > 59 || z > 59 {
			return 0
		}
		return x*3600 + y*60 + z
	}
	if y > 59 {
		return 0
	}
	return x*60 + y
}

// menuNoise are characters OCR produces from artwork rather than from text.
var menuNoise = regexp.MustCompile(`[^A-Za-z0-9'&:,!?/. -]+`)

// runsOfSpace collapses the gaps left behind once noise is removed.
var runsOfSpace = regexp.MustCompile(`\s{2,}`)

// cleanMenuName tidies the text before a running time.
//
// It is deliberately conservative. Over-cleaning invents titles: a line read as
// "eg 1 ow Tocasion" is not something to salvage, and returning nothing is a
// better answer than returning a plausible-looking guess that will be filed and
// never questioned.
func cleanMenuName(s string) string {
	s = menuNoise.ReplaceAllString(s, " ")
	s = runsOfSpace.ReplaceAllString(s, " ")
	s = strings.Trim(s, " .,:-'/")

	// Leading fragments OCR leaves from artwork: a word or two of one or two
	// characters before the real text starts.
	fields := strings.Fields(s)
	for len(fields) > 1 && len(fields[0]) <= 2 && !isWordish(fields[0]) {
		fields = fields[1:]
	}
	s = strings.Join(fields, " ")

	if len([]rune(s)) < 3 {
		return ""
	}

	// Mostly-punctuation or mostly-single-letter output is artwork, not a name.
	letters := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	if letters*2 < len([]rune(s)) {
		return ""
	}
	return s
}

// isWordish reports whether a short token is a real word rather than debris.
func isWordish(s string) bool {
	switch strings.ToLower(s) {
	case "a", "i", "an", "of", "in", "on", "to", "it", "is", "at", "up", "my", "no", "we", "dr", "mr", "ms", "st":
		return true
	}
	return false
}

// AssignByDuration matches menu entries to the disc's titles by running time.
//
// Both halves come from the same disc, which is what makes this sound where
// matching durations against an external database was not: every forty-five
// minute drama in the world has the same runtime as every other, but a name
// printed beside 45:30 *on this disc* belongs to the 45:30 title *on this disc*.
//
// tolerance is in seconds. Menus round, and OCR misreads digits, so an exact
// match cannot be required — but a loose tolerance on a disc of similar-length
// episodes assigns names to the wrong titles, so anything ambiguous is dropped
// rather than guessed.
func AssignByDuration(entries []MenuEntry, titleSecs []int, tolerance int) map[int]MenuEntry {
	out := map[int]MenuEntry{}
	used := map[int]bool{}

	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		best, bestDelta, ties := -1, tolerance+1, 0
		for i, d := range titleSecs {
			if used[i] {
				continue
			}
			delta := d - e.Seconds
			if delta < 0 {
				delta = -delta
			}
			if delta > tolerance {
				continue
			}
			switch {
			case delta < bestDelta:
				best, bestDelta, ties = i, delta, 1
			case delta == bestDelta:
				ties++
			}
		}
		// A tie means two titles are equally close, and picking either is a
		// coin toss that would be filed as fact.
		if best >= 0 && ties == 1 {
			out[best] = e
			used[best] = true
		}
	}
	return out
}
