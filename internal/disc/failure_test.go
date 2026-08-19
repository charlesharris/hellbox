package disc

import (
	"strings"
	"testing"
)

// Every message below is one hellbox has actually produced or will produce,
// taken from its logs. The point of grouping them is counting: "three discs
// failed" says nothing to act on, "three discs were not in the AACS key
// database" says to update the database.
func TestClassifyFailureOnRealMessages(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want FailureKind
	}{
		// The region problem, in the two shapes it actually arrives in.
		{"no titles found on dev:/dev/sr0: Error 'Scsi error - ILLEGAL REQUEST:READ OF SCRAMBLED SECTOR WITHOUT AUTHENTICATION'", FailureRegion},
		{"this disc is CSS, region [1] and the drive is RPC-2, no region set", FailureRegion},

		// Blu-ray, the two failures worth telling apart.
		{"aacs: no valid processing key for this disc", FailureAACSKey},
		{"libaacs: failed to read unit key", FailureAACSKey},
		{"BD+ handling failed", FailureBDPlus},

		{"rip title 0: no progress for 10m0s, so it was stopped", FailureStalled},
		{"scan did not finish within 45m0s", FailureStalled},
		{"write title_00.mkv: no space left on device", FailureSpace},
		{"dvdbackup not on PATH", FailureTooling},
		{"Scsi error - NOT READY:MEDIUM NOT PRESENT - TRAY OPEN", FailureUnreadable},

		// The failures this has actually produced, which the first version of
		// these patterns missed entirely — they were written for imagined
		// failures rather than observed ones.
		{"title 0: rip title 0: makemkvcon produced no output file: Failed to execute external program 'ccextractor' from location '/usr/bin/mmccextr'", FailureTooling},
		{"title 16: rip title 16: exit status 12: Operation successfully completed; Title #34 has length of 32 seconds which is less than minimum title length of 60 seconds and was therefore skipped", FailureMismatch},
		{"cancelled during title 0", FailureCancelled},
		{"decrypting the disc to disk: context canceled", FailureCancelled},

		// Not recognised, and deliberately not guessed at.
		{"could not record disc: database is locked", FailureOther},
		{"", FailureOther},
	} {
		if got := ClassifyFailure(tc.msg); got != tc.want {
			t.Errorf("ClassifyFailure(%.60q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

// The region messages contain "css" and "scrambled sector", which would also
// match a naive AACS rule. Order matters, and this pins it.
func TestRegionIsNotMistakenForAACS(t *testing.T) {
	msg := "Error 'Scsi error - ILLEGAL REQUEST:READ OF SCRAMBLED SECTOR WITHOUT AUTHENTICATION' occurred"
	if got := ClassifyFailure(msg); got == FailureAACSKey {
		t.Error("a CSS region failure was grouped as an AACS key failure")
	}
}

// Every group a failure can be put in should say what to do about it, or the
// grouping has bought nothing.
func TestEveryKnownKindHasAdvice(t *testing.T) {
	for _, k := range []FailureKind{
		FailureAACSKey, FailureBDPlus, FailureRegion,
		FailureUnreadable, FailureStalled, FailureSpace, FailureTooling,
		FailureMismatch, FailureCancelled,
	} {
		if FailureAdvice(k) == "" {
			t.Errorf("no advice for %q", k)
		}
	}
	if FailureAdvice(FailureOther) != "" {
		t.Error("FailureOther should offer no advice; it is what we do not understand")
	}
}

// The real message from the Star Trek disc that lost three of its four
// episodes. It has to group as a title mismatch, or it lands in "other" and
// says nothing about what went wrong across a collection.
func TestPartialDiscRefusalIsAMismatch(t *testing.T) {
	msg := "the decrypted copy holds 45m26s of the 3h1m35s the drive described (25%); refusing to rip a partial disc"
	if got := ClassifyFailure(msg); got != FailureMismatch {
		t.Errorf("ClassifyFailure(%q) = %q, want %q", msg, got, FailureMismatch)
	}
	if FailureAdvice(FailureMismatch) == "" {
		t.Error("a refused partial disc gives no advice; it is the failure most in need of it")
	}
}

// The message a damaged disc actually produces. It has to group as unreadable
// rather than as a title mismatch: the fix is to clean or replace the disc,
// and a mismatch sends you looking for a bug in hellbox instead.
func TestAMalformedCopyGroupsAsUnreadable(t *testing.T) {
	msg := "the decrypted copy is malformed: VTS_02_5.VOB is 5.0 GB, and a DVD VOB cannot exceed 1 GB " +
		"(dvdbackup reported 251 read errors; the disc is probably damaged)"
	if got := ClassifyFailure(msg); got != FailureUnreadable {
		t.Errorf("ClassifyFailure(...) = %q, want %q", got, FailureUnreadable)
	}
}

// The native DVD path emits these while reading a disc perfectly. Verified
// 2026-08-08: both appeared on a read whose output decoded without a single
// error and rendered correctly.
func TestBenignDecoderNoiseIsNotAFailure(t *testing.T) {
	benign := []string{
		"libdvdnav: Error cracking CSS key for /VIDEO_TS/VTS_03_1.VOB (0x00015820)",
		"libdvdread: Can't read name block. Probably not a DVD-ROM device.",
		"bdj.c:795: BD-J check: Failed to load JVM library",
		"bdplus_config.c:283: VM configuration not found",
	}
	for _, msg := range benign {
		if !IsBenign(msg) {
			t.Errorf("IsBenign(%q) = false, want true", msg)
		}
		// The dangerous one: "css" would otherwise group this under region and
		// send someone to inspect a drive that is working fine.
		if got := ClassifyFailure(msg); got != FailureOther {
			t.Errorf("ClassifyFailure(%q) = %q, want %q", msg, got, FailureOther)
		}
	}
}

// A real failure must still be found when benign noise is mixed in with it,
// which is the normal case: the noise comes first, the fault comes later.
func TestRealFailureSurvivesBenignNoise(t *testing.T) {
	msg := strings.Join([]string{
		"libdvdnav: Error cracking CSS key for /VIDEO_TS/VTS_03_1.VOB",
		"libdvdread: Can't read name block. Probably not a DVD-ROM device.",
		"write title_03.mkv: no space left on device",
	}, "\n")

	if got := ClassifyFailure(msg); got != FailureSpace {
		t.Errorf("ClassifyFailure = %q, want %q", got, FailureSpace)
	}
}

// A genuine region failure must still be classified as one. The benign filter
// must not have blunted the pattern it protects.
func TestGenuineRegionFailureStillClassifies(t *testing.T) {
	msg := "Region setting of drive does not match region of disc; trying to work around"
	if got := ClassifyFailure(msg); got != FailureRegion {
		t.Errorf("ClassifyFailure = %q, want %q", got, FailureRegion)
	}
}

func TestStripBenignKeepsEverythingElse(t *testing.T) {
	in := "libdvdread: Can't read name block.\nreal problem here\n"
	out := StripBenign(in)
	if strings.Contains(out, "name block") {
		t.Error("benign line survived")
	}
	if !strings.Contains(out, "real problem here") {
		t.Error("stripped a line it should have kept")
	}
}
