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
