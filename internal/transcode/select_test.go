package transcode

import (
	"strings"
	"testing"
)

func aud(i int, codec, lang, title string, def bool, ch int) Stream {
	return Stream{Index: i, Kind: "audio", Codec: codec, Language: lang, Title: title, Default: def, Channels: ch}
}
func sub(i int, codec, lang string) Stream {
	return Stream{Index: i, Kind: "subtitle", Codec: codec, Language: lang}
}
func vid(i int) Stream { return Stream{Index: i, Kind: "video", Codec: "h264"} }

// The case this exists for: a Blu-ray carries several audio tracks at three to
// four megabits each, which together cost more than the video budget.
func TestSelectKeepsOneAudioTrack(t *testing.T) {
	sel := Select([]Stream{
		vid(0),
		aud(1, "truehd", "eng", "", true, 8),
		aud(2, "ac3", "eng", "", false, 6),
		aud(3, "dts", "fra", "", false, 6),
		sub(4, "pgs", "eng"),
	}, "eng")

	if len(sel.Maps) != 3 {
		t.Fatalf("kept %v, want video + one audio + one subtitle", sel.Maps)
	}
	if sel.Maps[1] != "0:1" {
		t.Errorf("chose audio %s, want the default English track 0:1", sel.Maps[1])
	}
	if !sel.TranscodeAudio {
		t.Error("a TrueHD track was going to be copied, not re-encoded")
	}
	if len(sel.Dropped) != 2 {
		t.Errorf("dropped %v, want the two unused audio tracks reported", sel.Dropped)
	}
}

// Compact audio is copied. Re-encoding something already lossy only loses more,
// and a DVD's AC3 costs a few percent of the file.
func TestSelectCopiesCompactAudio(t *testing.T) {
	sel := Select([]Stream{vid(0), aud(1, "ac3", "eng", "", true, 6), sub(2, "dvd_subtitle", "eng")}, "eng")
	if sel.TranscodeAudio {
		t.Error("AC3 was going to be re-encoded")
	}
}

// The dangerous case. Many discs tag no language at all, and a filter that
// removed every track from an untagged disc would produce a silent film and
// report success.
func TestSelectNeverEmptiesAnUntaggedDisc(t *testing.T) {
	sel := Select([]Stream{
		vid(0),
		aud(1, "ac3", "", "", false, 6),
		sub(2, "dvd_subtitle", ""),
		sub(3, "dvd_subtitle", ""),
	}, "eng")

	if len(sel.Maps) != 4 {
		t.Fatalf("kept %v from an untagged disc, want everything", sel.Maps)
	}
}

// A language that matches nothing must be ignored, not obeyed.
func TestSelectFallsBackWhenLanguageMatchesNothing(t *testing.T) {
	sel := Select([]Stream{vid(0), aud(1, "ac3", "jpn", "", true, 6), sub(2, "pgs", "jpn")}, "eng")
	if len(sel.Maps) != 3 {
		t.Errorf("kept %v, want everything when no stream is in the wanted language", sel.Maps)
	}
}

// A commentary track is never the feature's audio, however it is flagged — and
// discs do sometimes mark one as default.
func TestSelectSkipsCommentaryEvenWhenDefault(t *testing.T) {
	sel := Select([]Stream{
		vid(0),
		aud(1, "ac3", "eng", "Director's Commentary", true, 2),
		aud(2, "ac3", "eng", "", false, 6),
	}, "eng")

	if sel.Maps[1] != "0:2" {
		t.Errorf("chose %s, want the feature audio 0:2 rather than the commentary", sel.Maps[1])
	}
}

// Two- and three-letter tags both appear on real discs.
func TestSelectMatchesEitherLanguageForm(t *testing.T) {
	for _, tag := range []string{"en", "eng"} {
		sel := Select([]Stream{vid(0), aud(1, "ac3", tag, "", true, 6), aud(2, "ac3", "fra", "", false, 6)}, "eng")
		if sel.Maps[1] != "0:1" {
			t.Errorf("language tag %q was not matched: kept %v", tag, sel.Maps)
		}
	}
}

// Nothing probed means nothing filtered: a caller without stream information
// must still get a complete file.
func TestNoStreamsKeepsEverything(t *testing.T) {
	got := argString(New("ffmpeg", "").args("in.mkv", "out.mkv", Profile{Quality: 20}))
	if !strings.Contains(got, " -map 0 ") {
		t.Errorf("unprobed source did not map everything:\n%s", got)
	}
	if !strings.Contains(got, " -c:a copy ") {
		t.Errorf("unprobed source did not copy audio:\n%s", got)
	}
}

// A lossless track re-encodes to AC3 at the configured bitrate; anything else
// is copied.
func TestAudioCodecArgs(t *testing.T) {
	hd := argString(New("ffmpeg", "").args("in.mkv", "out.mkv", Profile{
		Quality: 20, AudioKbps: 640, Language: "eng",
		Streams: []Stream{vid(0), aud(1, "truehd", "eng", "", true, 8)},
	}))
	if !strings.Contains(hd, " -c:a ac3 ") || !strings.Contains(hd, " -b:a 640k ") {
		t.Errorf("lossless audio not re-encoded:\n%s", hd)
	}

	sd := argString(New("ffmpeg", "").args("in.mkv", "out.mkv", Profile{
		Quality: 20, AudioKbps: 640, Language: "eng",
		Streams: []Stream{vid(0), aud(1, "ac3", "eng", "", true, 6)},
	}))
	if !strings.Contains(sd, " -c:a copy ") {
		t.Errorf("compact audio not copied:\n%s", sd)
	}
}

// The case this exists for: Hackers came out of a Blu-ray in stereo, and
// nothing anywhere recorded whether a 5.1 track had been passed over or the
// disc had only ever carried two channels. The selection was computed and
// discarded, so the finished file was the only evidence and it could not
// answer the question.
func TestSelectionSaysWhatItKeptAndDropped(t *testing.T) {
	sel := Select([]Stream{
		{Index: 0, Kind: "video", Codec: "h264"},
		{Index: 1, Kind: "audio", Codec: "ac3", Channels: 2, Language: "eng", Default: true},
		{Index: 2, Kind: "audio", Codec: "dts", Channels: 6, Language: "eng"},
		{Index: 3, Kind: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"},
	}, "eng")

	joined := strings.Join(sel.Kept, "; ") + " || " + strings.Join(sel.Dropped, "; ")
	if !strings.Contains(joined, "5.1") {
		t.Errorf("neither the kept nor the dropped list mentions the 5.1 track: %s", joined)
	}
	if len(sel.Kept) == 0 {
		t.Error("nothing was reported as kept, so the output has no account of itself")
	}
}

// A disc that flags its stereo track as default — many do, for players that
// cannot handle more — used to lose its 5.1 track to that flag. The disc has
// no idea what this library is played back on.
func TestTheWidestMixWinsOverTheDefaultFlag(t *testing.T) {
	sel := Select([]Stream{
		{Index: 0, Kind: "video", Codec: "h264"},
		{Index: 1, Kind: "audio", Codec: "ac3", Channels: 2, Language: "eng", Default: true},
		{Index: 2, Kind: "audio", Codec: "dts", Channels: 6, Language: "eng"},
	}, "eng")

	if !strings.Contains(strings.Join(sel.Kept, " "), "5.1") {
		t.Errorf("kept %v, want the 5.1 track over the default-flagged stereo one", sel.Kept)
	}
}

// Commentary is still excluded even when it is the widest track on the disc,
// which happens: an isolated score in 5.1 beside a stereo feature mix.
func TestAWideCommentaryTrackStillLoses(t *testing.T) {
	sel := Select([]Stream{
		{Index: 0, Kind: "video", Codec: "h264"},
		{Index: 1, Kind: "audio", Codec: "ac3", Channels: 2, Language: "eng"},
		{Index: 2, Kind: "audio", Codec: "dts", Channels: 6, Language: "eng", Title: "Director commentary"},
	}, "eng")

	if strings.Contains(strings.Join(sel.Kept, " "), "commentary") {
		t.Errorf("kept %v, want the feature's stereo mix rather than the 5.1 commentary", sel.Kept)
	}
}
