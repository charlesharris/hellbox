package drive

import "testing"

// The profile numbers below are from the MMC specification; the two marked are
// what this machine's drives actually reported.
func TestKindFromConfig(t *testing.T) {
	for _, tc := range []struct {
		profile uint16
		want    DiscKind
	}{
		{0x0040, DiscBluRay}, // BD-ROM — reported by the HL-DT-ST BD-RE BT10N
		{0x0000, DiscNone},   // no disc — reported by the empty ASUS drive
		{0x0010, DiscDVD},    // DVD-ROM
		{0x002B, DiscDVD},    // DVD+R DL
		{0x0043, DiscBluRay}, // BD-RE
		{0x0008, DiscCD},     // CD-ROM
		{0xFFFF, DiscUnknown},
	} {
		b := []byte{0, 0, 0, 0, 0, 0, byte(tc.profile >> 8), byte(tc.profile)}
		got, p, err := kindFromConfig(b)
		if err != nil {
			t.Errorf("profile 0x%04x: %v", tc.profile, err)
			continue
		}
		if got != tc.want || p != tc.profile {
			t.Errorf("profile 0x%04x = %q/0x%04x, want %q", tc.profile, got, p, tc.want)
		}
	}
}

// A short response must not read as "no disc", which would send a loaded drive
// down the wrong path entirely.
func TestKindFromShortConfig(t *testing.T) {
	if _, _, err := kindFromConfig([]byte{0, 0, 0}); err == nil {
		t.Error("a short GET CONFIGURATION response was accepted")
	}
}
