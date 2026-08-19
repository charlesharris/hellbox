// Package makemkv drives makemkvcon and parses its robot-mode output.
package makemkv

import (
	"fmt"
	"strconv"
	"strings"
)

// Attribute codes used by TINFO, SINFO and CINFO lines, from MakeMKV's
// apdefs.h (ap_ItemAttributeId).
//
// These are NOT authoritative. MakeMKV documents them only informally and they
// have shifted between releases. Every parsed record therefore also retains the
// complete raw code-to-value map (see Attrs), and the verbatim command output
// is written alongside each rip, so a wrong constant here can be corrected
// later without going back to the disc.
const (
	attrType             = 1
	attrName             = 2
	attrLangCode         = 3
	attrLangName         = 4
	attrCodecID          = 5
	attrCodecShort       = 6
	attrCodecLong        = 7
	attrChapterCount     = 8
	attrDuration         = 9
	attrDiskSize         = 10
	attrDiskSizeBytes    = 11
	attrBitrate          = 13
	attrAudioChannels    = 14
	attrSourceFileName   = 16
	attrAudioSampleRate  = 17
	attrVideoSize        = 19
	attrVideoAspectRatio = 20
	attrVideoFrameRate   = 21
	attrStreamFlags      = 22
	attrSegmentsCount    = 25
	attrSegmentsMap      = 26
	attrOutputFileName   = 27
	attrVolumeName       = 32
	attrMkvFlags         = 38
)

// Line is one parsed line of robot output. Kind is the prefix ("MSG", "TINFO",
// "PRGV", …) and Fields are the comma-separated values after it, unquoted.
type Line struct {
	Kind   string
	Fields []string
	Raw    string
}

// ParseLine splits a robot-mode line into its kind and fields. It returns
// ok=false for blank lines and anything without a recognised "KIND:" prefix,
// which makemkvcon emits occasionally around errors.
func ParseLine(raw string) (Line, bool) {
	s := strings.TrimRight(raw, "\r\n")
	if s == "" {
		return Line{}, false
	}
	idx := strings.IndexByte(s, ':')
	if idx <= 0 {
		return Line{}, false
	}
	kind := s[:idx]
	for _, r := range kind {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return Line{}, false
		}
	}
	return Line{Kind: kind, Fields: splitFields(s[idx+1:]), Raw: s}, true
}

// splitFields splits a robot-mode value list on commas, respecting double
// quotes. MakeMKV escapes an embedded quote by doubling it; some builds emit a
// backslash escape instead, so both are accepted.
func splitFields(s string) []string {
	var (
		fields []string
		buf    strings.Builder
		inQ    bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			if inQ && i+1 < len(s) && s[i+1] == '"' {
				buf.WriteByte('"')
				i++
				continue
			}
			inQ = !inQ
		case c == '\\' && inQ && i+1 < len(s) && s[i+1] == '"':
			buf.WriteByte('"')
			i++
		case c == ',' && !inQ:
			fields = append(fields, buf.String())
			buf.Reset()
		default:
			buf.WriteByte(c)
		}
	}
	fields = append(fields, buf.String())
	return fields
}

// field returns the nth field, or "" when the line is shorter than expected.
// Robot output field counts vary between MakeMKV versions, so every access is
// bounds-tolerant by design.
func (l Line) field(n int) string {
	if n < 0 || n >= len(l.Fields) {
		return ""
	}
	return l.Fields[n]
}

func (l Line) intField(n int) int {
	v, err := strconv.Atoi(strings.TrimSpace(l.field(n)))
	if err != nil {
		return 0
	}
	return v
}

// ParseDuration converts MakeMKV's "H:MM:SS" duration to seconds. It also
// accepts "MM:SS", and returns 0 for anything it cannot read.
func ParseDuration(s string) int {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	var total int
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0
		}
		total = total*60 + v
	}
	return total
}

// parseBytes reads an integer byte count, tolerating separators that some
// MakeMKV builds insert into large numbers.
func parseBytes(s string) int64 {
	clean := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if clean == "" {
		return 0
	}
	v, err := strconv.ParseInt(clean, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// Message is a MSG line: makemkvcon's human-readable log and error stream.
type Message struct {
	Code  int
	Flags int
	Text  string
}

func parseMessage(l Line) Message {
	return Message{Code: l.intField(0), Flags: l.intField(1), Text: l.field(3)}
}

// Progress is the current state of a long-running operation.
type Progress struct {
	Current   string // PRGC: the operation in flight, e.g. "Saving title 3"
	Total     string // PRGT: the overall operation
	Value     int    // PRGV current
	TotalVal  int    // PRGV total
	Max       int    // PRGV maximum, typically 65536
	HasValues bool
}

// Fraction returns overall completion in [0,1], or 0 when unknown.
func (p Progress) Fraction() float64 {
	if !p.HasValues || p.Max <= 0 {
		return 0
	}
	f := float64(p.TotalVal) / float64(p.Max)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// DriveLine is a DRV record describing one drive slot as MakeMKV sees it.
type DriveLine struct {
	Index      int
	Visible    int
	Enabled    int
	Flags      int
	DriveName  string
	DiscName   string
	DevicePath string
}

func parseDriveLine(l Line) DriveLine {
	return DriveLine{
		Index:      l.intField(0),
		Visible:    l.intField(1),
		Enabled:    l.intField(2),
		Flags:      l.intField(3),
		DriveName:  l.field(4),
		DiscName:   l.field(5),
		DevicePath: l.field(6),
	}
}

// attrString reads a known attribute from a raw attribute map.
func attrString(attrs map[int]string, code int) string { return attrs[code] }

func attrInt(attrs map[int]string, code int) int {
	v, err := strconv.Atoi(strings.TrimSpace(attrs[code]))
	if err != nil {
		return 0
	}
	return v
}

// describeAttrs renders an attribute map for a human reading a log, sorted so
// output is stable.
func describeAttrs(attrs map[int]string) string {
	if len(attrs) == 0 {
		return "(none)"
	}
	codes := make([]int, 0, len(attrs))
	for c := range attrs {
		codes = append(codes, c)
	}
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && codes[j] < codes[j-1]; j-- {
			codes[j], codes[j-1] = codes[j-1], codes[j]
		}
	}
	var b strings.Builder
	for i, c := range codes {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%d=%q", c, attrs[c])
	}
	return b.String()
}
