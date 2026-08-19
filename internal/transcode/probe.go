package transcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Source describes an input file, as far as encoding it needs to know.
type Source struct {
	Duration time.Duration

	// Interlaced reports whether the video is stored as fields rather than
	// whole frames. It decides whether the source is deinterlaced, which is not
	// a preference: encoding interlaced frames as progressive leaves comb lines
	// on every movement, and they cost bits as well as looking wrong.
	Interlaced bool

	Width, Height int
	FieldOrder    string

	// Streams is every stream the file carries, in file order.
	Streams []Stream
}

// Stream is one audio, video or subtitle stream.
type Stream struct {
	Index    int
	Kind     string // "video", "audio", "subtitle"
	Codec    string
	Channels int
	Language string
	Title    string
	Default  bool
}

// losslessAudio are the codecs worth re-encoding.
//
// A Blu-ray's TrueHD or DTS-HD Master Audio track runs three to four and a half
// megabits — more than the video budget for a whole film, and often several
// such tracks per disc. Compact codecs are copied untouched: re-encoding
// something already lossy only loses more.
var losslessAudio = map[string]bool{
	"truehd": true, "dts": true, "flac": true, "mlp": true,
	"pcm_bluray": true, "pcm_dvd": true, "pcm_s16le": true, "pcm_s24le": true,
}

// Lossless reports whether this stream is worth re-encoding.
//
// DTS is included because Blu-ray DTS is almost always DTS-HD Master Audio,
// whose lossy core ffmpeg reports simply as "dts". Treating it as lossless
// costs a re-encode of a lossy track at worst; treating it as compact costs
// three megabits a second on every disc that has one.
func (s Stream) Lossless() bool { return losslessAudio[strings.ToLower(s.Codec)] }

// probeOutput mirrors the part of ffprobe's JSON that matters here.
type probeOutput struct {
	Streams []struct {
		Index      int    `json:"index"`
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		FieldOrder string `json:"field_order"`
		Channels   int    `json:"channels"`
		Tags       struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default int `json:"default"`
		} `json:"disposition"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// interlacedOrders are ffprobe's names for a stream stored as fields.
//
// "progressive" and "unknown" are deliberately not here. An unknown field order
// is left alone: deinterlacing progressive video halves its detail, so guessing
// wrong in that direction is worse than doing nothing.
var interlacedOrders = map[string]bool{
	"tt": true, "bb": true, "tb": true, "bt": true,
}

// Probe reads what encoding needs to know about a file.
func (e *Encoder) Probe(ctx context.Context, path string) (*Source, error) {
	bin := e.ProbeBin()
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error",
		"-show_entries", "stream=index,codec_type,codec_name,width,height,field_order,channels:stream_tags=language,title:stream_disposition=default",
		"-show_entries", "format=duration",
		"-of", "json", path)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", bin, path, err)
	}

	var p probeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("read %s output for %s: %w", bin, path, err)
	}

	s := &Source{}
	if secs, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64); err == nil && secs > 0 {
		s.Duration = time.Duration(secs * float64(time.Second))
	}
	seenVideo := false
	for _, st := range p.Streams {
		s.Streams = append(s.Streams, Stream{
			Index:    st.Index,
			Kind:     st.CodecType,
			Codec:    st.CodecName,
			Channels: st.Channels,
			Language: strings.ToLower(strings.TrimSpace(st.Tags.Language)),
			Title:    st.Tags.Title,
			Default:  st.Disposition.Default != 0,
		})
		if st.CodecType == "video" && !seenVideo {
			seenVideo = true
			s.Width, s.Height = st.Width, st.Height
			s.FieldOrder = st.FieldOrder
			s.Interlaced = interlacedOrders[strings.ToLower(strings.TrimSpace(st.FieldOrder))]
		}
	}
	return s, nil
}

// ProbeBin is the ffprobe that sits beside the configured ffmpeg, so a custom
// build is probed with its own tools rather than whatever is on PATH.
func (e *Encoder) ProbeBin() string {
	if strings.HasSuffix(e.Bin, "ffmpeg") {
		return strings.TrimSuffix(e.Bin, "ffmpeg") + "ffprobe"
	}
	return "ffprobe"
}
