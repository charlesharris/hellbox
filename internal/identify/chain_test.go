package identify

import (
	"context"
	"testing"

	"hellbox/internal/dvd"
	"hellbox/internal/testsource"
)

// TestRealDiscIdentifiesFromItsOwnName runs the whole chain against a disc in a
// drive: read the name off the IFO, feed the nets, resolve.
//
//	HELLBOX_DVD_SOURCE=/dev/sr0 go test ./internal/identify/ -run RealDisc -v
func TestRealDiscIdentifiesFromItsOwnName(t *testing.T) {
	dev := testsource.Path(t, "HELLBOX_DVD_SOURCE", "HELLBOX_DVD_DEVICE")

	name, err := dvd.DiscTitle(dev)
	if err != nil {
		t.Fatalf("DiscTitle: %v", err)
	}
	t.Logf("disc says its name is %q", name)

	in := Input{
		VolumeLabel:    "DVD_VIDEO", // what the filesystem offers
		DiscType:       "dvd",
		DiscName:       name,
		DiscNameSource: "the DVD text data manager",
	}
	res, err := Run(context.Background(), []Net{LabelNet{}, DiscNameNet{}}, in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, c := range res.All {
		t.Logf("  %-9s %-34q conf=%.2f  %s", c.Net, c.Title, c.Confidence, c.Why)
	}
	t.Logf("BEST: %q (year=%d season=%d disc=%d kind=%s) contested=%v",
		res.Best.Title, res.Best.Year, res.Best.Season, res.Best.Disc, res.Best.Kind, res.Contested)

	if res.Best.Title == "" {
		t.Error("nothing identified the disc")
	}
}
