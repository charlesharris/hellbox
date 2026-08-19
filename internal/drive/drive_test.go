package drive

import "testing"

// Whether a drive reads Blu-ray decides whether a missing MakeMKV key is a
// problem: a key gates Blu-ray and nothing else. Both of these are drives that
// have actually been plugged into this machine.
func TestReadsBluRay(t *testing.T) {
	for _, tc := range []struct {
		vendor, model string
		want          bool
	}{
		{"HL-DT-ST", "BD-RE BT10N", true},
		{"ASUS", "SDRW-08D2S-U", false},
		{"PIONEER", "BD-RW BDR-212M", true},
		{"LG", "UHD BluRay Drive", true},
		{"TSSTcorp", "CDDVDW SH-224DB", false},
		{"", "", false},
	} {
		d := Drive{Vendor: tc.vendor, Model: tc.model}
		if got := d.ReadsBluRay(); got != tc.want {
			t.Errorf("ReadsBluRay(%q %q) = %v, want %v", tc.vendor, tc.model, got, tc.want)
		}
	}
}
