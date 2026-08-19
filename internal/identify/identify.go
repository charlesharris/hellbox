// Package identify works out what is actually on a disc.
//
// Naming has until now come from the volume label alone, which is why the
// library contains "STNGD5" and "NEXTGEN2". The label is not a title: it is
// eleven upper-case characters chosen by whoever authored the disc, and on this
// collection six physically different Still Game discs all carry the byte
// identical string STILL_GAME. No parser can separate those, because the
// information is not there.
//
// So identification is several independent attempts rather than one algorithm.
// Each net looks at a different kind of evidence — the label, the shape of the
// titles, a content fingerprint against a public database, eventually the text
// on the menus — and each says what it thinks and how sure it is. They disagree
// often. Resolve weighs them rather than trusting whichever ran last.
//
// A net that knows nothing returns nothing. That is a normal answer and not an
// error: the label net has nothing useful to say about a disc labelled DVD_VIDEO
// and should say so rather than inventing a film called "Dvd Video".
package identify

import (
	"context"
	"sort"
	"strings"
)

// Kind is what sort of thing a disc holds.
//
// Deliberately not library.Kind: identification is upstream of filing and must
// not depend on it, and "I could not tell" is a real answer here where it is not
// one there.
type Kind string

const (
	KindUnknown Kind = ""
	KindMovie   Kind = "movie"
	KindSeries  Kind = "series"
)

// Input is everything known about a disc before it has a name.
type Input struct {
	// VolumeLabel is the filesystem label, which may be empty, may be a title,
	// and may be DVD_VIDEO.
	VolumeLabel string

	// DiscType is "dvd" or "bluray". It matters because the evidence available
	// differs: a DVD is decrypted to disk and can be fingerprinted from its
	// VIDEO_TS file sizes, where a Blu-ray is read straight from the drive.
	DiscType string

	// Titles are the durations, in seconds, of everything worth ripping, in
	// disc order. The shape of that list says more than it looks: seven titles
	// of twenty-two minutes each is a television disc no matter what the label
	// claims.
	Titles []int

	// Fingerprint is hellbox's own disc fingerprint. It identifies the disc
	// across rips but means nothing to anyone else.
	Fingerprint string

	// ContentHash is the DiscDB-compatible hash, when it could be computed.
	// Empty when the disc's files were never on disk to measure.
	ContentHash string

	// DiscName is the name the disc gives itself, where it has one: a DVD's
	// Text Data Manager or a Blu-ray's bdmt_eng.xml. It is the strongest
	// evidence available before anything is ripped, and it is readable with no
	// key even on a disc that cannot be decrypted.
	//
	// Populated by the caller rather than read here, so that this package does
	// no device I/O and stays testable without a drive.
	DiscName string

	// DiscNameSource says where DiscName came from, for Candidate.Why. Something
	// like "the DVD text data manager" or "bdmt_eng.xml".
	DiscNameSource string
}

// Candidate is one net's proposal, with its reasoning.
type Candidate struct {
	Title string
	Year  int
	Kind  Kind

	// Season and Disc are zero when unknown, which for a label is most of the
	// time. Disc number is not season number and conflating them is how a disc
	// labelled STNG4 becomes season 4 when it is disc 4.
	Season int
	Disc   int

	// Net is which net proposed this, kept so a wrong name in the library can
	// be traced to the evidence that produced it.
	Net string

	// Confidence is 0 to 1. It is a claim about evidence, not about the answer
	// being right: a label net that read "ROMAN_HOLIDAY" is confident it read
	// the label correctly, not that the film is Roman Holiday.
	Confidence float64

	// Why is a short human-readable account, for the log and for slay.
	Why string
}

// Net is one way of identifying a disc.
type Net interface {
	// Name identifies the net in logs and in Candidate.Net.
	Name() string

	// Identify returns any candidates this net can support. Returning none is
	// normal. An error means the net could not run — a database that would not
	// open — not that the disc is unidentifiable.
	Identify(ctx context.Context, in Input) ([]Candidate, error)
}

// Result is what the nets between them concluded.
type Result struct {
	// Best is the leading candidate, or a zero Candidate when no net had
	// anything. Callers must handle the empty case: falling back to the volume
	// label is a decision for the caller, not for this package.
	Best Candidate

	// All is every candidate from every net, best first, kept so a disagreement
	// is inspectable rather than silently resolved.
	All []Candidate

	// Contested is true when the leading candidates disagree about the title.
	// Two nets arriving at the same name independently is worth far more than
	// one net being sure, and the opposite is worth flagging.
	Contested bool
}

// Run asks every net and resolves what they say.
//
// Nets run in sequence rather than concurrently. They are cheap next to reading
// a disc, and one of them will eventually shell out to OCR, at which point
// running them in parallel with a rip is a worse idea than it looks.
func Run(ctx context.Context, nets []Net, in Input) (Result, error) {
	var all []Candidate
	for _, n := range nets {
		cands, err := n.Identify(ctx, in)
		if err != nil {
			// One net failing must not lose the others' answers. The error is
			// dropped rather than returned for the same reason: a DiscDB that
			// will not open is not a reason to refuse to name the disc from its
			// label.
			continue
		}
		for _, c := range cands {
			if c.Net == "" {
				c.Net = n.Name()
			}
			if strings.TrimSpace(c.Title) == "" {
				continue
			}
			all = append(all, c)
		}
	}
	return Resolve(all), nil
}

// Resolve picks a winner from what the nets proposed.
//
// Agreement counts for more than confidence. Two nets reaching the same title
// from different evidence — a label that reads "Roman Holiday" and a content
// hash that matches Roman Holiday — is a far stronger signal than one net being
// sure on its own, so agreeing candidates are merged and their confidence
// raised rather than being made to compete.
func Resolve(all []Candidate) Result {
	if len(all) == 0 {
		return Result{}
	}

	// Grouped by normalised title so "Roman Holiday" and "ROMAN_HOLIDAY" from
	// two different nets count as agreement rather than as a dispute.
	groups := map[string][]Candidate{}
	var order []string
	for _, c := range all {
		k := normaliseTitle(c.Title)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], c)
	}

	merged := make([]Candidate, 0, len(order))
	for _, k := range order {
		merged = append(merged, mergeGroup(groups[k]))
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Confidence > merged[j].Confidence
	})

	res := Result{Best: merged[0], All: merged}

	// Contested when the runner-up is close enough that picking the leader is
	// close to a coin toss. The threshold is deliberately generous: a wrong
	// name filed silently is worse than one flagged for a look.
	if len(merged) > 1 && merged[0].Confidence-merged[1].Confidence < 0.2 {
		res.Contested = true
	}
	return res
}

// mergeGroup combines candidates that agree on the title.
func mergeGroup(g []Candidate) Candidate {
	sort.SliceStable(g, func(i, j int) bool { return g[i].Confidence > g[j].Confidence })
	best := g[0]

	if len(g) == 1 {
		return best
	}

	// Independent agreement raises confidence, with a ceiling: no amount of
	// agreement makes a guess a fact, and leaving room below 1 keeps a later
	// net that reads the actual title text able to outrank a consensus of
	// inferences.
	nets := map[string]bool{}
	for _, c := range g {
		nets[c.Net] = true
	}
	if len(nets) > 1 {
		best.Confidence += 0.15 * float64(len(nets)-1)
		if best.Confidence > 0.95 {
			best.Confidence = 0.95
		}
		var names []string
		for _, c := range g {
			names = append(names, c.Net)
		}
		best.Why += " (agreed by " + strings.Join(names, ", ") + ")"
	}

	// Details the leader lacked are taken from the others: a content hash may
	// know the year while the label knows nothing, and vice versa.
	for _, c := range g[1:] {
		if best.Year == 0 {
			best.Year = c.Year
		}
		if best.Season == 0 {
			best.Season = c.Season
		}
		if best.Disc == 0 {
			best.Disc = c.Disc
		}
		if best.Kind == KindUnknown {
			best.Kind = c.Kind
		}
	}
	return best
}

// normaliseTitle reduces a title to what two nets have to share to be counted
// as agreeing.
func normaliseTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
