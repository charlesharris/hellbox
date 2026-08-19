package dvd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Reading the disc's own idea of its name out of VIDEO_TS.IFO.
//
// A volume label is eleven upper-case characters chosen by an authoring house
// and is frequently just DVD_VIDEO. The IFO carries the real name elsewhere, in
// the Text Data Manager — a structure pointed to from offset 0xD4 of the Video
// Manager. On the disc that prompted this it holds
//
//	"=The Karate Kid (Special Edition)\tKarate Kid, The (Special Edition)\t"
//
// a display title and a sort title, on a disc whose volume label says nothing
// at all. That is prose a person typed, which puts it in the same class of
// evidence as a Blu-ray's bdmt_eng.xml and far above anything LabelNet can
// produce.
//
// The first guess was the 32-byte provider identifier at 0x40, because the
// title is exactly 32 characters long and the coincidence was persuasive. On
// this disc that field is **all zeros**, and the string is 0xC3 bytes further
// on in a structure reached by a pointer. The provider identifier is still read
// as a fallback, since some discs do populate it, but it is not the source.
//
// The IFO is not encrypted, so this needs no key, no CSS authentication and no
// decryption. It is available at scan time, before a single byte is ripped.
//
// It is not always a title. The field is nominally for the authoring provider,
// so plenty of discs carry a studio, a facility name, or nothing. Everything
// here is therefore written to abstain rather than guess: a field that does not
// look like prose returns nothing, because a confident wrong name cannot be
// overruled by the net that knows better.

// vmgMagic begins the Video Manager, and by extension VIDEO_TS.IFO.
var vmgMagic = []byte("DVDVIDEO-VMG")

const (
	// providerOffset is where the 32-byte provider identifier sits within the
	// Video Manager information table, per the DVD-Video specification.
	providerOffset = 0x40
	providerLen    = 32

	// sectorSize is a DVD logical block. The IFO always begins on one.
	sectorSize = 2048

	// scanSectors bounds the search for the Video Manager when reading a raw
	// device, where there is no filesystem to consult. VIDEO_TS.IFO lives near
	// the start of every disc; a few thousand sectors is generous and costs a
	// couple of seconds.
	scanSectors = 4096
)

// ErrNoVMG reports that no Video Manager was found.
var ErrNoVMG = errors.New("no DVDVIDEO-VMG found")

// DiscTitle returns the disc's own name, or "" when it has none.
//
// It prefers the Text Data Manager, which carries a title someone wrote, and
// falls back to the provider identifier. Either may be absent; both being
// absent is an ordinary answer and not an error.
func DiscTitle(source string) (string, error) {
	raw, err := readVMG(source)
	if err != nil {
		return "", err
	}
	if t := textDataTitle(source, raw); t != "" {
		return t, nil
	}
	if len(raw) >= providerOffset+providerLen {
		return cleanProvider(raw[providerOffset : providerOffset+providerLen]), nil
	}
	return "", nil
}

// txtdtOffset holds the sector offset of the Text Data Manager, relative to the
// start of the Video Manager.
const txtdtOffset = 0xD4

// textDataTitle reads the Text Data Manager and returns its display title.
//
// The format is a header followed by tab-separated text, the first item marked
// with '='. Rather than decode the language-unit tables in full — which vary by
// authoring suite and are poorly documented — this finds the printable run and
// takes the first field. That is enough for a name, and anything it cannot make
// sense of returns nothing, which is the right answer for a net.
func textDataTitle(source string, vmg []byte) string {
	if len(vmg) < txtdtOffset+4 {
		return ""
	}
	sector := binary.BigEndian.Uint32(vmg[txtdtOffset : txtdtOffset+4])
	if sector == 0 {
		return ""
	}

	blob, err := readTextData(source, sector)
	if err != nil || len(blob) == 0 {
		return ""
	}

	m := printableRun.Find(blob)
	if m == nil {
		return ""
	}
	text := strings.TrimPrefix(strings.TrimSpace(string(m)), "=")
	fields := strings.Split(text, "\t")
	if len(fields) == 0 {
		return ""
	}
	return cleanProvider([]byte(strings.TrimSpace(fields[0])))
}

// printableRun matches a run of readable text inside the Text Data Manager.
var printableRun = regexp.MustCompile(`[\x20-\x7e\t]{6,}`)

// ProviderID returns the disc's provider identifier, or "" when it holds
// nothing worth having.
//
// source may be a directory containing VIDEO_TS, the VIDEO_TS directory itself,
// a path to VIDEO_TS.IFO, or a raw device. The last case is the useful one and
// the reason this does not simply open a file: a disc in a drive has no
// mounted filesystem to read through, so the Video Manager is located by
// scanning sector boundaries for its magic instead. That works because the IFO
// is unencrypted and sector-aligned by definition.
func ProviderID(source string) (string, error) {
	raw, err := readVMG(source)
	if err != nil {
		return "", err
	}
	if len(raw) < providerOffset+providerLen {
		return "", ErrNoVMG
	}
	return cleanProvider(raw[providerOffset : providerOffset+providerLen]), nil
}

// readVMG returns the start of the Video Manager from whatever source is given.
func readVMG(source string) ([]byte, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", source, err)
	}

	if info.IsDir() {
		for _, candidate := range []string{
			filepath.Join(source, "VIDEO_TS", "VIDEO_TS.IFO"),
			filepath.Join(source, "video_ts", "video_ts.ifo"),
			filepath.Join(source, "VIDEO_TS.IFO"),
			filepath.Join(source, "video_ts.ifo"),
		} {
			if b, err := os.ReadFile(candidate); err == nil && bytes.HasPrefix(b, vmgMagic) {
				return b, nil
			}
		}
		return nil, ErrNoVMG
	}

	f, err := os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", source, err)
	}
	defer f.Close()

	// A plain IFO file starts with the magic.
	head := make([]byte, providerOffset+providerLen)
	if _, err := io.ReadFull(f, head); err == nil && bytes.HasPrefix(head, vmgMagic) {
		return head, nil
	}

	// Otherwise treat it as a raw disc and hunt for the magic.
	return scanForVMG(f)
}

// scanForVMG walks sector boundaries looking for the Video Manager.
func scanForVMG(r io.ReaderAt) ([]byte, error) {
	buf := make([]byte, sectorSize)
	for s := 0; s < scanSectors; s++ {
		n, err := r.ReadAt(buf, int64(s)*sectorSize)
		if n < providerOffset+providerLen {
			if err != nil {
				break
			}
			continue
		}
		if bytes.HasPrefix(buf, vmgMagic) {
			out := make([]byte, n)
			copy(out, buf[:n])
			return out, nil
		}
		if err != nil {
			break
		}
	}
	return nil, ErrNoVMG
}

// cleanProvider turns the raw field into a usable name, or "".
//
// Deliberately conservative, for the same reason MenuNet is: the field is
// nominally the authoring provider, so a good proportion of discs carry a
// studio, a facility, or padding. Returning nothing is a better answer than a
// plausible wrong one, because the design assumes a stronger net can overrule a
// weaker one and it cannot overrule a fabrication that arrived confident.
func cleanProvider(raw []byte) string {
	// Space and NUL padded to the full width.
	s := strings.TrimSpace(string(bytes.TrimRight(raw, "\x00")))
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == 0 || (unicode.IsControl(r) && r != '\t') {
			return -1
		}
		return r
	}, s))

	if len([]rune(s)) < 3 {
		return ""
	}
	if !utf8Printable(s) {
		return ""
	}

	// Mostly non-letters is padding or a serial, not a name.
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters*2 < len([]rune(s)) {
		return ""
	}

	if isGenericProvider(s) {
		return ""
	}
	return s
}

// genericProviders are values that identify nothing. Authoring suites write
// these by default and a great many discs never get past them.
var genericProviders = map[string]bool{
	"dvd_video": true, "dvdvideo": true, "dvd video": true, "dvd": true,
	"video_ts": true, "untitled": true, "unnamed": true, "unknown": true,
	"default": true, "none": true, "copyright": true, "generic": true,
	"sony dvd video": true, "dvd authoring": true,
}

func isGenericProvider(s string) bool {
	return genericProviders[strings.ToLower(strings.TrimSpace(s))]
}

func utf8Printable(s string) bool {
	for _, r := range s {
		if r == unicode.ReplacementChar {
			return false
		}
		if !unicode.IsPrint(r) && r != ' ' {
			return false
		}
	}
	return true
}

// readTextData returns the Text Data Manager region for a source.
//
// vmgSector is where the Video Manager begins, which for a raw device is found
// by scanning and for a file is zero. The sector offset in the IFO is relative
// to that, not absolute.
func readTextData(source string, sectorOffset uint32) ([]byte, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}

	// For a directory or a plain IFO the text manager is inside the same file.
	if info.IsDir() || info.Mode().IsRegular() {
		raw, err := readVMGFile(source)
		if err == nil && raw != nil {
			start := int(sectorOffset) * sectorSize
			if start < len(raw) {
				end := min2(start+sectorSize*2, len(raw))
				return raw[start:end], nil
			}
		}
	}

	f, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	base, err := findVMGSector(f)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, sectorSize*2)
	n, _ := f.ReadAt(buf, (int64(base)+int64(sectorOffset))*sectorSize)
	return buf[:n], nil
}

// readVMGFile returns a whole IFO when source is a file or a directory holding
// one, and nil when it is a device.
func readVMGFile(source string) ([]byte, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	candidates := []string{source}
	if info.IsDir() {
		candidates = []string{
			filepath.Join(source, "VIDEO_TS", "VIDEO_TS.IFO"),
			filepath.Join(source, "video_ts", "video_ts.ifo"),
			filepath.Join(source, "VIDEO_TS.IFO"),
		}
	}
	for _, c := range candidates {
		if b, err := os.ReadFile(c); err == nil && bytes.HasPrefix(b, vmgMagic) {
			return b, nil
		}
	}
	return nil, nil
}

// findVMGSector locates the Video Manager on a raw disc.
func findVMGSector(r io.ReaderAt) (int, error) {
	buf := make([]byte, len(vmgMagic))
	for s := 0; s < scanSectors; s++ {
		if _, err := r.ReadAt(buf, int64(s)*sectorSize); err != nil {
			break
		}
		if bytes.Equal(buf, vmgMagic) {
			return s, nil
		}
	}
	return 0, ErrNoVMG
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
