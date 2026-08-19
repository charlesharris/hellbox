package drive

import "testing"

// Recorded from the real disc: ROMAN_HOLIDAY, CSS-protected, region 1. The
// region mask here agrees with the one in the disc's own VIDEO_TS.IFO, which is
// the cross-check that this descriptor is being read correctly at all.
func TestParseCopyrightFromRealDisc(t *testing.T) {
	got := parseCopyright([]byte{0x00, 0x06, 0x00, 0x00, 0x01, 0xfe, 0x00, 0x00})

	if got.System != 1 {
		t.Errorf("System = %d, want 1 (CSS)", got.System)
	}
	if got.RegionMask != 0xfe {
		t.Errorf("RegionMask = 0x%02x, want 0xfe", got.RegionMask)
	}
	if !got.Protected() {
		t.Error("Protected() is false for a CSS disc")
	}
	if got.RegionFree() {
		t.Error("RegionFree() is true for a region 1 disc")
	}
	if r := got.Regions(); len(r) != 1 || r[0] != 1 {
		t.Errorf("Regions() = %v, want [1]", r)
	}
}

// The decision this whole path turns on: which discs can be ripped straight
// from the drive, and which have to be decrypted to disk first.
func TestPlayableIn(t *testing.T) {
	var (
		noRegion  = Region{Scheme: RPC2, TypeCode: 0, Mask: 0xff, UserChanges: 5}
		region1   = Region{Scheme: RPC2, TypeCode: 1, Mask: 0xfe, UserChanges: 4}
		region2   = Region{Scheme: RPC2, TypeCode: 1, Mask: 0xfd, UserChanges: 4}
		rpc1Drive = Region{Scheme: RPC1, TypeCode: 0, Mask: 0xff}

		cssRegion1  = Protection{System: 1, RegionMask: 0xfe}
		cssRegion2  = Protection{System: 1, RegionMask: 0xfd}
		cssAnywhere = Protection{System: 1, RegionMask: 0x00}
		unprotected = Protection{System: 0, RegionMask: 0x00}
	)

	tests := []struct {
		name  string
		disc  Protection
		drive Region
		want  bool
	}{
		// The case that started all of this.
		{"CSS region 1 in a drive with no region", cssRegion1, noRegion, false},
		{"CSS region 2 in a drive with no region", cssRegion2, noRegion, false},

		{"CSS region 1 in a region 1 drive", cssRegion1, region1, true},
		{"CSS region 2 in a region 2 drive", cssRegion2, region2, true},
		{"CSS region 1 in a region 2 drive", cssRegion1, region2, false},

		// A region-free disc needs the drive to have *a* region, but not any
		// particular one — the firmware still refuses to authenticate without.
		{"region-free CSS in a region 2 drive", cssAnywhere, region2, true},
		{"region-free CSS in a drive with no region", cssAnywhere, noRegion, false},

		// Nothing to decrypt, so nothing to match.
		{"unprotected disc in a drive with no region", unprotected, noRegion, true},

		// An RPC-1 drive enforces nothing.
		{"CSS region 1 in an RPC-1 drive", cssRegion1, rpc1Drive, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.disc.PlayableIn(tc.drive); got != tc.want {
				t.Errorf("PlayableIn() = %v, want %v (disc %v, drive %v)",
					got, tc.want, tc.disc, tc.drive)
			}
		})
	}
}

// A truncated response must not read as an unprotected disc, which would send a
// CSS disc down a rip that hangs.
func TestParseCopyrightRejectsShortResponse(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {0x00, 0x06, 0x00, 0x00, 0x01}} {
		if got := parseCopyright(b); got != (Protection{}) {
			t.Errorf("parseCopyright(%v) = %+v, want the zero Protection", b, got)
		}
	}
}
