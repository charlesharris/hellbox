package drive

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Linux CD-ROM ioctl requests, from <linux/cdrom.h>.
const (
	cdromEject       = 0x5309
	cdromCloseTray   = 0x5319
	cdromDriveStatus = 0x5326
	cdromLockDoor    = 0x5329

	// CDSL_CURRENT: address the currently selected slot on a changer. Plain
	// single-tray drives ignore it.
	cdslCurrent = 0x7fffffff
)

// Raw CDROM_DRIVE_STATUS return values, from <linux/cdrom.h>.
const (
	cdsNoInfo        = 0
	cdsNoDisc        = 1
	cdsTrayOpen      = 2
	cdsDriveNotReady = 3
	cdsDiscOK        = 4
)

// openDevice opens the drive without blocking. O_NONBLOCK matters: opening an
// optical device that holds no disc otherwise blocks until one is inserted.
func openDevice(path string) (int, error) {
	return unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
}

// queryStatus reads the drive's current tray and disc state.
//
// SCSI TEST UNIT READY is asked first, because CDROM_DRIVE_STATUS is not
// reliable: the ASUS SDRW-08D2S-U answers CDS_DISC_OK when it holds no readable
// medium at all. See scsi_linux.go for the detail. The ioctl remains as a
// fallback for a device that refuses SG_IO, where an answer that is sometimes
// wrong still beats no answer.
func queryStatus(path string) (Status, error) {
	if r, err := testUnitReady(path); err == nil {
		return statusFromSense(r), nil
	}
	return queryStatusIoctl(path)
}

// queryStatusIoctl reads the state through CDROM_DRIVE_STATUS.
//
// Unusually for an ioctl, CDROM_DRIVE_STATUS reports its answer through the
// syscall's return value rather than through a pointer argument, so this cannot
// use unix.IoctlGetInt.
func queryStatusIoctl(path string) (Status, error) {
	fd, err := openDevice(path)
	if err != nil {
		return StatusUnknown, fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), cdromDriveStatus, cdslCurrent)
	if errno != 0 {
		return StatusUnknown, fmt.Errorf("CDROM_DRIVE_STATUS on %s: %w", path, errno)
	}

	switch int(r) {
	case cdsNoDisc:
		return StatusNoDisc, nil
	case cdsTrayOpen:
		return StatusTrayOpen, nil
	case cdsDriveNotReady:
		return StatusNotReady, nil
	case cdsDiscOK:
		return StatusDiscOK, nil
	case cdsNoInfo:
		return StatusUnknown, nil
	default:
		return StatusUnknown, nil
	}
}

// eject unlocks the door and opens the tray. The unlock is not optional:
// MakeMKV and other readers leave the door locked, and CDROMEJECT fails with
// EBUSY against a locked door.
func eject(path string) error {
	fd, err := openDevice(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	// Best-effort: a drive that does not support locking still ejects fine.
	_, _, _ = unix.Syscall(unix.SYS_IOCTL, uintptr(fd), cdromLockDoor, 0)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), cdromEject, 0); errno != 0 {
		return fmt.Errorf("CDROMEJECT on %s: %w", path, errno)
	}
	return nil
}

// closeTray pulls the tray back in. Slot-loading and slim USB drives commonly
// refuse this in hardware; callers should treat failure as informational.
func closeTray(path string) error {
	fd, err := openDevice(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(fd)

	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), cdromCloseTray, 0); errno != 0 {
		return fmt.Errorf("CDROMCLOSETRAY on %s: %w", path, errno)
	}
	return nil
}
