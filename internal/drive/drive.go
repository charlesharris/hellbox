// Package drive discovers optical drives and reports their tray state.
package drive

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Status is the tray and disc state of a drive.
type Status int

const (
	StatusUnknown Status = iota
	StatusNoDisc
	StatusTrayOpen
	StatusNotReady // tray closing, or disc spinning up
	StatusDiscOK

	// StatusIncompatible means a disc is loaded that this drive cannot read —
	// a Blu-ray in a DVD-only drive, most often.
	//
	// Deliberately distinct from StatusNoDisc. Something is physically in the
	// drive and will stay there until a person removes it, so reporting an
	// empty drive would be both wrong and unhelpful: the operator would have no
	// idea why nothing was happening.
	StatusIncompatible
)

func (s Status) String() string {
	switch s {
	case StatusNoDisc:
		return "no disc"
	case StatusTrayOpen:
		return "tray open"
	case StatusNotReady:
		return "not ready"
	case StatusDiscOK:
		return "disc present"
	case StatusIncompatible:
		return "unreadable disc"
	default:
		return "unknown"
	}
}

// Drive is a physical optical drive attached to the host.
type Drive struct {
	// StableID identifies the drive across reboots and across changes in
	// /dev/srN numbering. Device paths are not stable once a second drive is
	// attached, so this is what the database and config key on.
	StableID string

	// DevicePath is the current /dev/srN. It may differ between boots.
	DevicePath string

	Vendor string
	Model  string
}

// Describe renders the drive for display, e.g. "ASUS SDRW-08D2S-U (/dev/sr0)".
func (d Drive) Describe() string {
	name := strings.TrimSpace(d.Vendor + " " + d.Model)
	if name == "" {
		name = d.StableID
	}
	return fmt.Sprintf("%s (%s)", name, d.DevicePath)
}

// Status reads the drive's current state.
func (d Drive) Status() (Status, error) { return queryStatus(d.DevicePath) }

// Eject opens the tray.
func (d Drive) Eject() error { return eject(d.DevicePath) }

// CloseTray retracts the tray, where the hardware supports it.
func (d Drive) CloseTray() error { return closeTray(d.DevicePath) }

var srPattern = regexp.MustCompile(`^sr[0-9]+$`)

// Discover returns every optical drive currently attached, sorted by stable ID
// so that iteration order is deterministic.
func Discover() ([]Drive, error) {
	names, err := opticalDeviceNames()
	if err != nil {
		return nil, err
	}

	byID := stableIDsByDevice()

	drives := make([]Drive, 0, len(names))
	for _, name := range names {
		devPath := filepath.Join("/dev", name)
		vendor, model := deviceIdentity(name)

		id, ok := byID[name]
		if !ok {
			// No /dev/disk/by-id link. Rare for optical drives but possible on
			// some controllers, so fall back to something still stable across
			// reboots rather than giving up and keying on /dev/srN.
			id = syntheticStableID(name, vendor, model)
		}

		drives = append(drives, Drive{
			StableID:   id,
			DevicePath: devPath,
			Vendor:     vendor,
			Model:      model,
		})
	}

	sort.Slice(drives, func(i, j int) bool { return drives[i].StableID < drives[j].StableID })
	return drives, nil
}

// opticalDeviceNames lists sr* block devices from sysfs, e.g. ["sr0", "sr1"].
func opticalDeviceNames() ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, fmt.Errorf("read /sys/block: %w", err)
	}
	var names []string
	for _, e := range entries {
		if srPattern.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// stableIDsByDevice maps "sr0" to its preferred /dev/disk/by-id name.
func stableIDsByDevice() map[string]string {
	entries, err := os.ReadDir("/dev/disk/by-id")
	if err != nil {
		return nil
	}

	candidates := map[string][]string{}
	for _, e := range entries {
		link := filepath.Join("/dev/disk/by-id", e.Name())
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			continue
		}
		base := filepath.Base(target)
		if srPattern.MatchString(base) {
			candidates[base] = append(candidates[base], e.Name())
		}
	}

	out := make(map[string]string, len(candidates))
	for dev, ids := range candidates {
		out[dev] = preferredID(ids)
	}
	return out
}

// preferredID picks one by-id name when a device has several. Names carrying
// vendor and serial ("usb-ASUS_SDRW-08D2S-U_M2AP1AB5728-0:0") are chosen over
// opaque "wwn-" aliases, because a human reads these in config and logs.
func preferredID(ids []string) string {
	sort.Strings(ids)
	for _, id := range ids {
		if !strings.HasPrefix(id, "wwn-") {
			return id
		}
	}
	return ids[0]
}

// deviceIdentity reads vendor and model strings from sysfs. Both are
// fixed-width and space-padded by the SCSI layer.
func deviceIdentity(name string) (vendor, model string) {
	read := func(attr string) string {
		b, err := os.ReadFile(filepath.Join("/sys/block", name, "device", attr))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return read("vendor"), read("model")
}

// syntheticStableID builds an identifier for a drive with no by-id link. It
// prefers the by-path topology (stable as long as the drive stays in the same
// port) and falls back to vendor and model.
func syntheticStableID(name, vendor, model string) string {
	if entries, err := os.ReadDir("/dev/disk/by-path"); err == nil {
		var paths []string
		for _, e := range entries {
			target, err := filepath.EvalSymlinks(filepath.Join("/dev/disk/by-path", e.Name()))
			if err == nil && filepath.Base(target) == name {
				paths = append(paths, e.Name())
			}
		}
		if len(paths) > 0 {
			sort.Strings(paths)
			return "path-" + paths[0]
		}
	}

	ident := strings.TrimSpace(vendor + "_" + model)
	if ident == "_" || ident == "" {
		return "dev-" + name
	}
	return "hw-" + strings.ReplaceAll(ident, " ", "_")
}

// ReadsBluRay reports whether this drive can read Blu-ray discs.
//
// Read from the model string rather than asked of the drive, because the
// question is only ever "does a missing MakeMKV key matter here" — and being
// wrong costs a health check the wrong colour, not a disc. Optical drive models
// name their highest supported format: a Blu-ray reader says so, a DVD writer
// says DVD.
func (d Drive) ReadsBluRay() bool {
	m := strings.ToUpper(d.Vendor + " " + d.Model)
	for _, s := range []string{"BD-RE", "BD-R", "BD-ROM", "BDRE", "BLU-RAY", "BLURAY", "UHD"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
