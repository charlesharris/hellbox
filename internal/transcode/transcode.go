// Package transcode turns a raw rip into a library file with ffmpeg.
//
// The raw rip is never touched. It is the system of record, and the whole
// reason ripping and transcoding are separate stages: a transcode can be redone
// with different settings, or after a bug, without going back to the disc.
//
// Hardware encoding through VAAPI is the default and software encoding is the
// fallback, because a machine whose GPU is unavailable should transcode slowly
// rather than not at all. On the Intel N100 this was measured rather than
// assumed: at matched output size the two are within 0.0013 SSIM of each other,
// and VAAPI is about five times faster.
package transcode

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Encoder runs ffmpeg.
type Encoder struct {
	// Bin is the ffmpeg executable, looked up on PATH when not absolute.
	Bin string

	// Device is the VAAPI render node. Empty selects software encoding.
	Device string
}

// New returns an Encoder. An empty bin selects "ffmpeg".
func New(bin, device string) *Encoder {
	if bin == "" {
		bin = "ffmpeg"
	}
	return &Encoder{Bin: bin, Device: device}
}

// Available reports whether ffmpeg can be found.
func (e *Encoder) Available() error {
	if filepath.IsAbs(e.Bin) {
		if _, err := os.Stat(e.Bin); err != nil {
			return fmt.Errorf("ffmpeg not found at %s: %w", e.Bin, err)
		}
		return nil
	}
	if _, err := exec.LookPath(e.Bin); err != nil {
		return fmt.Errorf("ffmpeg not on PATH: %w", err)
	}
	return nil
}

// Hardware reports whether this encoder will use the GPU.
func (e *Encoder) Hardware() bool { return e.Device != "" }

// Profile is a named set of encoding settings.
//
// There is deliberately no scaling and no frame rate conversion. The stack this
// replaces transcoded PAL television through a "HQ 720p30 Surround" preset,
// which resized every frame and resampled every field for no reason, producing
// episodes of 900 MB that looked worse than the disc. Whatever the disc holds is
// what gets encoded.
type Profile struct {
	// Name identifies the profile in the config and in logs.
	Name string

	// Quality is the encoder's quality parameter: qp for VAAPI, crf for
	// software. Lower is better. They are not the same scale, but they are
	// close enough on this material that one number serves both.
	Quality int

	// Preset is the software encoder's speed/efficiency tradeoff. Ignored by
	// VAAPI, which has no equivalent.
	Preset string

	// MaxKbps caps the output bitrate. Zero encodes at constant quality with no
	// ceiling.
	//
	// A quality parameter alone is a quantizer, not a size target, and what it
	// costs depends entirely on the source. The same setting that took a clean
	// film transfer from 5.4 GB to 1.78 GB produced television *larger than its
	// own source*, because it faithfully preserved broadcast noise. A ceiling
	// leaves clean material alone — it encodes well below the cap anyway — and
	// stops noisy material from spending its whole budget on grain.
	MaxKbps int

	// Interlaced marks a source stored as fields, which is deinterlaced before
	// encoding. Set from the source rather than configured: it is a property of
	// the disc, not a preference.
	Interlaced bool

	// MaxHeight caps the output height, preserving aspect ratio. Zero keeps
	// whatever the source is.
	//
	// Only ever downwards. A source already at or below the cap is untouched,
	// which is what keeps standard-definition discs free of the resampling that
	// made the previous stack's output worse than its input. What this is for
	// is high-definition source: a 1080p frame under a bitrate ceiling meant
	// for standard definition does not save space, it spends the saving on
	// looking soft. Fewer pixels at the same bitrate is the better trade.
	MaxHeight int

	// SourceHeight is what the source actually is, so scaling can be skipped
	// when it would do nothing.
	SourceHeight int

	// Streams is what the source carries, used to choose which to keep.
	// Empty keeps everything, which is what a caller that has not probed gets.
	Streams []Stream

	// Language is the preferred audio and subtitle language.
	Language string

	// AudioKbps is the bitrate for audio that has to be re-encoded. Lossless
	// Blu-ray tracks run three to four megabits; this is what they become.
	AudioKbps int
}

// Result is a finished transcode.
type Result struct {
	Path      string
	SizeBytes int64
	Elapsed   time.Duration
	Hardware  bool

	// Raw is ffmpeg's stderr, kept for the transcode log.
	Raw string

	// Streams is what was kept and what was dropped, so the choice is on the
	// record. It was computed and discarded before, which meant a finished file
	// gave no account of the tracks that did not make it into it.
	Streams Selection
}

// Progress is how far a transcode has got.
type Progress struct {
	// Fraction of the source duration completed, 0 when the duration is
	// unknown.
	Fraction float64

	// Speed is ffmpeg's own multiple of realtime, e.g. 28.7 for 28.7x.
	Speed float64
}

// Transcode encodes src to dst.
//
// dst is written through a temporary name and moved into place only on success,
// so an interrupted transcode cannot leave a file that looks finished. The
// caller supplies srcDuration so progress can be a fraction; zero disables it.
func (e *Encoder) Transcode(ctx context.Context, src, dst string, p Profile,
	srcDuration time.Duration, onProgress func(Progress)) (*Result, error) {

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}

	// Written beside the destination rather than in a scratch directory so the
	// move into place is a rename within one filesystem, which is atomic.
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".part")
	defer os.Remove(tmp)

	args := e.args(src, tmp, p)

	started := time.Now()
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second

	// -progress writes to stdout as plain key=value lines, which is far steadier
	// than scraping the status line ffmpeg draws on stderr.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", e.Bin, err)
	}

	sc := bufio.NewScanner(stdout)
	var prog Progress
	for sc.Scan() {
		if key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			if updateProgress(&prog, key, value, srcDuration) && onProgress != nil {
				onProgress(prog)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, lastLines(stderr.String(), 3))
	}

	info, err := os.Stat(tmp)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg reported success but wrote no output: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, fmt.Errorf("move the transcode into place: %w", err)
	}

	// Recomputed rather than threaded out of args: Select is pure, so this is
	// the same answer the command line was built from, and cheap.
	var sel Selection
	if len(p.Streams) > 0 {
		sel = Select(p.Streams, p.Language)
	}

	return &Result{
		Path:      dst,
		SizeBytes: info.Size(),
		Elapsed:   time.Since(started),
		Hardware:  e.Hardware(),
		Raw:       stderr.String(),
		Streams:   sel,
	}, nil
}

// args builds the ffmpeg command line.
//
// Split out so it can be read and tested without running anything: the ordering
// of VAAPI's device and format options is unforgiving, and a silent mistake
// here is an encode that either fails obscurely or quietly falls back to
// software.
func (e *Encoder) args(src, dst string, p Profile) []string {
	a := []string{"-hide_banner", "-nostdin", "-y"}

	if e.Hardware() {
		// Decode on the GPU and keep the frames there, so nothing crosses the
		// bus between decode and encode.
		a = append(a,
			"-hwaccel", "vaapi",
			"-hwaccel_device", e.Device,
			"-hwaccel_output_format", "vaapi",
		)
	}

	a = append(a, "-i", src)

	// Interlaced sources are deinterlaced to one frame per field, keeping the
	// full 50 or 60 Hz motion the material was shot with. Taking one frame per
	// *pair* would be smaller, but halves the temporal resolution of video that
	// was never film to begin with.
	if vf := p.filters(e.Hardware()); vf != "" {
		a = append(a, "-vf", vf)
	}

	// Which streams reach the output. With nothing probed, everything the disc
	// carried goes across, minus data streams, which Matroska will not take.
	sel := Selection{TranscodeAudio: false}
	if len(p.Streams) > 0 {
		sel = Select(p.Streams, p.Language)
	}
	if len(sel.Maps) > 0 {
		for _, m := range sel.Maps {
			a = append(a, "-map", m)
		}
	} else {
		a = append(a, "-map", "0", "-map", "-0:d?")
	}
	a = append(a, "-map_chapters", "0")

	// Compact audio is copied: re-encoding something already lossy only loses
	// more for a few percent of the file. Lossless audio is re-encoded, because
	// a TrueHD track costs more than the entire video budget.
	if sel.TranscodeAudio && p.AudioKbps > 0 {
		a = append(a, "-c:a", "ac3", "-b:a", strconv.Itoa(p.AudioKbps)+"k")
	} else {
		a = append(a, "-c:a", "copy")
	}
	a = append(a, "-c:s", "copy")

	if e.Hardware() {
		a = append(a, "-c:v", "h264_vaapi")
		if p.MaxKbps > 0 {
			// QVBR is quality-driven with a ceiling, which is exactly the shape
			// wanted: it behaves like constant quality until the cap, and only
			// then starts trading. Plain VBR at the same bitrate measured
			// slightly worse. A target below the ceiling gives it room to vary.
			a = append(a,
				"-rc_mode", "QVBR",
				"-global_quality", strconv.Itoa(p.Quality),
				"-b:v", strconv.Itoa(p.MaxKbps*4/5)+"k",
				"-maxrate", strconv.Itoa(p.MaxKbps)+"k")
		} else {
			a = append(a, "-qp", strconv.Itoa(p.Quality))
		}
	} else {
		a = append(a, "-c:v", "libx264", "-crf", strconv.Itoa(p.Quality))
		if p.Preset != "" {
			a = append(a, "-preset", p.Preset)
		}
		if p.MaxKbps > 0 {
			// x264 honours a ceiling on top of crf, so quality still leads.
			a = append(a,
				"-maxrate", strconv.Itoa(p.MaxKbps)+"k",
				"-bufsize", strconv.Itoa(p.MaxKbps*2)+"k")
		}
	}

	// The muxer is named rather than inferred. ffmpeg picks it from the output
	// file's extension, and the output is written to a ".part" name so an
	// interrupted encode cannot be mistaken for a finished one — which hides
	// the extension and leaves ffmpeg with "Invalid argument" and no clue as to
	// why. Everything in the pipeline is Matroska: the rips are, and this reads
	// one and writes another.
	a = append(a, "-f", "matroska")

	return append(a, "-progress", "pipe:1", "-nostats", dst)
}

// filters builds the video filter chain: deinterlacing first, then any
// downscale, so the scaler works on whole frames rather than fields.
//
// Empty when neither applies, because an empty -vf still forces a decode and
// re-encode path ffmpeg would otherwise skip.
func (p Profile) filters(hardware bool) string {
	var f []string
	if p.Interlaced {
		if hardware {
			f = append(f, "deinterlace_vaapi=rate=field")
		} else {
			f = append(f, "yadif=mode=send_field")
		}
	}
	if p.MaxHeight > 0 && p.SourceHeight > p.MaxHeight {
		if hardware {
			// -1 keeps the aspect ratio; VAAPI rounds to even itself.
			f = append(f, fmt.Sprintf("scale_vaapi=w=-1:h=%d", p.MaxHeight))
		} else {
			// -2 keeps the aspect ratio and forces an even width, which h264
			// requires and -1 does not guarantee.
			f = append(f, fmt.Sprintf("scale=-2:%d", p.MaxHeight))
		}
	}
	return strings.Join(f, ",")
}

// updateProgress folds one ffmpeg progress line into prog, reporting whether
// anything worth showing changed.
func updateProgress(prog *Progress, key, value string, srcDuration time.Duration) bool {
	switch key {
	case "out_time_us", "out_time_ms":
		// Both are microseconds despite the name of the second: ffmpeg has
		// reported out_time_ms in microseconds for its entire existence, and
		// reading it as milliseconds puts progress a thousand times too low.
		us, err := strconv.ParseInt(value, 10, 64)
		if err != nil || srcDuration <= 0 {
			return false
		}
		f := float64(us*1000) / float64(srcDuration)
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		prog.Fraction = f
		return true

	case "speed":
		v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(value), "x"), 64)
		if err != nil {
			return false
		}
		prog.Speed = v
		return true
	}
	return false
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

// DefaultDevice is the render node an Intel GPU normally presents.
const DefaultDevice = "/dev/dri/renderD128"

// ResolveDevice turns the configured vaapi_device into a device path.
//
// "auto" uses the usual render node when it is present and falls back to
// software when it is not, so the same configuration works on a machine with a
// GPU and one without. Anything else is taken literally, including empty, which
// forces software — a machine that is meant to use its GPU should fail loudly
// rather than quietly encode five times slower.
func ResolveDevice(configured string) string {
	if configured != "auto" {
		return configured
	}
	if _, err := os.Stat(DefaultDevice); err != nil {
		return ""
	}
	return DefaultDevice
}

// CheckDevice reports whether ffmpeg can actually initialise the device.
//
// Its presence in /dev is not enough: the render node exists whenever the
// kernel driver is loaded, while encoding also needs a VA driver for the chip.
// Without one — which is how this machine started out — every encode fails at
// the first frame with "Failed to initialise VAAPI connection", long after the
// job has been accepted.
func (e *Encoder) CheckDevice(ctx context.Context) error {
	if !e.Hardware() {
		return nil
	}
	cmd := exec.CommandContext(ctx, e.Bin,
		"-hide_banner", "-loglevel", "error",
		"-init_hw_device", "vaapi=hw:"+e.Device,
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=1",
		"-frames:v", "1", "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s cannot be used for encoding: %s",
			e.Device, lastLines(strings.TrimSpace(string(out)), 2))
	}
	return nil
}
