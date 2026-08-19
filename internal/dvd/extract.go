package dvd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hellbox/internal/disc"
)

// Extractor copies a title off a DVD.
//
// A rip is a remux and never a re-encode: the raw tree is the system of record
// and everything downstream reads from it. -c copy is not an optimisation here,
// it is the invariant.
type Extractor struct {
	// FFmpegBin is the ffmpeg executable. Empty finds it on PATH.
	FFmpegBin string

	// Preindex asks the demuxer for exact chapter marks at the cost of reading
	// the title twice.
	//
	// Off by default, which was measured rather than assumed: chapters arrive
	// without it, and on a real disc it moved a single mark by 124ms. At the
	// read speeds a DVD gives, doubling the read of a two-hour feature to gain
	// an eighth of a second is a bad trade.
	Preindex bool

	// StallTimeout fails a title that stops making progress. Zero waits
	// indefinitely.
	//
	// Progress means the output timestamp advancing, not output arriving. A
	// stalled read keeps talking — v1's rip that hung for seventeen hours was
	// emitting messages the whole time while reading nothing — so a timer reset
	// by any line would never have fired.
	StallTimeout time.Duration
}

// NewExtractor returns an Extractor with the defaults the evidence supports.
func NewExtractor() *Extractor {
	return &Extractor{FFmpegBin: "ffmpeg", Preindex: false, StallTimeout: 10 * time.Minute}
}

func (e *Extractor) bin() string {
	if e.FFmpegBin == "" {
		return "ffmpeg"
	}
	return e.FFmpegBin
}

// Progress is how far a title has got.
type Progress struct {
	// Fraction is 0 to 1 against the duration enumeration reported. It is
	// honest about being an estimate: the demuxer's own total can differ
	// slightly from the IFO's arithmetic.
	Fraction float64

	// OutTime is how much of the title has been written.
	OutTime time.Duration

	// Speed is the multiple of realtime, as ffmpeg reports it. A DVD read runs
	// at a few times realtime at best, and watching this fall is the earliest
	// sign of a disc going bad.
	Speed float64
}

// Result is a finished extraction.
type Result struct {
	Path      string
	SizeBytes int64
	Elapsed   time.Duration
}

// ErrStalled reports a title that stopped making progress.
var ErrStalled = errors.New("no progress")

// Extract writes one title to dest.
//
// dest is written directly; staging it somewhere hidden and moving it into
// place once it verifies is the caller's job, because only the caller knows
// whether it verified. That ordering is what stops a crash leaving something
// that looks finished.
func (e *Extractor) Extract(ctx context.Context, sourcePath string, title disc.Title, dest string, onProgress func(Progress)) (*Result, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}

	args := e.args(sourcePath, title, dest)
	cmd := exec.CommandContext(ctx, e.bin(), args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// ffmpeg's own diagnostics go to stderr and are kept for the rip log; the
	// machine-readable progress stream is on stdout. They are deliberately not
	// merged, because parsing progress out of prose is what -progress exists to
	// avoid.
	var errBuf strings.Builder
	cmd.Stderr = &errBuf

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	stallErr := e.watch(ctx, stdout, title.DurationSecs, onProgress, cmd)

	waitErr := cmd.Wait()
	switch {
	case stallErr != nil:
		os.Remove(dest)
		return nil, stallErr
	case ctx.Err() != nil:
		os.Remove(dest)
		return nil, ctx.Err()
	case waitErr != nil:
		return nil, fmt.Errorf("ffmpeg: %w: %s", waitErr, lastLines(errBuf.String(), 3))
	}

	info, err := os.Stat(dest)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg reported success but wrote nothing to %s", dest)
	}
	return &Result{Path: dest, SizeBytes: info.Size(), Elapsed: time.Since(started)}, nil
}

// args builds the extraction command.
func (e *Extractor) args(sourcePath string, title disc.Title, dest string) []string {
	args := []string{"-hide_banner", "-nostdin", "-y"}

	preindex := "0"
	if e.Preindex {
		preindex = "1"
	}
	args = append(args,
		"-f", "dvdvideo",
		"-preindex", preindex,
		// Region-free. Once libdvdcss is deriving keys the drive's region is
		// irrelevant, and asking for 0 keeps it that way.
		"-region", "0",
		"-title", strconv.Itoa(title.Index+1), // hellbox counts from 0, the demuxer from 1
		"-i", sourcePath,
		"-map", "0",
		"-c", "copy",
		// A title cut at a cell boundary can start with negative timestamps,
		// which some players read as a seek.
		"-avoid_negative_ts", "make_zero",
		"-progress", "pipe:1", "-nostats",
		dest,
	)
	return args
}

// watch reads the progress stream and enforces the stall timeout.
func (e *Extractor) watch(ctx context.Context, r io.Reader, totalSecs int, onProgress func(Progress), cmd *exec.Cmd) error {
	type sample struct {
		at      time.Time
		outTime time.Duration
	}
	last := sample{at: time.Now()}

	done := make(chan struct{})
	stalled := make(chan struct{})

	// The watchdog is separate from the reader so that a process which stops
	// emitting entirely is still caught. A timeout implemented inside the read
	// loop only fires when a line arrives, which is exactly when it is not
	// needed.
	if e.StallTimeout > 0 {
		go func() {
			t := time.NewTicker(15 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case now := <-t.C:
					if now.Sub(last.at) > e.StallTimeout {
						close(stalled)
						_ = cmd.Process.Signal(os.Interrupt)
						return
					}
				}
			}
		}()
	}

	var cur Progress
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch key {
		case "out_time_us", "out_time_ms":
			// ffmpeg's out_time_ms is microseconds despite the name, and has
			// been for years. Both keys are read the same way deliberately.
			us, err := strconv.ParseInt(value, 10, 64)
			if err != nil || us < 0 {
				continue
			}
			cur.OutTime = time.Duration(us) * time.Microsecond
			if cur.OutTime > last.outTime {
				last = sample{at: time.Now(), outTime: cur.OutTime}
			}
			if totalSecs > 0 {
				cur.Fraction = cur.OutTime.Seconds() / float64(totalSecs)
				if cur.Fraction > 1 {
					cur.Fraction = 1
				}
			}
		case "speed":
			cur.Speed = parseSpeed(value)
		case "progress":
			if onProgress != nil {
				onProgress(cur)
			}
		}
	}
	close(done)

	select {
	case <-stalled:
		return fmt.Errorf("%w for %s", ErrStalled, e.StallTimeout)
	default:
		return nil
	}
}

// parseSpeed reads ffmpeg's "1.23x".
func parseSpeed(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "x"))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
