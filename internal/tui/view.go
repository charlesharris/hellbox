package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"hellbox/internal/proto"
)

// Colours are chosen to survive both light and dark terminals, and no
// information is ever carried by colour alone — every state prints its name.
var (
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleHead    = lipgloss.NewStyle().Bold(true)
	styleLabel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleBar     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleSel     = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Bold(true)
	styleBanner  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	styleBannerW = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
)

func (m Model) View() string {
	var b strings.Builder

	b.WriteString(m.header())
	b.WriteString("\n")
	b.WriteString(m.banner())

	switch m.view {
	case viewHistory:
		b.WriteString(m.historyView())
	case viewLog:
		b.WriteString(m.logView())
	case viewQueue:
		b.WriteString(m.queueView())
	case viewFailures:
		b.WriteString(m.failuresView())
	default:
		b.WriteString(m.drivesView())
	}

	b.WriteString(m.transcodeLine())
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

// transcodeLine reports the transcode queue.
//
// Transcoding is not a property of any drive — it reads files and needs none —
// so it gets its own line rather than a column in the drive table. It is shown
// only when there is something to say, so an idle queue costs no space.
func (m Model) transcodeLine() string {
	t := m.status.Transcode
	if !t.Running && t.Pending == 0 && t.Failed == 0 {
		return ""
	}

	var parts []string
	if t.Running {
		how := "sw"
		if t.Hardware {
			how = "gpu"
		}
		line := fmt.Sprintf("transcoding %s title %d  %s %3.0f%%",
			t.Disc, t.TitleIndex, progressBar(t.Fraction, 20), t.Fraction*100)
		if t.Speed > 0 {
			line += fmt.Sprintf("  %.0fx %s", t.Speed, how)
		}
		parts = append(parts, line)
	}
	if t.Pending > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", t.Pending))
	}
	if t.Failed > 0 {
		parts = append(parts, styleErr.Render(fmt.Sprintf("%d failed", t.Failed)))
	}
	return "\n" + styleDim.Render("  ") + strings.Join(parts, styleDim.Render(" · ")) + "\n"
}

func (m Model) header() string {
	left := styleHead.Render("hellbox " + m.version)

	var right string
	switch {
	case !m.connected:
		right = styleErr.Render("disconnected")
	case !m.haveAny:
		right = styleDim.Render("waiting for status")
	default:
		drives := fmt.Sprintf("%d drive%s", len(m.drives), plural(len(m.drives)))
		right = styleDim.Render(fmt.Sprintf("%s · %d disc%s ripped · %s free",
			drives, m.status.DiscsRipped, plural(m.status.DiscsRipped),
			humanBytes(int64(m.status.FreeBytes))))
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right + "\n"
}

// banner renders health problems persistently rather than as a transient
// message. An expired MakeMKV key is the single likeliest cause of a mystery
// failure, and it must be impossible to miss.
func (m Model) banner() string {
	var lines []string
	for _, h := range m.status.Health {
		if h.OK {
			continue
		}
		style, mark := styleBannerW, "warning"
		if h.Fatal {
			style, mark = styleBanner, "PROBLEM"
		}
		lines = append(lines, style.Render(fmt.Sprintf(" %s  %s: %s", mark, h.Name, h.Detail)))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n\n"
}

func (m Model) drivesView() string {
	if !m.haveAny {
		return styleDim.Render("\n  connecting to hellboxd…\n")
	}
	if len(m.drives) == 0 {
		return styleWarn.Render("\n  no optical drives detected\n")
	}

	var b strings.Builder
	b.WriteString("\n")
	for i, d := range m.drives {
		b.WriteString(m.drivePanel(d, i == m.selected))
		b.WriteString("\n")
	}
	return b.String()
}

// drivePanel renders one drive as two lines: what is in it, and what is
// happening to it.
func (m Model) drivePanel(d proto.DriveSnapshot, selected bool) string {
	marker := "  "
	label := styleLabel.Render(pad(d.Label, 8))
	if selected {
		marker = styleSel.Render("▸ ")
		label = styleSel.Render(pad(d.Label, 8))
	}

	// Truncated while still plain text: truncate counts runes, and styling wraps
	// the string in escapes that are not displayed but would be counted.
	disc, discDim := d.DiscLabel, false
	if disc == "" {
		disc, discDim = discPlaceholder(d.State), true
	}
	disc = truncate(disc, max(10, m.width-26))
	if discDim {
		disc = styleDim.Render(disc)
	}

	first := fmt.Sprintf("%s%s %s  %s", marker, label,
		styleDim.Render(pad(d.DevicePath, 10)), disc)

	// Padded before styling: pad counts runes, and a styled string carries ANSI
	// escapes that are not displayed but would be counted.
	second := "     " + stateStyle(d.State).Render(pad(strings.ToUpper(string(d.State)), 13)) + "  "

	switch {
	case d.State == proto.StateDecrypting, d.State == proto.StateRipping, d.State == proto.StateVerifying:
		detail := d.Operation
		if d.TitleCount > 0 && d.CurrentTitle >= 0 {
			detail = fmt.Sprintf("title %d of %d", d.TitlesDone+1, d.TitleCount)
		}
		second += pad(detail, 18) + progressBar(d.Fraction, 20)
		second += fmt.Sprintf(" %3.0f%%", d.Fraction*100)
		if d.ETASeconds > 0 {
			second += styleDim.Render(fmt.Sprintf("   ~%s left", shortDuration(time.Duration(d.ETASeconds)*time.Second)))
		}

	case d.Error != "":
		second += styleErr.Render(truncate(d.Error, max(20, m.width-30)))

	default:
		second += styleDim.Render(stateHint(d.State))
	}

	return first + "\n" + second + "\n"
}

// discPlaceholder describes an absent disc in the drive's own terms.
func discPlaceholder(s proto.DriveState) string {
	switch s {
	case proto.StateTrayOpen:
		return "(tray open)"
	case proto.StateAbsent:
		return "(drive not responding)"
	case proto.StateIncompatible:
		return "(unreadable disc)"
	default:
		return "(empty)"
	}
}

// stateHint says what the drive is waiting for, so a resting drive never looks
// like a stuck one.
func stateHint(s proto.DriveState) string {
	switch s {
	case proto.StateEmpty:
		return "waiting for a disc"
	case proto.StateTrayOpen:
		return "ready for the next disc"
	case proto.StateLoading:
		return "spinning up"
	case proto.StateCancelled:
		return "cancelled; eject and reinsert the disc to try again"
	case proto.StateDecrypting:
		return "the drive cannot decrypt this disc; copying it to disk first"
	case proto.StateQueued:
		return "waiting for another drive to finish; one disc is worked on at a time"
	case proto.StateScanning:
		return "reading the disc structure"
	case proto.StateComplete:
		return "done — safe to reshelve"
	case proto.StateDuplicate:
		return "already ripped; nothing to do"
	case proto.StateIncompatible:
		return "swap the disc — this drive cannot read it"
	case proto.StateFailed:
		return "retained in the drive; press r to retry"
	case proto.StateAbsent:
		return "unplugged, or dropped by the kernel"
	}
	return ""
}

func stateStyle(s proto.DriveState) lipgloss.Style {
	switch s {
	case proto.StateComplete, proto.StateDuplicate:
		return styleOK
	case proto.StateFailed, proto.StateAbsent:
		return styleErr
	case proto.StateIncompatible:
		return styleWarn
	case proto.StateRipping, proto.StateScanning, proto.StateVerifying:
		return styleBar
	}
	return styleDim
}

func progressBar(fraction float64, width int) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction * float64(width))
	return styleBar.Render("["+strings.Repeat("█", filled)) +
		styleDim.Render(strings.Repeat("░", width-filled)) + styleBar.Render("]")
}

func (m Model) historyView() string {
	if len(m.jobs) == 0 {
		return styleDim.Render("\n  no jobs yet\n")
	}
	var b strings.Builder
	b.WriteString("\n" + styleHead.Render("  history") + "\n")
	for _, j := range m.jobs {
		if len(b.String()) > 0 && strings.Count(b.String(), "\n") > m.height-8 {
			break
		}
		when := j.CreatedAt.Local().Format("Jan 02 15:04")
		label := j.VolumeLabel
		if label == "" {
			label = "unlabelled " + shortFingerprint(j.Fingerprint)
		}
		// Only the variable-length label is truncated; the fixed columns before
		// it are already known to fit, and truncating the composed line would
		// count escape sequences as visible width.
		label = truncate(label, max(10, m.width-46))
		if j.TitlesTotal > 0 {
			label += fmt.Sprintf("  (%d/%d titles)", j.TitlesDone, j.TitlesTotal)
		}
		b.WriteString(fmt.Sprintf("   %s  %s %s  %s\n",
			styleDim.Render(when), jobStateStyle(j.State).Render(pad(j.State, 10)),
			pad(j.DriveLabel, 8), label))
		if j.Error != "" {
			b.WriteString("         " + styleErr.Render(truncate(j.Error, max(10, m.width-10))) + "\n")
		}
	}
	return b.String()
}

func jobStateStyle(s string) lipgloss.Style {
	switch s {
	case "complete":
		return styleOK
	case "failed", "cancelled":
		return styleErr
	}
	return styleDim
}

func (m Model) logView() string {
	if len(m.events) == 0 {
		return styleDim.Render("\n  nothing logged yet\n")
	}
	var b strings.Builder
	b.WriteString("\n" + styleHead.Render("  log") + "\n")

	// The daemon returns events newest-first, and they are rendered in that
	// order, so the head of the slice is what to keep when the screen is short.
	limit := max(1, m.height-8)
	shown := m.events
	if len(shown) > limit {
		shown = shown[:limit]
	}
	for _, e := range shown {
		style := styleDim
		switch e.Level {
		case "warn":
			style = styleWarn
		case "error":
			style = styleErr
		}
		b.WriteString(fmt.Sprintf("   %s  %s %s\n",
			styleDim.Render(e.At.Local().Format("15:04:05")),
			style.Render(pad(e.Level, 5)),
			truncate(e.Message, max(10, m.width-22))))
	}
	return b.String()
}

// failuresView lists discs that did not make it through, grouped by why.
//
// Grouped because the count is the point: "three discs failed" says nothing to
// act on, while "three discs were not in the AACS key database" says to update
// the database, and "five were cancelled" says nothing is wrong at all.
func (m Model) failuresView() string {
	if len(m.failures) == 0 {
		return "\n" + styleDim.Render("  nothing has failed") + "\n"
	}

	counts := map[string]int{}
	advice := map[string]string{}
	for _, f := range m.failures {
		counts[f.Kind]++
		if f.Advice != "" {
			advice[f.Kind] = f.Advice
		}
	}

	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if counts[kinds[i]] != counts[kinds[j]] {
			return counts[kinds[i]] > counts[kinds[j]]
		}
		return kinds[i] < kinds[j]
	})

	var b strings.Builder
	b.WriteString("\n" + styleHead.Render("  failures") + "\n")
	for _, k := range kinds {
		style := styleErr
		if k == "cancelled" {
			// Not a failure. Shown because it arrives through the same field,
			// and dimmed so it does not read as a problem.
			style = styleDim
		}
		line := fmt.Sprintf("   %s %-16s %s", style.Render(pad(k, 15)), fmt.Sprintf("%d", counts[k]), advice[k])
		b.WriteString(truncate(line, m.width) + "\n")
	}

	b.WriteString("\n")
	for i, f := range m.failures {
		if i > m.height-14 {
			b.WriteString(styleDim.Render(fmt.Sprintf("   … and %d more\n", len(m.failures)-i)))
			break
		}
		label := f.VolumeLabel
		if label == "" {
			label = "unlabelled " + shortFingerprint(f.Fingerprint)
		}
		line := fmt.Sprintf("   %s  %-15s %-20s %s",
			f.When.Local().Format("Jan 02 15:04"), f.Kind, truncate(label, 20), f.Error)
		b.WriteString(styleDim.Render(truncate(line, m.width)) + "\n")
	}
	return b.String()
}

// queueView lists the transcode queue.
//
// Waiting and running work sorts first, because a queue is asked "what is left"
// far more often than "what happened". Finished entries stay visible below it,
// since the other question it gets is whether a disc actually came out the far
// end — and a transcode that finished without reaching the library is exactly
// the case that would otherwise be invisible.
func (m Model) queueView() string {
	if len(m.queue) == 0 {
		return "\n" + styleDim.Render("  the transcode queue is empty") + "\n"
	}

	var b strings.Builder
	b.WriteString("\n")
	for i, j := range m.queue {
		marker := "  "
		if i == m.selected {
			marker = styleSel.Render("▸ ")
		}

		state := j.State
		style := styleDim
		switch j.State {
		case "running":
			style = styleBar
			if t := m.status.Transcode; t.Running {
				state = fmt.Sprintf("running %3.0f%%", t.Fraction*100)
			}
		case "pending":
			style = styleWarn
		case "failed":
			style = styleErr
		case "complete":
			style = styleOK
			if !j.Filed {
				// Finished but never reached the library. Without saying so the
				// file simply never appears and nothing explains why.
				state, style = "not filed", styleWarn
			}
		}

		size := ""
		if j.SizeBytes > 0 {
			size = humanBytes(j.SizeBytes)
		}

		line := fmt.Sprintf("%s%-22s t%02d  %-14s %10s",
			marker, truncate(j.Disc, 22), j.TitleIndex, style.Render(pad(state, 14)), size)
		b.WriteString(truncate(line, m.width) + "\n")

		if j.Error != "" && i == m.selected {
			b.WriteString(styleDim.Render(truncate("      "+j.Error, m.width)) + "\n")
		}
	}
	return b.String()
}

func (m Model) footer() string {
	keys := "d drives · h history · l log · t queue · x failures · e eject · c cancel · r retry · f forget · T re-encode · q quit"
	if m.view == viewQueue {
		keys = "d drives · t queue · ↑↓ select · c cancel running · r retry · q quit"
	}

	var status string
	switch {
	case m.flash != "" && m.flashErr:
		status = styleErr.Render(truncate(m.flash, m.width))
	case m.flash != "":
		status = styleOK.Render(truncate(m.flash, m.width))
	case m.lastErr != "":
		status = styleDim.Render(truncate(m.lastErr, m.width))
	}

	out := styleDim.Render(truncate(keys, m.width))
	if status != "" {
		out += "\n" + status
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func shortFingerprint(f string) string {
	if len(f) > 12 {
		return f[:12]
	}
	return f
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	if h := int(d.Hours()); h > 0 {
		return fmt.Sprintf("%dh%02dm", h, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// humanBytes matches the daemon's formatting exactly, base 1024 included. The
// same number reported two different ways by `hellboxd -check` and by slay
// would read as a bug in one of them.
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
