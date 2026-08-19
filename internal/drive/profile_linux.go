package drive

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The kind of disc in the drive, read with SCSI GET CONFIGURATION.
//
// Asked of the drive rather than inferred from what is on the disc, because
// everything downstream differs: a DVD is decrypted to disk with dvdbackup and
// read as VIDEO_TS, a Blu-ray is decrypted in place by libaacs and read as
// playlists. Guessing by looking for a VIDEO_TS directory would mean mounting
// the disc first, which needs privileges this daemon does not have.
const (
	scsiGetConfiguration = 0x46

	// rtCurrentProfile asks only for the profile in force, which is the one
	// question here.
	rtCurrentProfile = 0x01

	configLen = 8
)

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

// ReadDiscKind reports what kind of disc is loaded.
func ReadDiscKind(path string) (DiscKind, uint16, error) {
	fd, err := openDevice(path)
	if err != nil {
		return DiscUnknown, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	cdb := [10]byte{scsiGetConfiguration, rtCurrentProfile, 0, 0, 0, 0, 0, 0, configLen, 0}
	data := [configLen]byte{}
	sense := [senseBufLen]byte{}

	hdr := sgIOHdr{
		interfaceID:    sgInterfaceIDOrig,
		dxferDirection: sgDxferFromDev,
		cmdLen:         uint8(len(cdb)),
		mxSbLen:        senseBufLen,
		dxferLen:       configLen,
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
		return DiscUnknown, 0, fmt.Errorf("GET CONFIGURATION on %s: %w", path, errno)
	}
	if hdr.status != 0 {
		return DiscUnknown, 0, fmt.Errorf("GET CONFIGURATION on %s: scsi status 0x%02x", path, hdr.status)
	}
	return kindFromConfig(data[:])
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
