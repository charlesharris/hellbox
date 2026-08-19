// Package library files finished transcodes where Jellyfin can find them.
//
// It identifies as little as possible. Jellyfin already matches titles against
// its own metadata providers, so hellbox's job is to produce names Jellyfin can
// match and a layout it understands — not to duplicate a job something else
// does better. What hellbox knows that Jellyfin cannot is what came off which
// disc, and that is what it contributes.
package library

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"hellbox/internal/disc"
)

// Kind is what a disc turned out to hold.
type Kind string

const (
	// KindMovie is a disc with one feature and, usually, some extras.
	KindMovie Kind = "movie"

	// KindTV is a disc of episodes.
	KindTV Kind = "tv"

	// KindUnknown is a disc that fits neither pattern.
	KindUnknown Kind = "unknown"
)

// Thresholds for telling a feature from a set of episodes. These are deliberate
// guesses at what a disc looks like, checked against real discs rather than
// derived from anything.
const (
	// featureRatio is how much longer the main title must be than the disc's
	// median title before the disc is called a film. A film disc is lopsided —
	// Roman Holiday's feature runs 4.6 times its longest extra — while an
	// episode disc is not.
	//
	// Against the median rather than the second-longest, because a DVD
	// routinely carries its feature twice: widescreen and fullscreen, or the
	// variants of a seamless-branching disc. National Treasure offers two
	// identical 131-minute titles among nineteen extras, and comparing the top
	// two made the most obviously film-shaped disc yet look like neither a film
	// nor a set of episodes.
	featureRatio = 2.0

	// minFeatureSecs keeps a long episode from being read as a feature.
	minFeatureSecs = 45 * 60

	// episodeSpread is how far from the median an episode may run. Episodes of
	// one show are near enough the same length; extras on a film disc are not.
	episodeSpread = 0.25

	minEpisodeSecs = 10 * 60
	maxEpisodeSecs = 90 * 60
)

// Classify decides what a disc holds from the shape of its titles.
//
// Structure is all there is to go on. A disc carries no statement of what it
// is, and its volume label is frequently no help — the Still Game discs are all
// labelled STILL_GAME with nothing to say which is which, or that they hold
// episodes at all.
func Classify(titles []disc.Title) Kind {
	if len(titles) == 0 {
		return KindUnknown
	}

	durations := make([]int, 0, len(titles))
	for _, t := range titles {
		durations = append(durations, t.DurationSecs)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(durations)))

	longest := durations[0]
	if len(titles) == 1 {
		if longest >= minFeatureSecs {
			return KindMovie
		}
		return KindUnknown
	}

	median := durations[len(durations)/2]

	// A television disc can be lopsided too, when it carries a double-length
	// episode: a Star Trek disc of one 90-minute two-parter and two 45-minute
	// episodes has a longest title exactly twice its median, which is the
	// definition of a film disc used below.
	//
	// What tells them apart is the rest of the disc. Take the longest title
	// away and what remains on an episode disc is still episodes — several
	// titles of much the same length, all of episode length. What remains on a
	// film disc is extras: a scatter of short pieces with nothing in common.
	// So the shape of the remainder is asked first.
	if len(durations) > 1 && looksLikeEpisodes(durations[1:]) {
		return KindTV
	}

	// A film disc is lopsided: the main title dwarfs the body of the disc.
	if longest >= minFeatureSecs && float64(longest) >= featureRatio*float64(median) {
		return KindMovie
	}

	// An episode disc is level: several titles of much the same length.
	if looksLikeEpisodes(durations) {
		return KindTV
	}

	return KindUnknown
}

// looksLikeEpisodes reports whether a set of runtimes is several titles of much
// the same length, all plausibly an episode. durations must be sorted.
func looksLikeEpisodes(durations []int) bool {
	if len(durations) < 2 {
		return false
	}
	median := durations[len(durations)/2]
	if median < minEpisodeSecs || median > maxEpisodeSecs {
		return false
	}
	var alike int
	for _, d := range durations {
		if d >= minEpisodeSecs && withinSpread(d, median, episodeSpread) {
			alike++
		}
	}
	return alike >= 2 && alike*2 >= len(durations)
}

func withinSpread(value, median int, spread float64) bool {
	if median <= 0 {
		return false
	}
	diff := float64(value-median) / float64(median)
	return diff <= spread && diff >= -spread
}

// Name turns a disc's volume label into something Jellyfin can match.
//
// Volume labels are upper case with underscores, and often carry disc and
// season markers that would stop a title matching. What is left is handed to
// Jellyfin, which is far better at working out what a film called "Roman
// Holiday" is than hellbox could be.
func Name(volumeLabel string) string {
	s := strings.TrimSpace(volumeLabel)
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("_", " ", ".", " ", "-", " ").Replace(s)

	// Disc, season and format markers are noise to a metadata matcher. The
	// format ones say how a film was framed for a 4:3 television — letterbox,
	// widescreen, fullscreen, pan-and-scan — which is a property of the
	// transfer rather than of the film, and leaves a title no provider will
	// match: SPACEBALLS_LB is not a film called "Spaceballs Lb".
	//
	// Only unambiguous markers are dropped. Edition words like "special" or
	// "collectors" are left alone: they are frequently part of the real title,
	// and removing them would break more matches than it fixed.
	words := strings.Fields(s)
	kept := make([]string, 0, len(words))
	for i, w := range words {
		lower := strings.ToLower(w)
		if formatMarkers[lower] {
			continue
		}
		if lower == "disc" || lower == "disk" || lower == "dvd" || lower == "side" {
			// Drop the marker and the number that usually follows it.
			if i+1 < len(words) && isNumberish(words[i+1]) {
				continue
			}
			continue
		}
		if i > 0 && isNumberish(w) {
			prev := strings.ToLower(words[i-1])
			if prev == "disc" || prev == "disk" || prev == "dvd" || prev == "side" {
				continue
			}
		}
		kept = append(kept, w)
	}
	if len(kept) == 0 {
		kept = words
	}

	for i, w := range kept {
		kept[i] = titleCase(w)
	}
	return strings.Join(kept, " ")
}

// formatMarkers describe how a transfer was framed, not what it is.
var formatMarkers = map[string]bool{
	"lb": true, // letterbox
	"ws": true, // widescreen
	"fs": true, // fullscreen
	"ps": true, // pan and scan
}

func isNumberish(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// titleCase capitalises a word, leaving anything already mixed case alone so
// names like "McLeod" survive.
func titleCase(w string) string {
	if w == "" {
		return w
	}
	hasLower := strings.IndexFunc(w, unicode.IsLower) >= 0
	if hasLower {
		return w
	}
	r := []rune(strings.ToLower(w))
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// Placement is one file and where it belongs.
type Placement struct {
	TitleIndex int
	Path       string

	// Feature marks the main title of a film, as against its extras.
	Feature bool
}

// Plan decides where each of a disc's titles belongs under libraryDir.
//
// Films get Jellyfin's exact convention and should match without help. Episodes
// are filed under the show with the disc's fingerprint in the name and no
// episode numbers, because a disc does not carry them: the Still Game discs are
// each labelled STILL_GAME with nothing to distinguish them. The fingerprint
// makes every file traceable to the rip it came from, which is what a person
// renaming them needs.
func Plan(libraryDir string, d disc.Disc, kind Kind, sourceFor func(titleIndex int) string) []Placement {
	name := Name(d.VolumeLabel)
	if name == "" {
		name = "Unlabeled " + disc.Short(d.Fingerprint)
	}

	titles := append([]disc.Title(nil), d.Titles...)
	sort.Slice(titles, func(i, j int) bool { return titles[i].Index < titles[j].Index })

	var out []Placement
	switch kind {
	case KindMovie:
		feature := longestTitle(titles)
		for _, t := range titles {
			if t.Index == feature {
				out = append(out, Placement{
					TitleIndex: t.Index,
					Path:       filepath.Join(libraryDir, "Movies", name, name+".mkv"),
					Feature:    true,
				})
				continue
			}
			// Jellyfin treats an "extras" directory as bonus material rather
			// than as more copies of the film, which is what stops a featurette
			// being offered as an alternative version.
			out = append(out, Placement{
				TitleIndex: t.Index,
				Path: filepath.Join(libraryDir, "Movies", name, "extras",
					fmt.Sprintf("%s - t%02d.mkv", name, t.Index)),
			})
		}

	default:
		short := disc.Short(d.Fingerprint)
		for _, t := range titles {
			out = append(out, Placement{
				TitleIndex: t.Index,
				Path: filepath.Join(libraryDir, "TV", name,
					fmt.Sprintf("%s - %s - t%02d.mkv", name, short, t.Index)),
			})
		}
	}
	return out
}

func longestTitle(titles []disc.Title) int {
	best, bestDur := -1, -1
	for _, t := range titles {
		if t.DurationSecs > bestDur {
			best, bestDur = t.Index, t.DurationSecs
		}
	}
	return best
}

// Link hardlinks src into place at dst.
//
// A hardlink rather than a copy: the library and the transcoded tree are the
// same bytes, so filing costs no space and no time. It also means a file
// renamed in the library keeps pointing at the same data, which is the whole
// workflow for episodes.
//
// An existing dst is never replaced. It may be a file someone has renamed or
// re-encoded by hand, and silently overwriting that would lose work hellbox did
// not do.
func Link(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return ErrExists
	}
	if err := os.Link(src, dst); err != nil {
		return fmt.Errorf("link %s to %s: %w", src, dst, err)
	}
	return nil
}

// ErrExists reports that something is already at the destination.
var ErrExists = fmt.Errorf("a file is already in place")
