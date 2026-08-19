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
