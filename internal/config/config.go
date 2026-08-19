// Package config loads hellbox's single TOML configuration file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultPath is where hellboxd looks for its configuration.
const DefaultPath = "/etc/hellbox/config.toml"

// Config is the whole of hellbox's configuration. There is deliberately one
// file: the stack this replaces kept two that shadowed each other, and keeping
// them in sync was a recurring source of confusion.
type Config struct {
	RipsDir    string `toml:"rips_dir"`
	WorkDir    string `toml:"work_dir"`
	LibraryDir string `toml:"library_dir"`

	// TranscodedDir holds library-ready files. It is separate from LibraryDir
	// because naming them for Jellyfin needs the identification Phase 3 does;
	// until then they are correct files under disc-derived names.
	TranscodedDir string `toml:"transcoded_dir"`

	StatePath  string `toml:"state_path"`
	SocketPath string `toml:"socket_path"`

	// HTTPAddr is where the JSON API and event stream listen. Loopback only:
	// nothing here authenticates, so the bind address is the whole boundary.
	// Empty disables the HTTP API entirely.
	HTTPAddr string `toml:"http_addr"`

	// NativeDVD reads DVDs with libdvdread and libdvdcss under ffmpeg instead of
	// MakeMKV. No registration key, chapters preserved, and region-free.
	//
	// Measured against MakeMKV on the same title in the same drive: durations
	// agreed to 0.03 seconds, chapters and audio identical, one more subtitle
	// track kept. MakeMKV still tags subtitle languages better.
	NativeDVD bool `toml:"native_dvd"`

	// DVDPreindex asks the demuxer for exact chapter marks at the cost of reading
	// each title twice. Chapters arrive without it; measured, it moved a single
	// mark by 124ms, which is a poor trade against doubling a two-hour read.
	DVDPreindex bool `toml:"dvd_preindex"`

	// MinTitleSeconds excludes menu loops and stills from a rip. Everything
	// above it is ripped; sorting out what the titles mean happens later,
	// because pruning from disk is cheap and re-reading a disc is not.
	MinTitleSeconds int `toml:"min_title_seconds"`

	// MinOutputBytes is the floor below which an output file is treated as a
	// failed rip rather than a very short title.
	MinOutputBytes int64 `toml:"min_output_bytes"`

	MaxRipAttempts int `toml:"max_rip_attempts"`

	// ScanAttempts bounds how many times a disc is scanned before the drive
	// gives up on it.
	//
	// A drive reports its disc ready over SCSI before MakeMKV can necessarily
	// open it, so the first scan after insertion can fail on a disc that reads
	// perfectly a few seconds later. MaxRipAttempts cannot cover this: it is
	// counted per disc, and until a scan succeeds there is no disc to count
	// against.
	ScanAttempts int `toml:"scan_attempts"`

	// ScanRetryDelay is the pause between scan attempts, long enough for a disc
	// that was still spinning up to have settled.
	ScanRetryDelay Duration `toml:"scan_retry_delay"`

	// ScanTimeout bounds a single scan attempt. Zero waits indefinitely.
	//
	// A scan is bounded work, so unlike a rip it can simply be given a
	// deadline rather than a progress watchdog. It needs one: a scan that
	// hangs hangs forever, and because the attempt never returns, the retries
	// never come round either.
	//
	// The default is deliberately far above what a scan should take. Most
	// finish inside a minute, but one here took fourteen and a half — reading
	// nothing at all for minutes at a stretch, on a drive that cannot decrypt
	// the disc and is retrying its way through — and still succeeded. A
	// deadline that stops work which would have finished is worse than the
	// hang it guards against, so this is set to catch something plainly stuck
	// rather than something slow.
	ScanTimeout Duration `toml:"scan_timeout"`

	// DecryptFallback allows a disc the drive cannot decrypt to be copied to
	// disk and decrypted there before it is ripped.
	//
	// An RPC-2 drive with no region set cannot authenticate CSS, which leaves a
	// disc that scans perfectly and will not rip. libdvdcss does not need that
	// authentication, so copying the disc out with dvdbackup and ripping the
	// copy works where the drive alone does not — and costs none of the handful
	// of region changes a drive permits before locking permanently.
	//
	// It needs dvdbackup installed, and work_dir space for a whole disc.
	DecryptFallback bool `toml:"decrypt_fallback"`

	// DVDBackupBin is the dvdbackup executable. Empty finds it on PATH.
	DVDBackupBin string `toml:"dvdbackup_bin"`

	// RipStallTimeout fails a title that has made no progress for this long
	// rather than waiting on it.
	//
	// MakeMKV does not always fail a rip it cannot finish: on a disc the drive
	// will not decrypt it retries internally, emitting messages but reading
	// nothing, and waits forever. Zero disables the watchdog and restores that
	// behaviour.
	RipStallTimeout Duration `toml:"rip_stall_timeout"`

	// Transcode turns a verified rip into a library file. Off leaves the rips
	// and does nothing else, which is what Phase 1 did.
	Transcode bool `toml:"transcode"`

	// TranscodeQuality is the encoder's quality parameter — qp for VAAPI, crf
	// for software, lower being better. They are different scales but close
	// enough on DVD material that one number serves both.
	//
	// Measured on this hardware against a real DVD: 20 gives SSIM 0.978 and
	// about 1.25 GB for a two-hour film, and the returns above it are poor —
	// 18 costs another 500 MB for 0.0017 SSIM.
	TranscodeQuality int `toml:"transcode_quality"`

	// TranscodeMaxKbps caps the output bitrate. Zero encodes at constant
	// quality with no ceiling.
	//
	// The quality parameter alone is a quantizer, not a size target, and what
	// it costs depends on the source. The setting that took a clean film from
	// 5.4 GB to 1.78 GB produced television larger than its own source, because
	// it preserved broadcast noise faithfully. Measured here: with a 2500 kbps
	// ceiling a film encodes at 1.36 Mb/s — untouched, it was never near the
	// cap — while a noisy PAL episode drops from 1976 MB to 430 MB.
	TranscodeMaxKbps int `toml:"transcode_max_kbps"`

	// TranscodeMaxHeight caps the output height, preserving aspect ratio. Zero
	// keeps whatever the source is.
	//
	// Only ever downwards, so standard-definition discs are untouched — the
	// stack this replaces upscaled 576p television to 720p and made it both
	// larger and worse. What this is for is Blu-ray: a 1080p frame under a
	// bitrate ceiling sized for standard definition does not save space, it
	// spends the saving on looking soft. At 720p the same bitrate covers a
	// quarter of the pixels.
	//
	// Zero by default: resolution is kept, and the bitrate ceiling does the
	// work instead. Spending the budget on fewer pixels is the worse trade
	// while there are bits to spare.
	TranscodeMaxHeight int `toml:"transcode_max_height"`

	// PreferredLanguage selects which audio and subtitle tracks are kept.
	//
	// A filter that matches nothing is ignored rather than obeyed: many discs
	// tag no language at all, and removing every track from an untagged disc
	// would produce a silent film and call it a success.
	PreferredLanguage string `toml:"preferred_language"`

	// AudioKbps is the bitrate for audio that has to be re-encoded. Only
	// lossless tracks are; anything already compact is copied untouched.
	AudioKbps int `toml:"audio_kbps"`

	// SoftwarePreset is libx264's speed/efficiency tradeoff. VAAPI has no
	// equivalent and ignores it.
	SoftwarePreset string `toml:"software_preset"`

	// FFmpegBin is the ffmpeg executable. Empty finds it on PATH.
	FFmpegBin string `toml:"ffmpeg_bin"`

	// VAAPIDevice is the render node used for hardware encoding.
	//
	// "auto" uses the usual node when it exists and falls back to software when
	// it does not. Empty forces software. A path names a specific device.
	//
	// Hardware encoding is roughly five times faster than software here for
	// output within 0.0013 SSIM of it, so falling back is a real cost — but a
	// machine whose GPU is unavailable should transcode slowly rather than not
	// at all.
	VAAPIDevice string `toml:"vaapi_device"`

	// FileToLibrary hardlinks finished transcodes into LibraryDir under the
	// layout Jellyfin expects.
	//
	// Off by default as of 2026-08-09, because slay files the library now and
	// two writers into one tree produce two entries for every disc: this path
	// names a film from the disc's shape alone, so "Roman Holiday" appears
	// beside slay's "Roman Holiday (1953)" and Jellyfin shows both.
	//
	// It is kept, and it still works, because it is the whole library with
	// Rails switched off — a hellboxd that can rip and file with no database,
	// no container and no browser is worth being able to fall back to. Turn it
	// on only when nothing else is writing LibraryDir.
	//
	// Identification here is left to Jellyfin, which does it against real
	// metadata providers. hellbox contributes the one thing Jellyfin cannot
	// know — which disc a file came from — and otherwise only produces names
	// Jellyfin can match. Episodes are filed unnumbered, because a disc
	// carries no episode numbers to read.
	FileToLibrary bool `toml:"file_to_library"`

	// EjectOnSuccess keeps the physical convention that an open tray means the
	// disc is done and safe to reshelve.
	EjectOnSuccess bool `toml:"eject_on_success"`

	// EjectOnFailure defaults to false so a failed disc stays in its drive.
	// Ejecting on failure would mean handing back a disc that did not rip,
	// which is easy to reshelve by mistake and hard to notice.
	EjectOnFailure bool `toml:"eject_on_failure"`

	PollInterval Duration `toml:"poll_interval"`

	MakeMKVBin string `toml:"makemkv_bin"`

	// MakeMKVSettingsPath is makemkvcon's settings.conf, which holds the
	// registration key. Empty means the home directory of the user hellboxd
	// runs as.
	MakeMKVSettingsPath string `toml:"makemkv_settings_path"`

	// AutoRefreshKey lets the daemon install MakeMKV's published beta key when
	// the one in place has expired.
	//
	// MakeMKV will not run without a valid key and the beta key lapses roughly
	// monthly, so without this an unattended appliance stops working every few
	// weeks for a reason unrelated to any disc. Refreshing is only attempted
	// after a health check finds the key bad, never on a schedule.
	AutoRefreshKey bool `toml:"auto_refresh_key"`

	// BetaKeyURL is where the published key is read from. Configurable so a
	// change of forum layout or address can be worked around without a new
	// binary.
	BetaKeyURL string `toml:"beta_key_url"`

	// KeyRefreshInterval is the shortest gap between two refresh attempts. A key
	// that has expired with no replacement published yet must not turn into a
	// request every time health is re-checked.
	KeyRefreshInterval Duration `toml:"key_refresh_interval"`

	Drives []DriveConfig `toml:"drives"`
}

// DriveConfig is optional per-drive configuration. Drives are discovered
// automatically; these entries exist only to name or disable one.
type DriveConfig struct {
	StableID string `toml:"stable_id"`
	Label    string `toml:"label"`
	Disabled bool   `toml:"disabled"`
}

// Duration wraps time.Duration so TOML can carry "2s" as a string.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		RipsDir:            "/srv/media/rips",
		WorkDir:            "/srv/media/work",
		LibraryDir:         "/srv/media/library",
		TranscodedDir:      "/srv/media/transcoded",
		Transcode:          true,
		TranscodeQuality:   20,
		TranscodeMaxKbps:   6000,
		TranscodeMaxHeight: 0,
		PreferredLanguage:  "eng",
		AudioKbps:          640,
		FileToLibrary:      false,
		SoftwarePreset:     "medium",
		VAAPIDevice:        "auto",
		StatePath:          "/var/lib/hellbox/state.db",
		SocketPath:         "/run/hellbox/hellbox.sock",
		HTTPAddr:           "127.0.0.1:9494",
		NativeDVD:          true,
		DVDPreindex:        false,
		MinTitleSeconds:    60,
		MinOutputBytes:     10 << 20,
		MaxRipAttempts:     2,
		ScanAttempts:       3,
		ScanRetryDelay:     Duration{15 * time.Second},
		RipStallTimeout:    Duration{10 * time.Minute},
		DecryptFallback:    true,
		EjectOnSuccess:     true,
		EjectOnFailure:     false,
		PollInterval:       Duration{2 * time.Second},
		MakeMKVBin:         "makemkvcon",

		// An empty BetaKeyURL means the address built into the binary; the
		// canonical value lives in the makemkv package rather than being
		// duplicated here where the two could drift apart.
		AutoRefreshKey:     true,
		KeyRefreshInterval: Duration{6 * time.Hour},
	}
}

// Load reads configuration from path, applying defaults for anything absent. A
// missing file is not an error: hellbox runs on defaults so that a first start
// needs no setup.
func Load(path string) (Config, error) {
	cfg := Default()

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, cfg.Validate()
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

// Validate checks the configuration is internally coherent. It does not touch
// the filesystem; that is the daemon's startup health check.
func (c Config) Validate() error {
	for _, p := range []struct{ name, value string }{
		{"rips_dir", c.RipsDir},
		{"state_path", c.StatePath},
		{"socket_path", c.SocketPath},
	} {
		if p.value == "" {
			return fmt.Errorf("%s must be set", p.name)
		}
		if !filepath.IsAbs(p.value) {
			return fmt.Errorf("%s must be an absolute path, got %q", p.name, p.value)
		}
	}
	if c.MinTitleSeconds < 0 {
		return fmt.Errorf("min_title_seconds must not be negative")
	}
	if c.MaxRipAttempts < 1 {
		return fmt.Errorf("max_rip_attempts must be at least 1")
	}
	if c.ScanAttempts < 1 {
		return fmt.Errorf("scan_attempts must be at least 1")
	}
	if c.ScanAttempts > 1 && c.ScanRetryDelay.Duration <= 0 {
		return fmt.Errorf("scan_retry_delay must be positive when scan_attempts is above 1")
	}
	if c.ScanTimeout.Duration < 0 {
		return fmt.Errorf("scan_timeout must not be negative")
	}
	if c.RipStallTimeout.Duration < 0 {
		return fmt.Errorf("rip_stall_timeout must not be negative")
	}
	if c.FileToLibrary && (c.LibraryDir == "" || !filepath.IsAbs(c.LibraryDir)) {
		return fmt.Errorf("library_dir must be an absolute path when file_to_library is on, got %q", c.LibraryDir)
	}
	if c.Transcode {
		if c.TranscodedDir == "" || !filepath.IsAbs(c.TranscodedDir) {
			return fmt.Errorf("transcoded_dir must be an absolute path when transcode is on, got %q", c.TranscodedDir)
		}
		// 0 is lossless for both encoders and 51 the worst h264 allows.
		if c.TranscodeMaxHeight < 0 {
			return fmt.Errorf("transcode_max_height must not be negative")
		}
		if c.TranscodeMaxKbps < 0 {
			return fmt.Errorf("transcode_max_kbps must not be negative")
		}
		if c.TranscodeQuality < 0 || c.TranscodeQuality > 51 {
			return fmt.Errorf("transcode_quality must be between 0 and 51, got %d", c.TranscodeQuality)
		}
	}
	if c.PollInterval.Duration <= 0 {
		return fmt.Errorf("poll_interval must be positive")
	}
	if c.AutoRefreshKey && c.KeyRefreshInterval.Duration <= 0 {
		return fmt.Errorf("key_refresh_interval must be positive when auto_refresh_key is set")
	}
	if c.MakeMKVSettingsPath != "" && !filepath.IsAbs(c.MakeMKVSettingsPath) {
		return fmt.Errorf("makemkv_settings_path must be an absolute path, got %q", c.MakeMKVSettingsPath)
	}
	return nil
}

// DriveFor returns the configuration for a drive, and whether one was given.
func (c Config) DriveFor(stableID string) (DriveConfig, bool) {
	for _, d := range c.Drives {
		if d.StableID == stableID {
			return d, true
		}
	}
	return DriveConfig{}, false
}
