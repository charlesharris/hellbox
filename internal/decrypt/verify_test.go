package decrypt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The disc this exists for: a Star Trek DVD with 251 unrecovered read errors.
// dvdbackup exited successfully having written VTS_02_5.VOB as five gigabytes
// — one gigabyte of data, a four gigabyte hole, then stray writes at what looks
// like an absolute disc sector used as a file offset. Nothing downstream could
// read it: MakeMKV reported one title where the drive reported four, and three
// episodes were lost with the disc ejected as done.
func TestOversizedVOBIsRejected(t *testing.T) {
	dir := t.TempDir()

	// Written sparse, exactly as dvdbackup left it — the real file used a
	// gigabyte of disk while claiming five.
	f, err := os.Create(filepath.Join(dir, "VTS_02_5.VOB"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxVOBBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	err = verifyCopy(dir, 251)
	if err == nil {
		t.Fatal("a 1 GB+ VOB was accepted; the DVD specification forbids it")
	}
	if !strings.Contains(err.Error(), "VTS_02_5.VOB") {
		t.Errorf("error does not name the offending file: %v", err)
	}
	// The read-error count is what points at the disc rather than at hellbox.
	if !strings.Contains(err.Error(), "251") {
		t.Errorf("error omits the read errors, which are the actual cause: %v", err)
	}
}

// A copy at exactly the limit is legal and common: DVDs fill VOBs to the cap
// and start a new one, so most discs have several at precisely this size.
func TestVOBAtTheLimitIsAccepted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"VTS_01_1.VOB", "VTS_01_2.VOB", "VIDEO_TS.IFO"} {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(name, ".VOB") {
			if err := f.Truncate(maxVOBBytes); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()
	}
	if err := verifyCopy(dir, 0); err != nil {
		t.Errorf("a copy with full-size VOBs was rejected: %v", err)
	}
}

// The gap this closes: verification ran when a copy was written but not when
// one was found, so the malformed Star Trek copy was reused on the next
// attempt — 13.7 GB of it, announced as a saving. A disc that had been cleaned
// would never have been read again, because the broken copy of it always won.
func TestACorruptCopyIsNotReused(t *testing.T) {
	root := t.TempDir()
	key := "abc123"

	dir := filepath.Join(Path(root, key), outputName, "VIDEO_TS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "VTS_02_5.VOB"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxVOBBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(Path(root, key), completeMarker), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if res, ok := Existing(root, key); ok {
		t.Fatalf("a corrupt copy was offered for reuse: %+v", res)
	}
	// And it is gone, so the next attempt decrypts rather than finding it again.
	if _, err := os.Stat(Path(root, key)); !os.IsNotExist(err) {
		t.Error("the corrupt copy was left in place; the next attempt would find it")
	}
}
