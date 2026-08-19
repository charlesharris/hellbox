package drive

import "testing"

// The bytes below are what the ASUS SDRW-08D2S-U actually returned, recorded so
// the decode cannot drift. Getting this wrong is expensive in a way most
// parsing bugs are not: reading "region set" from a drive that has none sends a
// disc down a rip that hangs instead of the decrypt path that works.
func TestParseRPCStateFromRealDrive(t *testing.T) {
	// REPORT KEY, key format 08h, from a drive that has never had a region set.
	got := parseRPCState([]byte{0x00, 0x06, 0x00, 0x00, 0x25, 0xff, 0x01, 0x00})

	if got.Scheme != RPC2 {
		t.Errorf("Scheme = %v, want RPC2", got.Scheme)
	}
	if got.TypeCode != 0 {
		t.Errorf("TypeCode = %d, want 0 (no region set)", got.TypeCode)
	}
	if got.Mask != 0xff {
		t.Errorf("Mask = 0x%02x, want 0xff", got.Mask)
	}
	if got.UserChanges != 5 || got.VendorResets != 4 {
		t.Errorf("changes = %d user / %d vendor, want 5 / 4", got.UserChanges, got.VendorResets)
	}
	if got.IsSet() {
		t.Error("IsSet() is true for a drive with no region set")
	}
	if got.CanDecryptCSS() {
		t.Error("CanDecryptCSS() is true for an RPC-2 drive with no region; " +
			"this is the check that keeps a disc off a rip that would hang")
	}
}

func TestRegionInterpretation(t *testing.T) {
	tests := []struct {
		name       string
		region     Region
		set        bool
		canDecrypt bool
		regions    []int
	}{
		{
			name:       "RPC-2 set to region 1",
			region:     Region{Scheme: RPC2, TypeCode: 1, Mask: 0xfe, UserChanges: 4},
			set:        true,
			canDecrypt: true,
			regions:    []int{1},
		},
		{
			name:       "RPC-2 locked to region 2",
			region:     Region{Scheme: RPC2, TypeCode: 3, Mask: 0xfd, UserChanges: 0},
			set:        true,
			canDecrypt: true,
			regions:    []int{2},
		},
		{
			// An RPC-1 drive enforces nothing, so an unset region restricts it
			// no more than a set one does.
			name:       "RPC-1 with no region",
			region:     Region{Scheme: RPC1, TypeCode: 0, Mask: 0xff},
			set:        false,
			canDecrypt: true,
		},
		{
			name:       "RPC-2 with no region",
			region:     Region{Scheme: RPC2, TypeCode: 0, Mask: 0xff, UserChanges: 5},
			set:        false,
			canDecrypt: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.region.IsSet(); got != tc.set {
				t.Errorf("IsSet() = %v, want %v", got, tc.set)
			}
			if got := tc.region.CanDecryptCSS(); got != tc.canDecrypt {
				t.Errorf("CanDecryptCSS() = %v, want %v", got, tc.canDecrypt)
			}
			got := tc.region.Regions()
			if len(got) != len(tc.regions) {
				t.Fatalf("Regions() = %v, want %v", got, tc.regions)
			}
			for i := range got {
				if got[i] != tc.regions[i] {
					t.Errorf("Regions() = %v, want %v", got, tc.regions)
				}
			}
		})
	}
}

// A short or empty response must not be read as a drive with no region, which
// would send every disc down the decrypt path.
func TestParseRPCStateRejectsShortResponse(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0x00, 0x06, 0x00}} {
		if got := parseRPCState(b); got != (Region{}) {
			t.Errorf("parseRPCState(%v) = %+v, want the zero Region", b, got)
		}
	}
}
