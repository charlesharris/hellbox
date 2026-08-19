package drive

import "fmt"

// What a disc's copy protection and region coding mean.
//
// The SCSI command that fetches the copyright descriptor lives in
// protection_linux.go. What the six bytes it returns imply about whether a
// given drive can read a given disc is here, because that reasoning is worth
// testing and none of it needs a drive.

// Protection is how a disc is protected, and where it may be played.
type Protection struct {
	// System is the copy protection in use: 0 none, 1 CSS or CPPM, 2 CPRM.
	System uint8

	// RegionMask has a bit set for every region the disc may *not* be played
	// in. Zero means the disc is region-free.
	RegionMask uint8
}

// Protected reports whether the disc is encrypted at all. An unprotected disc
// needs no drive region and rips from any drive.
func (p Protection) Protected() bool { return p.System != 0 }

// RegionFree reports whether the disc plays in every region.
func (p Protection) RegionFree() bool { return p.RegionMask == 0 }

// Regions lists the regions the disc may be played in.
func (p Protection) Regions() []int {
	var out []int
	for i := 0; i < 8; i++ {
		if p.RegionMask&(1<<i) == 0 {
			out = append(out, i+1)
		}
	}
	return out
}

// PlayableIn reports whether a drive in the given region state can read this
// disc's encrypted content.
//
// An unprotected disc is always readable. A protected one needs a drive that
// has a region set at all, and that region has to be one the disc allows.
func (p Protection) PlayableIn(r Region) bool {
	if !p.Protected() {
		return true
	}
	if !r.CanDecryptCSS() {
		return false
	}
	if p.RegionFree() || r.Scheme != RPC2 {
		return true
	}
	// Both masks use a set bit for "not permitted", so a region both allow is
	// one where neither has its bit set.
	return ^p.RegionMask&^r.Mask != 0
}

// String describes the disc for a log line.
func (p Protection) String() string {
	if !p.Protected() {
		return "unprotected"
	}
	system := "CSS"
	switch p.System {
	case 2:
		system = "CPRM"
	case 1:
	default:
		system = fmt.Sprintf("protection type %d", p.System)
	}
	if p.RegionFree() {
		return system + ", all regions"
	}
	return fmt.Sprintf("%s, region %v", system, p.Regions())
}

// parseCopyright decodes the copyright descriptor. Byte 4 is the protection
// system and byte 5 the region management information.
func parseCopyright(b []byte) Protection {
	if len(b) < 6 {
		return Protection{}
	}
	return Protection{System: b[4], RegionMask: b[5]}
}
