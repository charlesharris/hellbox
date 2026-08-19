package makemkv

import (
	"reflect"
	"testing"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		kind   string
		fields []string
		ok     bool
	}{
		{
			name:   "message with quoted text containing commas",
			raw:    `MSG:3007,0,2,"Title #2 has length of 29 seconds, which is less than minimum","%1","x"`,
			kind:   "MSG",
			fields: []string{"3007", "0", "2", "Title #2 has length of 29 seconds, which is less than minimum", "%1", "x"},
			ok:     true,
		},
		{
			name:   "drive line with empty trailing fields",
			raw:    `DRV:1,256,999,0,"","",""`,
			kind:   "DRV",
			fields: []string{"1", "256", "999", "0", "", "", ""},
			ok:     true,
		},
		{
			name:   "title attribute",
			raw:    `TINFO:0,9,0,"0:29:08"`,
			kind:   "TINFO",
			fields: []string{"0", "9", "0", "0:29:08"},
			ok:     true,
		},
		{
			name:   "doubled quote is an escaped quote",
			raw:    `TINFO:0,2,0,"He said ""hello"" loudly"`,
			kind:   "TINFO",
			fields: []string{"0", "2", "0", `He said "hello" loudly`},
			ok:     true,
		},
		{
			name:   "backslash escape is also accepted",
			raw:    `TINFO:0,2,0,"a \"b\" c"`,
			kind:   "TINFO",
			fields: []string{"0", "2", "0", `a "b" c`},
			ok:     true,
		},
		{
			name:   "progress values",
			raw:    `PRGV:12345,30000,65536`,
			kind:   "PRGV",
			fields: []string{"12345", "30000", "65536"},
			ok:     true,
		},
		{name: "blank line is skipped", raw: "", ok: false},
		{name: "no prefix is skipped", raw: "just some text", ok: false},
		{name: "lowercase prefix is not a record", raw: "msg:1,2", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, ok := ParseLine(tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if l.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", l.Kind, tc.kind)
			}
			if !reflect.DeepEqual(l.Fields, tc.fields) {
				t.Errorf("fields = %#v, want %#v", l.Fields, tc.fields)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"0:29:08", 1748},
		{"1:23:45", 5025},
		{"2:00:00", 7200},
		{"29:08", 1748},
		{"0:00:00", 0},
		{"", 0},
		{"garbage", 0},
		{"1:2:3:4", 0},
		{"-1:00", 0},
	}
	for _, tc := range tests {
		if got := ParseDuration(tc.in); got != tc.want {
			t.Errorf("ParseDuration(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseBytes(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1046125177", 1046125177},
		{"1,046,125,177", 1046125177},
		{"", 0},
		{"none", 0},
	}
	for _, tc := range tests {
		if got := parseBytes(tc.in); got != tc.want {
			t.Errorf("parseBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestProgressFraction(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
		want float64
	}{
		{"half", Progress{TotalVal: 32768, Max: 65536, HasValues: true}, 0.5},
		{"no values yet", Progress{}, 0},
		{"zero max is not a divide by zero", Progress{TotalVal: 5, Max: 0, HasValues: true}, 0},
		{"clamped above one", Progress{TotalVal: 70000, Max: 65536, HasValues: true}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Fraction(); got != tc.want {
				t.Errorf("Fraction() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFieldAccessIsBoundsTolerant(t *testing.T) {
	// Robot output field counts vary between MakeMKV releases, so a short line
	// must not panic.
	l, ok := ParseLine("MSG:1005")
	if !ok {
		t.Fatal("expected the line to parse")
	}
	if got := l.field(9); got != "" {
		t.Errorf("field(9) = %q, want empty", got)
	}
	if got := l.intField(9); got != 0 {
		t.Errorf("intField(9) = %d, want 0", got)
	}
}

func TestVersionFromMessages(t *testing.T) {
	msgs := []Message{
		{Text: "MakeMKV v1.18.4 linux(x64-release) started"},
		{Text: "Automatic checking for updates is enabled"},
	}
	if got := versionFromMessages(msgs); got != "1.18.4" {
		t.Errorf("versionFromMessages = %q, want 1.18.4", got)
	}
	if got := versionFromMessages(nil); got != "" {
		t.Errorf("versionFromMessages(nil) = %q, want empty", got)
	}
}

// MakeMKV announces every title it analyses and then offers only some of them.
// A disc the drive cannot decrypt can be analysed in full and offered almost
// nothing: one here announced a 2:52:52 feature and offered a 3:10 trailer,
// with nothing in the structured output to say so. Silently ripping the trailer
// and recording the disc as done is the failure this guards against.
func TestAnnouncedTitlesCountsWhatMakeMKVAnalysed(t *testing.T) {
	msgs := []Message{
		{Code: 3028, Text: "Title #1 was added (32 cell(s), 2:52:52)"},
		{Code: 3025, Text: "Title #2 has length of 31 seconds ... therefore skipped"},
		{Code: 3028, Text: "Title #3 was added (2 cell(s), 0:03:10)"},
		{Code: 5011, Text: "Operation successfully completed"},
	}
	if got := announcedTitles(msgs); got != 2 {
		t.Errorf("announcedTitles() = %d, want 2", got)
	}

	// Titles below the length threshold are skipped deliberately and are not
	// evidence of anything being wrong.
	if got := announcedTitles([]Message{{Code: 3025, Text: "skipped"}}); got != 0 {
		t.Errorf("announcedTitles() counted a deliberately skipped title: %d", got)
	}
}
