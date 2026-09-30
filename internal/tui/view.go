package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// view draws the whole screen: body, optional status row (transient status or
// update notice), footer.
func (m model) view() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	footer := truncateLine(styleFooter.Render(m.footer()), w)
	if h <= 1 {
		return footer
	}
	rows := make([]string, 0, h)
	if bw, bh := m.bodyDimensions(); bh > 0 {
		rows = append(rows, strings.Split(m.body(bw, bh), "\n")...)
	}
	if text, style := m.statusText(); text != "" && len(rows)+1 < h {
		rows = append(rows, truncateLine(style.Render(text), w))
	}
	rows = append(rows, footer)
	if len(rows) > h {
		rows = rows[:h]
	}
	return strings.Join(rows, "\n")
}

// body draws the body area: the three panels in review/edit mode, one overlay
// panel in every picker mode.
func (m model) body(w, h int) string {
	switch m.mode {
	case ModeReview, ModeEdit:
		return m.panels(w, h)
	default:
		return m.overlay(w, h)
	}
}

// panels lays out the corpus column and the record/details column.
func (m model) panels(w, h int) string {
	l := computeLayout(w, h)
	leftRows, leftSel := m.corpusContent(l.leftW, h)
	left := box(panelCorpus.title(), m.focus == panelCorpus, l.leftW, h, leftRows, leftSel)

	recRows, recSel := m.recordContent(l.rightW, l.recordH)
	if m.mode == ModeReview {
		l = l.fitRecord(len(recRows))
	}
	right := box(panelRecord.title(), m.focus == panelRecord, l.rightW, l.recordH, recRows, recSel)
	if l.detailsH > 0 {
		detRows, detSel := m.detailsContent(l.rightW, l.detailsH)
		right = lipgloss.JoinVertical(lipgloss.Left, right,
			box(panelDetails.title(), m.focus == panelDetails, l.rightW, l.detailsH, detRows, detSel))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// overlay centres a picker panel in the body area.
func (m model) overlay(w, h int) string {
	ow := w
	if ow > 64 {
		ow = 64
	}
	title, rows, sel := m.overlayContent(ow-2, h-2)
	oh := len(rows) + 2
	if oh > h {
		oh = h
	}
	if oh < 0 {
		oh = 0
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box(title, true, ow, oh, rows, sel))
}

// overlayContent builds the rows of the overlay panel for the current mode.
func (m model) overlayContent(iw, maxRows int) (string, []string, int) {
	if iw < 1 {
		iw = 1
	}
	if maxRows < 1 {
		maxRows = 1
	}
	switch m.mode {
	case ModeHelp:
		return "Help", helpRows(helpGroups(), iw, maxRows), -1
	case ModePalette:
		rows, sel := m.paletteContent(iw, maxRows)
		return "Commands", rows, sel
	case ModeSearch:
		rows, sel := m.searchContent(iw, maxRows)
		return "Search", rows, sel
	case ModeFilter:
		rows, sel := m.filterContent(iw, maxRows)
		return "Filter", rows, sel
	case ModeFlags:
		rows, sel := m.flagsContent(iw, maxRows)
		return "Flags", rows, sel
	case ModeDuplicates:
		rows, sel := m.duplicatesContent(iw, maxRows)
		return "Duplicates", rows, sel
	case ModeExport:
		rows, sel := m.exportContent(iw, maxRows)
		return "Export", rows, sel
	case ModeCreateFlags:
		return "Flags file", wrapRows([]string{fmt.Sprintf("Create %s with starter flags? y/n", m.createPath)}, iw), -1
	}
	return "Quet", nil, -1
}

// corpusContent renders the Quet status panel: identity, position, counters,
// active filter, rate, then the selectable filter/facet list.
func (m model) corpusContent(w, h int) ([]string, int) {
	if w < 2 || h < 2 {
		return nil, -1
	}
	iw, ih := w-2, h-2
	c := m.sess.Counts()
	pos := 0
	if cur := m.sess.Current(); cur >= 0 {
		pos = cur + 1
	}
	rows := []string{
		styleAccent.Render("Quet"),
		"Quick Utility for",
		"Evaluating Text",
		"",
		fmt.Sprintf("%d / %d", pos, m.sess.Len()),
		counterLine("✓ Approved", c.Approved, iw),
		counterLine("✗ Rejected", c.Rejected, iw),
		counterLine("? Review", c.NeedsReview, iw),
		counterLine("· Unreviewed", c.Unreviewed, iw),
		"Filter: " + m.sess.Filter().String(),
		fmt.Sprintf("%.1f rec/min", m.sess.Rate(time.Now())),
	}
	if avail := ih - len(rows) - 1; avail >= 1 && len(m.facets.rows) > 0 {
		rows = append(rows, "")
		list, sel := listRows(len(m.facets.rows), m.facets.cursor, avail, func(i int) string {
			r := m.facets.rows[i]
			return truncateLine(fmt.Sprintf("%s  %d", r.label, r.count), iw-4)
		})
		return appendList(rows, list, sel)
	}
	return rows, -1
}

// recordContent renders the current record's text (or the editor in ModeEdit).
func (m model) recordContent(w, h int) ([]string, int) {
	if w < 2 || h < 2 {
		return nil, -1
	}
	if m.mode == ModeEdit {
		return strings.Split(m.editor.View(), "\n"), -1
	}
	cur := m.sess.Current()
	if cur < 0 {
		return []string{styleMuted.Render("No records")}, -1
	}
	text := m.sess.FinalText(cur)
	if text == "" {
		text = "(empty)"
	}
	return wrapRows([]string{text}, w-2), -1
}

// detailsContent renders the current record's metadata, flags and diagnostics.
func (m model) detailsContent(w, h int) ([]string, int) {
	if w < 2 || h < 2 {
		return nil, -1
	}
	cur := m.sess.Current()
	if cur < 0 {
		return []string{styleMuted.Render("No records")}, -1
	}
	r := m.sess.Record(cur)
	if r == nil {
		return []string{styleMuted.Render("No records")}, -1
	}
	st := m.sess.State(cur)

	rows := []string{"id: " + r.ID, metaLine("source", r.Source), metaLine("batch", r.Batch)}
	rows = append(rows, "manual: "+listOrDash(st.ManualFlags))
	rows = append(rows, "suggested: "+listOrDash(m.sess.SuggestedFlags(cur)))
	if diags := m.sess.Diagnostics(cur); len(diags) > 0 {
		names := make([]string, 0, len(diags))
		for _, d := range diags {
			names = append(names, d.Name)
		}
		rows = append(rows, "diagnostics: "+strings.Join(names, ", "))
	}
	if st.Edited() {
		rows = append(rows, "edited")
	}
	if dups := m.sess.Duplicates(cur); len(dups) > 0 {
		rows = append(rows, fmt.Sprintf("duplicates: %d other record(s)", len(dups)))
	}
	return wrapRows(rows, w-2), -1
}

// paletteContent renders the command palette: query line, then matches.
func (m model) paletteContent(iw, maxRows int) ([]string, int) {
	rows := []string{truncateLine(m.pal.input.View(), iw), ""}
	cmds := m.paletteFiltered()
	if len(cmds) == 0 {
		rows = append(rows, styleMuted.Render("no matching commands"))
		return rows, -1
	}
	list, sel := listRows(len(cmds), m.pal.cursor, maxRows-len(rows), func(i int) string {
		c := cmds[i]
		if c.keys == "" {
			return truncateLine(c.name, iw-2)
		}
		return truncateLine(fmt.Sprintf("%-34s %s", c.name, c.keys), iw-2)
	})
	return appendList(rows, list, sel)
}

// searchContent renders the search box and live results.
func (m model) searchContent(iw, maxRows int) ([]string, int) {
	rows := []string{truncateLine(m.search.input.View(), iw), ""}
	if len(m.search.results) == 0 {
		msg := "type to search"
		if strings.TrimSpace(m.search.input.Value()) != "" {
			msg = "no matches"
		}
		rows = append(rows, styleMuted.Render(msg))
		return rows, -1
	}
	list, sel := m.searchListRows(maxRows-len(rows), iw)
	return appendList(rows, list, sel)
}

// filterContent renders the filter picker list.
func (m model) filterContent(iw, maxRows int) ([]string, int) {
	list, sel := m.filterListRows(maxRows, iw)
	if len(list) == 0 {
		return []string{styleMuted.Render("no filters available")}, -1
	}
	return list, sel
}

// flagsContent renders the manual flag picker plus the selected flag's
// description.
func (m model) flagsContent(iw, maxRows int) ([]string, int) {
	avail := maxRows - 3
	if avail < 1 {
		avail = maxRows
	}
	list, sel := m.flagListRows(avail, iw)
	if len(list) == 0 {
		return []string{styleMuted.Render("no manual flags defined")}, -1
	}
	rows := append([]string(nil), list...)
	if r, ok := m.flags.current(); ok && maxRows-len(rows) >= 2 {
		desc := r.desc
		if desc == "" {
			desc = "(no description)"
		}
		rows = append(rows, "")
		rows = append(rows, styleMuted.Render("Description:"))
		rows = append(rows, wrapRows([]string{desc}, iw)...)
	}
	return rows, sel
}

// duplicatesContent renders the duplicate group of the current record.
func (m model) duplicatesContent(iw, maxRows int) ([]string, int) {
	list, sel := m.dupListRows(maxRows, iw)
	if len(list) == 0 {
		return []string{styleMuted.Render("no duplicates for this record")}, -1
	}
	return list, sel
}

// exportContent renders the export presets and the editable output path.
func (m model) exportContent(iw, maxRows int) ([]string, int) {
	avail := maxRows - 3
	if avail < 1 {
		avail = maxRows
	}
	list, sel := m.exportListRows(avail, iw)
	if len(list) == 0 {
		return []string{styleMuted.Render("no export presets available")}, -1
	}
	rows := append([]string(nil), list...)
	rows = append(rows, "")
	rows = append(rows, truncateLine(m.exp.path.View(), iw))
	if m.exp.pending {
		rows = append(rows, styleStatus.Render("Overwrite? y/n"))
	}
	return rows, sel
}

// appendList appends a rendered list to rows, shifting the selected index.
func appendList(rows, list []string, sel int) ([]string, int) {
	if sel >= 0 {
		sel += len(rows)
	}
	return append(rows, list...), sel
}

// footer returns the context shortcuts for the current mode.
func (m model) footer() string {
	switch m.mode {
	case ModeEdit:
		return "ctrl+s save  esc cancel  ctrl+r revert"
	case ModeFlags:
		return "j/k move  space toggle  enter apply  esc close"
	case ModePalette, ModeSearch, ModeDuplicates:
		return "esc close  enter select"
	case ModeFilter:
		return "esc close  enter apply"
	case ModeExport:
		return "enter export  tab edit path  esc close"
	case ModeHelp:
		return "esc close"
	case ModeCreateFlags:
		return "y create  n/esc cancel"
	default:
		return "w approve  s reject  a/d navigate  e edit  f flags  space review  ? help"
	}
}
