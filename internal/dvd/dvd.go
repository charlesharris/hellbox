// Package dvd reads a DVD without MakeMKV.
//
// ffmpeg as shipped in Ubuntu 26.04 is built --enable-libdvdread
// --enable-libdvdnav and carries a dvdvideo demuxer that does every job hellbox
// previously needed MakeMKV for: title selection, chapter preservation,
// region-free playback and menu extraction. It has no registration key and
// nothing about it expires.
//
// Decryption is the part that was in doubt and is not any more. Verified on
// 2026-08-08 against a region 1/3/4 CSS disc in an RPC-2 drive with no region
// set: libdvdcss engages transparently under libdvdread, derives the title key
// from the disc when the drive refuses to hand one over, and the extracted
// video decodes cleanly. No region change was spent. DVDCSS_METHOD of key, disc
// and title all produced byte-identical output, so no override is needed.
//
// Two log lines from this stack look alarming and are not failures:
//
//	libdvdnav: Error cracking CSS key for /VIDEO_TS/VTS_03_1.VOB
//	libdvdread: Can't read name block. Probably not a DVD-ROM device.
//
// The first is emitted for title sets other than the one being read; the second
// when the input is a directory rather than a device. Both appear on reads that
// succeed completely, and neither may be matched as an error.
package dvd

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"hellbox/internal/disc"
)

// maxTitles bounds the probe sweep.
//
// The DVD-Video specification allows 99 titles. Real discs carry a dozen or so,
// and every probe costs a read, so the sweep stops at the first empty title
// rather than running to the limit — this is only a backstop against a disc
// that reports something at every index.
const maxTitles = 99

// Enumerator probes a DVD's titles.
type Enumerator struct {
	// FFprobeBin is the ffprobe executable. Empty finds it on PATH.
	FFprobeBin string

	// MinSeconds excludes menu loops and stills. Unlike MakeMKV's --minlength
	// this is an ordinary filter with no coupling to title numbering: DVD title
	// numbers are a property of the disc, so changing this between a scan and a
	// rip is safe. Under MakeMKV it was not, because it numbered titles within
	// its own filtered list.
	MinSeconds int
}

// NewEnumerator returns an Enumerator with the usual defaults.
func NewEnumerator() *Enumerator {
	return &Enumerator{FFprobeBin: "ffprobe", MinSeconds: 60}
}

func (e *Enumerator) bin() string {
	if e.FFprobeBin == "" {
		return "ffprobe"
	}
	return e.FFprobeBin
}

// Available reports whether ffprobe can be found.
func (e *Enumerator) Available() error {
	if _, err := exec.LookPath(e.bin()); err != nil {
		return fmt.Errorf("ffprobe not on PATH: %w", err)
	}
	return nil
}

// Enumerate walks the disc's titles.
//
// source is a device path, a directory holding VIDEO_TS, or an ISO — libdvdread
// takes all three, and a decrypted copy on disk is read exactly like a disc.
// Reading from a read-only mount was verified to decrypt correctly, because
// libdvdread resolves the mount point back to the underlying block device.
//
// Titles are probed in order until one comes back empty. **An absent title
// reports a duration of zero rather than failing**, which was established
// against a real disc: titles 13 and 14 of a twelve-title disc both returned
// successfully with nothing in them. Treating a non-zero exit as the end
// condition would have walked all 99.
func (e *Enumerator) Enumerate(ctx context.Context, source string) (*disc.Disc, error) {
	d := &disc.Disc{Type: disc.TypeDVD}

	for n := 1; n <= maxTitles; n++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		probe, err := e.probeTitle(ctx, source, n)
		if err != nil {
			// A title that will not probe ends the sweep rather than failing the
			// disc: everything found so far is real and worth keeping.
			break
		}
		if probe == nil || probe.durationSecs() <= 0 {
			break
		}
		d.Titles = append(d.Titles, probe.title(len(d.Titles)))
	}

	if len(d.Titles) == 0 {
		return nil, fmt.Errorf("no titles found on %s", source)
	}
	return d, nil
}

// Filtered returns the titles worth ripping.
func (e *Enumerator) Filtered(titles []disc.Title) []disc.Title {
	min := e.MinSeconds
	if min <= 0 {
		min = 60
	}
	out := make([]disc.Title, 0, len(titles))
	for _, t := range titles {
		if t.DurationSecs >= min {
			out = append(out, t)
		}
	}
	return out
}

// probeTitle runs one ffprobe against one title.
func (e *Enumerator) probeTitle(ctx context.Context, source string, title int) (*probeResult, error) {
	args := []string{
		"-hide_banner", "-v", "quiet",
		"-f", "dvdvideo",
		"-title", strconv.Itoa(title),
		// Region-free playback. The drive's region is irrelevant once libdvdcss
		// is deriving keys, and asking for 0 keeps it that way.
		"-region", "0",
		"-i", source,
		"-show_format", "-show_streams", "-show_chapters",
		"-of", "json",
	}
	cmd := exec.CommandContext(ctx, e.bin(), args...)
	out, err := cmd.Output() // stderr dropped: see the package comment on benign messages
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return parseProbe(out)
}

// ---------- ffprobe JSON ----------

type probeResult struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Chapters []struct {
		StartTime string `json:"start_time"`
	} `json:"chapters"`
	Streams []probeStream `json:"streams"`
}

type probeStream struct {
	Index         int               `json:"index"`
	CodecName     string            `json:"codec_name"`
	CodecType     string            `json:"codec_type"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	DisplayAspect string            `json:"display_aspect_ratio"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	RFrameRate    string            `json:"r_frame_rate"`
	Channels      int               `json:"channels"`
	SampleRate    string            `json:"sample_rate"`
	Disposition   map[string]int    `json:"disposition"`
	Tags          map[string]string `json:"tags"`
}

func parseProbe(b []byte) (*probeResult, error) {
	var p probeResult
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	return &p, nil
}

func (p *probeResult) durationSecs() int {
	f, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int(f)
}

// title converts a probe into a disc.Title at the given index.
func (p *probeResult) title(index int) disc.Title {
	t := disc.Title{
		Index:        index,
		DurationSecs: p.durationSecs(),
		Chapters:     len(p.Chapters),
		OutputFile:   disc.TitleFileName(index),
	}
	for _, s := range p.Streams {
		t.Streams = append(t.Streams, s.stream())
	}
	return t
}

func (s probeStream) stream() disc.Stream {
	out := disc.Stream{
		Index:       s.Index,
		Kind:        s.CodecType,
		Codec:       s.CodecName,
		Channels:    s.Channels,
		SampleRate:  s.SampleRate,
		AspectRatio: s.DisplayAspect,
		FrameRate:   s.RFrameRate,
	}
	if s.Width > 0 && s.Height > 0 {
		out.Resolution = fmt.Sprintf("%dx%d", s.Width, s.Height)
	}
	if s.Tags != nil {
		out.Language = s.Tags["language"]
		// DVD subpicture streams carry a VIEWPORT tag saying whether the track
		// was authored for a widescreen or a letterboxed transfer. MakeMKV never
		// surfaced this; it is worth keeping because it is exactly the kind of
		// marker that otherwise ends up glued to a title.
		if v := s.Tags["VIEWPORT"]; v != "" {
			out.Name = v
		}
	}
	var flags []string
	for _, k := range []string{"default", "forced"} {
		if s.Disposition[k] == 1 {
			flags = append(flags, k)
		}
	}
	out.Flags = strings.Join(flags, ",")
	return out
}

// ---------- PAL ----------

// PAL reports whether a disc is a 25fps 576-line transfer.
//
// This matters far more than it looks, and only for identification. A PAL disc
// runs film at 25fps instead of 24, so everything on it is about 4.17% shorter
// than the runtime any metadata provider lists: a 24:00 episode arrives as
// 23:00. Runtime matching that ignores it will mis-align a region 2 disc
// systematically — and plausibly, by one episode rather than by obvious
// nonsense, which is the worst way to be wrong.
//
// Detected from the streams rather than from the disc's region code, because
// the frame rate is what actually determines the discrepancy and a region 1
// disc could in principle carry PAL content.
func PAL(titles []disc.Title) bool {
	for _, t := range titles {
		for _, s := range t.Streams {
			if s.Kind != "video" {
				continue
			}
			if strings.HasSuffix(s.Resolution, "x576") {
				return true
			}
			if rateIs25(s.FrameRate) {
				return true
			}
		}
	}
	return false
}

// palSpeedup is the ratio between a PAL runtime and the film's true runtime.
const palSpeedup = 25.0 / 24.0

// TrueSeconds converts a runtime measured off a PAL disc back to the runtime a
// provider would list, so the two can be compared.
func TrueSeconds(palSeconds int) int {
	return int(float64(palSeconds)*palSpeedup + 0.5)
}

// rateIs25 reports whether an ffprobe rational frame rate is 25fps.
func rateIs25(rate string) bool {
	rate = strings.TrimSpace(rate)
	if rate == "" {
		return false
	}
	num, den, ok := strings.Cut(rate, "/")
	if !ok {
		return rate == "25"
	}
	n, err1 := strconv.ParseFloat(num, 64)
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return false
	}
	v := n / d
	return v > 24.9 && v < 25.1
}
