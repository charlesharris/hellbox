package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A config file written before a setting existed must keep working. The
// installed /etc/hellbox/config.toml is not rewritten by an upgrade — `make
// install` deliberately keeps the existing file — so any new setting that did
// not default cleanly would stop the daemon starting after an upgrade, with an
// error about a key the operator has never heard of.
func TestOlderConfigStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	// Verbatim from before scan_attempts and scan_retry_delay existed.
	old := `
rips_dir    = "/srv/media/rips"
work_dir    = "/srv/media/work"
library_dir = "/srv/media/library"
state_path  = "/var/lib/hellbox/state.db"
socket_path = "/run/hellbox/hellbox.sock"
min_title_seconds = 60
max_rip_attempts  = 2
poll_interval     = "2s"
`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a config predating the scan settings failed to load: %v", err)
	}
	if cfg.ScanAttempts != 3 {
		t.Errorf("ScanAttempts = %d, want the default 3", cfg.ScanAttempts)
	}
	if cfg.ScanRetryDelay.Duration.Seconds() != 15 {
		t.Errorf("ScanRetryDelay = %s, want the default 15s", cfg.ScanRetryDelay)
	}
	if !cfg.Transcode || cfg.TranscodedDir == "" || cfg.TranscodeQuality != 20 {
		t.Errorf("transcode settings did not default: transcode=%v dir=%q quality=%d",
			cfg.Transcode, cfg.TranscodedDir, cfg.TranscodeQuality)
	}
	if cfg.VAAPIDevice != "auto" {
		t.Errorf("VAAPIDevice = %q, want the default \"auto\"", cfg.VAAPIDevice)
	}

	// The values the file did set must survive the defaulting.
	if cfg.MaxRipAttempts != 2 || cfg.MinTitleSeconds != 60 {
		t.Errorf("file values were lost: %+v", cfg)
	}
}

// The example file is what a new install gets, so it has to be valid.
func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatalf("config.example.toml does not load: %v", err)
	}
	if cfg.ScanAttempts < 1 {
		t.Errorf("ScanAttempts = %d", cfg.ScanAttempts)
	}
}
