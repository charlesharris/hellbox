// Package disc holds the description of a physical disc and its titles.
//
// The description produced here is the single most valuable artifact hellbox
// creates. Later phases identify what a disc actually contains by reasoning
// over title runtimes and stream layouts, and they must be able to do that
// against discs ripped months earlier without the disc being present. Anything
// cheap to record now is recorded.
package disc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Type distinguishes disc formats.
type Type string

const (
	TypeDVD     Type = "dvd"
	TypeBluRay  Type = "bluray"
	TypeUnknown Type = "unknown"
)

// Disc describes one physical disc.
type Disc struct {
	Fingerprint string `json:"fingerprint"`
	VolumeLabel string `json:"volume_label"`

	// DiscName is the name the disc gives itself: a DVD's Text Data Manager or
	// a Blu-ray's bdmt_eng.xml. Unlike the volume label it is prose someone
	// typed, and it is frequently present when the label says DVD_VIDEO.
	//
	// It is recorded separately rather than replacing VolumeLabel because the
	// two disagree and both are evidence. The fingerprint still derives from
	// the label alone, so adding this cannot change which discs are recognised.
	DiscName   string `json:"disc_name,omitempty"`
	Type       Type   `json:"type"`
	TotalBytes int64  `json:"total_bytes"`

	ScannedAt      time.Time `json:"scanned_at"`
	DriveStableID  string    `json:"drive_stable_id,omitempty"`
	DriveModel     string    `json:"drive_model,omitempty"`
	MakeMKVVersion string    `json:"makemkv_version,omitempty"`

	Titles []Title `json:"titles"`
}

// Title is one selectable title on the disc.
type Title struct {
	Index         int    `json:"index"`
	DurationSecs  int    `json:"duration_secs"`
	Chapters      int    `json:"chapters"`
	SizeBytes     int64  `json:"size_bytes"`
	SourceFile    string `json:"source_file,omitempty"`
	SegmentsCount int    `json:"segments_count,omitempty"`
	SegmentsMap   string `json:"segments_map,omitempty"`
	Name          string `json:"name,omitempty"`

	// OutputFile is the name hellbox gives the ripped file, always
	// "title_NN.mkv". MakeMKV's own suggestion is derived from its guesswork
	// about content and is not stable, so it is recorded but not used.
	OutputFile           string `json:"output_file,omitempty"`
	MakeMKVSuggestedName string `json:"makemkv_suggested_name,omitempty"`

	Streams []Stream `json:"streams"`

	// Attrs is every attribute MakeMKV reported, by raw numeric code. It is
	// retained so that a mistaken assumption about which code means what can be
	// corrected by re-reading this file rather than the disc.
	Attrs map[int]string `json:"attrs,omitempty"`
}

// Duration renders the title runtime as H:MM:SS.
func (t Title) Duration() string {
	d := time.Duration(t.DurationSecs) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// Stream is one elementary stream inside a title.
type Stream struct {
	Index       int    `json:"index"`
	Kind        string `json:"kind"` // video, audio, subtitle
	Codec       string `json:"codec,omitempty"`
	Language    string `json:"language,omitempty"`
	LangName    string `json:"language_name,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	SampleRate  string `json:"sample_rate,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	FrameRate   string `json:"frame_rate,omitempty"`
	Bitrate     string `json:"bitrate,omitempty"`
	Flags       string `json:"flags,omitempty"`
	Name        string `json:"name,omitempty"`

	Attrs map[int]string `json:"attrs,omitempty"`
}

// ComputeFingerprint derives a stable identifier for a physical disc from its
// structure alone, with no reading of the disc body. This is what lets hellbox
// recognise a disc it has already ripped within seconds of it being inserted,
// which matters because the operator is not standing over the machine.
//
// Titles are sorted before hashing so that any variation in the order
// makemkvcon enumerates them cannot change the result.
func ComputeFingerprint(volumeLabel string, titles []Title) string {
	type entry struct {
		dur  int
		size int64
	}
	entries := make([]entry, 0, len(titles))
	for _, t := range titles {
		entries = append(entries, entry{t.DurationSecs, t.SizeBytes})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].dur != entries[j].dur {
			return entries[i].dur < entries[j].dur
		}
		return entries[i].size < entries[j].size
	})

	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00", strings.TrimSpace(volumeLabel), len(titles))
	for _, e := range entries {
		fmt.Fprintf(h, "%d:%d\x00", e.dur, e.size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Short returns the abbreviated fingerprint used in directory names.
func Short(fingerprint string) string {
	if len(fingerprint) < 12 {
		return fingerprint
	}
	return fingerprint[:12]
}

// genericLabels are volume labels that carry no information. Authoring tools
// emit them constantly, so they must not become directory names.
var genericLabels = map[string]bool{
	"":                  true,
	"dvd_video":         true,
	"dvdvideo":          true,
	"logical_volume_id": true,
	"unknown":           true,
	"untitled":          true,
	"new_volume":        true,
	"bd_rom":            true,
	"bluray":            true,
}

// DirName builds the rip directory name for a disc:
//
//	2026-07-27--still-game-series-1-disc-1--a3f9c2e10b47
//
// The name encodes disc identity only. Nothing here reflects a guess about what
// the disc contains; associating a disc with a film or a series happens in a
// later phase by writing records, never by renaming this directory.
func (d Disc) DirName() string {
	slug := Slug(d.VolumeLabel)
	if genericLabels[strings.ToLower(strings.TrimSpace(d.VolumeLabel))] || slug == "" {
		// The disc's own name, where the label gave nothing. This is still
		// naming by disc identity rather than by guessed content: the string
		// is on the disc, put there by whoever authored it, and is not an
		// inference about what the disc contains.
		//
		// It turns "2026-08-09--unlabeled--31181d14abe3" into
		// "2026-08-09--the-karate-kid-special-edition--31181d14abe3", which is
		// the difference between a browsable rips tree and a wall of hashes.
		if s := Slug(d.DiscName); s != "" && !genericLabels[strings.ToLower(strings.TrimSpace(d.DiscName))] {
			slug = s
		} else {
			slug = "unlabeled"
		}
	}
	date := d.ScannedAt
	if date.IsZero() {
		date = time.Now()
	}
	return fmt.Sprintf("%s--%s--%s", date.Format("2006-01-02"), slug, Short(d.Fingerprint))
}

// Slug reduces a volume label to a filesystem-safe fragment.
func Slug(s string) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// TitleFileName is the name hellbox gives a ripped title.
func TitleFileName(index int) string { return fmt.Sprintf("title_%02d.mkv", index) }

// TotalDuration sums every title runtime, for display and sanity checks.
func (d Disc) TotalDuration() time.Duration {
	var total int
	for _, t := range d.Titles {
		total += t.DurationSecs
	}
	return time.Duration(total) * time.Second
}
