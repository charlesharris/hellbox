package transcode

import (
	"strings"
	"testing"
	"time"
)

func argString(a []string) string { return " " + strings.Join(a, " ") + " " }

// VAAPI's options only work in one order, and getting it wrong does not fail
// loudly: ffmpeg either errors somewhere unrelated or quietly encodes in
// software at a fifth of the speed, which looks like a slow machine rather than
// a misconfiguration.
func TestHardwareArgsSetUpVAAPIBeforeTheInput(t *testing.T) {
	a := New("ffmpeg", "/dev/dri/renderD128").args("in.mkv", "out.mkv", Profile{Quality: 20})
	got := argString(a)

	for _, want := range []string{
		" -hwaccel vaapi ",
		" -hwaccel_device /dev/dri/renderD128 ",
		" -hwaccel_output_format vaapi ",
		" -c:v h264_vaapi ",
		" -qp 20 ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q:\n%s", want, got)
		}
	}

	// The hwaccel options configure the decoder, so they are meaningless after
	// the input they apply to.
	if idxOf(a, "-hwaccel") > idxOf(a, "-i") {
		t.Error("-hwaccel comes after -i, where it does not apply to the input")
	}
	if strings.Contains(got, " libx264 ") {
		t.Error("a hardware encode also asked for the software encoder")
	}
}

// The fallback has to be a real encode, not a hardware one with the device
// omitted.
func TestSoftwareArgsUseNoVAAPI(t *testing.T) {
	got := argString(New("ffmpeg", "").args("in.mkv", "out.mkv", Profile{Quality: 20, Preset: "medium"}))

	for _, want := range []string{" -c:v libx264 ", " -crf 20 ", " -preset medium "} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "vaapi") {
		t.Errorf("software encode mentions vaapi:\n%s", got)
	}
}

// Everything the disc carried has to survive. Audio and subtitles are copied
// rather than re-encoded, and chapters are carried over explicitly — none of
// which happens by default.
func TestEverythingIsCarriedOver(t *testing.T) {
	got := argString(New("ffmpeg", "/dev/dri/renderD128").args("in.mkv", "out.mkv", Profile{Quality: 20}))

	for _, want := range []string{" -map 0 ", " -map_chapters 0 ", " -c:a copy ", " -c:s copy "} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q:\n%s", want, got)
		}
	}
	// Data streams cannot be muxed into Matroska and abort the encode.
	if !strings.Contains(got, " -map -0:d? ") {
		t.Errorf("data streams are not excluded:\n%s", got)
	}
}

// No scaling and no frame rate conversion. The stack this replaces resized PAL
// television to 720p30 for no reason, producing 900 MB episodes that looked
// worse than the disc.
func TestNothingIsResampled(t *testing.T) {
	for _, e := range []*Encoder{New("ffmpeg", "/dev/dri/renderD128"), New("ffmpeg", "")} {
		got := argString(e.args("in.mkv", "out.mkv", Profile{Quality: 20}))
		for _, unwanted := range []string{" -vf ", " -s ", " -r ", " scale", " fps="} {
			if strings.Contains(got, unwanted) {
				t.Errorf("args resample the source with %q:\n%s", unwanted, got)
			}
		}
	}
}

// out_time_ms is microseconds despite its name, and always has been. Reading it
// as milliseconds leaves progress stuck near zero for the whole encode, which
// reads as a stalled job rather than a units bug.
func TestProgressReadsMicroseconds(t *testing.T) {
	const dur = time.Hour // 3,600,000,000 µs
	var p Progress

	if !updateProgress(&p, "out_time_us", "1800000000", dur) {
		t.Fatal("out_time_us was not accepted")
	}
	if p.Fraction < 0.49 || p.Fraction > 0.51 {
		t.Errorf("half way through an hour reported as %.4f, want ~0.5", p.Fraction)
	}

	p = Progress{}
	if !updateProgress(&p, "out_time_ms", "1800000000", dur) {
		t.Fatal("out_time_ms was not accepted")
	}
	if p.Fraction < 0.49 || p.Fraction > 0.51 {
		t.Errorf("out_time_ms read as milliseconds: %.6f, want ~0.5", p.Fraction)
	}
}

func TestProgressParsesSpeedAndClamps(t *testing.T) {
	var p Progress
	if !updateProgress(&p, "speed", "28.7x", time.Hour) || p.Speed != 28.7 {
		t.Errorf("speed = %v, want 28.7", p.Speed)
	}

	// ffmpeg overshoots the nominal duration on the last packets.
	updateProgress(&p, "out_time_us", "4000000000", time.Hour)
	if p.Fraction != 1 {
		t.Errorf("fraction = %v past the end, want it clamped to 1", p.Fraction)
	}

	// An unknown duration means no fraction rather than a divide by zero.
	var q Progress
	if updateProgress(&q, "out_time_us", "1000000", 0) || q.Fraction != 0 {
		t.Errorf("fraction = %v with no known duration, want 0", q.Fraction)
	}
}

func TestProgressIgnoresOtherKeys(t *testing.T) {
	var p Progress
	for _, k := range []string{"frame", "fps", "bitrate", "total_size", "progress"} {
		if updateProgress(&p, k, "123", time.Hour) {
			t.Errorf("key %q was treated as progress", k)
		}
	}
}

func idxOf(a []string, s string) int {
	for i, v := range a {
		if v == s {
			return i
		}
	}
	return -1
}

// The output is written to a ".part" name so an interrupted encode cannot be
// mistaken for a finished one, which hides the extension ffmpeg infers the
// container from. Without an explicit muxer it fails with "Invalid argument"
// and says nothing about the cause.
func TestMuxerIsNamedExplicitly(t *testing.T) {
	for _, e := range []*Encoder{New("ffmpeg", "/dev/dri/renderD128"), New("ffmpeg", "")} {
		got := argString(e.args("in.mkv", "/srv/media/x/.out.mkv.part", Profile{Quality: 20}))
		if !strings.Contains(got, " -f matroska ") {
			t.Errorf("no explicit muxer, so ffmpeg must guess from a .part extension:\n%s", got)
		}
	}
}

// A quality parameter is a quantizer, not a size target, and what it costs
// depends entirely on the source. The setting that took a clean film from
// 5.4 GB to 1.78 GB produced television larger than its own source, because it
// preserved broadcast noise faithfully. The ceiling is what stops that.
func TestBitrateCapUsesQualityDrivenRateControl(t *testing.T) {
	got := argString(New("ffmpeg", "/dev/dri/renderD128").
		args("in.mkv", "out.mkv", Profile{Quality: 20, MaxKbps: 2500}))

	for _, want := range []string{" -rc_mode QVBR ", " -global_quality 20 ", " -maxrate 2500k ", " -b:v 2000k "} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q:\n%s", want, got)
		}
	}
	// -qp selects constant-QP, which ignores the ceiling entirely.
	if strings.Contains(got, " -qp ") {
		t.Errorf("a capped encode also set -qp, which ignores the cap:\n%s", got)
	}
}

// Zero means no ceiling, which has to stay available: it is what a source
// already known to be well behaved should get.
func TestNoCapUsesConstantQuality(t *testing.T) {
	got := argString(New("ffmpeg", "/dev/dri/renderD128").
		args("in.mkv", "out.mkv", Profile{Quality: 20}))
	if !strings.Contains(got, " -qp 20 ") {
		t.Errorf("args missing constant quality:\n%s", got)
	}
	if strings.Contains(got, "maxrate") || strings.Contains(got, "QVBR") {
		t.Errorf("an uncapped encode set a ceiling anyway:\n%s", got)
	}
}

func TestSoftwareHonoursTheCap(t *testing.T) {
	got := argString(New("ffmpeg", "").args("in.mkv", "out.mkv", Profile{Quality: 20, MaxKbps: 2500}))
	for _, want := range []string{" -crf 20 ", " -maxrate 2500k ", " -bufsize 5000k "} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q:\n%s", want, got)
		}
	}
}

// Interlaced sources are deinterlaced to one frame per field, keeping the full
// motion the material was shot with. Encoding fields as frames leaves comb
// lines on every movement, which cost bits as well as looking wrong.
func TestInterlacedSourcesAreDeinterlaced(t *testing.T) {
	hw := argString(New("ffmpeg", "/dev/dri/renderD128").
		args("in.mkv", "out.mkv", Profile{Quality: 20, Interlaced: true}))
	if !strings.Contains(hw, " deinterlace_vaapi=rate=field ") {
		t.Errorf("hardware path does not deinterlace to one frame per field:\n%s", hw)
	}

	sw := argString(New("ffmpeg", "").args("in.mkv", "out.mkv", Profile{Quality: 20, Interlaced: true}))
	if !strings.Contains(sw, " yadif=mode=send_field ") {
		t.Errorf("software path does not deinterlace to one frame per field:\n%s", sw)
	}
}

// Progressive video must be left alone: deinterlacing it halves its detail.
func TestProgressiveSourcesAreNotFiltered(t *testing.T) {
	for _, e := range []*Encoder{New("ffmpeg", "/dev/dri/renderD128"), New("ffmpeg", "")} {
		got := argString(e.args("in.mkv", "out.mkv", Profile{Quality: 20}))
		if strings.Contains(got, " -vf ") {
			t.Errorf("progressive source was filtered:\n%s", got)
		}
	}
}

// ffprobe reports several names for interlaced, and "unknown" is not one of
// them: deinterlacing progressive video on a guess is worse than doing nothing.
func TestInterlacedFieldOrders(t *testing.T) {
	for _, order := range []string{"tt", "bb", "tb", "bt", "TT"} {
		if !interlacedOrders[strings.ToLower(order)] {
			t.Errorf("field_order %q not recognised as interlaced", order)
		}
	}
	for _, order := range []string{"progressive", "unknown", ""} {
		if interlacedOrders[order] {
			t.Errorf("field_order %q treated as interlaced", order)
		}
	}
}

// Only ever downwards. Upscaling is what made the previous stack's output both
// larger and worse than the disc it came from.
func TestScalingIsOnlyEverDown(t *testing.T) {
	// 1080p Blu-ray source, capped at 720 — scaled.
	hd := New("ffmpeg", "/dev/dri/renderD128").
		args("in.mkv", "out.mkv", Profile{Quality: 20, MaxHeight: 720, SourceHeight: 1080})
	if !strings.Contains(argString(hd), "scale_vaapi=w=-1:h=720") {
		t.Errorf("1080p source was not scaled down:\n%s", argString(hd))
	}

	// 576p PAL DVD under the same cap — untouched.
	for _, h := range []int{480, 576, 720} {
		got := argString(New("ffmpeg", "/dev/dri/renderD128").
			args("in.mkv", "out.mkv", Profile{Quality: 20, MaxHeight: 720, SourceHeight: h}))
		if strings.Contains(got, "scale") {
			t.Errorf("a %dp source was scaled against a 720 cap:\n%s", h, got)
		}
	}

	// No cap means no scaling whatever the source.
	got := argString(New("ffmpeg", "/dev/dri/renderD128").
		args("in.mkv", "out.mkv", Profile{Quality: 20, SourceHeight: 1080}))
	if strings.Contains(got, "scale") {
		t.Errorf("scaled with no cap set:\n%s", got)
	}
}

// Deinterlacing must come before the scaler, or the scaler works on interleaved
// fields and turns comb lines into permanent artefacts.
func TestDeinterlaceRunsBeforeScaling(t *testing.T) {
	p := Profile{Quality: 20, Interlaced: true, MaxHeight: 720, SourceHeight: 1080}

	hw := p.filters(true)
	if !strings.HasPrefix(hw, "deinterlace_vaapi") || !strings.Contains(hw, "scale_vaapi") {
		t.Errorf("hardware filter order wrong: %q", hw)
	}
	sw := p.filters(false)
	if !strings.HasPrefix(sw, "yadif") || !strings.Contains(sw, "scale=") {
		t.Errorf("software filter order wrong: %q", sw)
	}
}

// An empty -vf still forces a decode and re-encode path ffmpeg would otherwise
// skip, so it must be omitted rather than passed empty.
func TestNoFilterChainWhenNothingApplies(t *testing.T) {
	if got := (Profile{Quality: 20, SourceHeight: 480}).filters(true); got != "" {
		t.Errorf("filters() = %q, want empty", got)
	}
}
