package drive

import "testing"

func TestStatusFromSense(t *testing.T) {
	tests := []struct {
		name string
		in   senseResult
		want Status
	}{
		{"good status means a readable disc", senseResult{good: true}, StatusDiscOK},

		// Observed on the ASUS SDRW-08D2S-U with a Blu-ray loaded, while
		// CDROM_DRIVE_STATUS was simultaneously reporting CDS_DISC_OK.
		{"incompatible medium", senseResult{senseKey: 0x02, asc: 0x30, ascq: 0x00}, StatusIncompatible},
		{"unknown format", senseResult{senseKey: 0x02, asc: 0x30, ascq: 0x01}, StatusIncompatible},

		{"medium not present", senseResult{senseKey: 0x02, asc: 0x3a, ascq: 0x00}, StatusNoDisc},
		{"medium not present, tray closed", senseResult{senseKey: 0x02, asc: 0x3a, ascq: 0x01}, StatusNoDisc},
		{"medium not present, tray open", senseResult{senseKey: 0x02, asc: 0x3a, ascq: 0x02}, StatusTrayOpen},

		{"becoming ready", senseResult{senseKey: 0x02, asc: 0x04, ascq: 0x01}, StatusNotReady},
		{"initialising command required", senseResult{senseKey: 0x02, asc: 0x04, ascq: 0x02}, StatusNotReady},

		// Not-ready sense that says nothing about the medium must not be read
		// as an assertion either way; the poller simply looks again.
		{"unrecognised not-ready sense", senseResult{senseKey: 0x02, asc: 0x00, ascq: 0x00}, StatusNotReady},

		// A scratched disc reports MEDIUM ERROR, which is emphatically not an
		// absent disc: the disc is there and should be attempted.
		{"medium error is not absence", senseResult{senseKey: 0x03, asc: 0x11, ascq: 0x00}, StatusUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusFromSense(tt.in); got != tt.want {
				t.Errorf("statusFromSense(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSense(t *testing.T) {
	// Fixed format (response code 0x70), as returned by the ASUS drive.
	fixed := make([]byte, 18)
	fixed[0] = 0x70
	fixed[2] = 0x02 // NOT READY
	fixed[12] = 0x30
	fixed[13] = 0x00

	got := parseSense(fixed)
	if got.senseKey != 0x02 || got.asc != 0x30 || got.ascq != 0x00 {
		t.Errorf("fixed format: got key=%#x asc=%#x ascq=%#x, want 0x2/0x30/0x00",
			got.senseKey, got.asc, got.ascq)
	}

	// Descriptor format (response code 0x72) puts them in different places.
	desc := []byte{0x72, 0x02, 0x3a, 0x02, 0, 0, 0, 0}
	got = parseSense(desc)
	if got.senseKey != 0x02 || got.asc != 0x3a || got.ascq != 0x02 {
		t.Errorf("descriptor format: got key=%#x asc=%#x ascq=%#x, want 0x2/0x3a/0x02",
			got.senseKey, got.asc, got.ascq)
	}
	if s := statusFromSense(got); s != StatusTrayOpen {
		t.Errorf("descriptor sense mapped to %v, want tray open", s)
	}
}

// Truncated or absent sense data must not panic or be read as meaningful.
func TestParseSenseShortBuffers(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0x70}, {0x70, 0x00}, {0x72, 0x02}} {
		got := parseSense(b)
		if got.good {
			t.Errorf("parseSense(%v) reported good status", b)
		}
	}
}

func TestStatusStrings(t *testing.T) {
	if StatusIncompatible.String() != "unreadable disc" {
		t.Errorf("StatusIncompatible renders as %q", StatusIncompatible.String())
	}
	// Every status must render as something a person can read in the TUI.
	for s := StatusUnknown; s <= StatusIncompatible; s++ {
		if s.String() == "" {
			t.Errorf("status %d has no string", s)
		}
	}
}
