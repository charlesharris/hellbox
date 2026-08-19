package dvd

import (
	"testing"

	"hellbox/internal/disc"
	"hellbox/internal/library"
)

// The JSON below mirrors the shape ffprobe actually emitted for The Karate Kid
// on 2026-08-08 — 720x480, SAR 32:27 / DAR 16:9, 29.97fps, AC3 stereo tagged
// eng and fre, dvd_subtitle tracks carrying a VIEWPORT tag. Field names and
// value formats are copied from that run rather than imagined.

const karateKidTitle1 = `{
  "streams": [
    {"index":0,"codec_name":"mpeg2video","codec_type":"video","width":720,"height":480,
     "display_aspect_ratio":"16:9","r_frame_rate":"30000/1001","avg_frame_rate":"30000/1001",
     "disposition":{"default":1,"forced":0}},
    {"index":1,"codec_name":"ac3","codec_type":"audio","channels":2,"sample_rate":"48000",
     "disposition":{"default":1,"forced":0},"tags":{"language":"eng"}},
    {"index":2,"codec_name":"ac3","codec_type":"audio","channels":2,"sample_rate":"48000",
     "disposition":{"default":0,"forced":0},"tags":{"language":"fre"}},
    {"index":3,"codec_name":"dvd_subtitle","codec_type":"subtitle",
     "disposition":{"default":0,"forced":0},"tags":{"language":"eng","VIEWPORT":"Widescreen"}}
  ],
  "chapters": [
    {"start_time":"0.000000"},{"start_time":"229.666667"},{"start_time":"537.333333"}
  ],
  "format": {"duration":"7608.400000"}
}`

// Titles past the end of the disc return successfully with nothing in them.
// This is the real behaviour and it is what ends the enumeration sweep.
const emptyTitle = `{"streams":[],"chapters":[],"format":{"duration":"0.000000"}}`

func TestParseProbeReadsTheFeature(t *testing.T) {
	p, err := parseProbe([]byte(karateKidTitle1))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := p.durationSecs(), 7608; got != want {
		t.Errorf("duration = %d, want %d", got, want)
	}

	title := p.title(0)
	if title.Chapters != 3 {
		t.Errorf("chapters = %d, want 3", title.Chapters)
	}
	if title.OutputFile != "title_00.mkv" {
		t.Errorf("OutputFile = %q", title.OutputFile)
	}
	if len(title.Streams) != 4 {
		t.Fatalf("streams = %d, want 4", len(title.Streams))
	}

	v := title.Streams[0]
	if v.Kind != "video" || v.Codec != "mpeg2video" {
		t.Errorf("video stream = %+v", v)
	}
	if v.Resolution != "720x480" {
		t.Errorf("resolution = %q, want 720x480", v.Resolution)
	}
	if v.AspectRatio != "16:9" {
		t.Errorf("aspect = %q, want 16:9", v.AspectRatio)
	}

	if title.Streams[2].Language != "fre" {
		t.Errorf("second audio language = %q, want fre", title.Streams[2].Language)
	}
	// VIEWPORT is information MakeMKV never surfaced: whether a subpicture was
	// authored for widescreen or letterbox.
	if title.Streams[3].Name != "Widescreen" {
		t.Errorf("subtitle VIEWPORT = %q, want Widescreen", title.Streams[3].Name)
	}
}

func TestEmptyTitleEndsEnumeration(t *testing.T) {
	p, err := parseProbe([]byte(emptyTitle))
	if err != nil {
		t.Fatalf("an empty title must parse, not error: %v", err)
	}
	if p.durationSecs() != 0 {
		t.Errorf("duration = %d, want 0 — this is the end-of-disc signal", p.durationSecs())
	}
}

func TestParseProbeRejectsRubbish(t *testing.T) {
	if _, err := parseProbe([]byte("not json")); err == nil {
		t.Error("expected an error from malformed output")
	}
}

func TestFilteredDropsShortTitles(t *testing.T) {
	e := &Enumerator{MinSeconds: 60}
	in := []disc.Title{
		{Index: 0, DurationSecs: 7608},
		{Index: 1, DurationSecs: 6}, // the 18-subtitle stub on the real disc
		{Index: 2, DurationSecs: 88},
	}
	got := e.Filtered(in)
	if len(got) != 2 {
		t.Fatalf("kept %d titles, want 2", len(got))
	}
	for _, tt := range got {
		if tt.DurationSecs < 60 {
			t.Errorf("kept a %ds title", tt.DurationSecs)
		}
	}
}

// ---------- PAL ----------

func TestPALDetection(t *testing.T) {
	ntsc := []disc.Title{{Streams: []disc.Stream{
		{Kind: "video", Resolution: "720x480", FrameRate: "30000/1001"}}}}
	if PAL(ntsc) {
		t.Error("an NTSC disc must not be detected as PAL")
	}

	byHeight := []disc.Title{{Streams: []disc.Stream{
		{Kind: "video", Resolution: "720x576", FrameRate: "25/1"}}}}
	if !PAL(byHeight) {
		t.Error("576-line video is PAL")
	}

	// A disc could in principle be 25fps without being 576-line, which is why
	// the frame rate is checked independently.
	byRate := []disc.Title{{Streams: []disc.Stream{
		{Kind: "video", Resolution: "720x480", FrameRate: "25/1"}}}}
	if !PAL(byRate) {
		t.Error("25fps video is PAL")
	}
}

func TestPALIgnoresNonVideoStreams(t *testing.T) {
	audioOnly := []disc.Title{{Streams: []disc.Stream{
		{Kind: "audio", FrameRate: "25/1"}}}}
	if PAL(audioOnly) {
		t.Error("only video streams determine PAL")
	}
}

// The correction that stops a region 2 disc being mis-aligned by one episode.
// A 24-minute episode on a PAL disc measures ~23 minutes; comparing that raw
// against a provider's listing is what goes wrong.
func TestTrueSecondsUndoesPALSpeedup(t *testing.T) {
	// 24:00 of film, shown at 25fps, measures 1382s on the disc.
	const onDisc = 1382
	got := TrueSeconds(onDisc)

	if got < 1435 || got > 1445 {
		t.Errorf("TrueSeconds(%d) = %d, want ~1440 (24m00s)", onDisc, got)
	}
	// The uncorrected error is nearly a minute on a 24-minute episode, which is
	// larger than the gap between many adjacent episodes.
	if onDisc == got {
		t.Error("correction did nothing")
	}
}

func TestRateIs25(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"25/1", true},
		{"25", true},
		{"30000/1001", false},
		{"24000/1001", false},
		{"", false},
		{"garbage", false},
		{"25/0", false},
	} {
		if got := rateIs25(c.in); got != c.want {
			t.Errorf("rateIs25(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The Karate Kid's real title shape, from the Phase A sweep. A film disc: one
// long feature against a body of extras.
func TestKarateKidClassifiesAsFilm(t *testing.T) {
	secs := []int{7608, 119, 225, 782, 497, 600, 1439, 1284, 126, 88, 64, 6}
	var titles []disc.Title
	for i, s := range secs {
		titles = append(titles, disc.Title{Index: i, DurationSecs: s})
	}
	if got := library.Classify(titles); got != library.KindMovie {
		t.Errorf("Classify = %q, want %q", got, library.KindMovie)
	}
}
