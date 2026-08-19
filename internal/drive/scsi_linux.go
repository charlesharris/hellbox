package drive

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SCSI TEST UNIT READY, issued through the SG_IO ioctl.
//
// This exists because CDROM_DRIVE_STATUS cannot be trusted. On the ASUS
// SDRW-08D2S-U it returns CDS_DISC_OK for a drive holding no readable medium at
// all, with the tray open or closed — the drive lies and the kernel relays it
// faithfully. Detection built on that alone has the daemon scan an empty drive
// on every startup and land in FAILED for a disc that was never there.
//
// TEST UNIT READY asks the drive directly and answers in sense data, which
// distinguishes cases the ioctl collapses together: no medium, tray open, a
// disc still spinning up, and — the one that prompted this — a disc that is
// physically present but of a type this drive cannot read.
const (
	sgIO = 0x2285 // SG_IO

	sgDxferNone = -1 // SG_DXFER_NONE: no data transferred

	sgInterfaceIDOrig = 'S'

	// senseBufLen is generous: 18 bytes carry fixed-format sense, and drives
	// may return descriptor-format sense that is longer.
	senseBufLen = 32

	// scsiTimeoutMs bounds a command that would otherwise hang on a drive that
	// has stopped responding. A poll must never block a worker.
	scsiTimeoutMs = 5000
)

// sgIOHdr mirrors struct sg_io_hdr from <scsi/sg.h>. Field order and padding
// must match the kernel's layout exactly.
type sgIOHdr struct {
	interfaceID    int32
	dxferDirection int32
	cmdLen         uint8
	mxSbLen        uint8
	iovecCount     uint16
	dxferLen       uint32
	dxferp         uintptr
	cmdp           uintptr
	sbp            uintptr
	timeout        uint32
	flags          uint32
	packID         int32
	_              [4]byte // padding to align usrPtr
	usrPtr         uintptr
	status         uint8
	maskedStatus   uint8
	msgStatus      uint8
	sbLenWr        uint8
	hostStatus     uint16
	driverStatus   uint16
	resid          int32
	duration       uint32
	info           uint32
}

// testUnitReady issues TEST UNIT READY and returns the drive's answer.
func testUnitReady(path string) (senseResult, error) {
	fd, err := openDevice(path)
	if err != nil {
		return senseResult{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	cdb := [6]byte{0, 0, 0, 0, 0, 0} // TEST UNIT READY
	sense := [senseBufLen]byte{}

	hdr := sgIOHdr{
		interfaceID:    sgInterfaceIDOrig,
		dxferDirection: sgDxferNone,
		cmdLen:         uint8(len(cdb)),
		mxSbLen:        senseBufLen,
		cmdp:           uintptr(unsafe.Pointer(&cdb[0])),
		sbp:            uintptr(unsafe.Pointer(&sense[0])),
		timeout:        scsiTimeoutMs,
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), sgIO, uintptr(unsafe.Pointer(&hdr)))

	// The kernel reads and writes through the raw pointers held in hdr, which
	// the garbage collector cannot see. Keep the buffers alive until the ioctl
	// has returned.
	runtime.KeepAlive(&cdb)
	runtime.KeepAlive(&sense)

	if errno != 0 {
		return senseResult{}, fmt.Errorf("SG_IO on %s: %w", path, errno)
	}

	if hdr.status == 0 && hdr.hostStatus == 0 && (hdr.driverStatus&0x0f) == 0 {
		return senseResult{good: true}, nil
	}
	if hdr.sbLenWr < 14 {
		// A failure with no usable sense data. Reported as such rather than
		// guessed at, so the caller can fall back.
		return senseResult{}, fmt.Errorf("SG_IO on %s: status 0x%02x with no sense data", path, hdr.status)
	}
	return parseSense(sense[:hdr.sbLenWr]), nil
}
