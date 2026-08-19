// Package source decides how a disc will be read, before anything reads it.
//
// Every disc is enumerated first and extracted second, and the two are
// deliberately separable. Enumeration needs no decryption on either format: a
// DVD's IFO structures are plaintext, and a Blu-ray's playlists and BDMV/META
// are too. Extraction is where DRM bites.
//
// That ordering is what lets hellbox say something useful about a disc it
// cannot rip. A BD+ Blu-ray reaches the interface as "Firefly: Disc 1 — 4
// episodes, BD+, needs MakeMKV" rather than as an opaque failure, because the
// episode count came from a read that never needed a key.
//
// The Path a disc took is recorded against it. Nobody owns a manifest of which
// of their discs carry BD+, and asking them to know is the wrong question, so
// the collection reports on itself instead: after a few dozen discs the health
// view can say how many needed MakeMKV, which is the same thing as saying how
// exposed the machine is to a licence expiring.
package source

import (
	"fmt"

	"hellbox/internal/bluray"
	"hellbox/internal/disc"
)

// Path is the mechanism that read, or must read, a disc.
type Path string

const (
	// PathNativeDVD is libdvdread and libdvdcss under ffmpeg's dvdvideo
	// demuxer, reading the disc in place. The ordinary case, and the one that
	// needs no licence.
	PathNativeDVD Path = "native-dvd"

	// PathDecryptCopy is a DVD copied out with dvdbackup first and read from
	// the copy. Kept for discs the drive struggles with and for retries, but no
	// longer the routine answer to a region-locked drive: libdvdcss decrypts in
	// place, which was verified against a region 1/3/4 disc in a drive with no
	// region set.
	PathDecryptCopy Path = "decrypt-copy"

	// PathNativeBluRay is libaacs under ffmpeg's bluray protocol. Works when
	// AACS is the only protection.
	PathNativeBluRay Path = "native-bluray-aacs"

	// PathMakeMKV is the fallback for what the free stack cannot open. In
	// practice that means BD+, whose virtual machine libbdplus cannot run for
	// want of data no distribution ships.
	PathMakeMKV Path = "makemkv"

	// PathUnknown is a disc not yet routed.
	PathUnknown Path = ""
)

// NeedsLicence reports whether a path depends on MakeMKV, and therefore on a
// registration key that expires.
func (p Path) NeedsLicence() bool { return p == PathMakeMKV }

// Plan is what enumeration concluded, and what extraction should do about it.
type Plan struct {
	// Disc is the description built without decrypting anything.
	Disc disc.Disc

	// Path is how the disc must be read.
	Path Path

	// Blocked marks a disc that cannot be extracted by any available means —
	// the free stack refuses it and its fallback is unavailable. The disc is
	// still fully described; only the bytes are out of reach.
	Blocked bool

	// Reason explains a blocked disc, or names the protection on one that is
	// merely awkward. Written for a person reading the interface.
	Reason string

	// DiscName is the disc's own idea of its name, when it has one. Only
	// Blu-rays carry this, in BDMV/META/DL/bdmt_eng.xml, and it is far better
	// evidence than a volume label: "FIREFLY: DISC 1" against "FIREFLYUS_D1".
	DiscName string

	// Artwork lists cover images the disc carries, if any.
	Artwork []string
}

// Summary renders the plan as one line for a log or a card.
func (p Plan) Summary() string {
	name := p.DiscName
	if name == "" {
		name = p.Disc.VolumeLabel
	}
	if name == "" {
		name = "unlabelled disc"
	}
	n := len(p.Disc.Titles)
	titles := "titles"
	if n == 1 {
		titles = "title"
	}
	if p.Reason != "" {
		return fmt.Sprintf("%s — %d %s, %s", name, n, titles, p.Reason)
	}
	return fmt.Sprintf("%s — %d %s", name, n, titles)
}

// PlanBluRay routes a Blu-ray from what enumeration found.
//
// makeMKVAvailable is passed in rather than detected here so that the decision
// is a pure function of what is known: a disc that needs MakeMKV on a machine
// without it is blocked, and must say so plainly instead of failing later with
// a decryption error that looks like a bad disc.
func PlanBluRay(info *bluray.Info, minSecs int, makeMKVAvailable bool) Plan {
	p := Plan{
		Disc:     info.Disc(minSecs),
		DiscName: info.DiscName,
		Artwork:  info.Thumbnails,
	}

	switch {
	case info.Protection.NeedsMakeMKV():
		p.Path = PathMakeMKV
		if makeMKVAvailable {
			p.Reason = "BD+, which needs MakeMKV"
		} else {
			p.Blocked = true
			p.Reason = "BD+, which needs MakeMKV, and MakeMKV is not available"
		}
	case info.Protection.AACS && !info.Protection.AACSHandled:
		// AACS that libaacs cannot handle is usually a disc missing from the key
		// database. MakeMKV derives keys differently and may manage it.
		p.Path = PathMakeMKV
		p.Blocked = !makeMKVAvailable
		p.Reason = "AACS not in the key database; try a newer KEYDB.cfg"
	default:
		p.Path = PathNativeBluRay
		if info.Protection.AACS {
			p.Reason = "AACS"
		}
	}
	return p
}

// PlanDVD routes a DVD.
//
// driveCanDecrypt says whether the drive will perform CSS authentication. It is
// deliberately not the deciding factor any more. libdvdcss derives a title key
// from the disc when the drive refuses, which was verified end to end against a
// region 1/3/4 CSS disc in an RPC-2 drive with no region set — so the native
// path is tried first regardless, and the copy exists for discs that actually
// fail rather than for discs that merely look like they will.
//
// That is a reversal of v1, where every region-blocked disc paid half an hour
// for a copy it did not need.
func PlanDVD(d disc.Disc, driveCanDecrypt bool) Plan {
	p := Plan{Disc: d, Path: PathNativeDVD}
	if !driveCanDecrypt {
		p.Reason = "CSS, decrypted by libdvdcss rather than by the drive"
	}
	return p
}

// Fallback returns the path to try when one has failed, and whether there is
// one at all.
//
// A DVD that will not read in place gets the copy: dvdbackup reads
// sequentially and tolerates a disc that libdvdread gives up on. There is
// nothing after that, and a disc that fails both is a disc to clean or replace.
func Fallback(p Path) (Path, bool) {
	switch p {
	case PathNativeDVD:
		return PathDecryptCopy, true
	case PathNativeBluRay:
		return PathMakeMKV, true
	}
	return PathUnknown, false
}

// Mix counts how a collection was read, for the health view.
type Mix map[Path]int

// Add records one disc.
func (m Mix) Add(p Path) {
	if p == PathUnknown {
		return
	}
	m[p]++
}

// LicenceExposure is how many discs needed MakeMKV, and therefore how much of
// the collection stops working when a key expires.
func (m Mix) LicenceExposure() int {
	var n int
	for p, c := range m {
		if p.NeedsLicence() {
			n += c
		}
	}
	return n
}
