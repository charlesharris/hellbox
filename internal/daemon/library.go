package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"

	"hellbox/internal/library"
)

// fileUnfiled files every finished transcode the library has no link for.
//
// Filing otherwise only ever reacts to a transcode finishing, which strands
// anything encoded before filing existed, or while it was switched off, or
// while the library could not be written to. Reconciling from what is recorded
// lets the library catch up on its own rather than needing every title encoded
// again to produce a file that is already there.
//
// Run at startup and whenever the queue goes quiet. It is cheap — a query and,
// for anything genuinely missing, a hardlink.
func (d *Daemon) fileUnfiled(ctx context.Context) {
	if !d.cfg.FileToLibrary {
		return
	}
	pending, err := d.st.UnfiledTranscodes(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.logEvent("warn", fmt.Sprintf("could not check what still needs filing: %v", err), nil, nil)
		}
		return
	}
	for _, u := range pending {
		if ctx.Err() != nil {
			return
		}
		// A transcode recorded as complete whose file has since been moved or
		// removed is not an error worth shouting about, but it must not be
		// recorded as filed either.
		if _, err := os.Stat(u.OutputPath); err != nil {
			continue
		}
		d.fileTitle(ctx, u.DiscID, u.TitleIndex, u.OutputPath)
	}
}

// fileTitle puts a finished transcode where Jellyfin can find it.
//
// Filing happens per title as each transcode finishes rather than once a disc
// is wholly done, so a long disc appears in the library as it goes instead of
// all at once at the end. The placement is derived from the whole disc each
// time, which is what keeps the answer the same whichever title finishes first.
//
// Nothing here identifies anything. Jellyfin matches names against its own
// metadata providers and does it far better than hellbox could; hellbox's part
// is to produce a name and a layout Jellyfin understands, and to know which
// disc a file came from — which is the thing Jellyfin cannot know.
func (d *Daemon) fileTitle(ctx context.Context, discID int64, titleIndex int, source string) {
	if !d.cfg.FileToLibrary {
		return
	}

	rec, err := d.st.DiscWithTitles(ctx, discID)
	if err != nil {
		d.logEvent("warn", fmt.Sprintf("could not read disc %d to file it: %v", discID, err), nil, nil)
		return
	}

	kind := library.Classify(rec.Titles)
	var placement *library.Placement
	for _, p := range library.Plan(d.cfg.LibraryDir, rec, kind, nil) {
		if p.TitleIndex == titleIndex {
			placement = &p
			break
		}
	}
	if placement == nil {
		return
	}

	switch err := library.Link(source, placement.Path); {
	case errors.Is(err, library.ErrExists):
		// Already there. Either hellbox filed it before, or a person has put
		// something at that name — and replacing the second would lose work
		// hellbox did not do. Recorded either way so the link is known.
		_ = d.st.RecordLibraryLink(ctx, discID, titleIndex, placement.Path)
		return
	case err != nil:
		d.logEvent("warn", fmt.Sprintf("could not file title %d of %s: %v",
			titleIndex, displayName(rec.VolumeLabel, rec.Fingerprint), err), nil, nil)
		return
	}

	if err := d.st.RecordLibraryLink(ctx, discID, titleIndex, placement.Path); err != nil {
		d.logEvent("warn", fmt.Sprintf("filed title %d but could not record it: %v", titleIndex, err), nil, nil)
	}

	what := string(kind)
	if placement.Feature {
		what = "feature"
	}
	d.logEvent("info", fmt.Sprintf("filed %s title %d as %s (%s)",
		displayName(rec.VolumeLabel, rec.Fingerprint), titleIndex, placement.Path, what), nil, nil)
}
