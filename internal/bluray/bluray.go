// Package bluray reads what a Blu-ray holds, without decrypting it.
//
// The separation matters more here than anywhere else in hellbox. A Blu-ray's
// playlists and its BDMV/META metadata are not encrypted — only the .m2ts
// payload is — so a disc that cannot be played can still be described
// completely: its name, its cover art, how many episodes it holds, how long
// each runs, and what streams each carries.
//
// That is not a curiosity. Both Blu-rays tested during the v2 design were BD+
// protected and neither could be decrypted by the free stack, yet both
// enumerated perfectly. A disc hellbox cannot rip should therefore reach the
// user as "Firefly: Disc 1 — 4 episodes, BD+, needs MakeMKV" rather than as an
// opaque failure. So enumeration always runs, and it always runs first.
//
// Everything here is a parser over libbluray's own tools, bd_info and
// bd_list_titles. They open the device directly: no mount, no root.
package bluray

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hellbox/internal/disc"
)

// Protection is what stands between hellbox and the disc's payload.
type Protection struct {
	AACS   bool
	BDPlus bool

	// AACSHandled and BDPlusHandled are libbluray's own verdict on whether it
	// can deal with each scheme. On the discs tested, AACS was handled and BD+
	// was not, which is the combination that decides everything downstream.
	AACSHandled   bool
	BDPlusHandled bool

	// BDJ reports a disc whose menus are Java. It has no bearing on ripping
	// playlists and is recorded only because a missing JVM produces alarming
	// log lines that are not failures.
	BDJ bool

	// DiscID and MKBVersion come from AACS and identify the pressing. Worth
	// keeping: the disc id is a far better external identifier than a volume
	// label, and hellbox has nothing else like it.
	DiscID     string
	MKBVersion int
}

// NeedsMakeMKV reports a disc the free stack cannot decrypt but MakeMKV may be
// able to.
//
// BD+ is the only such case seen. libbdplus is installed but the BD+ virtual
// machine data it needs is shipped by no distribution, so bdplus_init() fails
// and libbluray refuses the disc outright.
func (p Protection) NeedsMakeMKV() bool {
	return p.BDPlus && !p.BDPlusHandled
}

// Describe renders the protection state for a log line or the UI.
func (p Protection) Describe() string {
	switch {
	case p.BDPlus && !p.BDPlusHandled:
		return "BD+ (not handled — needs MakeMKV)"
	case p.BDPlus:
		return "AACS + BD+"
	case p.AACS && p.AACSHandled:
		return "AACS"
	case p.AACS:
		return "AACS (not handled)"
	}
	return "none"
}

// Playlist is one entry from bd_list_titles.
//
// The stream counts are the useful part and the reason this type is not just a
// duration. Duration alone cannot separate a 28-minute featurette from a
// 43-minute episode, but stream layout can: on Firefly disc 1 every episode
// carried 4-5 audio tracks and 6 subtitle tracks, the featurette carried one
// audio track and no subtitles, and the two dozen junk playlists carried no
// audio at all.
type Playlist struct {
	Index        int
	File         string // 00001.mpls
	DurationSecs int
	Chapters     int
	Angles       int
	Clips        int

	Video     int
	Audio     int
	Subtitles int // PG, presentation graphics
	Overlays  int // IG, interactive graphics
}

// Info is everything readable from a Blu-ray without decrypting it.
type Info struct {
	VolumeLabel string

	// DiscName comes from BDMV/META/DL/bdmt_eng.xml and is prose a human typed
	// — "FIREFLY: DISC 1". It is far stronger evidence than a volume label and
	// is what the identification net reads.
	DiscName   string
	Thumbnails []string

	Protection Protection

	// MainTitle is the playlist libbluray nominates as the feature. It is
	// recorded and deliberately not trusted: on a disc of episodes it names the
	// longest one, which is how v1 turned a four-episode disc into one file.
	MainTitle int

	Playlists []Playlist
}

// minRealSecs is the shortest a playlist may run and still be content.
//
// Below this are stings, transitions, logo idents and the one-second stubs a
// disc uses for menu plumbing. Firefly disc 1 carried twenty-four of them.
const minRealSecs = 60

// Content returns the playlists that hold something worth ripping.
//
// The audio requirement is what makes this work. Every real title has at least
// one audio track; every junk playlist on the disc tested had none. Duration
// alone would have kept all twenty-four of them if the threshold were low, and
// dropped a short featurette if it were high.
func (i Info) Content(minSecs int) []Playlist {
	if minSecs <= 0 {
		minSecs = minRealSecs
	}
	var out []Playlist
	for _, p := range i.Playlists {
		if p.DurationSecs < minSecs || p.Audio < 1 {
			continue
		}
		out = append(out, p)
	}

	// Sorted by playlist filename, which is the disc's own ordering and not the
	// order libbluray enumerates in.
	//
	// This is load-bearing for television. libbluray returned Firefly disc 1 as
	// 00008, 00002, 00001, 00006, 00003, 00004 — putting the pilot third — while
	// the playlist numbers run 00001 (pilot), 00002, 00003, 00004 in broadcast
	// order. Episode assignment aligns a disc's titles against a season's
	// episodes in sequence, so enumeration order would have mislabelled every
	// episode on the disc while looking entirely plausible.
	sort.Slice(out, func(a, b int) bool { return out[a].File < out[b].File })

	return dedupe(out)
}

// dedupe collapses playlists that describe the same content.
//
// A disc routinely offers the same title several times over for seamless
// branching, and several more as decoys that exist only to confuse a ripper.
// Two playlists of identical duration and identical stream layout are the same
// title as far as hellbox is concerned, and the lowest-numbered one wins
// because that is the one the disc's own menus tend to reference.
func dedupe(in []Playlist) []Playlist {
	seen := map[string]bool{}
	var out []Playlist
	for _, p := range in {
		key := fmt.Sprintf("%d|%d|%d|%d|%d", p.DurationSecs, p.Chapters, p.Video, p.Audio, p.Subtitles)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// Titles converts the content playlists into hellbox's own title records.
//
// Index is hellbox's own 0-based ordering, not libbluray's, so that a title
// number means the same thing here as it does for a DVD. The playlist file is
// kept in SourceFile because it is what has to be handed back to ffmpeg to read
// the title again, and losing it would mean re-enumerating the disc.
func (i Info) Titles(minSecs int) []disc.Title {
	content := i.Content(minSecs)
	out := make([]disc.Title, 0, len(content))
	for n, p := range content {
		out = append(out, disc.Title{
			Index:        n,
			DurationSecs: p.DurationSecs,
			Chapters:     p.Chapters,
			SourceFile:   p.File,
			OutputFile:   disc.TitleFileName(n),
			Streams:      placeholderStreams(p),
		})
	}
	return out
}

// placeholderStreams records how many streams of each kind a playlist carries.
//
// bd_list_titles reports counts rather than descriptions — it will say five
// audio tracks without saying what they are — so these carry no codec or
// language. The real stream table arrives from ffprobe once the disc can
// actually be opened, and on a BD+ disc it never does. Recording the counts
// anyway is what lets the UI say "4 episodes, 5 audio tracks each" about a disc
// nothing can decrypt.
func placeholderStreams(p Playlist) []disc.Stream {
	var out []disc.Stream
	n := 0
	add := func(kind string, count int) {
		for c := 0; c < count; c++ {
			out = append(out, disc.Stream{Index: n, Kind: kind})
			n++
		}
	}
	add("video", p.Video)
	add("audio", p.Audio)
	add("subtitle", p.Subtitles)
	return out
}

// Disc assembles the full disc description.
func (i Info) Disc(minSecs int) disc.Disc {
	d := disc.Disc{
		VolumeLabel: i.VolumeLabel,
		Type:        disc.TypeBluRay,
		Titles:      i.Titles(minSecs),
	}
	d.Fingerprint = disc.ComputeFingerprint(d.VolumeLabel, d.Titles)
	return d
}

// Reader runs libbluray's tools.
type Reader struct {
	InfoBin   string // bd_info
	TitlesBin string // bd_list_titles
}

// NewReader returns a Reader using the tools on PATH.
func NewReader() *Reader {
	return &Reader{InfoBin: "bd_info", TitlesBin: "bd_list_titles"}
}

// Available reports whether both tools can be found.
func (r *Reader) Available() error {
	for _, bin := range []string{r.InfoBin, r.TitlesBin} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not on PATH (install libbluray-bin): %w", bin, err)
		}
	}
	return nil
}

// Enumerate reads the disc in devicePath.
//
// Both tools write their complaints about BD+ and a missing JVM to stderr and
// their answers to stdout, so stderr is discarded here rather than merged. Those
// complaints are not failures: a disc that cannot be decrypted still enumerates,
// and treating the noise as an error would throw away the answer.
func (r *Reader) Enumerate(ctx context.Context, devicePath string) (*Info, error) {
	infoOut, err := r.run(ctx, r.InfoBin, devicePath)
	if err != nil {
		return nil, fmt.Errorf("bd_info: %w", err)
	}
	info := ParseInfo(infoOut)

	titlesOut, err := r.run(ctx, r.TitlesBin, devicePath)
	if err != nil {
		// A disc whose playlists cannot be listed is still worth reporting with
		// whatever bd_info gave, which includes the name and the protection.
		return &info, fmt.Errorf("bd_list_titles: %w", err)
	}
	main, playlists := ParseTitles(titlesOut)
	info.MainTitle = main
	info.Playlists = playlists
	return &info, nil
}

func (r *Reader) run(ctx context.Context, bin, devicePath string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, devicePath)
	out, err := cmd.Output() // stderr deliberately dropped; see Enumerate
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return string(out), nil
}

// ---------- parsing ----------

// titleLine matches one line of bd_list_titles output:
//
//	index:   7 duration: 00:42:43 chapters:  13 angles:  1 clips:   1 (playlist: 00002.mpls) V:1 A:5  PG:6  IG:0  SV:0 SA:0
var titleLine = regexp.MustCompile(
	`index:\s*(\d+)\s+duration:\s*(\d+):(\d{2}):(\d{2})\s+chapters:\s*(\d+)\s+angles:\s*(\d+)\s+clips:\s*(\d+)\s+\(playlist:\s*([^\)]+?)\)\s*V:(\d+)\s+A:(\d+)\s+PG:(\d+)\s+IG:(\d+)`)

var mainTitleLine = regexp.MustCompile(`^Main title:\s*(\d+)`)

// ParseTitles reads bd_list_titles output.
func ParseTitles(out string) (mainTitle int, playlists []Playlist) {
	mainTitle = -1
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := mainTitleLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			mainTitle = atoi(m[1])
			continue
		}
		m := titleLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		playlists = append(playlists, Playlist{
			Index:        atoi(m[1]),
			DurationSecs: atoi(m[2])*3600 + atoi(m[3])*60 + atoi(m[4]),
			Chapters:     atoi(m[5]),
			Angles:       atoi(m[6]),
			Clips:        atoi(m[7]),
			File:         strings.TrimSpace(m[8]),
			Video:        atoi(m[9]),
			Audio:        atoi(m[10]),
			Subtitles:    atoi(m[11]),
			Overlays:     atoi(m[12]),
		})
	}
	return mainTitle, playlists
}

// ParseInfo reads bd_info output.
//
// The format is "Label : value" with generous whitespace, plus an indented list
// of thumbnails under a count. Anything unrecognised is ignored rather than
// treated as an error: bd_info prints a good deal that hellbox has no use for,
// and new fields must not break parsing.
func ParseInfo(out string) Info {
	var info Info
	var inThumbs bool

	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)

		// Thumbnail filenames are indented continuation lines under
		// "Thumbnail count", with no "key : value" shape of their own.
		if inThumbs {
			if line != "" && !strings.Contains(line, ":") && strings.HasPrefix(raw, "\t") {
				info.Thumbnails = append(info.Thumbnails, line)
				continue
			}
			if strings.HasPrefix(raw, "\t") {
				continue
			}
			inThumbs = false
		}

		key, value, ok := splitField(line)
		if !ok {
			continue
		}

		switch key {
		case "volume identifier":
			info.VolumeLabel = value
		case "disc name":
			info.DiscName = value
		case "aacs detected":
			info.Protection.AACS = isYes(value)
		case "aacs handled":
			info.Protection.AACSHandled = isYes(value)
		case "bd+ detected":
			info.Protection.BDPlus = isYes(value)
		case "bd+ handled":
			info.Protection.BDPlusHandled = isYes(value)
		case "bd-j detected":
			info.Protection.BDJ = isYes(value)
		case "disc id":
			info.Protection.DiscID = value
		case "aacs mkb version":
			info.Protection.MKBVersion = atoi(value)
		case "thumbnail count":
			inThumbs = true
		}
	}
	return info
}

// splitField breaks "Key    : value" into a lowercased key and its value.
//
// The value may itself contain a colon — "FIREFLY: DISC 1" does — so only the
// first separator counts.
func splitField(line string) (key, value string, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	value = strings.TrimSpace(line[i+1:])
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func isYes(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "yes") }

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
