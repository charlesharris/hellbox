package drive

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// VolumeLabel reads the disc's filesystem label.
//
// Read with blkid rather than by parsing the filesystem, because a Blu-ray's
// label lives in its UDF logical volume descriptor while a DVD's is in the
// ISO 9660 primary volume descriptor — and on a Blu-ray the ISO descriptor is
// present but empty, so reading the obvious one gets nothing. blkid knows both
// and is part of util-linux, which is already required for the system to boot.
func VolumeLabel(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "blkid", "-o", "value", "-s", "LABEL", path).Output()
	if err != nil {
		// blkid exits non-zero when it finds nothing to report, which is not an
		// error: an unlabelled disc is ordinary.
		return "", nil
	}
	label := strings.TrimSpace(string(out))
	if label == "" {
		return "", nil
	}
	if !isPrintable(label) {
		return "", fmt.Errorf("unreadable volume label on %s", path)
	}
	return label, nil
}

func isPrintable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
