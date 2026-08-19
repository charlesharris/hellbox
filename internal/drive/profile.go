package drive

import "fmt"

// Which pipeline a disc goes down, decoded from its MMC profile number.
//
// GET CONFIGURATION is in profile_linux.go; the profile table and the lookup
// are here. The table is the part that changes as formats are added, and it is
// checkable without a disc.

// DiscKind is the family of disc in a drive.
type DiscKind string

const (
	DiscNone    DiscKind = "none"
	DiscCD      DiscKind = "cd"
	DiscDVD     DiscKind = "dvd"
	DiscBluRay  DiscKind = "bluray"
	DiscUnknown DiscKind = "unknown"
)

// profiles are the MMC profile numbers worth telling apart. The list is not
// exhaustive: what matters is which pipeline a disc goes down, so every DVD
// flavour collapses to one answer and every Blu-ray flavour to another.
var profiles = map[uint16]DiscKind{
	0x0000: DiscNone,
	0x0008: DiscCD, 0x0009: DiscCD, 0x000A: DiscCD,
	0x0010: DiscDVD, 0x0011: DiscDVD, 0x0012: DiscDVD, 0x0013: DiscDVD,
	0x0014: DiscDVD, 0x0015: DiscDVD, 0x0016: DiscDVD, 0x0017: DiscDVD,
	0x001A: DiscDVD, 0x001B: DiscDVD, 0x002A: DiscDVD, 0x002B: DiscDVD,
	0x0040: DiscBluRay, 0x0041: DiscBluRay, 0x0042: DiscBluRay,
	0x0043: DiscBluRay, 0x0050: DiscBluRay,
}

// kindFromConfig reads the current profile out of the eight-byte header.
// Bytes 6 and 7 hold it, big-endian.
func kindFromConfig(b []byte) (DiscKind, uint16, error) {
	if len(b) < 8 {
		return DiscUnknown, 0, fmt.Errorf("short GET CONFIGURATION response")
	}
	p := uint16(b[6])<<8 | uint16(b[7])
	if k, ok := profiles[p]; ok {
		return k, p, nil
	}
	return DiscUnknown, p, nil
}
