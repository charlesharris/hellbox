package makemkv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realPage reproduces the markup the forum actually serves around the key,
// including the codebox wrapper and the trailing validity sentence.
const realPage = `<div class="content">As stated on a main page all features of MakeMKV are free while program is in beta.<br>
<br>
The current beta key is <div class="codebox"><p>Code: <a href="#" onclick="selectCode(this); return false;">Select all</a></p><pre><code>T-BSaJ6gwgMx4eIggWkVYXiVP_6zehm7WAO9dEydvzOHFHoZ6YQ82BL5cGpYDxvyRWnS</code></pre></div> and is valid until end of July 2026. Please check back for updated key on this page.</div>`

const wantKey = "T-BSaJ6gwgMx4eIggWkVYXiVP_6zehm7WAO9dEydvzOHFHoZ6YQ82BL5cGpYDxvyRWnS"

func TestParseBetaKey(t *testing.T) {
	got, err := ParseBetaKey(realPage)
	if err != nil {
		t.Fatalf("ParseBetaKey: %v", err)
	}
	if got.Key != wantKey {
		t.Errorf("key = %q, want %q", got.Key, wantKey)
	}
	if got.ValidUntil != "end of July 2026" {
		t.Errorf("ValidUntil = %q, want %q", got.ValidUntil, "end of July 2026")
	}
}

// A key quoted in a later reply must not win over the one in the original post.
func TestParseBetaKeyPrefersFirstCodeBlock(t *testing.T) {
	page := realPage + `<div class="content">I tried <code>T-OLDKEYOLDKEYOLDKEYOLDKEYOLDKEYOLDKEYOLDKEYOLDKEY123</code> and it failed.</div>`
	got, err := ParseBetaKey(page)
	if err != nil {
		t.Fatalf("ParseBetaKey: %v", err)
	}
	if got.Key != wantKey {
		t.Errorf("key = %q, want the first code block %q", got.Key, wantKey)
	}
}

func TestParseBetaKeyNoKey(t *testing.T) {
	if _, err := ParseBetaKey(`<div>The key has been removed pending an update.</div>`); err == nil {
		t.Fatal("expected an error when the page carries no key")
	}
}

// The key may arrive HTML-escaped; unescaping must happen before matching.
func TestParseBetaKeyUnescapes(t *testing.T) {
	page := `<pre><code>T-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ&#43;klmno</code></pre>`
	got, err := ParseBetaKey(page)
	if err != nil {
		t.Fatalf("ParseBetaKey: %v", err)
	}
	if !strings.Contains(got.Key, "+") {
		t.Errorf("key = %q, want the escaped + decoded", got.Key)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".MakeMKV", "settings.conf")

	if got, err := ReadSettingsKey(path); err != nil || got != "" {
		t.Fatalf("ReadSettingsKey on a missing file = %q, %v; want \"\", nil", got, err)
	}
	if err := WriteSettingsKey(path, wantKey); err != nil {
		t.Fatalf("WriteSettingsKey: %v", err)
	}
	got, err := ReadSettingsKey(path)
	if err != nil {
		t.Fatalf("ReadSettingsKey: %v", err)
	}
	if got != wantKey {
		t.Errorf("key = %q, want %q", got, wantKey)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600 for a file holding a credential", perm)
	}
}

// Other settings a person set by hand must survive a key refresh.
func TestWriteSettingsKeyPreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.conf")
	original := "app_DestinationDir = \"/srv/media/rips\"\napp_Key = \"T-oldoldoldoldoldoldoldoldoldoldoldoldoldoldoldold12\"\napp_DefaultSelectionString = \"+sel:all\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteSettingsKey(path, wantKey); err != nil {
		t.Fatalf("WriteSettingsKey: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		`app_DestinationDir = "/srv/media/rips"`,
		`app_DefaultSelectionString = "+sel:all"`,
		`app_Key = "` + wantKey + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("settings.conf lost %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "oldoldold") {
		t.Error("the previous key is still present")
	}

	// The prior file is kept, so a bad refresh can be undone by hand.
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bak) != original {
		t.Error("backup does not match the original file")
	}
}

// Appending must not corrupt a settings file that has no key line yet.
func TestWriteSettingsKeyAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.conf")
	if err := os.WriteFile(path, []byte("app_Java = \"/usr/bin/java\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteSettingsKey(path, wantKey); err != nil {
		t.Fatalf("WriteSettingsKey: %v", err)
	}
	got, err := ReadSettingsKey(path)
	if err != nil || got != wantKey {
		t.Fatalf("key = %q, %v; want %q", got, err, wantKey)
	}
}

// Nothing that fails the key shape may reach settings.conf. If the page layout
// changes, the failure must be a refusal, not a corrupted settings file.
func TestWriteSettingsKeyRejectsJunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.conf")
	for _, junk := range []string{"", "not a key", "<html>404</html>", "T-tooshort"} {
		if err := WriteSettingsKey(path, junk); err == nil {
			t.Errorf("WriteSettingsKey(%q) succeeded; want a refusal", junk)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected key created a settings file")
	}
}

func TestRefreshBetaKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(realPage))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "settings.conf")

	res, err := RefreshBetaKey(context.Background(), srv.URL, path)
	if err != nil {
		t.Fatalf("RefreshBetaKey: %v", err)
	}
	if !res.Changed {
		t.Error("first refresh reported no change")
	}
	if res.Fetched.Key != wantKey {
		t.Errorf("fetched %q, want %q", res.Fetched.Key, wantKey)
	}

	// Refreshing again finds the same key already installed. That is the signal
	// that MakeMKV has not published a replacement yet, and must not be reported
	// as a successful fix.
	res, err = RefreshBetaKey(context.Background(), srv.URL, path)
	if err != nil {
		t.Fatalf("second RefreshBetaKey: %v", err)
	}
	if res.Changed {
		t.Error("second refresh reported a change when the key was identical")
	}
}

func TestRefreshBetaKeyHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "settings.conf")
	if _, err := RefreshBetaKey(context.Background(), srv.URL, path); err == nil {
		t.Fatal("expected an error on HTTP 404")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a failed fetch wrote a settings file")
	}
}

// The key must never appear whole in a log line.
func TestBetaKeyStringRedacts(t *testing.T) {
	s := BetaKey{Key: wantKey, ValidUntil: "end of July 2026"}.String()
	if strings.Contains(s, wantKey) {
		t.Errorf("String() leaked the key: %s", s)
	}
	if !strings.Contains(s, "end of July 2026") {
		t.Errorf("String() dropped the validity note: %s", s)
	}
}

// The messages below are verbatim from MakeMKV 1.18.4 on this machine,
// captured by running makemkvcon against each key state in turn.
func TestKeyStatusFromMessages(t *testing.T) {
	const (
		banner  = "MakeMKV v1.18.4 linux(x64-release) started"
		updates = "Automatic checking for updates is enabled, you may disable it in preferences if you don't want MakeMKV to contact web server."
		noDisc  = "Failed to open disc"
		invalid = "The stored activation key is invalid. I guess someone tampered with settings... Please purchase an activation key if you've found this application useful."
		tooOld  = "This application version is too old.  Please download the latest version at http://www.makemkv.com/ or enter a registration key to continue using the current version."
	)

	tests := []struct {
		name    string
		msgs    []Message
		present bool
		expired bool
	}{
		{
			name:    "a good key produces no complaint",
			msgs:    []Message{{Code: 1005, Text: banner}, {Code: 5074, Text: updates}, {Code: 5010, Text: noDisc}},
			present: true, expired: false,
		},
		{
			// Detected by code. The prose carries neither "expired" nor
			// "invalid key" in that order, so substring matching alone missed it.
			name:    "an invalid key is caught by its message code",
			msgs:    []Message{{Code: 1005, Text: banner}, {Code: 5020, Text: invalid}, {Code: 5021, Text: tooOld}},
			present: true, expired: true,
		},
		{
			name:    "a version too old to run needs the same repair",
			msgs:    []Message{{Code: 1005, Text: banner}, {Code: 5021, Text: tooOld}},
			present: true, expired: true,
		},
		{
			// Wording hellbox has never seen, caught by the prose backstop.
			name:    "unknown code with expiry wording",
			msgs:    []Message{{Code: 9999, Text: "Your registration key has expired."}},
			present: true, expired: true,
		},
		{
			name:    "no messages at all",
			msgs:    nil,
			present: true, expired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := keyStatusFromMessages(tt.msgs)
			if got.Present != tt.present || got.Expired != tt.expired {
				t.Errorf("got present=%v expired=%v, want present=%v expired=%v (detail %q)",
					got.Present, got.Expired, tt.present, tt.expired, got.Detail)
			}
		})
	}
}

// A settings file with no key draws no complaint from makemkvcon at all — it
// starts and says nothing — so the file has to be inspected directly. Without
// this, a machine that had never been registered reported "accepted".
func TestCheckKeyDetectsMissingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".MakeMKV", "settings.conf")

	r := &Runner{Bin: "/nonexistent/makemkvcon", SettingsPath: path}

	// No settings file at all.
	st, err := r.CheckKey(context.Background())
	if err != nil {
		t.Fatalf("CheckKey: %v", err)
	}
	if st.Present {
		t.Error("a missing settings file was reported as having a key")
	}

	// Present, but with no key line in it.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("app_Java = \"/usr/bin/java\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = r.CheckKey(context.Background())
	if err != nil {
		t.Fatalf("CheckKey: %v", err)
	}
	if st.Present {
		t.Error("a settings file with no key was reported as having one")
	}
	if !strings.Contains(st.Detail, path) {
		t.Errorf("detail %q does not name the file that needs fixing", st.Detail)
	}
}

// makemkvcon finds its key at $HOME/.MakeMKV/settings.conf, so the runner must
// hand it a HOME matching the file hellbox manages. Otherwise a configured
// settings path changes only where the key is written, and makemkvcon goes on
// reading a different file — a refresh that reports success and changes nothing.
func TestRunnerHomeDir(t *testing.T) {
	r := &Runner{SettingsPath: "/var/lib/hellbox/mk/.MakeMKV/settings.conf"}
	if got := r.homeDir(); got != "/var/lib/hellbox/mk" {
		t.Errorf("homeDir = %q, want /var/lib/hellbox/mk", got)
	}
	if got := (&Runner{}).homeDir(); got != "" {
		t.Errorf("homeDir with no settings path = %q, want empty", got)
	}
}

// Code 5021 covers two different problems and distinguishes them only in its
// prose. An expired key is replaced by fetching the published one, which the
// daemon does by itself; a build too old to accept any key needs MakeMKV
// updating, which it cannot do. Confusing them means refreshing the key every
// few hours forever while telling the operator to fix the one thing that is not
// wrong.
func TestKeyStatusTellsAnOldBuildFromAnExpiredKey(t *testing.T) {
	// Verbatim from MakeMKV 1.18.4 once its build was too old for the beta key.
	tooOld := keyStatusFromMessages([]Message{{
		Code: 5021,
		Text: "This application version is too old. Please download the latest " +
			"version at http://www.makemkv.com/ or enter a registration key to " +
			"continue using the current version.",
	}})
	if !tooOld.VersionTooOld {
		t.Error("a build too old for any key was reported as an expired key")
	}
	if !tooOld.Expired {
		t.Error("a build too old for any key should still stop rips")
	}

	// An ordinary expired key must not be mistaken for an old build, or the
	// daemon would stop repairing the one thing it can repair.
	expired := keyStatusFromMessages([]Message{{
		Code: 5020,
		Text: "The stored activation key is invalid. I guess someone tampered with settings...",
	}})
	if expired.VersionTooOld {
		t.Error("an invalid key was reported as an old build")
	}
	if !expired.Expired {
		t.Error("an invalid key was not reported as needing replacement")
	}
}
