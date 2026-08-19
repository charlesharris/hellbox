// Package decrypt copies a CSS-protected disc to disk, decrypting it on the
// way, so that it can be ripped by a drive that refuses to decrypt it itself.
//
// An RPC-2 drive with no region set will not perform CSS authentication, which
// leaves a disc that scans perfectly and cannot be read. libdvdcss does not
// depend on that authentication: when the drive refuses to hand over a title
// key it derives the key from the disc's own data instead. dvdbackup drives
// libdvdcss and is the only new runtime dependency this path adds.
//
// The copy is a means, not a product. It lives under work_dir and is removed
// once the rip is verified — the rips directory holds the rip, and re-reading
// the disc is what all of this exists to avoid.
package decrypt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// outputName is what the copy is called inside its own scratch directory.
//
// dvdbackup otherwise names the directory after the DVD's *title*, which is
// neither the volume label nor predictable — "Roman Holiday" for a disc
// labelled ROMAN_HOLIDAY. Naming it removes the guesswork.
const outputName = "disc"

// completeMarker is written only after dvdbackup has finished successfully.
//
// A copy is reused across rip attempts, so "the directory exists" cannot be the
// test for whether it is usable: a daemon killed mid-decrypt leaves a directory
// holding most of a disc, and reusing that would rip a truncated film and
// verify it as fine. The marker is written last, so its presence means the copy
// was finished.
const completeMarker = ".complete"

// Path is where a decrypted copy of the disc identified by key lives.
//
// The name is derived from the disc rather than made unique, which is what lets
// a failed rip be retried without decrypting the disc a second time — half an
// hour that would otherwise be spent again on every attempt.
func Path(destRoot, key string) string {
	return filepath.Join(destRoot, "decrypt-"+key)
}

// Existing returns a finished copy of the disc identified by key, if one is on
// disk from an earlier attempt.
func Existing(destRoot, key string) (*Result, bool) {
	root := Path(destRoot, key)
	if _, err := os.Stat(filepath.Join(root, completeMarker)); err != nil {
		return nil, false
	}
	folder := filepath.Join(root, outputName)
	if info, err := os.Stat(filepath.Join(folder, "VIDEO_TS")); err != nil || !info.IsDir() {
		return nil, false
	}
	// Verified on reuse, not only when written. A copy that was corrupt when
	// made is still corrupt when found, and reusing it silently defeats the
	// point of putting the disc back in: a cleaned disc would never be read
	// again, because the broken copy of it always wins.
	//
	// Removed rather than merely rejected, so the next attempt decrypts afresh
	// instead of finding it again.
	if err := verifyCopy(filepath.Join(folder, "VIDEO_TS"), 0); err != nil {
		os.RemoveAll(root)
		return nil, false
	}

	size, err := dirSize(folder)
	if err != nil {
		return nil, false
	}
	return &Result{Folder: folder, Root: root, Bytes: size}, true
}

// maxVOBBytes is the largest a DVD VOB can be: 1 GB, less one 64 KB block.
//
// The limit is in the DVD-Video specification, so a larger file is not a big
// VOB — it is a broken one, and nothing downstream will read the disc
// correctly. A Star Trek disc produced a VTS_02_5.VOB of five gigabytes: one
// gigabyte of real data, a four gigabyte hole, then stray writes at an offset
// that looks like an absolute disc sector used as a file position. MakeMKV
// then reported one title where the drive had reported four, and three
// episodes were silently lost.
const maxVOBBytes = 1073709056

// readErrorPattern matches the complaints dvdbackup and libdvdread make about
// sectors they could not read.
var readErrorPattern = regexp.MustCompile(`(?i)error reading|read error|cannot read block|input/output error`)

// verifyCopy checks that a decrypted copy is structurally a DVD.
//
// dvdbackup exits successfully having written a copy it could not read
// properly, so its exit status says nothing useful. The file sizes do: they are
// specified, and a VOB above the limit means the copy is corrupt however
// cleanly the program finished.
func verifyCopy(videoTS string, readErrors int) error {
	entries, err := os.ReadDir(videoTS)
	if err != nil {
		return fmt.Errorf("read %s: %w", videoTS, err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(strings.ToUpper(e.Name()), ".VOB") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > maxVOBBytes {
			msg := fmt.Sprintf("the decrypted copy is malformed: %s is %.1f GB, "+
				"and a DVD VOB cannot exceed 1 GB", e.Name(), float64(info.Size())/(1<<30))
			if readErrors > 0 {
				msg += fmt.Sprintf(" (dvdbackup reported %d read error%s; the disc is probably damaged)",
					readErrors, plural(readErrors))
			}
			return errors.New(msg)
		}
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Discard removes a decrypted copy.
func Discard(res *Result) error {
	if res == nil || res.Root == "" {
		return nil
	}
	return os.RemoveAll(res.Root)
}

// Decrypter runs dvdbackup.
type Decrypter struct {
	// Bin is the dvdbackup executable, looked up on PATH when not absolute.
	Bin string
}

// New returns a Decrypter. An empty bin selects "dvdbackup".
func New(bin string) *Decrypter {
	if bin == "" {
		bin = "dvdbackup"
	}
	return &Decrypter{Bin: bin}
}

// Available reports whether dvdbackup can be found.
func (d *Decrypter) Available() error {
	if filepath.IsAbs(d.Bin) {
		if _, err := os.Stat(d.Bin); err != nil {
			return fmt.Errorf("dvdbackup not found at %s: %w", d.Bin, err)
		}
		return nil
	}
	if _, err := exec.LookPath(d.Bin); err != nil {
		return fmt.Errorf("dvdbackup not on PATH: %w", err)
	}
	return nil
}

// Result is a finished decrypt.
type Result struct {
	// Folder contains VIDEO_TS and is what makemkvcon is pointed at.
	Folder string

	// Root is the scratch directory holding Folder. Removing it removes the
	// whole copy.
	Root string

	// Bytes is the size of the copy.
	Bytes int64

	// Raw is dvdbackup's complete output, kept for the rip log.
	Raw string

	// ReadErrors is how many sectors dvdbackup complained about. A copy can be
	// structurally valid and still have been read off a failing disc, and the
	// count is the only warning of that there is.
	ReadErrors int
}

// Progress is how far a decrypt has got.
type Progress struct {
	// Fraction spans the title set being copied, not the whole disc.
	//
	// dvdbackup reports a percentage within a part and the part's position
	// within its title set, and says nothing about how many title sets a disc
	// has. A fraction that restarts at each one is honest; a fabricated
	// disc-wide figure would not be. Operation carries the context that makes
	// the restart legible.
	Fraction float64

	// Operation is dvdbackup's own description, e.g. "Copying Title, part 2/6".
	Operation string
}

// progressLine matches dvdbackup's progress output, which reports a percentage
// within a part and the part's position in the whole title.
var progressLine = regexp.MustCompile(`^(.*?part (\d+)/(\d+)): (\d+)% done`)

// Mirror decrypts the disc in devicePath into a new directory under destRoot.
//
// The copy goes into a scratch directory of its own so that a decrypt which
// fails part way leaves nothing that could be mistaken for a complete one, and
// so that removing it afterwards is a single call.
func (d *Decrypter) Mirror(ctx context.Context, devicePath, destRoot, key string, onProgress func(Progress)) (*Result, error) {
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", destRoot, err)
	}

	// Anything already at this path is either an incomplete copy or one a
	// caller chose not to reuse. Either way dvdbackup will not write into a
	// directory that exists, so it goes first.
	root := Path(destRoot, key)
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("clear %s: %w", root, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create decrypt directory: %w", err)
	}

	args := []string{"-i", devicePath, "-M", "-o", root, "-n", outputName, "-p"}
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }

	// libdvdcss caches title keys in $HOME/.dvdcss by default, which the
	// daemon's home directory is not writable for: the unit grants write access
	// to ~/.MakeMKV and nothing else. Pointing the cache at work_dir keeps it
	// writable under that confinement, and keeps everything this path creates
	// in one place. It sits beside the scratch directories rather than inside
	// one, so it survives the copy being removed and can serve the next disc.
	cache := filepath.Join(destRoot, ".dvdcss-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("create libdvdcss cache directory: %w", err)
	}
	cmd.Env = append(os.Environ(), "DVDCSS_CACHE="+cache)

	// dvdbackup writes progress to stderr and everything else to stdout; both
	// matter, and interleaving them keeps the log in the order it happened.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("start %s: %w", d.Bin, err)
	}

	var raw strings.Builder
	var readErrors int
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sc.Split(scanLinesOrReturns)
	for sc.Scan() {
		line := sc.Text()
		raw.WriteString(line)
		raw.WriteByte('\n')
		if readErrorPattern.MatchString(line) {
			readErrors++
		}
		if onProgress != nil {
			if p, ok := parseProgress(line); ok {
				onProgress(p)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		os.RemoveAll(root)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("dvdbackup: %w: %s", err, lastLines(raw.String(), 3))
	}

	folder := filepath.Join(root, outputName)
	info, err := os.Stat(filepath.Join(folder, "VIDEO_TS"))
	if err != nil || !info.IsDir() {
		os.RemoveAll(root)
		return nil, fmt.Errorf("dvdbackup reported success but produced no VIDEO_TS in %s", folder)
	}

	size, err := dirSize(folder)
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}

	// Checked before the copy is marked complete, so a corrupt one is never
	// reusable and never mistaken for a finished decrypt. dvdbackup exits
	// successfully having written a copy it could not read properly, so its
	// exit status cannot be the test.
	if err := verifyCopy(filepath.Join(folder, "VIDEO_TS"), readErrors); err != nil {
		os.RemoveAll(root)
		return nil, err
	}

	// Written last: until this exists the copy must not be reused, however
	// complete it looks.
	if err := os.WriteFile(filepath.Join(root, completeMarker), nil, 0o644); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("mark the copy complete: %w", err)
	}

	return &Result{Folder: folder, Root: root, Bytes: size, Raw: raw.String(), ReadErrors: readErrors}, nil
}

// parseProgress reads one of dvdbackup's progress lines.
func parseProgress(line string) (Progress, bool) {
	m := progressLine.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Progress{}, false
	}
	part, _ := strconv.Atoi(m[2])
	parts, _ := strconv.Atoi(m[3])
	pct, _ := strconv.Atoi(m[4])
	if parts <= 0 || part <= 0 {
		return Progress{}, false
	}

	// The percentage is within the current part, so it has to be folded into
	// the parts already done or the bar restarts at every one.
	f := (float64(part-1) + float64(pct)/100) / float64(parts)
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return Progress{Fraction: f, Operation: strings.TrimSpace(m[1])}, true
}

// scanLinesOrReturns splits on carriage returns as well as newlines.
//
// dvdbackup redraws its progress with a bare carriage return and emits no
// newline until the copy ends, so a scanner splitting on newlines alone reads
// the entire decrypt as one line and reports nothing until it is over.
func scanLinesOrReturns(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure %s: %w", path, err)
	}
	return total, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
