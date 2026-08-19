package bluray

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"hellbox/internal/library"
)

// The fixtures are verbatim output from libbluray's own tools, captured from
// Firefly disc 1 (a BD+ disc) on 2026-08-09. They are real rather than
// hand-written on purpose: every previous parser in this codebase that was
// written against an imagined format turned out to be wrong about it.

func load(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func fireflyInfo(t *testing.T) Info {
	t.Helper()
	info := ParseInfo(load(t, "firefly-bd_info.txt"))
	main, pls := ParseTitles(load(t, "firefly-bd_list_titles.txt"))
	info.MainTitle = main
	info.Playlists = pls
	return info
}

func TestParseInfoReadsDiscMetadata(t *testing.T) {
	info := ParseInfo(load(t, "firefly-bd_info.txt"))

	if got, want := info.VolumeLabel, "FIREFLYUS_D1"; got != want {
		t.Errorf("VolumeLabel = %q, want %q", got, want)
	}
	// The whole point of BdmtNet: prose a human typed, off a disc that cannot
	// be decrypted. Note the value contains a colon, which the field splitter
	// must not treat as a separator.
	if got, want := info.DiscName, "FIREFLY: DISC 1"; got != want {
		t.Errorf("DiscName = %q, want %q", got, want)
	}
	if len(info.Thumbnails) == 0 {
		t.Error("expected cover art to be listed")
	}
}

func TestParseInfoReadsProtection(t *testing.T) {
	p := ParseInfo(load(t, "firefly-bd_info.txt")).Protection

	if !p.AACS || !p.AACSHandled {
		t.Errorf("AACS should be detected and handled, got detected=%v handled=%v", p.AACS, p.AACSHandled)
	}
	// The finding that reshaped the Blu-ray design: BD+ present, not handled.
	if !p.BDPlus {
		t.Error("BD+ should be detected")
	}
	if p.BDPlusHandled {
		t.Error("BD+ must not be reported as handled; libbdplus has no VM data")
	}
	if !p.NeedsMakeMKV() {
		t.Error("a BD+ disc the free stack cannot handle must route to MakeMKV")
	}
	if p.DiscID == "" {
		t.Error("expected an AACS disc id")
	}
	if p.MKBVersion != 9 {
		t.Errorf("MKBVersion = %d, want 9", p.MKBVersion)
	}
}

// A disc with no BD+ at all must not be sent down the MakeMKV path.
func TestAACSOnlyDiscDoesNotNeedMakeMKV(t *testing.T) {
	p := ParseInfo("AACS detected : yes\nAACS handled : yes\nBD+ detected : no\n").Protection
	if p.NeedsMakeMKV() {
		t.Error("an AACS-only disc must be read natively")
	}
	if got, want := p.Describe(), "AACS"; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}

func TestParseTitlesReadsEveryPlaylist(t *testing.T) {
	main, pls := ParseTitles(load(t, "firefly-bd_list_titles.txt"))

	if len(pls) != 29 {
		t.Fatalf("parsed %d playlists, want 29", len(pls))
	}
	// libbluray nominates the 87-minute pilot. Recorded, and deliberately not
	// trusted — trusting it is exactly how v1 turned this disc into one file.
	if main != 8 {
		t.Errorf("MainTitle = %d, want 8", main)
	}

	var pilot *Playlist
	for i := range pls {
		if pls[i].File == "00001.mpls" {
			pilot = &pls[i]
		}
	}
	if pilot == nil {
		t.Fatal("00001.mpls not found")
	}
	if got, want := pilot.DurationSecs, 1*3600+26*60+42; got != want {
		t.Errorf("pilot duration = %d, want %d", got, want)
	}
	if pilot.Chapters != 21 || pilot.Audio != 5 || pilot.Subtitles != 6 {
		t.Errorf("pilot = %+v; want 21 chapters, 5 audio, 6 subs", *pilot)
	}
}

// The regression that matters most: this disc holds four episodes, and v1
// produced one file from it.
//
// Six playlists survive, not five. The sixth is 00008.mpls, a 2:29 piece with
// one audio track and no subtitles — a trailer or promo. It is kept
// deliberately: the standing rule is to rip everything above the threshold,
// because pruning a file from disk is trivial and putting the disc back in the
// drive is not. What matters is that all four episodes survive and all
// twenty-three silent stubs do not.
func TestContentFindsEveryEpisodeAndDropsJunk(t *testing.T) {
	content := fireflyInfo(t).Content(60)

	kept := map[string]bool{}
	for _, p := range content {
		kept[p.File] = true
	}

	episodes := []string{
		"00001.mpls", // pilot, 1:26:42
		"00002.mpls", // 0:42:43
		"00003.mpls", // 0:43:54
		"00004.mpls", // 0:43:58
	}
	for _, e := range episodes {
		if !kept[e] {
			t.Errorf("episode %s was dropped — this is the v1 box-set bug", e)
		}
	}
	if !kept["00006.mpls"] {
		t.Error("the 28-minute featurette was dropped")
	}

	if len(content) != 6 {
		var got []string
		for _, p := range content {
			got = append(got, fmt.Sprintf("%s(%ds,A:%d)", p.File, p.DurationSecs, p.Audio))
		}
		t.Errorf("Content() returned %d playlists (%v), want 6", len(content), got)
	}
}

// The threshold is a real knob on Blu-ray in a way it is not on DVD. At 60s
// this disc yields a 2:29 promo alongside the episodes; at 300s it yields only
// the four episodes and the featurette. Neither is wrong, and the point of the
// test is that the episodes survive either way.
func TestRaisingTheThresholdDropsThePromoButKeepsEpisodes(t *testing.T) {
	content := fireflyInfo(t).Content(300)

	if len(content) != 5 {
		t.Errorf("at 300s got %d playlists, want 5", len(content))
	}
	for _, p := range content {
		if p.File == "00008.mpls" {
			t.Error("the 2:29 promo should not survive a 300s threshold")
		}
	}
}

// Twenty-four playlists on this disc carry no audio at all. Duration alone
// keeps some of them; the audio requirement removes all of them.
func TestContentRejectsSilentPlaylists(t *testing.T) {
	for _, p := range fireflyInfo(t).Content(60) {
		if p.Audio < 1 {
			t.Errorf("kept a playlist with no audio: %s", p.File)
		}
	}
}

func TestDedupeCollapsesIdenticalPlaylists(t *testing.T) {
	in := []Playlist{
		{File: "00001.mpls", DurationSecs: 5202, Chapters: 21, Video: 1, Audio: 5, Subtitles: 6},
		{File: "00050.mpls", DurationSecs: 5202, Chapters: 21, Video: 1, Audio: 5, Subtitles: 6},
		{File: "00002.mpls", DurationSecs: 2563, Chapters: 13, Video: 1, Audio: 5, Subtitles: 6},
	}
	got := dedupe(in)
	if len(got) != 2 {
		t.Fatalf("dedupe kept %d, want 2", len(got))
	}
	if got[0].File != "00001.mpls" {
		t.Errorf("lowest-numbered duplicate should win, got %s", got[0].File)
	}
}

func TestTitlesAreRenumberedAndKeepTheirPlaylist(t *testing.T) {
	titles := fireflyInfo(t).Titles(60)

	for i, tt := range titles {
		if tt.Index != i {
			t.Errorf("title %d has Index %d; indices must be hellbox's own 0-based order", i, tt.Index)
		}
		// Without the playlist name the title cannot be read again without
		// re-enumerating the disc.
		if tt.SourceFile == "" {
			t.Errorf("title %d lost its playlist name", i)
		}
		if tt.OutputFile == "" {
			t.Errorf("title %d has no output name", i)
		}
	}
}

// Firefly disc 1 is a disc built to defeat the feature-ratio test: the pilot is
// 1.975x the median, just under the 2.0 threshold. It classifies correctly only
// because Classify asks the shape of the remainder first (commit a485595,
// written for a hypothetical Star Trek disc). This is that disc.
func TestFireflyClassifiesAsTelevision(t *testing.T) {
	d := fireflyInfo(t).Disc(60)

	if got := library.Classify(d.Titles); got != library.KindTV {
		t.Errorf("Classify = %q, want %q — a four-episode disc must not be filed as a film", got, library.KindTV)
	}
}

func TestDiscFingerprintIsStable(t *testing.T) {
	a := fireflyInfo(t).Disc(60)
	b := fireflyInfo(t).Disc(60)
	if a.Fingerprint != b.Fingerprint {
		t.Error("fingerprint must be deterministic across enumerations")
	}
	if a.Fingerprint == "" {
		t.Error("fingerprint must not be empty")
	}
	if a.Type != "bluray" {
		t.Errorf("Type = %q, want bluray", a.Type)
	}
}

// Malformed or truncated output must yield nothing rather than panic. bd_info
// on a disc it cannot open prints only its complaints.
func TestParsersTolerateRubbish(t *testing.T) {
	if _, pls := ParseTitles("bdplus.c:240: bdplus_init() failed!\n\n"); len(pls) != 0 {
		t.Errorf("expected no playlists from noise, got %d", len(pls))
	}
	info := ParseInfo("")
	if info.VolumeLabel != "" || info.Protection.AACS {
		t.Error("empty input should produce a zero Info")
	}
}

// libbluray enumerates Firefly disc 1 as 00008, 00002, 00001, 00006, 00003,
// 00004 — the pilot third. Playlist number is the disc's real order, and
// episode assignment aligns titles against episodes in sequence, so taking
// enumeration order would mislabel every episode while looking plausible.
func TestContentIsInDiscOrderNotEnumerationOrder(t *testing.T) {
	content := fireflyInfo(t).Content(60)

	var got []string
	for _, p := range content {
		got = append(got, p.File)
	}
	want := []string{"00001.mpls", "00002.mpls", "00003.mpls", "00004.mpls", "00006.mpls", "00008.mpls"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("playlist order = %v, want %v", got, want)
		}
	}

	// The pilot must be title 0: it is episode 1.
	titles := fireflyInfo(t).Titles(60)
	if titles[0].SourceFile != "00001.mpls" {
		t.Errorf("title 0 is %s, want the pilot 00001.mpls", titles[0].SourceFile)
	}
	if titles[0].DurationSecs != 5202 {
		t.Errorf("title 0 duration = %d, want the 1:26:42 pilot", titles[0].DurationSecs)
	}
}
