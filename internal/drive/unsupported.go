//go:build !linux

package drive

// Everything in this package that talks to a drive does so through Linux
// ioctls — CDROM_DRIVE_STATUS for the tray, SG_IO for the SCSI commands that
// report region, protection and disc kind. There is no portable equivalent and
// hellboxd is only ever deployed on Linux.
//
// The stubs exist so the rest of the tree compiles and tests anywhere. That is
// not a small thing: the parsers these calls feed — sense.go, region.go,
// protection.go, profile.go — hold most of the judgement in this package, and
// before this file they could not be run on a laptop because they shared a
// package with the syscalls.
//
// Each returns an error naming what is missing rather than a plausible zero
// value. §2.1 of the design is about hardware lying and the kernel relaying the
// lie; a stub quietly answering "no disc" would be this package telling the
// same kind of lie about itself.

import (
	"fmt"
	"runtime"
)

func unsupported(op string) error {
	return fmt.Errorf("%s: optical drives are only supported on linux, not %s", op, runtime.GOOS)
}

func queryStatus(path string) (Status, error) {
	return StatusUnknown, unsupported("read drive status of " + path)
}

func eject(path string) error { return unsupported("eject " + path) }

func closeTray(path string) error { return unsupported("close tray of " + path) }

// ReadProtection reports how the disc in the drive is protected.
func ReadProtection(path string) (Protection, error) {
	return Protection{}, unsupported("read disc protection of " + path)
}

// ReadRegion reports the drive's region state.
func ReadRegion(path string) (Region, error) {
	return Region{}, unsupported("read region state of " + path)
}

// ReadDiscKind reports what kind of disc is loaded.
func ReadDiscKind(path string) (DiscKind, uint16, error) {
	return DiscUnknown, 0, unsupported("read disc kind of " + path)
}
