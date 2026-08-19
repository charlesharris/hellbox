package disc

import "strings"

// FailureKind is why a disc did not make it through, grouped so a pattern is
// visible across a collection.
//
// The point of grouping is counting. "Three discs failed" says nothing about
// what to do; "three discs were not in the AACS key database" says the database
// needs updating, and "three discs use BD+" says something else entirely. The
// raw message is always kept beside the group — these are a reading of it, not
// a replacement.
type FailureKind string

const (
	// FailureAACSKey is a Blu-ray whose key is not in the database. libaacs
	// derives keys from what it has and cannot crack an unknown disc, which is
	// the one thing MakeMKV does that this stack does not.
	FailureAACSKey FailureKind = "aacs-key"

	// FailureBDPlus is a Blu-ray using BD+, whose virtual machine libbdplus
	// implements less completely than MakeMKV does.
	FailureBDPlus FailureKind = "bd-plus"

	// FailureRegion is a disc the drive will not decrypt for want of a region.
	FailureRegion FailureKind = "region"

	// FailureUnreadable is a disc the drive could not read: scratched, dirty,
	// or a format it does not take.
	FailureUnreadable FailureKind = "unreadable"

	// FailureStalled is work that stopped making progress and was ended.
	FailureStalled FailureKind = "stalled"

	// FailureSpace is a full disk.
	FailureSpace FailureKind = "no-space"

	// FailureTooling is a missing or broken external program.
	FailureTooling FailureKind = "tooling"

	// FailureCancelled is work a person or a restart stopped. Not a failure at
	// all, and grouped only because it arrives through the same field: a
	// cancelled job records "cancelled" where a failed one records why.
	FailureCancelled FailureKind = "cancelled"

	// FailureMismatch is a rip that ran out of titles, or wrote a file the scan
	// did not describe. It means the source hellbox scanned and the source it
	// ripped from disagreed.
	FailureMismatch FailureKind = "title-mismatch"

	// FailureOther is anything not recognised. Kept deliberately vague rather
	// than guessed at: a wrong group is worse than no group, because it is
	// counted.
	FailureOther FailureKind = "other"
)

// failurePatterns map message fragments onto groups, most specific first.
//
// Matching on message text is normally a mistake — codes are stable where prose
// is not — but these messages come from four different programs, most of which
// have no codes at all. The raw message survives alongside, so a pattern that
// stops matching costs a grouping, not information.
var failurePatterns = []struct {
	kind     FailureKind
	fragment string
}{
	// Cancellation first: it is not a failure, and its message says nothing
	// else that could be matched.
	{FailureCancelled, "cancelled"},
	{FailureCancelled, "context canceled"},

	{FailureAACSKey, "aacs"},
	{FailureAACSKey, "no valid processing key"},
	{FailureAACSKey, "unit key"},
	{FailureAACSKey, "vuk"},
	{FailureBDPlus, "bd+"},
	{FailureBDPlus, "bdplus"},
	{FailureRegion, "region"},
	{FailureRegion, "scrambled sector"},
	{FailureRegion, "css"},
	{FailureStalled, "no progress for"},
	{FailureStalled, "did not finish within"},
	{FailureSpace, "no space left"},
	{FailureSpace, "disk full"},
	{FailureTooling, "failed to execute external program"},
	{FailureTooling, "not on path"},
	{FailureTooling, "executable file not found"},
	{FailureTooling, "no such file or directory"},
	{FailureUnreadable, "the disc is probably damaged"},
	{FailureUnreadable, "a dvd vob cannot exceed"},
	{FailureUnreadable, "medium not present"},
	{FailureUnreadable, "input/output error"},
	{FailureUnreadable, "failed to open disc"},
	{FailureUnreadable, "no titles found"},
	{FailureUnreadable, "cannot read"},
	{FailureUnreadable, "unreadable"},

	// Last, because its message is whatever MakeMKV said about a title that
	// was not there — which is only recognisable by elimination.
	{FailureMismatch, "refusing to rip a partial disc"},
	{FailureMismatch, "produced no output file"},
	{FailureMismatch, "titles saved, 1 failed"},
	{FailureMismatch, "was therefore skipped"},
}

// benignFragments are alarming lines that appear on work which succeeded.
//
// The native DVD path emits both of these while reading a disc perfectly. The
// first is libdvdnav complaining about a title set other than the one being
// read; the second is libdvdread noting that its input is a directory rather
// than a device, which happens whenever a decrypted copy or a mount is the
// source. Both were observed on 2026-08-08 alongside video that decoded without
// a single error.
//
// They matter because they would otherwise be matched: "error cracking css key"
// contains "css", so a rip that failed for an unrelated reason would be filed
// under region — and a region grouping carries the advice "the drive has no
// region set", sending someone to look at a drive that is working.
//
// Whole lines carrying these are removed before any grouping is attempted, so
// what is left is whatever actually went wrong.
var benignFragments = []string{
	"error cracking css key",
	"can't read name block",
	"probably not a dvd-rom device",
	"bd-j check: failed to load jvm",
	"vm configuration not found",
}

// IsBenign reports whether a line is known-harmless noise from the decoding
// stack rather than a description of a fault.
func IsBenign(line string) bool {
	l := strings.ToLower(line)
	for _, f := range benignFragments {
		if strings.Contains(l, f) {
			return true
		}
	}
	return false
}

// StripBenign removes known-harmless lines from a multi-line message.
func StripBenign(message string) string {
	lines := strings.Split(message, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if IsBenign(l) {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

// ClassifyFailure reads a failure message into a group.
//
// Benign lines are removed first. A message consisting of nothing but benign
// noise is not a failure at all and groups as FailureOther rather than being
// matched on a fragment that happens to appear inside a harmless warning.
func ClassifyFailure(message string) FailureKind {
	m := strings.ToLower(StripBenign(message))
	for _, p := range failurePatterns {
		if strings.Contains(m, p.fragment) {
			return p.kind
		}
	}
	return FailureOther
}

// FailureAdvice is what to do about a group, or "" when there is nothing
// useful to say.
func FailureAdvice(k FailureKind) string {
	switch k {
	case FailureAACSKey:
		return "the disc is not in the AACS key database; try a newer KEYDB.cfg"
	case FailureBDPlus:
		return "the disc uses BD+, which libbdplus handles incompletely"
	case FailureRegion:
		return "the drive has no region set, so it will not decrypt this disc"
	case FailureSpace:
		return "free some space and try again"
	case FailureTooling:
		return "a program hellbox depends on is missing"
	case FailureStalled:
		return "the disc stopped being read; it may be damaged"
	case FailureUnreadable:
		return "the drive could not read the disc; it may be dirty or damaged"
	case FailureMismatch:
		return "the scan and the ripped source disagreed about the titles"
	case FailureCancelled:
		return "stopped deliberately; nothing is wrong with the disc"
	}
	return ""
}
