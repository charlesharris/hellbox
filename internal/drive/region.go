package drive

import "fmt"

// A drive's DVD region state, and what it allows.
//
// REPORT KEY itself is in region_linux.go. The eight bytes it returns pack
// five fields, and whether those fields add up to "this drive can authenticate
// CSS" is the question the whole check exists to answer — so it is decoded and
// interpreted here, where it can be tested against captured bytes.

// RPCScheme is whether the drive enforces region coding in firmware.
type RPCScheme uint8

const (
	// RPC1 drives do not enforce regions. Nothing here restricts them.
	RPC1 RPCScheme = 0
	// RPC2 drives enforce regions in firmware, and will refuse CSS
	// authentication until a region has been set.
	RPC2 RPCScheme = 1
)

// Region is a drive's region state.
type Region struct {
	// Scheme is RPC1 or RPC2. Only RPC2 drives restrict anything.
	Scheme RPCScheme

	// TypeCode is the drive's setting state: 0 none, 1 set, 2 set with one
	// change left, 3 set permanently.
	TypeCode uint8

	// Mask has a bit set for every region the drive will *not* play. 0xff means
	// no region has been chosen and nothing is playable.
	Mask uint8

	// UserChanges is how many times the region may still be changed, and
	// VendorResets how many resets a vendor tool could still perform. An RPC2
	// drive locks to its last region when UserChanges reaches zero.
	UserChanges  uint8
	VendorResets uint8
}

// IsSet reports whether a region has been chosen.
func (r Region) IsSet() bool { return r.TypeCode != 0 && r.Mask != 0xff }

// Regions lists the regions the drive will play, empty when none is set.
func (r Region) Regions() []int {
	if !r.IsSet() {
		return nil
	}
	var out []int
	for i := 0; i < 8; i++ {
		if r.Mask&(1<<i) == 0 {
			out = append(out, i+1)
		}
	}
	return out
}

// CanDecryptCSS reports whether the drive is able to authenticate a CSS-
// protected disc at all.
//
// An RPC2 drive with no region set cannot, and that is the whole point of
// reading this: the condition is invisible until a rip hangs.
func (r Region) CanDecryptCSS() bool {
	return r.Scheme != RPC2 || r.IsSet()
}

// String describes the region state for a log line or a health check.
func (r Region) String() string {
	scheme := "RPC-1"
	if r.Scheme == RPC2 {
		scheme = "RPC-2"
	}
	if !r.IsSet() {
		return fmt.Sprintf("%s, no region set (%d changes available)", scheme, r.UserChanges)
	}
	return fmt.Sprintf("%s, region %v (%d changes left)", scheme, r.Regions(), r.UserChanges)
}

// parseRPCState decodes the eight-byte RPC state REPORT KEY returns.
//
// Byte 4 packs three fields: the type code in the top two bits, then vendor
// resets, then user changes in the low three. Bytes 5 and 6 are the region mask
// and the RPC scheme.
func parseRPCState(b []byte) Region {
	if len(b) < 7 {
		return Region{}
	}
	return Region{
		TypeCode:     (b[4] >> 6) & 0x03,
		VendorResets: (b[4] >> 3) & 0x07,
		UserChanges:  b[4] & 0x07,
		Mask:         b[5],
		Scheme:       RPCScheme(b[6]),
	}
}
