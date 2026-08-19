package dvd

import (
	"os"
	"path/filepath"
	"testing"
)

// buildVMG makes a synthetic Video Manager with the given provider field.
func buildVMG(provider string) []byte {
	b := make([]byte, sectorSize)
	copy(b, vmgMagic)
	f := make([]byte, providerLen)
	for i := range f {
		f[i] = ' '
	}
	copy(f, provider)
	copy(b[providerOffset:], f)
	return b
}

// The field that prompted this: exactly 32 characters, on a disc whose volume
// label is DVD_VIDEO.
func TestProviderIDReadsARealTitle(t *testing.T) {
	const want = "The Karate Kid (Special Edition)"
	if len(want) != providerLen {
		t.Fatalf("the specimen should be exactly %d chars, is %d", providerLen, len(want))
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "VIDEO_TS.IFO")
	if err := os.WriteFile(path, buildVMG(want), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ProviderID(path)
	if err != nil {
		t.Fatalf("ProviderID: %v", err)
	}
	if got != want {
		t.Errorf("ProviderID = %q, want %q", got, want)
	}
}

func TestProviderIDFindsTheIFOInADirectory(t *testing.T) {
	dir := t.TempDir()
	vts := filepath.Join(dir, "VIDEO_TS")
	if err := os.MkdirAll(vts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vts, "VIDEO_TS.IFO"), buildVMG("Roman Holiday"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ProviderID(dir)
	if err != nil {
		t.Fatalf("ProviderID: %v", err)
	}
	if got != "Roman Holiday" {
		t.Errorf("ProviderID = %q", got)
	}
}

// A disc in a drive has no filesystem to read through, so the Video Manager is
// found by scanning sector boundaries. This is the path that matters.
func TestProviderIDScansARawImageForTheVMG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disc.img")

	img := make([]byte, sectorSize*40)
	copy(img[sectorSize*17:], buildVMG("Spaceballs")) // not at sector 0
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ProviderID(path)
	if err != nil {
		t.Fatalf("ProviderID: %v", err)
	}
	if got != "Spaceballs" {
		t.Errorf("ProviderID = %q, want Spaceballs", got)
	}
}

// The field is nominally the authoring provider, so abstaining matters more
// than reaching. A confident wrong name cannot be overruled by a better net.
func TestProviderIDAbstainsOnJunk(t *testing.T) {
	for _, junk := range []string{
		"", "  ", "DVD_VIDEO", "dvd video", "UNTITLED", "ab",
		"1234567890123456", "SONY DVD VIDEO", "----------------",
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "VIDEO_TS.IFO")
		if err := os.WriteFile(path, buildVMG(junk), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ProviderID(path)
		if err != nil {
			t.Fatalf("ProviderID(%q): %v", junk, err)
		}
		if got != "" {
			t.Errorf("ProviderID(%q) = %q, want abstention", junk, got)
		}
	}
}

func TestProviderIDHandlesNulPadding(t *testing.T) {
	b := make([]byte, sectorSize)
	copy(b, vmgMagic)
	copy(b[providerOffset:], "Alien\x00\x00\x00\x00")

	dir := t.TempDir()
	path := filepath.Join(dir, "VIDEO_TS.IFO")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := ProviderID(path)
	if got != "Alien" {
		t.Errorf("ProviderID = %q, want Alien", got)
	}
}

func TestProviderIDReportsAMissingVMG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notadisc.bin")
	if err := os.WriteFile(path, make([]byte, sectorSize*4), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProviderID(path); err == nil {
		t.Error("expected an error for a file with no Video Manager")
	}
}

// The result that matters, against a disc actually in a drive. Verified
// 2026-08-09 returning "The Karate Kid (Special Edition)" in 5.3 seconds from
// a disc whose volume label is DVD_VIDEO.
//
//	HELLBOX_DVD_DEVICE=/dev/sr0 go test ./internal/dvd/ -run RealDiscTitle -v
func TestRealDiscTitle(t *testing.T) {
	dev := realDevice(t)
	got, err := DiscTitle(dev)
	if err != nil {
		t.Fatalf("DiscTitle(%s): %v", dev, err)
	}
	t.Logf("DiscTitle = %q", got)
	if got == "" {
		t.Log("this disc carries no name — an ordinary answer, not a failure")
	}
}

// Runs against a disc actually in a drive:
//
//	HELLBOX_DVD_DEVICE=/dev/sr0 go test ./internal/dvd/ -run RealProvider -v
func TestRealProviderID(t *testing.T) {
	dev := realDevice(t)
	got, err := ProviderID(dev)
	if err != nil {
		t.Fatalf("ProviderID(%s): %v", dev, err)
	}
	t.Logf("provider identifier: %q", got)
	if got == "" {
		t.Log("this disc carries nothing usable in the field — a normal answer")
	}
}
