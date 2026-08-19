package drive

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The drive's DVD region state, read with SCSI REPORT KEY.
//
// This matters because a drive that has never had a region set cannot decrypt
// CSS at all. The failure is silent and misleading: the disc's IFO structures
// are not encrypted, so it scans perfectly and reports every title, and then
// the rip stops partway through the first one — MakeMKV retries internally
// without reading anything and never returns. One such rip sat for seventeen
// hours before this check existed.
//
// It is read with REPORT KEY rather than the kernel's DVD_AUTH ioctl. On the
// ASUS SDRW-08D2S-U the ioctl path returns a response that decodes to a
// plausible but wrong answer — a region that looks set when none is — while
// REPORT KEY is exact.
const (
	sgDxferFromDev = -3 // SG_DXFER_FROM_DEV

	scsiReportKey = 0xa4

	// keyFormatRPCState asks for the drive's own region state rather than any
	// key belonging to the disc.
	keyFormatRPCState = 0x08

	rpcStateLen = 8
)

// ReadRegion reports the drive's region state.
func ReadRegion(path string) (Region, error) {
	fd, err := openDevice(path)
	if err != nil {
		return Region{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	// REPORT KEY, key class 0, key format 08h, allocation length 8.
	cdb := [12]byte{scsiReportKey, 0, 0, 0, 0, 0, 0, 0, 0, rpcStateLen, keyFormatRPCState, 0}
	data := [rpcStateLen]byte{}
	sense := [senseBufLen]byte{}

	hdr := sgIOHdr{
		interfaceID:    sgInterfaceIDOrig,
		dxferDirection: sgDxferFromDev,
		cmdLen:         uint8(len(cdb)),
		mxSbLen:        senseBufLen,
		dxferLen:       rpcStateLen,
		dxferp:         uintptr(unsafe.Pointer(&data[0])),
		cmdp:           uintptr(unsafe.Pointer(&cdb[0])),
		sbp:            uintptr(unsafe.Pointer(&sense[0])),
		timeout:        scsiTimeoutMs,
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), sgIO, uintptr(unsafe.Pointer(&hdr)))

	// The kernel reads and writes through raw pointers the garbage collector
	// cannot see. Keep every buffer alive until the ioctl has returned.
	runtime.KeepAlive(&cdb)
	runtime.KeepAlive(&data)
	runtime.KeepAlive(&sense)

	if errno != 0 {
		return Region{}, fmt.Errorf("REPORT KEY on %s: %w", path, errno)
	}
	if hdr.status != 0 {
		return Region{}, fmt.Errorf("REPORT KEY on %s: scsi status 0x%02x", path, hdr.status)
	}
	return parseRPCState(data[:]), nil
}
