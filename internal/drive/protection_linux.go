package drive

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The disc's copy protection and region coding, read with SCSI READ DISC
// STRUCTURE.
//
// Both answers come from one command, and both are needed before a rip: a disc
// with no protection rips from any drive, while a CSS-protected one needs a
// drive whose region it matches. Asking the disc directly avoids reading its
// filesystem, and avoids a thirty-five minute decrypt of a disc that never
// needed decrypting.
const (
	scsiReadDiscStructure = 0xad

	// formatCopyright asks for the copyright and region fields rather than the
	// physical layer description.
	formatCopyright = 0x01

	copyrightLen = 8
)

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

// ReadProtection reports how the disc in the drive is protected.
func ReadProtection(path string) (Protection, error) {
	fd, err := openDevice(path)
	if err != nil {
		return Protection{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	// READ DISC STRUCTURE, media type DVD, layer 0, format 01h.
	cdb := [12]byte{scsiReadDiscStructure, 0, 0, 0, 0, 0, 0, formatCopyright, 0, copyrightLen, 0, 0}
	data := [copyrightLen]byte{}
	sense := [senseBufLen]byte{}

	hdr := sgIOHdr{
		interfaceID:    sgInterfaceIDOrig,
		dxferDirection: sgDxferFromDev,
		cmdLen:         uint8(len(cdb)),
		mxSbLen:        senseBufLen,
		dxferLen:       copyrightLen,
		dxferp:         uintptr(unsafe.Pointer(&data[0])),
		cmdp:           uintptr(unsafe.Pointer(&cdb[0])),
		sbp:            uintptr(unsafe.Pointer(&sense[0])),
		timeout:        scsiTimeoutMs,
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), sgIO, uintptr(unsafe.Pointer(&hdr)))

	runtime.KeepAlive(&cdb)
	runtime.KeepAlive(&data)
	runtime.KeepAlive(&sense)

	if errno != 0 {
		return Protection{}, fmt.Errorf("READ DISC STRUCTURE on %s: %w", path, errno)
	}
	if hdr.status != 0 {
		return Protection{}, fmt.Errorf("READ DISC STRUCTURE on %s: scsi status 0x%02x", path, hdr.status)
	}
	return parseCopyright(data[:]), nil
}

// parseCopyright decodes the copyright descriptor. Byte 4 is the protection
// system and byte 5 the region management information.
func parseCopyright(b []byte) Protection {
	if len(b) < 6 {
		return Protection{}
	}
	return Protection{System: b[4], RegionMask: b[5]}
}
