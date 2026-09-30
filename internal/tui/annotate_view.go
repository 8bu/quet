package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/annotate"
)

// annotHelpMaxWidth caps the help overlay width.
const annotHelpMaxWidth = 100

// annotFooterMaxRows caps how many rows the wrapped footer may take.
const annotFooterMaxRows = 3

// view draws the whole screen: body, optional status row, footer.
func (m annotModel) view() string {
	w, bh, footer, status := m.layout()
	_, h := m.screenSize()
	if h <= len(footer) {
		return strings.Join(footer[:h], "\n")
	}
	rows := make([]string, 0, h)
	if bh > 0 {
		rows = append(rows, strings.Split(m.body(w, bh), "\n")...)
	}
	if status {
		style := styleStatus
		if m.statusErr {
			style = styleError
		}
		rows = append(rows, truncateLine(style.Render(m.status), w))
	}
	rows = append(rows, footer...)
	if len(rows) > h {
		rows = rows[:h]
	}
	return strings.Join(rows, "\n")
}

// screenSize returns the terminal size, defaulting to 80x24 before the first
// WindowSizeMsg.
func (m annotModel) screenSize() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

// layout splits the screen into the body, the optional status row and the
// wrapped footer rows. It returns the drawing width, the body height, the
// rendered footer rows and whether the status row is shown.
func (m annotModel) layout() (int, int, []string, bool) {
	w, h := m.screenSize()
	footer := wrapFooter(m.footerItems(), w, max(1, min(annotFooterMaxRows, h/4)))
	status := m.status != "" && h >= len(footer)+2
	bh := h - len(footer)
	if status {
		bh--
	}
	return w, max(0, bh), footer, status
}

// bodyDimensions returns the drawing width and the body height (screen minus
// the footer rows and the optional status row).
func (m annotModel) bodyDimensions() (int, int) {
	w, bh, _, _ := m.layout()
	return w, bh
}

// body draws the body area: header and record panels in main and span mode,
// one centred overlay panel in every picker mode.
func (m annotModel) body(w, h int) string {
	switch m.mode {
	case annotTypes:
		rows, sel := m.typesContent(min(w, 64)-2, h-2)
		return overlayBox("Type", w, h, min(w, 64), rows, sel)
	case annotFilter:
		rows, sel := m.filterContent(min(w, 40)-2, h-2)
		return overlayBox("Filter", w, h, min(w, 40), rows, sel)
	case annotHelp:
		ow := min(w, annotHelpMaxWidth)
		return overlayBox("Help", w, h, ow, m.helpContent(ow-2, h-2), -1)
	case annotGoto:
		ow := min(w, 48)
		return overlayBox("Go to", w, h, ow, m.gotoContent(ow-2), -1)
	}
	header := m.headerRows()
	headH := len(header) + 2
	if h < headH+3 {
		return m.recordBox(w, h)
	}
	return box("Quet", false, w, headH, header, -1) + "\n" + m.recordBox(w, h-headH)
}

// overlayBox centres a ow-wide panel holding rows in a w x h area.
func overlayBox(title string, w, h, ow int, rows []string, sel int) string {
	oh := max(0, min(len(rows)+2, h))
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box(title, true, ow, oh, rows, sel))
}

// headerRows renders identity, progress, counts, filter and session speed.
func (m annotModel) headerRows() []string {
	c := m.sess.Counts()
	title := styleTitle.Render("Quet — annotate") + " · " +
		filepath.Base(m.sess.QueuePath()) + " → " + filepath.Base(m.sess.OutPath())
	cur := 0
	if m.sess.Len() > 0 {
		cur = m.sess.Cursor() + 1
	}
	counts := fmt.Sprintf("%d / %d  ·  complete %d · uncertain %d · skipped %d · remaining %d",
		cur, c.Total, c.Complete, c.Uncertain, c.Skipped, c.Remaining)
	if c.Other > 0 {
		counts += fmt.Sprintf(" · other %d", c.Other)
	}
	pos, n := m.sess.FilterPosition()
	posText := "-"
	if pos > 0 {
		posText = fmt.Sprint(pos)
	}
	marked, rate := m.sess.Speed()
	progress := fmt.Sprintf("filter %s %s/%d  ·  session %d · %.1f/min", m.sess.Filter(), posText, n, marked, rate)
	return []string{title, counts, progress}
}

// recordBox draws the record panel: the text (target or selection
// highlighted) followed by the draft's type, target and saved status.
func (m annotModel) recordBox(w, h int) string {
	if m.sess.Len() == 0 {
		return box("Record", true, w, h, []string{"no records"}, -1)
	}
	iw, ih := w-2, h-2
	if w < 4 || h < 3 {
		iw, ih = w, h
	}
	i := m.sess.Cursor()
	item := m.sess.Item(i)
	d := m.sess.Draft(i)
	runes := []rune(item.Text)

	lo, hi, head := -1, -1, -1
	switch {
	case m.mode == annotSpan:
		lo, hi = m.span.bounds()
		head = m.span.head
	case d.Target != nil:
		lo, hi = d.Target.Start, d.Target.End-1
	}
	text, focus := selectionRows(runes, max(iw, 1), lo, hi, head)

	var extras []string
	if m.mode == annotSpan && lo >= 0 && hi < len(runes) {
		extras = append(extras, styleMuted.Render(fmt.Sprintf("selection: %q [%d,%d)", string(runes[lo:hi+1]), lo, hi+1)))
	}
	extras = append(extras, "")
	extras = append(extras, wrapRows(m.metaLines(i, d), max(iw, 1))...)

	if avail := max(1, ih-len(extras)); len(text) > avail {
		start, end := windowRange(len(text), focus, avail)
		text = text[start:end]
	}
	title := fmt.Sprintf("Record %d / %d", i+1, m.sess.Len())
	if l, ok := m.sess.Label(i); ok {
		title += " · " + statusBadge(l.Status)
	}
	return box(title, true, w, h, append(text, extras...), -1)
}

// statusBadge renders a saved annotation status with its mark: ✓ complete,
// ? uncertain, – skipped; other schema statuses by name alone.
func statusBadge(status string) string {
	switch status {
	case annotate.StatusComplete:
		return "✓ " + status
	case annotate.StatusUncertain:
		return "? " + status
	case annotate.StatusSkipped:
		return "– " + status
	}
	return status
}

// gotoContent renders the go-to prompt: the query line and what it accepts.
func (m annotModel) gotoContent(iw int) []string {
	iw = max(iw, 1)
	query := styleAccent.Render("› ") + string(m.gotoQuery)
	if len(m.gotoQuery) == 0 {
		query += styleMuted.Render("position or id")
	}
	hint := fmt.Sprintf("1-%d or an exact record id", m.sess.Len())
	return []string{truncateLine(query, iw), styleMuted.Render(truncateLine(hint, iw))}
}

// metaLines renders the draft type and target, the saved status, the record
// id, the revisit notice of a labeled record and the unsaved-draft marker.
func (m annotModel) metaLines(i int, d annotate.Draft) []string {
	typ := "Type: -"
	if d.Type != "" {
		typ = "Type: " + d.Type
		for _, t := range m.sess.Schema().Types {
			if t.Name == d.Type && t.Description != "" {
				typ += "  " + styleMuted.Render(squeeze(t.Description))
			}
		}
	}
	target := "Target: null"
	if d.Target != nil {
		target = fmt.Sprintf("Target: %q [%d,%d)", d.Target.Text, d.Target.Start, d.Target.End)
	}
	status := "Status: unfinished"
	l, labeled := m.sess.Label(i)
	if labeled {
		status = "Status: " + l.Status
	}
	lines := []string{typ, target, status, styleMuted.Render("ID: " + m.sess.Item(i).ID)}
	if labeled {
		lines = append(lines, styleAccent.Render("Saved: "+l.Status+" — edit with t/x/n, resave with enter/u/s"))
	}
	if m.sess.Dirty(i) {
		lines = append(lines, styleAccent.Render("● unsaved draft — enter/u/s to save, esc to discard"))
	}
	return lines
}

// runeCells returns the display width of r; control characters (newlines,
// tabs) take one cell so a selection over them stays visible.
func runeCells(r rune) int {
	if unicode.IsControl(r) {
		return 1
	}
	return lipgloss.Width(string(r))
}

// wrapRunes splits runes into rows of at most w cells, breaking after the last
// space of a row when possible and after every newline. Each row is a
// [start,end) rune range.
func wrapRunes(runes []rune, w int) [][2]int {
	if w < 1 {
		return nil
	}
	var rows [][2]int
	start, width, brk := 0, 0, -1
	for i, r := range runes {
		cw := runeCells(r)
		if width+cw > w && i > start {
			cut := i
			if brk > start && brk < i {
				cut = brk
			}
			rows = append(rows, [2]int{start, cut})
			start, width, brk = cut, 0, -1
			for _, pr := range runes[cut:i] {
				width += runeCells(pr)
			}
		}
		width += cw
		if unicode.IsSpace(r) {
			brk = i + 1
		}
		if r == '\n' {
			rows = append(rows, [2]int{start, i + 1})
			start, width, brk = i+1, 0, -1
		}
	}
	if start < len(runes) || len(rows) == 0 {
		rows = append(rows, [2]int{start, len(runes)})
	}
	return rows
}

// selectionRows renders runes wrapped to w cells with lo..hi (inclusive)
// highlighted: reversed with an accented head in span mode (head >= 0),
// accented otherwise. It also returns the row to keep visible: the head's,
// else the highlight start's, else 0.
func selectionRows(runes []rune, w, lo, hi, head int) ([]string, int) {
	sel, headStyle := styleAccent, styleAccent
	if head >= 0 {
		sel, headStyle = styleSelected, styleAccent.Reverse(true)
	}
	anchor := head
	if anchor < 0 {
		anchor = lo
	}
	ranges := wrapRunes(runes, w)
	out := make([]string, 0, len(ranges))
	focus := 0
	for ri, rg := range ranges {
		if anchor >= rg[0] && anchor < rg[1] {
			focus = ri
		}
		var b strings.Builder
		var run []rune
		var runStyle *lipgloss.Style
		flush := func() {
			if len(run) == 0 {
				return
			}
			if runStyle == nil {
				b.WriteString(string(run))
			} else {
				b.WriteString(runStyle.Render(string(run)))
			}
			run = run[:0]
		}
		for j := rg[0]; j < rg[1]; j++ {
			var st *lipgloss.Style
			switch {
			case j == head:
				st = &headStyle
			case j >= lo && j <= hi:
				st = &sel
			}
			if st != runStyle {
				flush()
				runStyle = st
			}
			r := runes[j]
			switch {
			case r == '\n' && st == nil:
				continue
			case unicode.IsControl(r):
				r = ' '
			}
			run = append(run, r)
		}
		flush()
		out = append(out, b.String())
	}
	return out, focus
}

// typesContent renders the type picker: query line, numbered matching types,
// then the highlighted type's description.
func (m annotModel) typesContent(iw, maxRows int) ([]string, int) {
	iw, maxRows = max(iw, 1), max(maxRows, 1)
	types := m.sess.Schema().Types
	matches := m.types.matches(types)
	query := styleAccent.Render("› ") + string(m.types.query)
	if len(m.types.query) == 0 {
		query += styleMuted.Render("type to filter")
	}
	rows := []string{query}
	var desc []string
	if c := m.types.cursor; c >= 0 && c < len(matches) {
		if d := squeeze(types[matches[c]].Description); d != "" {
			desc = wrapRows([]string{d}, iw)
			if len(desc) > 3 {
				desc = desc[:3]
			}
		}
	}
	if len(matches) == 0 {
		return append(rows, styleMuted.Render("no matching type")), -1
	}
	current := m.sess.Draft(m.sess.Cursor()).Type
	avail := maxRows - 1
	if len(desc) > 0 {
		avail -= len(desc) + 1
	}
	list, sel := listRows(len(matches), m.types.cursor, max(avail, 1), func(j int) string {
		idx := matches[j]
		num := "  "
		if idx < 9 {
			num = fmt.Sprintf("%d ", idx+1)
		}
		line := num + types[idx].Name
		if types[idx].Name == current {
			line += "  " + styleMuted.Render("(current)")
		}
		return truncateLine(line, iw-2)
	})
	rows, sel = appendList(rows, list, sel)
	if len(desc) > 0 {
		rows = append(rows, "")
		for _, d := range desc {
			rows = append(rows, styleMuted.Render(d))
		}
	}
	return rows, sel
}

// filterContent renders the filter picker with each filter's record count.
func (m annotModel) filterContent(iw, maxRows int) ([]string, int) {
	iw, maxRows = max(iw, 1), max(maxRows, 1)
	filters := annotate.Filters()
	return listRows(len(filters), m.filters.cursor, maxRows, func(i int) string {
		count := 0
		if i < len(m.filters.counts) {
			count = m.filters.counts[i]
		}
		line := fmt.Sprintf("%-11s %d", filters[i], count)
		if filters[i] == m.sess.Filter() {
			line += "  " + styleMuted.Render("(active)")
		}
		return truncateLine(line, iw-2)
	})
}

// helpLayout returns the help overlay's inner width and row budget for the
// current screen.
func (m annotModel) helpLayout() (int, int) {
	w, h := m.bodyDimensions()
	return max(min(w, annotHelpMaxWidth)-2, 1), max(h-2, 1)
}

// helpMaxScroll returns the largest useful help scroll offset.
func (m annotModel) helpMaxScroll() int {
	iw, maxRows := m.helpLayout()
	return max(0, len(helpRows(annotHelpGroups(), iw, maxRows))-maxRows)
}

// helpContent returns the visible window of the help reference.
func (m annotModel) helpContent(iw, maxRows int) []string {
	iw, maxRows = max(iw, 1), max(maxRows, 1)
	rows := helpRows(annotHelpGroups(), iw, maxRows)
	off := max(0, min(m.helpScroll, len(rows)-maxRows))
	return rows[off:min(len(rows), off+maxRows)]
}

// annotHelpGroups returns the annotation keyboard reference.
func annotHelpGroups() []helpGroup {
	return []helpGroup{
		{"Label", []helpItem{
			{"enter", "Complete"},
			{"u", "Uncertain"},
			{"s", "Skip"},
			{"t", "Type picker"},
			{"1-9", "Set type N"},
			{"x", "Select target span"},
			{"n", "Null target"},
			{"esc", "Discard draft"},
			{"z", "Undo last save"},
			{"revise", "Edit, then enter/u/s"},
		}},
		{"Navigate", []helpItem{
			{"a/h/←", "Previous, any status"},
			{"d/l/→", "Next, any status"},
			{"[/]", "Prev/next in filter"},
			{"g/G", "First/last record"},
			{": #", "Go to position or id"},
			{"f", "Filter"},
		}},
		{"Target span", []helpItem{
			{"h/l/←/→", "Move one character"},
			{"w/b", "Next/previous word"},
			{"H/L", "Head left/right"},
			{"shift+←/→", "Head left/right"},
			{"W/B", "Extend to word"},
			{"0/home", "First character"},
			{"$/end", "Last character"},
			{"enter", "Accept target"},
			{"n", "Null target"},
			{"esc", "Cancel"},
		}},
		{"Type picker", []helpItem{
			{"type", "Fuzzy filter"},
			{"↑/↓ ctrl+p/n", "Move"},
			{"j/k", "Move if no query"},
			{"1-9", "Pick type N"},
			{"backspace", "Edit query"},
			{"enter", "Select"},
			{"esc", "Cancel"},
		}},
		{"Other", []helpItem{
			{"?", "Help"},
			{"q", "Quit; twice if unsaved"},
			{"ctrl+c", "Quit now"},
			{"j/k", "Scroll help"},
		}},
	}
}

// footerItems returns the context shortcuts for the current mode, one
// "key desc" item each.
func (m annotModel) footerItems() []string {
	switch m.mode {
	case annotSpan:
		return []string{"h/l move", "w/b word", "H/L grow", "W/B extend", "0/$ ends", "enter accept", "n null", "esc cancel", "? help"}
	case annotTypes:
		return []string{"type to filter", "↑/↓ move", "1-9 pick", "enter select", "esc cancel"}
	case annotFilter:
		return []string{"j/k move", "enter apply", "esc close"}
	case annotHelp:
		return []string{"j/k scroll", "esc close"}
	case annotGoto:
		return []string{"type position or id", "enter go", "esc cancel"}
	default:
		return []string{"t type", "x target", "n null", "enter complete", "u uncertain", "s skip", "a/d prev/next",
			"[/] prev/next " + m.sess.Filter().String(), "z undo", ": go to", "f filter", "? help", "q quit"}
	}
}

// wrapFooter lays items out in rows of at most w cells, breaking only between
// items (two-space separators). Items beyond maxRows share the last row, which
// is then truncated, as is any single item wider than w.
func wrapFooter(items []string, w, maxRows int) []string {
	var lines []string
	cur := ""
	for _, it := range items {
		switch {
		case cur == "":
			cur = it
		case len(lines)+1 < maxRows && lipgloss.Width(cur)+2+lipgloss.Width(it) > w:
			lines = append(lines, cur)
			cur = it
		default:
			cur += "  " + it
		}
	}
	lines = append(lines, cur)
	for i, l := range lines {
		lines[i] = styleFooter.Render(truncateLine(l, w))
	}
	return lines
}
