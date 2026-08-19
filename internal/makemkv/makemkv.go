package makemkv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"hellbox/internal/disc"
)

// Runner executes makemkvcon.
type Runner struct {
	// Bin is the makemkvcon executable, looked up on PATH when not absolute.
	Bin string

	// SettingsPath is the settings.conf holding the registration key.
	//
	// makemkvcon locates it as $HOME/.MakeMKV/settings.conf, so the runner
	// hands it a HOME derived from this path. Without that, configuring a
	// settings path would change only where hellbox *writes* the key, while
	// makemkvcon went on reading a different file — a refresh that appeared to
	// succeed and changed nothing.
	SettingsPath string

	// StallTimeout stops a title rip that has made no progress for this long.
	// Zero waits indefinitely, which is what the daemon did before a rip was
	// observed to hang for seventeen hours on a disc it could not decrypt.
	StallTimeout time.Duration
}

// Source is what makemkvcon is pointed at. It exists because a disc cannot
// always be read from the drive it is in: a CSS-protected disc in a drive with
// no region set has to be decrypted to disk first and then read from there.
//
// Titles are numbered identically either way — verified by scanning one disc
// both ways and comparing every index, duration, size and output name — so a
// scan of the drive and a rip from the decrypted copy can be mixed safely.
type Source string

// DeviceSource reads the disc in a drive.
func DeviceSource(devicePath string) Source { return Source("dev:" + devicePath) }

// FolderSource reads a decrypted VIDEO_TS tree on disk. The folder is the one
// *containing* VIDEO_TS, not VIDEO_TS itself.
//
// A folder source carries no volume label: makemkvcon reports CINFO 2, 30 and
// 32 for a drive and none of them for a folder. Anything identifying the disc —
// hellbox's fingerprint and its rip directory name both derive from that label
// — must therefore come from a scan of the drive, never of the copy.
func FolderSource(folder string) Source { return Source("file:" + folder) }

// IsFolder reports whether this source is a decrypted copy rather than a drive.
func (s Source) IsFolder() bool { return strings.HasPrefix(string(s), "file:") }

// DevicePath returns the device a drive source names, and "" for a folder.
func (s Source) DevicePath() string {
	rest, ok := strings.CutPrefix(string(s), "dev:")
	if !ok {
		return ""
	}
	return rest
}

// New returns a Runner. An empty bin selects "makemkvcon", and an empty
// settings path selects the running user's own.
func New(bin, settingsPath string) *Runner {
	if bin == "" {
		bin = "makemkvcon"
	}
	if settingsPath == "" {
		settingsPath = DefaultSettingsPath()
	}
	return &Runner{Bin: bin, SettingsPath: settingsPath}
}

// homeDir is the HOME makemkvcon is given, derived by stripping
// ".MakeMKV/settings.conf" from the settings path.
func (r *Runner) homeDir() string {
	if r.SettingsPath == "" {
		return ""
	}
	return filepath.Dir(filepath.Dir(r.SettingsPath))
}

// Available reports whether makemkvcon can be found.
func (r *Runner) Available() error {
	if filepath.IsAbs(r.Bin) {
		if _, err := os.Stat(r.Bin); err != nil {
			return fmt.Errorf("makemkvcon not found at %s: %w", r.Bin, err)
		}
		return nil
	}
	if _, err := exec.LookPath(r.Bin); err != nil {
		return fmt.Errorf("makemkvcon not on PATH: %w", err)
	}
	return nil
}

// ScanResult is the outcome of reading a disc's structure.
type ScanResult struct {
	Disc disc.Disc

	// Raw is the complete verbatim output of the scan. It is written next to
	// the rip so that a parsing mistake can be corrected without the disc.
	Raw string

	// Messages are the MSG lines, kept for diagnostics.
	Messages []Message
}

// Scan reads a disc's title structure without reading its content.
//
// This is fast — typically under thirty seconds for a DVD — which is what makes
// recognising an already-ripped disc cheap enough to do on every insertion.
//
// minLengthSecs must match the value later passed to RipTitle. MakeMKV numbers
// titles within its *filtered* list, not within everything on the disc, so
// scanning with one threshold and ripping with another silently rips the wrong
// titles.
func (r *Runner) Scan(ctx context.Context, src Source, minLengthSecs int) (*ScanResult, error) {
	args := []string{
		"-r",
		"--cache=1",
		fmt.Sprintf("--minlength=%d", minLengthSecs),
		"info",
		string(src),
	}

	var raw strings.Builder
	res := &ScanResult{}

	titleAttrs := map[int]map[int]string{}
	streamAttrs := map[int]map[int]string{}
	discAttrs := map[int]string{}
	var titleCount int
	var driveModel string

	// A folder source names no device, so the DRV line that carries the drive
	// model simply never matches. That is correct: a decrypted copy was not
	// read from a drive.
	devicePath := src.DevicePath()

	err := r.run(ctx, args, func(line string) {
		raw.WriteString(line)
		raw.WriteByte('\n')

		l, ok := ParseLine(line)
		if !ok {
			return
		}
		switch l.Kind {
		case "MSG":
			res.Messages = append(res.Messages, parseMessage(l))
		case "DRV":
			d := parseDriveLine(l)
			if d.DevicePath == devicePath && d.DriveName != "" {
				driveModel = d.DriveName
			}
		case "TCOUNT":
			titleCount = l.intField(0)
		case "CINFO":
			discAttrs[l.intField(0)] = l.field(2)
		case "TINFO":
			t := l.intField(0)
			if titleAttrs[t] == nil {
				titleAttrs[t] = map[int]string{}
			}
			titleAttrs[t][l.intField(1)] = l.field(3)
		case "SINFO":
			t, s := l.intField(0), l.intField(1)
			key := t<<16 | s
			if streamAttrs[key] == nil {
				streamAttrs[key] = map[int]string{}
			}
			streamAttrs[key][l.intField(2)] = l.field(4)
		}
	})
	if err != nil {
		return nil, err
	}

	res.Raw = raw.String()

	d := disc.Disc{
		VolumeLabel:    strings.TrimSpace(attrString(discAttrs, attrVolumeName)),
		ScannedAt:      time.Now(),
		DriveModel:     driveModel,
		MakeMKVVersion: versionFromMessages(res.Messages),
		Type:           disc.TypeUnknown,
	}

	indices := make([]int, 0, len(titleAttrs))
	for idx := range titleAttrs {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	for _, idx := range indices {
		attrs := titleAttrs[idx]
		t := disc.Title{
			Index:                idx,
			DurationSecs:         ParseDuration(attrString(attrs, attrDuration)),
			Chapters:             attrInt(attrs, attrChapterCount),
			SizeBytes:            parseBytes(attrString(attrs, attrDiskSizeBytes)),
			SourceFile:           attrString(attrs, attrSourceFileName),
			SegmentsCount:        attrInt(attrs, attrSegmentsCount),
			SegmentsMap:          attrString(attrs, attrSegmentsMap),
			Name:                 attrString(attrs, attrName),
			MakeMKVSuggestedName: attrString(attrs, attrOutputFileName),
			OutputFile:           disc.TitleFileName(idx),
			Attrs:                attrs,
		}

		for key, sAttrs := range streamAttrs {
			if key>>16 != idx {
				continue
			}
			t.Streams = append(t.Streams, disc.Stream{
				Index:       key & 0xffff,
				Kind:        normaliseStreamKind(attrString(sAttrs, attrType)),
				Codec:       firstNonEmpty(attrString(sAttrs, attrCodecShort), attrString(sAttrs, attrCodecID)),
				Language:    attrString(sAttrs, attrLangCode),
				LangName:    attrString(sAttrs, attrLangName),
				Channels:    attrInt(sAttrs, attrAudioChannels),
				SampleRate:  attrString(sAttrs, attrAudioSampleRate),
				Resolution:  attrString(sAttrs, attrVideoSize),
				AspectRatio: attrString(sAttrs, attrVideoAspectRatio),
				FrameRate:   attrString(sAttrs, attrVideoFrameRate),
				Bitrate:     attrString(sAttrs, attrBitrate),
				Flags:       firstNonEmpty(attrString(sAttrs, attrStreamFlags), attrString(sAttrs, attrMkvFlags)),
				Name:        attrString(sAttrs, attrName),
				Attrs:       sAttrs,
			})
		}
		sort.Slice(t.Streams, func(i, j int) bool { return t.Streams[i].Index < t.Streams[j].Index })

		d.Titles = append(d.Titles, t)
		d.TotalBytes += t.SizeBytes
	}

	// MakeMKV announces every title it analyses and then offers only some of
	// them. A disc it cannot decrypt can be analysed in full and offered almost
	// nothing — one here announced a 2:52:52 title and offered a 3:10 trailer —
	// and nothing in the structured output says so. Surfacing it is the
	// difference between a rip that is visibly incomplete and one that looks
	// finished.
	if announced := announcedTitles(res.Messages); announced > len(d.Titles) {
		res.Messages = append(res.Messages, Message{
			Text: fmt.Sprintf("hellbox: MakeMKV analysed %d titles but offered %d; "+
				"the drive may be unable to read all of this disc", announced, len(d.Titles)),
		})
	}

	if titleCount > 0 && titleCount != len(d.Titles) {
		// Not fatal — MakeMKV occasionally reports TCOUNT before filtering —
		// but worth surfacing, since it means the parse and the tool disagree.
		res.Messages = append(res.Messages, Message{
			Text: fmt.Sprintf("hellbox: TCOUNT reported %d titles but %d were parsed", titleCount, len(d.Titles)),
		})
	}

	d.Type = inferType(d)
	d.Fingerprint = disc.ComputeFingerprint(d.VolumeLabel, d.Titles)

	if len(d.Titles) == 0 {
		return res, fmt.Errorf("no titles found on %s: %s", src, summarise(res.Messages))
	}

	res.Disc = d
	return res, nil
}

// announcedTitles counts the titles MakeMKV said it added, which is not always
// the number it then offers.
func announcedTitles(msgs []Message) int {
	var n int
	for _, m := range msgs {
		// "Title #3 was added (2 cell(s), 0:03:10)"
		if m.Code == msgTitleAdded {
			n++
		}
	}
	return n
}

// RipProgress reports how far a title rip has got.
type RipProgress struct {
	TitleIndex int
	Operation  string
	Fraction   float64
}

// RipResult describes one completed title rip.
type RipResult struct {
	TitleIndex int
	Path       string
	SizeBytes  int64
	Raw        string
	Messages   []Message
	Elapsed    time.Duration
}

// RipTitle rips a single title to destDir as title_NN.mkv.
//
// Titles are ripped one at a time rather than with MakeMKV's "all" selector.
// That costs a few seconds of disc spin-up per title, and buys three things
// that matter more: a failure on the fifth title does not discard the four
// already written, progress is attributable to a specific title, and hellbox
// controls the output filename instead of inheriting MakeMKV's content-guessing
// naming scheme.
//
// minLengthSecs must be the value the disc was scanned with, so that
// titleIndex refers to the same title the scan described.
func (r *Runner) RipTitle(ctx context.Context, src Source, titleIndex int, destDir string, minLengthSecs int, onProgress func(RipProgress)) (*RipResult, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", destDir, err)
	}

	// Rip into a scratch directory so the file MakeMKV produces can be
	// identified unambiguously regardless of what it decides to call it.
	staging, err := os.MkdirTemp(destDir, ".rip-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	args := []string{
		"-r",
		"--progress=-same",
		"--cache=1024",
		fmt.Sprintf("--minlength=%d", minLengthSecs),
		"mkv",
		string(src),
		fmt.Sprint(titleIndex),
		staging,
	}

	started := time.Now()
	res := &RipResult{TitleIndex: titleIndex}

	var raw strings.Builder
	var prog Progress

	// A stalled rip is stopped rather than waited on. The context handed to run
	// is derived so the watchdog can end the command without disturbing the
	// caller's own cancellation, which has to stay distinguishable from this.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	stall := newStallTimer(r.StallTimeout, started)
	if iv := stall.checkInterval(); iv > 0 {
		watchdogDone := make(chan struct{})
		defer close(watchdogDone)
		go func() {
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-watchdogDone:
					return
				case <-runCtx.Done():
					return
				case now := <-t.C:
					if stall.expired(now) {
						cancelRun()
						return
					}
				}
			}
		}()
	}

	err = r.run(runCtx, args, func(line string) {
		raw.WriteString(line)
		raw.WriteByte('\n')

		l, ok := ParseLine(line)
		if !ok {
			return
		}
		switch l.Kind {
		case "MSG":
			res.Messages = append(res.Messages, parseMessage(l))
		case "PRGC":
			prog.Current = l.field(2)
		case "PRGT":
			prog.Total = l.field(2)
		case "PRGV":
			prog.Value, prog.TotalVal, prog.Max = l.intField(0), l.intField(1), l.intField(2)
			prog.HasValues = true
			stall.observe(prog.Value, prog.TotalVal, prog.Max, time.Now())
			if onProgress != nil {
				op := prog.Current
				if op == "" {
					op = prog.Total
				}
				onProgress(RipProgress{TitleIndex: titleIndex, Operation: op, Fraction: prog.Fraction()})
			}
		}
	})

	res.Raw = raw.String()
	res.Elapsed = time.Since(started)

	// Checked before err, because a stall ends the command by cancelling it and
	// would otherwise be reported as the cancellation it was implemented with.
	if stall.stalled() {
		return res, fmt.Errorf("rip title %d: no progress for %s, so it was stopped; "+
			"a drive with no region set stalls like this on a region-coded disc: %s",
			titleIndex, r.StallTimeout, summarise(res.Messages))
	}
	if err != nil {
		return res, fmt.Errorf("rip title %d: %w: %s", titleIndex, err, summarise(res.Messages))
	}

	produced, err := singleMKV(staging)
	if err != nil {
		return res, fmt.Errorf("rip title %d: %w: %s", titleIndex, err, summarise(res.Messages))
	}

	final := filepath.Join(destDir, disc.TitleFileName(titleIndex))
	if err := os.Rename(produced, final); err != nil {
		return res, fmt.Errorf("move title %d into place: %w", titleIndex, err)
	}

	info, err := os.Stat(final)
	if err != nil {
		return res, fmt.Errorf("stat %s: %w", final, err)
	}

	res.Path = final
	res.SizeBytes = info.Size()
	return res, nil
}

// singleMKV returns the one .mkv in dir, erroring if there is not exactly one.
func singleMKV(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read staging dir: %w", err)
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".mkv") {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", errors.New("makemkvcon produced no output file")
	default:
		return "", fmt.Errorf("makemkvcon produced %d output files, expected 1", len(found))
	}
}

// Version returns the makemkvcon version string.
func (r *Runner) Version(ctx context.Context) (string, error) {
	var msgs []Message
	err := r.run(ctx, []string{"-r", "info", "disc:9999"}, func(line string) {
		if l, ok := ParseLine(line); ok && l.Kind == "MSG" {
			msgs = append(msgs, parseMessage(l))
		}
	})
	// A deliberately invalid disc index always fails; the version banner is
	// printed before it does, so the error is expected and ignored.
	v := versionFromMessages(msgs)
	if v == "" {
		if err != nil {
			return "", fmt.Errorf("could not determine makemkvcon version: %w", err)
		}
		return "", errors.New("could not determine makemkvcon version")
	}
	return v, nil
}

// KeyStatus describes the state of the MakeMKV registration key.
type KeyStatus struct {
	Present bool
	Expired bool
	Detail  string

	// VersionTooOld reports that MakeMKV itself is the problem, not the key.
	//
	// The two look alike — both come back as code 5021 and both stop every rip
	// — but the remedies have nothing in common. An expired key is replaced by
	// fetching the published one, which the daemon does by itself. A version
	// too old to accept any key needs MakeMKV updating, which it cannot do, and
	// no number of key refreshes will ever help. Telling the operator the key
	// expired sends them to fix the one thing that is not wrong.
	VersionTooOld bool
}

// MSG codes MakeMKV uses for registration problems. Codes are matched before
// prose because they are stable: the wording of 5020 in 1.18.4 is "The stored
// activation key is invalid. I guess someone tampered with settings...", which
// no reasonable substring list would have anticipated.
const (
	// msgTitleAdded is emitted for every title MakeMKV analyses, whether or not
	// it goes on to offer it.
	msgTitleAdded = 3028

	msgKeyInvalid  = 5020 // stored activation key is invalid
	msgKeyRequired = 5021 // version too old, or a registration key is needed
)

var (
	versionRE = regexp.MustCompile(`MakeMKV\s+v([0-9][0-9.]*)`)

	// MakeMKV says this when the build is too old to accept a beta key at all,
	// whatever key is installed.
	versionTooOldRE = regexp.MustCompile(`(?i)(version is too old|download the latest version)`)

	// Prose matching is kept as a backstop for codes not listed above, since
	// the wording differs between releases.
	keyExpiredRE = regexp.MustCompile(`(?i)(expired|evaluation period|out of date|too old|no longer valid)`)
	keyMissingRE = regexp.MustCompile(`(?i)(registration key|activation key|enter.*key|not registered|invalid key|shareware)`)
)

// CheckKey reports whether MakeMKV holds a usable registration key.
//
// The beta key MakeMKV publishes expires roughly monthly. When it lapses, rips
// fail for a reason that has nothing to do with the disc or the drive, and the
// resulting error is easy to misread. Surfacing it directly is one of the
// clearest wins available over the tool this replaces.
func (r *Runner) CheckKey(ctx context.Context) (KeyStatus, error) {
	// A settings file with no key produces no complaint from makemkvcon at all
	// — it starts and says nothing — so the file is inspected directly. Relying
	// on the messages alone reported "accepted" for a machine that had never
	// been registered, which is precisely the state of a fresh install.
	if path := r.SettingsPath; path != "" {
		switch key, err := ReadSettingsKey(path); {
		case err != nil:
			return KeyStatus{Present: false, Detail: err.Error()}, nil
		case key == "":
			return KeyStatus{Present: false, Detail: "no registration key in " + path}, nil
		}
	}

	var msgs []Message
	err := r.run(ctx, []string{"-r", "info", "disc:9999"}, func(line string) {
		if l, ok := ParseLine(line); ok && l.Kind == "MSG" {
			msgs = append(msgs, parseMessage(l))
		}
	})
	if len(msgs) == 0 && err != nil {
		return KeyStatus{}, fmt.Errorf("could not query makemkvcon: %w", err)
	}

	return keyStatusFromMessages(msgs), nil
}

// keyStatusFromMessages reads a key verdict out of makemkvcon's output.
//
// Separated from the command so the judgement — which messages mean the key
// needs replacing — is testable without MakeMKV installed.
func keyStatusFromMessages(msgs []Message) KeyStatus {
	// Codes first: they are stable where the prose is not. MakeMKV 1.18.4
	// phrases 5020 as "The stored activation key is invalid. I guess someone
	// tampered with settings...", which no plausible substring list would have
	// anticipated.
	for _, m := range msgs {
		if m.Code == msgKeyInvalid || m.Code == msgKeyRequired {
			// 5021 covers two different problems and says which only in its
			// prose, so here — and only here — the wording has to be read. A
			// build too old to accept any key is not something a new key fixes,
			// and treating it as an expired key means refreshing forever while
			// reporting the wrong cause.
			if versionTooOldRE.MatchString(m.Text) {
				return KeyStatus{Present: true, Expired: true, VersionTooOld: true, Detail: m.Text}
			}
			return KeyStatus{Present: true, Expired: true, Detail: m.Text}
		}
	}
	for _, m := range msgs {
		if keyExpiredRE.MatchString(m.Text) {
			return KeyStatus{Present: true, Expired: true, Detail: m.Text}
		}
		if keyMissingRE.MatchString(m.Text) {
			return KeyStatus{Present: false, Detail: m.Text}
		}
	}
	return KeyStatus{Present: true}
}

func versionFromMessages(msgs []Message) string {
	for _, m := range msgs {
		if match := versionRE.FindStringSubmatch(m.Text); match != nil {
			return match[1]
		}
	}
	return ""
}

// run executes makemkvcon, delivering each stdout line to onLine. Lines are
// delivered synchronously from a single goroutine, so callers need no locking.
func (r *Runner) run(ctx context.Context, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second

	// makemkvcon reads its key from $HOME/.MakeMKV/settings.conf, so it is
	// pointed at the file hellbox manages rather than whatever HOME happens to
	// be. Under systemd those can differ.
	if home := r.homeDir(); home != "" {
		cmd.Env = append(os.Environ(), "HOME="+home)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", r.Bin, err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		// Robot lines carrying long segment maps can exceed the default limit.
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			onLine(sc.Text())
		}
		if err := sc.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			onLine(fmt.Sprintf("MSG:0,0,0,\"hellbox: error reading makemkvcon output: %v\"", err))
		}
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w: %s", err, s)
		}
		return err
	}
	return nil
}

func normaliseStreamKind(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "video":
		return "video"
	case "audio":
		return "audio"
	case "subtitles", "subtitle":
		return "subtitle"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// inferType guesses the disc format from its titles. Blu-ray sources are named
// with .m2ts/.mpls, DVD sources with .VOB or an IFO-derived title set.
func inferType(d disc.Disc) disc.Type {
	for _, t := range d.Titles {
		src := strings.ToLower(t.SourceFile)
		switch {
		case strings.Contains(src, ".m2ts"), strings.Contains(src, ".mpls"):
			return disc.TypeBluRay
		case strings.Contains(src, ".vob"), strings.Contains(src, "vts_"):
			return disc.TypeDVD
		}
	}
	return disc.TypeUnknown
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// summarise picks the messages most likely to explain a failure.
func summarise(msgs []Message) string {
	var out []string
	for _, m := range msgs {
		t := strings.TrimSpace(m.Text)
		if t == "" {
			continue
		}
		if strings.Contains(strings.ToLower(t), "fail") ||
			strings.Contains(strings.ToLower(t), "error") ||
			strings.Contains(strings.ToLower(t), "unable") {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		for i := len(msgs) - 1; i >= 0 && len(out) < 2; i-- {
			if t := strings.TrimSpace(msgs[i].Text); t != "" {
				out = append(out, t)
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	if len(out) == 0 {
		return "no diagnostic output"
	}
	return strings.Join(out, "; ")
}
