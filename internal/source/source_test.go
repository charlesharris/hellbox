package source

import (
	"strings"
	"testing"

	"hellbox/internal/bluray"
	"hellbox/internal/disc"
)

// fireflyLike reproduces what enumeration returned from the real Firefly disc:
// AACS handled, BD+ present and not handled, four episodes among the playlists.
func fireflyLike() *bluray.Info {
	return &bluray.Info{
		VolumeLabel: "FIREFLYUS_D1",
		DiscName:    "FIREFLY: DISC 1",
		Thumbnails:  []string{"Firefly_metadata_640.jpg"},
		Protection: bluray.Protection{
			AACS: true, AACSHandled: true,
			BDPlus: true, BDPlusHandled: false,
			DiscID: "B0D76EF0",
		},
		Playlists: []bluray.Playlist{
			{File: "00001.mpls", DurationSecs: 5202, Chapters: 21, Video: 1, Audio: 5, Subtitles: 6},
			{File: "00002.mpls", DurationSecs: 2563, Chapters: 13, Video: 1, Audio: 5, Subtitles: 6},
			{File: "00003.mpls", DurationSecs: 2634, Chapters: 13, Video: 1, Audio: 4, Subtitles: 6},
			{File: "00004.mpls", DurationSecs: 2638, Chapters: 13, Video: 1, Audio: 5, Subtitles: 6},
			{File: "00099.mpls", DurationSecs: 1, Video: 1}, // junk
		},
	}
}

// The property the whole package exists for: a disc nothing can decrypt is
// still described completely.
func TestBDPlusDiscIsFullyDescribedDespiteBeingUnreadable(t *testing.T) {
	p := PlanBluRay(fireflyLike(), 60, false)

	if !p.Blocked {
		t.Error("with no MakeMKV a BD+ disc must be reported as blocked")
	}
	if p.Path != PathMakeMKV {
		t.Errorf("Path = %q, want %q", p.Path, PathMakeMKV)
	}
	// Blocked must never mean "we know nothing".
	if len(p.Disc.Titles) != 4 {
		t.Errorf("described %d titles, want 4 — enumeration must survive DRM", len(p.Disc.Titles))
	}
	if p.DiscName != "FIREFLY: DISC 1" {
		t.Errorf("DiscName = %q", p.DiscName)
	}
	if len(p.Artwork) == 0 {
		t.Error("cover art was lost")
	}
	if p.Disc.Fingerprint == "" {
		t.Error("a blocked disc still needs a fingerprint, or it will be re-read forever")
	}
}

func TestBDPlusRoutesToMakeMKVWhenAvailable(t *testing.T) {
	p := PlanBluRay(fireflyLike(), 60, true)

	if p.Blocked {
		t.Error("with MakeMKV available the disc is awkward, not blocked")
	}
	if p.Path != PathMakeMKV {
		t.Errorf("Path = %q, want %q", p.Path, PathMakeMKV)
	}
	if !p.Path.NeedsLicence() {
		t.Error("the MakeMKV path must count against licence exposure")
	}
}

func TestAACSOnlyDiscReadsNatively(t *testing.T) {
	info := fireflyLike()
	info.Protection.BDPlus = false

	p := PlanBluRay(info, 60, true)
	if p.Path != PathNativeBluRay {
		t.Errorf("Path = %q, want %q — an AACS-only disc needs no licence", p.Path, PathNativeBluRay)
	}
	if p.Path.NeedsLicence() {
		t.Error("the native Blu-ray path must not count against licence exposure")
	}
	if p.Blocked {
		t.Error("an AACS-only disc is not blocked")
	}
}

// A disc missing from KEYDB.cfg is a different problem from BD+ and deserves
// different advice, because updating the key database might actually fix it.
func TestUnhandledAACSSaysWhatToTry(t *testing.T) {
	info := fireflyLike()
	info.Protection.BDPlus = false
	info.Protection.AACSHandled = false

	p := PlanBluRay(info, 60, true)
	if !strings.Contains(strings.ToLower(p.Reason), "keydb") {
		t.Errorf("Reason = %q; should point at the key database", p.Reason)
	}
}

// The v1 reversal: a drive that will not authenticate CSS no longer sends the
// disc down the half-hour copy path, because libdvdcss decrypts in place.
func TestRegionBlockedDVDStillReadsInPlace(t *testing.T) {
	d := disc.Disc{VolumeLabel: "DVD_VIDEO", Type: disc.TypeDVD,
		Titles: []disc.Title{{Index: 0, DurationSecs: 7608}}}

	p := PlanDVD(d, false)
	if p.Path != PathNativeDVD {
		t.Errorf("Path = %q, want %q — the copy is for discs that fail, not discs that look like they will", p.Path, PathNativeDVD)
	}
	if p.Blocked {
		t.Error("a CSS disc in a region-locked drive is not blocked")
	}
	if p.Reason == "" {
		t.Error("the reason should record that libdvdcss did the work, not the drive")
	}
}

func TestFallbacks(t *testing.T) {
	if got, ok := Fallback(PathNativeDVD); !ok || got != PathDecryptCopy {
		t.Errorf("DVD fallback = %q,%v; want the decrypt copy", got, ok)
	}
	if got, ok := Fallback(PathNativeBluRay); !ok || got != PathMakeMKV {
		t.Errorf("Blu-ray fallback = %q,%v; want MakeMKV", got, ok)
	}
	// There is nothing after a copy that failed, and pretending otherwise would
	// retry a damaged disc forever.
	if _, ok := Fallback(PathDecryptCopy); ok {
		t.Error("the decrypt copy must be the end of the line")
	}
	if _, ok := Fallback(PathMakeMKV); ok {
		t.Error("MakeMKV must be the end of the line")
	}
}

// Health reports the collection's DRM mix so nobody has to know which of their
// discs are Fox pressings.
func TestMixReportsLicenceExposure(t *testing.T) {
	m := Mix{}
	for i := 0; i < 14; i++ {
		m.Add(PathNativeBluRay)
	}
	for i := 0; i < 6; i++ {
		m.Add(PathMakeMKV)
	}
	m.Add(PathNativeDVD)
	m.Add(PathUnknown) // must not be counted

	if got := m.LicenceExposure(); got != 6 {
		t.Errorf("LicenceExposure = %d, want 6", got)
	}
	if m[PathUnknown] != 0 {
		t.Error("an unrouted disc must not be recorded")
	}
	if m[PathNativeBluRay] != 14 {
		t.Errorf("native Blu-ray count = %d, want 14", m[PathNativeBluRay])
	}
}

func TestSummaryPrefersTheDiscsOwnName(t *testing.T) {
	p := PlanBluRay(fireflyLike(), 60, true)
	got := p.Summary()

	if !strings.Contains(got, "FIREFLY: DISC 1") {
		t.Errorf("Summary = %q; should use the disc's own name over the volume label", got)
	}
	if !strings.Contains(got, "4 titles") {
		t.Errorf("Summary = %q; should say how much is on the disc", got)
	}
	if !strings.Contains(got, "BD+") {
		t.Errorf("Summary = %q; should say why it is awkward", got)
	}
}

func TestSummaryFallsBackToVolumeLabelThenPlaceholder(t *testing.T) {
	info := fireflyLike()
	info.DiscName = ""
	if got := PlanBluRay(info, 60, true).Summary(); !strings.Contains(got, "FIREFLYUS_D1") {
		t.Errorf("Summary = %q; should fall back to the volume label", got)
	}

	info.VolumeLabel = ""
	if got := PlanBluRay(info, 60, true).Summary(); !strings.Contains(got, "unlabelled") {
		t.Errorf("Summary = %q; should not render an empty name", got)
	}
}
