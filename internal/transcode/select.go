package transcode

import (
	"fmt"
	"strings"
)

// Selection is which streams reach the output, and how each is treated.
type Selection struct {
	// Maps are the ffmpeg -map arguments, in output order.
	Maps []string

	// TranscodeAudio is true when the chosen audio has to be re-encoded rather
	// than copied.
	TranscodeAudio bool

	// Dropped describes what was left behind, for the log. Silently discarding
	// a commentary track is the kind of thing noticed months later.
	Dropped []string

	// Kept describes what was chosen, in the same terms. Without it "dropped
	// nothing" and "there was only ever one track" read identically, and a disc
	// that came out in stereo gives no way to tell whether a 5.1 track was
	// passed over or never existed.
	Kept []string
}

// commentaryWords mark a track as commentary rather than the feature's audio.
var commentaryWords = []string{"comment", "director", "cast", "crew", "isolated score"}

// isCommentary reports whether a stream's title marks it as commentary.
func isCommentary(s Stream) bool {
	t := strings.ToLower(s.Title)
	for _, w := range commentaryWords {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// Select chooses the streams to keep: the video, one audio track, and the
// subtitles worth having.
//
// A Blu-ray carries several audio tracks at three to four megabits each, so
// keeping them all costs more than the video. One is kept — the disc's own
// default where it has one, otherwise the first in the preferred language,
// otherwise simply the first.
//
// The language filter never empties the output. Many discs tag nothing at all,
// and a filter that removed every track from an untagged disc would produce a
// silent film and call it a success, so a filter that matches nothing is
// treated as no filter.
func Select(streams []Stream, language string) Selection {
	language = strings.ToLower(strings.TrimSpace(language))

	var sel Selection
	var audio, subs []Stream
	videoSeen := false

	for _, s := range streams {
		switch s.Kind {
		case "video":
			if !videoSeen {
				videoSeen = true
				sel.Maps = append(sel.Maps, fmt.Sprintf("0:%d", s.Index))
				sel.Kept = append(sel.Kept, describe("video", s))
			} else {
				sel.Dropped = append(sel.Dropped, fmt.Sprintf("extra video stream %d", s.Index))
			}
		case "audio":
			audio = append(audio, s)
		case "subtitle":
			subs = append(subs, s)
		}
	}

	if chosen, ok := pickAudio(audio, language); ok {
		sel.Maps = append(sel.Maps, fmt.Sprintf("0:%d", chosen.Index))
		sel.Kept = append(sel.Kept, describe("audio", chosen))
		sel.TranscodeAudio = chosen.Lossless()
		for _, s := range audio {
			if s.Index != chosen.Index {
				sel.Dropped = append(sel.Dropped, describe("audio", s))
			}
		}
	}

	kept := filterByLanguage(subs, language)
	for _, s := range kept {
		sel.Maps = append(sel.Maps, fmt.Sprintf("0:%d", s.Index))
		sel.Kept = append(sel.Kept, describe("subtitle", s))
	}
	for _, s := range subs {
		if !contains(kept, s.Index) {
			sel.Dropped = append(sel.Dropped, describe("subtitle", s))
		}
	}
	return sel
}

// pickAudio chooses the one audio track to keep.
func pickAudio(audio []Stream, language string) (Stream, bool) {
	if len(audio) == 0 {
		return Stream{}, false
	}

	// A commentary track is never the feature's audio, however it is flagged —
	// and discs do sometimes mark one as default.
	feature := make([]Stream, 0, len(audio))
	for _, s := range audio {
		if !isCommentary(s) {
			feature = append(feature, s)
		}
	}
	if len(feature) == 0 {
		feature = audio
	}

	inLang := feature
	if language != "" {
		if m := matching(feature, language); len(m) > 0 {
			inLang = m
		}
	}

	// The widest mix wins, and the disc's default flag only breaks ties.
	//
	// It was the other way round, and a disc that flags its stereo track as
	// default — which many do, for players that cannot handle more — had its
	// 5.1 track dropped. Trusting that flag means letting the disc decide
	// something it has no idea about: what this library is played back on.
	//
	// Channel count is a claim about content rather than about intent, which is
	// why it goes first. Commentary and the wrong language are already gone by
	// here, so the widest remaining track is the feature's main mix.
	best := inLang[0]
	for _, s := range inLang[1:] {
		switch {
		case s.Channels > best.Channels:
			best = s
		case s.Channels == best.Channels && s.Default && !best.Default:
			best = s
		}
	}
	return best, true
}

// filterByLanguage keeps the streams in the preferred language, or all of them
// when none says what language it is.
func filterByLanguage(streams []Stream, language string) []Stream {
	if language == "" {
		return streams
	}
	if m := matching(streams, language); len(m) > 0 {
		return m
	}
	return streams
}

// matching returns the streams tagged with the given language. Both the
// two- and three-letter forms are accepted, since discs use either.
func matching(streams []Stream, language string) []Stream {
	var out []Stream
	for _, s := range streams {
		if s.Language == "" {
			continue
		}
		if s.Language == language || strings.HasPrefix(language, s.Language) || strings.HasPrefix(s.Language, language) {
			out = append(out, s)
		}
	}
	return out
}

func contains(streams []Stream, index int) bool {
	for _, s := range streams {
		if s.Index == index {
			return true
		}
	}
	return false
}

func describe(kind string, s Stream) string {
	parts := []string{kind, s.Codec}
	if s.Channels > 2 {
		parts = append(parts, fmt.Sprintf("%d.1", s.Channels-1))
	} else if s.Channels > 0 {
		parts = append(parts, fmt.Sprintf("%dch", s.Channels))
	}
	if s.Language != "" {
		parts = append(parts, s.Language)
	}
	if s.Title != "" {
		parts = append(parts, `"`+s.Title+`"`)
	}
	return strings.Join(parts, " ")
}
