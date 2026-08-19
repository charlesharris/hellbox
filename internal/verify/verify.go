// Package verify checks that a ripped title is what the disc said it would be.
//
// v1's verification was deliberately shallow because ffmpeg was not yet a
// dependency: exit code zero, the right number of files, each above a size
// floor, each beginning with Matroska's magic bytes. Every one of those passes
// on a file that is three-quarters missing.
//
// It happened. A decrypted copy came out malformed, MakeMKV reported one title
// where the drive had reported four, and three episodes were silently lost — a
// rip that verified clean and was not. The check that catches it is comparing
// the output's actual duration against the duration enumeration reported, and
// it is cheap now that ffprobe is in the pipeline from the first stage.
//
// The governing rule is the one v1 arrived at the hard way: check the artifact
// against a specification, never against the producing program's opinion of
// itself. dvdbackup exits successfully having written a copy it could not read.
package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ebmlMagic begins every Matroska file.
var ebmlMagic = []byte{0x1A, 0x45, 0xDF, 0xA3}

// DefaultTolerance is how far an output's duration may sit from the duration
// enumeration reported, as a fraction.
//
// A remux is not frame-exact against a disc's own arithmetic: cell boundaries,
// padding trimmed from the start, and rounding in the IFO all move the total by
// a little. 2% is loose enough to absorb that and tight enough that a missing
// episode cannot hide — a title short by one episode of four is out by 25%.
//
// This number is a guess checked against nothing yet. Log every near miss and
// tighten it once fifty discs have been through.
const DefaultTolerance = 0.02

// MinToleranceSecs stops the fractional tolerance collapsing on short titles.
// Two per cent of a 64-second extra is a second and a quarter, which is inside
// the noise of where a cell begins.
const MinToleranceSecs = 5

// Expectation is what enumeration said the title would be.
type Expectation struct {
	DurationSecs int

	// Video, Audio and Subtitles are stream counts. Zero means "do not check",
	// because a Blu-ray enumerated through bd_list_titles knows its counts while
	// a title ripped with a stream selection applied deliberately has fewer.
	Video     int
	Audio     int
	Subtitles int

	// MinBytes rejects a file too small to be anything. Output below this is a
	// failed rip rather than a short title.
	MinBytes int64
}

// Result is the verdict on one file.
type Result struct {
	OK       bool
	Problems []string

	// Measured is what ffprobe actually found, kept so a near miss can be
	// logged with both numbers rather than just a complaint.
	DurationSecs int
	SizeBytes    int64
}

// Error renders the problems as one message, or "" when there are none.
func (r Result) Error() string {
	if len(r.Problems) == 0 {
		return ""
	}
	return strings.Join(r.Problems, "; ")
}

// Verifier checks output files.
type Verifier struct {
	// FFprobeBin is the ffprobe executable. Empty finds it on PATH.
	FFprobeBin string

	// Tolerance overrides DefaultTolerance when non-zero.
	Tolerance float64
}

// New returns a Verifier with the usual defaults.
func New() *Verifier { return &Verifier{FFprobeBin: "ffprobe", Tolerance: DefaultTolerance} }

func (v *Verifier) bin() string {
	if v.FFprobeBin == "" {
		return "ffprobe"
	}
	return v.FFprobeBin
}

func (v *Verifier) tolerance() float64 {
	if v.Tolerance <= 0 {
		return DefaultTolerance
	}
	return v.Tolerance
}

// Title verifies one output file against what was expected of it.
//
// Every check that can be made is made, and all failures are collected rather
// than returning on the first. A file that is both short and missing its audio
// says more about what went wrong than a file that is merely "short".
func (v *Verifier) Title(ctx context.Context, path string, want Expectation) (Result, error) {
	var res Result

	info, err := os.Stat(path)
	if err != nil {
		res.Problems = append(res.Problems, fmt.Sprintf("no output file at %s", path))
		return res, nil
	}
	res.SizeBytes = info.Size()

	if want.MinBytes > 0 && info.Size() < want.MinBytes {
		res.Problems = append(res.Problems,
			fmt.Sprintf("output is %s, below the %s floor — a failed rip rather than a short title",
				humanBytes(info.Size()), humanBytes(want.MinBytes)))
		// No point probing a file this small; it will only fail confusingly.
		return res, nil
	}

	if err := checkMagic(path); err != nil {
		res.Problems = append(res.Problems, err.Error())
		return res, nil
	}

	probe, err := v.probe(ctx, path)
	if err != nil {
		res.Problems = append(res.Problems, fmt.Sprintf("could not probe the output: %v", err))
		return res, nil
	}
	res.DurationSecs = probe.durationSecs()

	// The check v1 lacked, and the one that catches a truncated rip.
	if want.DurationSecs > 0 {
		if d := durationProblem(res.DurationSecs, want.DurationSecs, v.tolerance()); d != "" {
			res.Problems = append(res.Problems, d)
		}
	}

	counts := probe.counts()
	for _, c := range []struct {
		kind string
		want int
		got  int
	}{
		{"video", want.Video, counts["video"]},
		{"audio", want.Audio, counts["audio"]},
		{"subtitle", want.Subtitles, counts["subtitle"]},
	} {
		if c.want > 0 && c.got < c.want {
			res.Problems = append(res.Problems,
				fmt.Sprintf("expected %d %s stream(s), found %d", c.want, c.kind, c.got))
		}
	}

	res.OK = len(res.Problems) == 0
	return res, nil
}

// durationProblem reports how the measured duration fails, or "".
func durationProblem(got, want int, tolerance float64) string {
	allowed := math.Max(float64(want)*tolerance, MinToleranceSecs)
	diff := math.Abs(float64(got - want))
	if diff <= allowed {
		return ""
	}
	pct := 100 * diff / float64(want)
	if got < want {
		return fmt.Sprintf("output runs %s but the disc said %s — short by %.1f%%, the rip is truncated",
			hms(got), hms(want), pct)
	}
	return fmt.Sprintf("output runs %s but the disc said %s — longer by %.1f%%",
		hms(got), hms(want), pct)
}

// Disc cross-checks a whole disc's worth of output.
//
// Judged on total running time rather than on file count, because a count hides
// the case the loss actually took: every expected file present, one of them
// short. A disc missing a quarter of its running time has lost an episode
// however many files are on disk.
func (v *Verifier) Disc(results []Result, wantTotalSecs int) []string {
	var problems []string

	var gotTotal int
	for _, r := range results {
		gotTotal += r.DurationSecs
	}
	if wantTotalSecs > 0 {
		if d := durationProblem(gotTotal, wantTotalSecs, v.tolerance()); d != "" {
			problems = append(problems, "across the whole disc, "+d)
		}
	}
	return problems
}

func checkMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	buf := make([]byte, len(ebmlMagic))
	if _, err := f.Read(buf); err != nil {
		return fmt.Errorf("%s is unreadable: %w", path, err)
	}
	for i, b := range ebmlMagic {
		if buf[i] != b {
			return fmt.Errorf("%s does not begin with Matroska's magic bytes", path)
		}
	}
	return nil
}

// ---------- ffprobe ----------

type probeOut struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

func (v *Verifier) probe(ctx context.Context, path string) (*probeOut, error) {
	cmd := exec.CommandContext(ctx, v.bin(),
		"-hide_banner", "-v", "quiet",
		"-show_format", "-show_streams", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	var p probeOut
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	return &p, nil
}

func (p *probeOut) durationSecs() int {
	f, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int(f + 0.5)
}

func (p *probeOut) counts() map[string]int {
	m := map[string]int{}
	for _, s := range p.Streams {
		m[s.CodecType]++
	}
	return m
}

func hms(secs int) string {
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
