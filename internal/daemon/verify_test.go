package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkv writes a file whose first bytes are a valid EBML header, padded to size.
func mkv(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := append(append([]byte{}, ebmlMagic...), make([]byte, max(0, size-len(ebmlMagic)))...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestVerifyOutput(t *testing.T) {
	dir := t.TempDir()

	t.Run("a good file passes", func(t *testing.T) {
		if err := verifyOutput(mkv(t, dir, "good.mkv", 2048), 1024); err != nil {
			t.Errorf("verifyOutput: %v", err)
		}
	})

	t.Run("a missing file is not silently accepted", func(t *testing.T) {
		err := verifyOutput(filepath.Join(dir, "absent.mkv"), 1024)
		if err == nil {
			t.Fatal("a missing output passed verification")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Errorf("error does not say the file is missing: %v", err)
		}
	})

	// The case this exists for: makemkvcon exits 0 having written a stub. Left
	// unchecked, the disc would be recorded as ripped and never read again.
	t.Run("a truncated file is rejected", func(t *testing.T) {
		err := verifyOutput(mkv(t, dir, "stub.mkv", 64), 1024)
		if err == nil {
			t.Fatal("a 64-byte output passed a 1 KB floor")
		}
		if !strings.Contains(err.Error(), "below") {
			t.Errorf("error does not explain the size problem: %v", err)
		}
	})

	t.Run("a file that is not Matroska is rejected", func(t *testing.T) {
		path := filepath.Join(dir, "notmkv.mkv")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 2048)), 0o644); err != nil {
			t.Fatal(err)
		}
		err := verifyOutput(path, 1024)
		if err == nil {
			t.Fatal("a non-Matroska file passed verification")
		}
		if !strings.Contains(err.Error(), "EBML") {
			t.Errorf("error does not mention the header: %v", err)
		}
	})

	// An empty file is short and headerless at once; it must fail on size
	// rather than reaching the header read and erroring less clearly.
	t.Run("an empty file is rejected", func(t *testing.T) {
		path := filepath.Join(dir, "empty.mkv")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := verifyOutput(path, 1024); err == nil {
			t.Fatal("an empty output passed verification")
		}
	})

	// A floor of zero must still reject a file that is not Matroska: the size
	// check being disabled cannot be allowed to disable the header check too.
	t.Run("a zero floor still checks the header", func(t *testing.T) {
		path := filepath.Join(dir, "zerofloor.bin")
		if err := os.WriteFile(path, []byte("not matroska"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := verifyOutput(path, 0); err == nil {
			t.Fatal("a non-Matroska file passed with a zero size floor")
		}
	})
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{10 << 20, "10.0 MB"},
		{897_000_000_000, "835.4 GB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.in); got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
