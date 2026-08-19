package daemon

import (
	"bytes"
	"fmt"
	"os"
)

// ebmlMagic opens every Matroska file. Checking it distinguishes a truncated or
// empty output from a real one.
var ebmlMagic = []byte{0x1A, 0x45, 0xDF, 0xA3}

// verifyOutput performs Phase 1's deliberately shallow check on a ripped title.
//
// This confirms a file exists, is not a stub, and is Matroska. It does not
// confirm the content is complete. The check that would catch a subtly
// truncated rip — comparing actual stream duration against the duration
// MakeMKV reported at scan time — needs ffprobe, which arrives as a dependency
// with transcoding in Phase 2. Until then this is honest about its limits
// rather than implying more assurance than it provides.
func verifyOutput(path string, minBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("output missing: %w", err)
	}
	if info.Size() < minBytes {
		return fmt.Errorf("output is %s, below the %s floor for a real title",
			humanBytes(info.Size()), humanBytes(minBytes))
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer f.Close()

	header := make([]byte, len(ebmlMagic))
	if _, err := f.Read(header); err != nil {
		return fmt.Errorf("read output header: %w", err)
	}
	if !bytes.Equal(header, ebmlMagic) {
		return fmt.Errorf("output is not a Matroska file (bad EBML header)")
	}
	return nil
}

// humanBytes renders a byte count for a human reading an error message.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
