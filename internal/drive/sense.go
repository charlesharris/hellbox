package drive

// SCSI sense data, and what it says about the drive.
//
// Kept apart from the ioctl that fetches it (scsi_linux.go) because these two
// things fail differently and are worth separating: issuing SG_IO needs Linux
// and a real drive, while deciding what sense key 0x3a/0x02 means about a tray
// is judgement, and judgement should be testable on any machine with no disc
// anywhere near it.

// senseResult is the outcome of a TEST UNIT READY.
type senseResult struct {
	// good reports GOOD status: the unit is ready and a readable medium is
	// loaded. Nothing else needs interpreting when this is true.
	good bool

	senseKey uint8
	asc      uint8 // additional sense code
	ascq     uint8 // additional sense code qualifier
}

// parseSense reads sense key, ASC and ASCQ out of fixed- or descriptor-format
// sense data.
func parseSense(b []byte) senseResult {
	if len(b) < 3 {
		return senseResult{}
	}
	switch b[0] & 0x7f {
	case 0x72, 0x73: // descriptor format
		if len(b) < 4 {
			return senseResult{}
		}
		return senseResult{senseKey: b[1] & 0x0f, asc: b[2], ascq: b[3]}
	default: // 0x70/0x71, fixed format
		if len(b) < 14 {
			return senseResult{senseKey: b[2] & 0x0f}
		}
		return senseResult{senseKey: b[2] & 0x0f, asc: b[12], ascq: b[13]}
	}
}

// Additional sense codes that describe what is, or is not, in the drive.
const (
	ascNotReady          = 0x04 // ASCQ 0x01: becoming ready (spinning up)
	ascIncompatibleMedia = 0x30 // a disc is present that this drive cannot read
	ascMediumNotPresent  = 0x3a // ASCQ 0x02: tray open
)

// statusFromSense maps a TEST UNIT READY result onto a drive Status.
//
// Separated from the ioctl so the mapping — the part that encodes judgement
// about what each code means — can be tested without hardware.
func statusFromSense(r senseResult) Status {
	if r.good {
		return StatusDiscOK
	}

	switch r.asc {
	case ascMediumNotPresent:
		// 0x3a/0x02 is "tray open"; every other qualifier means closed and
		// empty. The distinction matters: an open tray is the resting state
		// between discs, while a closed empty drive is simply idle.
		if r.ascq == 0x02 {
			return StatusTrayOpen
		}
		return StatusNoDisc

	case ascIncompatibleMedia:
		// A disc is loaded that this drive cannot read — a Blu-ray in a DVD
		// drive, most often. Distinct from an empty drive: something is in
		// there, and it will sit there until a person takes it out.
		return StatusIncompatible

	case ascNotReady:
		// 0x04/0x01 is spinning up. Other qualifiers cover a unit that needs an
		// initialising command; treating them all as "not ready yet" is right,
		// because the poller simply looks again.
		return StatusNotReady
	}

	// Sense that does not describe medium presence. NotReady keeps the poller
	// looking rather than asserting something untrue about the drive.
	if r.senseKey == 0x02 {
		return StatusNotReady
	}
	return StatusUnknown
}
