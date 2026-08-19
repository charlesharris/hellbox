package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"hellbox/internal/disc"
	"hellbox/internal/drive"
	"hellbox/internal/dvd"
	"hellbox/internal/makemkv"
	"hellbox/internal/verify"
)

// The native DVD path: libdvdread and libdvdcss under ffmpeg, in place of
// MakeMKV.
//
// It produces the same makemkv.ScanResult and makemkv.RipResult the rest of the
// worker already handles. Those are plain data structures rather than anything
// MakeMKV-specific, so using them as the shared currency avoids restructuring
// eight hundred lines of working pipeline to swap out what fills them in. An
// interface can earn its place later; today the branch is two functions.
//
// Measured against MakeMKV on the same title, in the same drive, before this
// was wired in: durations agreed to 0.03 seconds, chapters and audio were
// identical, and the native output kept one more subtitle track. What MakeMKV
// still does better is tag subtitle languages — it labelled one track "chi"
// where the native path leaves it untagged.

// nativeScan enumerates a DVD without MakeMKV.
//
// The volume label comes from blkid rather than the demuxer, because
// libdvdread does not report one and both the fingerprint and the rip
// directory name derive from it. The disc's own name is read from the Text
// Data Manager at the same time: it costs five seconds, needs no key, and is
// far better evidence than the label — "The Karate Kid (Special Edition)"
// against "DVD_VIDEO".
func (w *Worker) nativeScan(ctx context.Context) (*makemkv.ScanResult, error) {
	enum := &dvd.Enumerator{FFprobeBin: w.cfg.FFmpegBin, MinSeconds: w.cfg.MinTitleSeconds}
	if enum.FFprobeBin != "" {
		enum.FFprobeBin = ffprobeBeside(enum.FFprobeBin)
	}

	label, err := drive.VolumeLabel(ctx, w.drv.DevicePath)
	if err != nil {
		w.logf("warn", "%s: could not read the volume label: %v", w.label, err)
	}

	d, err := enum.Enumerate(ctx, w.drv.DevicePath)
	if err != nil {
		return nil, err
	}

	d.VolumeLabel = label
	d.Type = disc.TypeDVD
	d.ScannedAt = time.Now()
	d.DriveStableID = w.drv.StableID
	d.DriveModel = w.drv.Describe()

	// Titles below the threshold are dropped here rather than during
	// enumeration, so disc.json records everything the disc holds while only
	// the worthwhile titles are ripped. Unlike MakeMKV, the numbering is a
	// property of the disc, so filtering cannot shift what a title index means.
	all := d.Titles
	d.Titles = enum.Filtered(all)
	d.Fingerprint = disc.ComputeFingerprint(d.VolumeLabel, d.Titles)

	name, nameErr := dvd.DiscTitle(w.drv.DevicePath)
	if nameErr == nil && name != "" {
		d.DiscName = name
		w.logf("info", "%s: the disc calls itself %q", w.label, name)
	}

	var raw strings.Builder
	fmt.Fprintf(&raw, "native dvd enumeration (ffmpeg dvdvideo demuxer)\n")
	fmt.Fprintf(&raw, "device: %s\nvolume label: %s\ndisc name: %s\n", w.drv.DevicePath, label, name)
	fmt.Fprintf(&raw, "pal: %v\ntitles found: %d, above %ds: %d\n\n",
		dvd.PAL(all), len(all), w.cfg.MinTitleSeconds, len(d.Titles))
	for _, t := range all {
		kept := "  kept"
		if t.DurationSecs < w.cfg.MinTitleSeconds {
			kept = "skipped"
		}
		fmt.Fprintf(&raw, "%s title %2d  %-9s %2d chapters  %d streams\n",
			kept, t.Index, t.Duration(), t.Chapters, len(t.Streams))
	}

	return &makemkv.ScanResult{Disc: *d, Raw: raw.String()}, nil
}

// nativeRipTitle extracts one title without MakeMKV.
//
// The output name is hellbox's own title_NN.mkv, matching the MakeMKV path, so
// nothing downstream can tell which produced a given file.
func (w *Worker) nativeRipTitle(ctx context.Context, t disc.Title, ripDir string,
	onProgress func(makemkv.RipProgress)) (*makemkv.RipResult, error) {

	ex := &dvd.Extractor{
		FFmpegBin: w.cfg.FFmpegBin,
		Preindex:  w.cfg.DVDPreindex,
		// The same watchdog MakeMKV gets. Progress means the output timestamp
		// advancing, not lines arriving.
		StallTimeout: w.cfg.RipStallTimeout.Duration,
	}

	out := filepath.Join(ripDir, disc.TitleFileName(t.Index))
	res, err := ex.Extract(ctx, w.drv.DevicePath, t, out, func(p dvd.Progress) {
		if onProgress == nil {
			return
		}
		onProgress(makemkv.RipProgress{
			TitleIndex: t.Index,
			Fraction:   p.Fraction,
			Operation:  fmt.Sprintf("%.1fx", p.Speed),
		})
	})
	if err != nil {
		return nil, err
	}

	return &makemkv.RipResult{
		TitleIndex: t.Index,
		Path:       res.Path,
		SizeBytes:  res.SizeBytes,
		Elapsed:    res.Elapsed,
		Raw: fmt.Sprintf("native extraction: %s\n%d bytes in %s\n",
			res.Path, res.SizeBytes, res.Elapsed.Round(time.Second)),
	}, nil
}

// verifyNativeTitle checks an extracted title against what enumeration said.
//
// This is the check v1 never had and the one that catches the failure that
// cost three episodes: a file that exists, is large, and has valid Matroska
// magic, but holds a fraction of the runtime the disc claimed.
func (w *Worker) verifyNativeTitle(ctx context.Context, path string, t disc.Title) error {
	v := &verify.Verifier{FFprobeBin: ffprobeBeside(w.cfg.FFmpegBin)}
	res, err := v.Title(ctx, path, verify.Expectation{
		DurationSecs: t.DurationSecs,
		MinBytes:     w.cfg.MinOutputBytes,
	})
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("%s", res.Error())
	}
	return nil
}

// ffprobeBeside turns an ffmpeg path into the ffprobe next to it.
//
// Config names ffmpeg, not ffprobe, and an install with ffmpeg somewhere
// unusual almost certainly has ffprobe in the same place. Guessing here beats
// adding a second setting that would be wrong whenever the first one was.
func ffprobeBeside(ffmpegBin string) string {
	if ffmpegBin == "" || ffmpegBin == "ffmpeg" {
		return "ffprobe"
	}
	dir, base := filepath.Split(ffmpegBin)
	return filepath.Join(dir, strings.Replace(base, "ffmpeg", "ffprobe", 1))
}

// scanFileName is what the verbatim scan output is stored as.
//
// It was always "makemkv-info.txt", which stopped being true the moment the
// native path wrote ffmpeg output into it. The rips tree is meant to describe
// itself, and a file whose name says one program while its contents say
// another is exactly the sort of small lie that costs somebody an hour later.
func scanFileName(native bool) string {
	if native {
		return "scan-native.txt"
	}
	return "makemkv-info.txt"
}

// publishDisc puts a disc's description onto the event stream.
//
// This is what lets the catalog fill itself. The drives and log events say what
// is happening; this says what the disc *is* — its identity, its titles, their
// runtimes, and the name it gives itself. A client that has these can build a
// catalog without ever reading the filesystem.
//
// Sent at two moments, and both matter. After enumeration, because a disc that
// cannot be ripped is still worth cataloguing completely — a BD+ Blu-ray
// reaches the interface as "Firefly: Disc 1, 4 episodes" rather than an error.
// After the rip, because only then is there a rip directory to point at.
func (w *Worker) publishDisc(stage string, d disc.Disc, ripDir string, blocked bool, reason string) {
	if w.publish == nil {
		return
	}
	titles := make([]map[string]any, 0, len(d.Titles))
	for _, t := range d.Titles {
		var audio, subs int
		for _, s := range t.Streams {
			switch s.Kind {
			case "audio":
				audio++
			case "subtitle":
				subs++
			}
		}
		titles = append(titles, map[string]any{
			"index":            t.Index,
			"duration_seconds": t.DurationSecs,
			"chapters":         t.Chapters,
			"source_file":      t.SourceFile,
			"audio_count":      audio,
			"subtitle_count":   subs,
		})
	}

	w.publish("disc", map[string]any{
		"stage":          stage, // enumerated | ripped
		"fingerprint":    d.Fingerprint,
		"volume_label":   d.VolumeLabel,
		"disc_name":      d.DiscName,
		"disc_type":      string(d.Type),
		"read_path":      w.readPath(),
		"rip_dir":        ripDir,
		"blocked":        blocked,
		"blocked_reason": reason,
		"drive":          w.label,
		"titles":         titles,
	})
}

// readPath names the mechanism that read this disc, for the catalog's DRM mix.
func (w *Worker) readPath() string {
	if w.cfg.NativeDVD {
		return "native-dvd"
	}
	return "makemkv"
}
