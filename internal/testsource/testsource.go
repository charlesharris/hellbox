// Package testsource resolves the disc a hardware test should read.
//
// The tests that touch real media are skipped unless an environment variable
// names something to read. That used to be spelled HELLBOX_DVD_DEVICE, which
// was both a lie and a limitation: none of the code underneath requires a
// device. libdvdread takes a device path, a directory holding VIDEO_TS, or an
// ISO, and libbluray's tools take a device, a BDMV directory or an ISO, so an
// image on disk exercises the same enumeration and extraction as a disc in a
// drive.
//
// That matters because the slow, fragile part of testing this system is the
// optical drive: two hours a disc, one machine that has one, and a result that
// changes if somebody swaps the disc. An image is none of those things. It is
// repeatable, it is fast, and the reasoning it exercises — playlist selection,
// PAL detection, duration verification, the identification chain — is all of
// the reasoning that has ever actually been wrong.
//
// Drive-level behaviour still needs a drive: region state, tray transitions,
// TEST UNIT READY, MakeMKV. That is Linux-only and stays Linux-only.
package testsource

import (
	"os"
	"testing"
)

// Path returns the source named by env — a device, a directory or an ISO —
// and skips the test when it is unset.
//
// deprecated is the variable's former name. Finding that one set is a hard
// failure rather than a skip, because a skip is indistinguishable from a pass
// in test output: setting the old name and seeing green would read as "the
// hardware test ran" when nothing ran at all.
func Path(t *testing.T, env, deprecated string) string {
	t.Helper()

	if v := os.Getenv(env); v != "" {
		return v
	}
	if old := os.Getenv(deprecated); old != "" {
		t.Fatalf("%s is set but no longer read; it is now %s, which also accepts a directory or an ISO", deprecated, env)
	}
	t.Skipf("set %s to a device, a directory or an ISO to run this", env)
	return ""
}
