package makemkv

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// BetaKeyURL is the forum post where MakeMKV publishes the current beta key.
//
// MakeMKV requires a valid key to run at all — the free-for-DVD promise covers
// which discs may be converted, not whether the program starts. The beta key
// expires roughly monthly, so an appliance that is not watched will otherwise
// stop working for a reason that has nothing to do with the disc or the drive.
const BetaKeyURL = "https://forum.makemkv.com/forum/viewtopic.php?t=1053"

// keyRE matches a MakeMKV beta key: a "T-" prefix followed by an opaque
// payload. The shape is checked before anything is written, so a page whose
// layout has changed cannot put arbitrary text into settings.conf.
var keyRE = regexp.MustCompile(`\bT-[A-Za-z0-9@_+/*-]{40,120}\b`)

// codeBlockRE finds the <code> blocks the forum wraps the key in. Taking the
// key from the code block first, and only falling back to a scan of the whole
// page, avoids picking up a key quoted in a reply below the original post.
var codeBlockRE = regexp.MustCompile(`(?is)<code[^>]*>(.*?)</code>`)

// validUntilRE captures the human-readable expiry note beside the key, e.g.
// "valid until end of July 2026". It is reported to the operator, never parsed
// into a date: MakeMKV writes it as prose and the phrasing varies.
var validUntilRE = regexp.MustCompile(`(?i)valid until ([^.<]{1,60})`)

// settingsKeyRE matches the app_Key line of MakeMKV's settings.conf.
var settingsKeyRE = regexp.MustCompile(`(?mi)^[ \t]*app_Key[ \t]*=.*$`)

// BetaKey is a published key together with the validity note beside it.
type BetaKey struct {
	Key        string
	ValidUntil string // prose, e.g. "end of July 2026"; empty if the page omitted it
}

// String renders the key for logs with the payload elided. A registration key
// is a credential and does not belong in a log file or a TUI panel.
func (b BetaKey) String() string {
	if b.Key == "" {
		return "(no key)"
	}
	suffix := ""
	if b.ValidUntil != "" {
		suffix = ", valid until " + b.ValidUntil
	}
	return fmt.Sprintf("%s…%s (%d chars%s)", b.Key[:min2(6, len(b.Key))],
		b.Key[max2(0, len(b.Key)-4):], len(b.Key), suffix)
}

// ParseBetaKey extracts the current beta key from the forum page.
func ParseBetaKey(page string) (BetaKey, error) {
	var out BetaKey

	for _, m := range codeBlockRE.FindAllStringSubmatch(page, -1) {
		candidate := strings.TrimSpace(html.UnescapeString(m[1]))
		if keyRE.MatchString(candidate) {
			out.Key = keyRE.FindString(candidate)
			break
		}
	}
	if out.Key == "" {
		out.Key = keyRE.FindString(html.UnescapeString(page))
	}
	if out.Key == "" {
		return BetaKey{}, fmt.Errorf("no MakeMKV key found on the page; its layout may have changed")
	}

	if m := validUntilRE.FindStringSubmatch(page); m != nil {
		out.ValidUntil = strings.TrimSpace(m[1])
	}
	return out, nil
}

// FetchBetaKey downloads and parses the currently published beta key.
func FetchBetaKey(ctx context.Context, url string) (BetaKey, error) {
	if url == "" {
		url = BetaKeyURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return BetaKey{}, fmt.Errorf("build request for %s: %w", url, err)
	}
	// The forum serves a plain page to a plain client; identifying hellbox keeps
	// the request honest about what is making it.
	req.Header.Set("User-Agent", "hellbox/"+userAgentVersion+" (+https://github.com/charris/hellbox)")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return BetaKey{}, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return BetaKey{}, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	// The page is a few tens of kilobytes; the cap stops a redirected or
	// misbehaving endpoint from being read into memory without bound.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return BetaKey{}, fmt.Errorf("read %s: %w", url, err)
	}
	return ParseBetaKey(string(body))
}

// DefaultSettingsPath is where makemkvcon reads its registration key from, for
// the user the daemon runs as. It returns "" when the home directory cannot be
// determined, which the caller reports rather than guessing at a path.
func DefaultSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".MakeMKV", "settings.conf")
}

// ReadSettingsKey returns the key currently in settings.conf, or "" when the
// file does not exist or holds no key. A missing file is not an error: it is
// the normal state before MakeMKV has ever been registered.
func ReadSettingsKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	line := settingsKeyRE.FindString(string(b))
	if line == "" {
		return "", nil
	}
	_, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", nil
	}
	return strings.Trim(strings.TrimSpace(value), `"`), nil
}

// WriteSettingsKey installs key into settings.conf, preserving every other
// setting in the file.
//
// The previous file is kept as settings.conf.bak. MakeMKV's settings file may
// hold configuration a person set by hand, and a key refresh should never be
// the reason that is lost.
func WriteSettingsKey(path, key string) error {
	if !keyRE.MatchString(key) {
		return fmt.Errorf("refusing to write a value that is not a MakeMKV key")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	// A registration key is a credential: default to owner-only, and keep the
	// existing mode when the file is already there and stricter.
	mode := os.FileMode(0o600)
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if fi, statErr := os.Stat(path); statErr == nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(path+".bak", existing, mode); err != nil {
			return fmt.Errorf("back up %s: %w", path, err)
		}
	case os.IsNotExist(err):
		existing = nil
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}

	line := fmt.Sprintf("app_Key = %q", key)
	var updated string
	switch {
	case settingsKeyRE.Match(existing):
		updated = settingsKeyRE.ReplaceAllLiteralString(string(existing), line)
	case len(existing) == 0:
		updated = line + "\n"
	default:
		updated = strings.TrimRight(string(existing), "\n") + "\n" + line + "\n"
	}

	// Written to a temporary file in the same directory and renamed, so an
	// interrupted write cannot leave makemkvcon with a truncated settings file
	// and no key at all.
	tmp, err := os.CreateTemp(dir, ".settings.conf.*")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary settings file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("set permissions on temporary settings file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary settings file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// RefreshResult describes what a key refresh did.
type RefreshResult struct {
	Fetched BetaKey
	Changed bool // false when the published key was already installed
	Path    string
}

// RefreshBetaKey fetches the published beta key and installs it, unless it is
// already the key in place.
//
// An unchanged key means MakeMKV has not yet published a replacement for one
// that has expired. That is reported rather than treated as success, because
// the daemon cannot fix it and the operator needs to know why rips are still
// failing.
func RefreshBetaKey(ctx context.Context, url, settingsPath string) (RefreshResult, error) {
	if settingsPath == "" {
		return RefreshResult{}, fmt.Errorf("no MakeMKV settings path; set makemkv_settings_path in the config")
	}

	fetched, err := FetchBetaKey(ctx, url)
	if err != nil {
		return RefreshResult{}, err
	}

	current, err := ReadSettingsKey(settingsPath)
	if err != nil {
		return RefreshResult{}, err
	}
	if current == fetched.Key {
		return RefreshResult{Fetched: fetched, Changed: false, Path: settingsPath}, nil
	}
	if err := WriteSettingsKey(settingsPath, fetched.Key); err != nil {
		return RefreshResult{}, err
	}
	return RefreshResult{Fetched: fetched, Changed: true, Path: settingsPath}, nil
}

// userAgentVersion is set by the daemon package so the fetch identifies the
// running release without makemkv importing daemon.
var userAgentVersion = "dev"

// SetUserAgentVersion records the release string used when fetching a key.
func SetUserAgentVersion(v string) { userAgentVersion = v }

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
