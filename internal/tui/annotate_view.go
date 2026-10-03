package tui

import (
	"fmt"
	"path/filepath"
	"slices"
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

// headerRows renders identity, progress, counts, filter and session speed; a re-check
// session also shows how many labels the canonical file holds and how many are in the queue.
func (m annotModel) headerRows() []string {
	c := m.sess.Counts()
	recheck := m.sess.Recheck()
	name := "Quet — annotate"
	if recheck {
		name = "Quet — re-check"
	}
	title := styleTitle.Render(name) + " · " +
		filepath.Base(m.sess.QueuePath()) + " → " + filepath.Base(m.sess.OutPath())
	cur := 0
	if m.sess.Len() > 0 {
		cur = m.sess.Cursor() + 1
	}
	position := fmt.Sprintf("%d / %d", cur, c.Total)
	if recheck {
		position = fmt.Sprintf("Re-check %d / %d", cur, m.sess.Len())
	}
	counts := fmt.Sprintf("%s  ·  complete %d · uncertain %d · skipped %d · remaining %d",
		position, c.Complete, c.Uncertain, c.Skipped, c.Remaining)
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
	if recheck {
		labels := fmt.Sprintf("Labels: %d total · %d in current queue", m.sess.LabelsTotal(), m.sess.LabelsInQueue())
		rows := []string{title, counts, labels}
		if m.sess.HasProposals() {
			rows = append(rows, m.proposalHeader())
		}
		return append(rows, progress)
	}
	if m.sess.HasProposals() {
		return []string{title, counts, m.proposalHeader(), progress}
	}
	return []string{title, counts, progress}
}

// proposalHeader renders the header row of a session with proposals: how many apply
// to the queue and how many ids were ignored.
func (m annotModel) proposalHeader() string {
	return styleProposal.Render(proposalSummary(m.sess))
}

// styleProposal renders advisory proposal text: italic and in its own colour, so it is
// never mistaken for the saved annotation.
var styleProposal = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("141"))

// proposalLines renders record i's proposal block: a title saying it is not accepted,
// the suggested type, span fields (with their statuses), status, confidence, reason
// and note, and whether it matches the saved label or is invalid under the schema. It
// is nil without a proposal.
func (m annotModel) proposalLines(i, iw int) []string {
	p, ok := m.sess.Proposal(i)
	if !ok {
		return nil
	}
	typ := "null"
	if p.Type != nil {
		typ = *p.Type
	}
	confidence := "-"
	if p.Confidence != nil {
		confidence = fmt.Sprintf("%.2f", *p.Confidence)
	}
	lines := []string{"Type: " + typ}
	for _, sp := range m.sess.Schema().Spans {
		lines = append(lines, capitalize(sp.Name)+": "+spanValue(p.Spans[sp.Name])+m.proposalSpanStatus(p, sp))
	}
	lines = append(lines, "Status: "+p.Status, "Confidence: "+confidence)
	if p.Reason != "" {
		lines = append(lines, "Reason: "+squeeze(p.Reason))
	}
	if p.Note != "" {
		lines = append(lines, "Note: "+squeeze(p.Note))
	}
	if err := m.sess.ProposalProblem(i); err != nil {
		lines = append(lines, "⚠ invalid: "+squeeze(err.Error()))
	} else if m.sess.ProposalMatches(i) {
		lines = append(lines, "✓ matches current")
	}
	rows := []string{styleProposal.Bold(true).Render("Proposal — not accepted")}
	for _, row := range wrapRows(lines, iw) {
		rows = append(rows, styleProposal.Render(row))
	}
	return rows
}

// proposalSpanStatus returns the ` · status` suffix of span sp in proposal p: its
// suggested status, else the default accepting it would save. It is empty for a span
// without statuses, one that is null for the proposed type and a null-label status.
func (m annotModel) proposalSpanStatus(p annotate.Proposal, sp annotate.SpanDef) string {
	typ := ""
	if p.Type != nil {
		typ = *p.Type
	}
	schema := m.sess.Schema()
	if len(sp.Statuses) == 0 || schema.NullSpan(sp.Name, typ) || schema.NullLabel(p.Status) {
		return ""
	}
	if st := p.SpanStatus[sp.Name]; st != "" {
		return " · " + st
	}
	return " · " + sp.Statuses[0] + " (default)"
}

// recordBox draws the record panel: the text (span fields or the live selection
// highlighted) followed by the draft's type, span fields and saved status. When the
// record has a proposal, the draft section is titled Current and a distinct,
// advisory Proposal block follows it.
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
	spans := m.sess.Schema().Spans
	active := spans[m.activeSpan]

	// Highlights: the other fields in their colours, then the active one on top
	// (in span mode the live selection replaces its highlight).
	var marks []spanMark
	for k, sp := range spans {
		if t := d.Spans[sp.Name]; k != m.activeSpan && t != nil {
			marks = append(marks, spanMark{t.Start, t.End - 1, spanStyle(k)})
		}
	}
	lo, hi, head, anchor := -1, -1, -1, -1
	var headStyle lipgloss.Style
	switch t := d.Spans[active.Name]; {
	case m.mode == annotSpan:
		lo, hi = m.span.bounds()
		head = m.span.head
		headStyle = spanStyle(m.activeSpan).Reverse(true)
		marks = append(marks, spanMark{lo, hi, styleSelected})
	case t != nil:
		anchor = t.Start
		marks = append(marks, spanMark{t.Start, t.End - 1, spanStyle(m.activeSpan)})
	case len(marks) > 0:
		anchor = marks[0].lo
	}
	text, focus := selectionRows(runes, max(iw, 1), marks, head, headStyle, anchor)

	var extras []string
	if m.mode == annotSpan && lo >= 0 && hi < len(runes) {
		label := "selection"
		if m.multiSpan() {
			label += " (" + active.Name + ")"
		}
		extras = append(extras, styleMuted.Render(fmt.Sprintf("%s: %q [%d,%d)", label, string(runes[lo:hi+1]), lo, hi+1)))
	}
	extras = append(extras, "")
	if prop := m.proposalLines(i, max(iw, 1)); len(prop) > 0 {
		extras = append(extras, styleTitle.Render("Current"))
		extras = append(extras, wrapRows(m.metaLines(i, d), max(iw, 1))...)
		extras = append(extras, "")
		extras = append(extras, prop...)
	} else {
		extras = append(extras, wrapRows(m.metaLines(i, d), max(iw, 1))...)
	}

	if avail := max(1, ih-len(extras)); len(text) > avail {
		start, end := windowRange(len(text), focus, avail)
		text = text[start:end]
	}
	title := fmt.Sprintf("Record %d / %d", i+1, m.sess.Len())
	if l, ok := m.sess.Label(i); ok {
		title += " · " + statusBadge(l.Status)
	}
	if m.mode == annotSpan && m.multiSpan() {
		title += " · selecting " + active.Name
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

// metaLines renders the draft type and span fields, the saved status, the record
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
	lines := []string{typ}
	for k, sp := range m.sess.Schema().Spans {
		lines = append(lines, m.spanLine(i, d, k, sp))
	}
	status := "Status: unfinished"
	l, labeled := m.sess.Label(i)
	if labeled {
		status = "Status: " + l.Status
	}
	lines = append(lines, status, styleMuted.Render("ID: "+m.sess.Item(i).ID))
	if labeled {
		lines = append(lines, styleAccent.Render("Saved: "+l.Status+" — edit with t/x/n, resave with enter/u/s"))
	}
	if m.sess.Dirty(i) {
		lines = append(lines, styleAccent.Render("● unsaved draft — enter/u/s to save, esc to discard"))
	}
	return lines
}

// spanLine renders span field k of draft d: `Label: "text" [a,b)` or `Label: null`
// with ` · status` (and `(default)` while unset) for a field with statuses. With
// several fields each line is in its field's colour and the active one is marked.
func (m annotModel) spanLine(i int, d annotate.Draft, k int, sp annotate.SpanDef) string {
	value := spanValue(d.Spans[sp.Name])
	if status, isDefault := m.sess.SpanStatus(i, sp.Name); status != "" {
		value += " · " + status
		if isDefault {
			value += " (default)"
		}
	}
	if !m.multiSpan() {
		return capitalize(sp.Name) + ": " + value
	}
	marker := "  "
	if k == m.activeSpan {
		marker = "▸ "
	}
	return marker + spanStyle(k).Render(capitalize(sp.Name)+":") + " " + value
}

// spanValue renders a span value as `"text" [start,end)`, or null.
func spanValue(t *annotate.Target) string {
	if t == nil {
		return "null"
	}
	return fmt.Sprintf("%q [%d,%d)", t.Text, t.Start, t.End)
}

// capitalize upper-cases the first rune of s: the display label of a span name.
func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// spanPalette colours the span fields by schema index; the first is the accent
// colour of the single-target screen.
var spanPalette = []lipgloss.Style{
	styleAccent,
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")),
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("113")),
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220")),
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")),
	lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("209")),
}

// spanStyle returns the colour of span field k, repeating the palette past its end.
func spanStyle(k int) lipgloss.Style { return spanPalette[k%len(spanPalette)] }

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

// spanMark is an inclusive rune range highlighted in style.
type spanMark struct {
	lo, hi int
	style  lipgloss.Style
}

// selectionRows renders runes wrapped to w cells with each mark highlighted in
// its style (a later mark draws over an earlier one) and the rune at head, when
// head >= 0, in headStyle. It also returns the row to keep visible: the head's,
// else anchor's, else 0.
func selectionRows(runes []rune, w int, marks []spanMark, head int, headStyle lipgloss.Style, anchor int) ([]string, int) {
	if head >= 0 {
		anchor = head
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
		runStyle := -1 // index into marks; len(marks) is the head, -1 plain
		flush := func() {
			if len(run) == 0 {
				return
			}
			switch {
			case runStyle < 0:
				b.WriteString(string(run))
			case runStyle == len(marks):
				b.WriteString(headStyle.Render(string(run)))
			default:
				b.WriteString(marks[runStyle].style.Render(string(run)))
			}
			run = run[:0]
		}
		for j := rg[0]; j < rg[1]; j++ {
			st := -1
			if j == head {
				st = len(marks)
			} else {
				for k := len(marks) - 1; k >= 0; k-- {
					if j >= marks[k].lo && j <= marks[k].hi {
						st = k
						break
					}
				}
			}
			if st != runStyle {
				flush()
				runStyle = st
			}
			r := runes[j]
			switch {
			case r == '\n' && st < 0:
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
	filters := m.sess.Filters()
	nameW := 11
	for _, f := range filters {
		nameW = max(nameW, len(f.String())+1)
	}
	return listRows(len(filters), m.filters.cursor, maxRows, func(i int) string {
		count := 0
		if i < len(m.filters.counts) {
			count = m.filters.counts[i]
		}
		line := fmt.Sprintf("%-*s %d", nameW, filters[i], count)
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
	return max(0, len(helpRows(annotHelpGroups(m.sess.Schema(), m.sess.HasProposals()), iw, maxRows))-maxRows)
}

// helpContent returns the visible window of the help reference.
func (m annotModel) helpContent(iw, maxRows int) []string {
	iw, maxRows = max(iw, 1), max(maxRows, 1)
	rows := helpRows(annotHelpGroups(m.sess.Schema(), m.sess.HasProposals()), iw, maxRows)
	off := max(0, min(m.helpScroll, len(rows)-maxRows))
	return rows[off:min(len(rows), off+maxRows)]
}

// annotHelpGroups returns the annotation keyboard reference for schema; proposals
// adds the Proposals group of a session with a proposals file. A single target
// reads as it always did; several span fields (or span statuses) add the tab and
// status keys and say that x and n act on the active field.
func annotHelpGroups(schema *annotate.Schema, proposals bool) []helpGroup {
	multi := len(schema.Spans) > 1
	name := schema.Spans[0].Name
	label := []helpItem{
		{"enter", "Complete"},
		{"u", "Uncertain"},
		{"s", "Skip"},
		{"t", "Type picker"},
		{"1-9", "Set type N"},
	}
	spanTitle := capitalize(name) + " span"
	accept, null := "Accept "+name, "Null "+name
	if multi {
		label = append(label,
			helpItem{"tab", "Next field"},
			helpItem{"shift+tab", "Previous field"},
			helpItem{"x", "Select active span"},
			helpItem{"n", "Null active field"})
		spanTitle, accept, null = "Span field", "Set active field", "Null active field"
	} else {
		label = append(label, helpItem{"x", "Select " + name + " span"}, helpItem{"n", "Null " + name})
	}
	if schema.HasSpanStatuses() {
		cycle := "Cycle " + name + " status"
		if multi {
			cycle = "Cycle active status"
		}
		label = append(label, helpItem{"c", cycle})
	}
	reviseDesc := "Edit, then enter/u/s"
	if multi {
		reviseDesc = "Edit, then save"
	}
	label = append(label,
		helpItem{"esc", "Discard draft"},
		helpItem{"z", "Undo last save"},
		helpItem{"revise", reviseDesc})
	span := []helpItem{
		{"h/l/←/→", "Move one character"},
		{"w/b", "Next/previous word"},
		{"H/L", "Head left/right"},
		{"shift+←/→", "Head left/right"},
		{"W/B", "Extend to word"},
		{"0/home", "First character"},
		{"$/end", "Last character"},
		{"enter", accept},
		{"n", null},
	}
	if multi {
		span = append(span, helpItem{"tab", "Next field"}, helpItem{"shift+tab", "Previous field"})
	}
	span = append(span, helpItem{"esc", "Cancel"})
	groups := []helpGroup{
		{"Label", label},
		{"Navigate", []helpItem{
			{"a/h/←", "Previous, any status"},
			{"d/l/→", "Next, any status"},
			{"[/]", "Prev/next in filter"},
			{"g/G", "First/last record"},
			{": #", "Go to position or id"},
			{"f", "Filter"},
		}},
		{spanTitle, span},
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
	if proposals {
		groups = slices.Insert(groups, len(groups)-1, helpGroup{"Proposals", []helpItem{
			{"p", "Accept and save"},
			{"P", "Load, then edit"},
			{"advisory", "Suggestions only"},
			{"confidence", "Not ground truth"},
			{"saving", "p/enter/u/s only"},
		}})
	}
	return groups
}

// footerItems returns the context shortcuts for the current mode, one
// "key desc" item each.
func (m annotModel) footerItems() []string {
	switch m.mode {
	case annotSpan:
		return append([]string{"h/l move", "w/b word", "H/L grow", "W/B extend", "0/$ ends"}, m.spanModeFooterItems()...)
	case annotTypes:
		return []string{"type to filter", "↑/↓ move", "1-9 pick", "enter select", "esc cancel"}
	case annotFilter:
		return []string{"j/k move", "enter apply", "esc close"}
	case annotHelp:
		return []string{"j/k scroll", "esc close"}
	case annotGoto:
		return []string{"type position or id", "enter go", "esc cancel"}
	default:
		items := []string{"t type"}
		items = append(items, m.spanFooterItems()...)
		items = append(items, "enter complete", "u uncertain", "s skip", "a/d prev/next",
			"[/] prev/next "+m.sess.Filter().String(), "z undo", ": go to", "f filter", "? help", "q quit")
		if m.sess.Len() > 0 {
			if _, ok := m.sess.Proposal(m.sess.Cursor()); ok {
				items = append([]string{"p accept proposal", "P edit proposal"}, items...)
			}
		}
		return items
	}
}

// spanFooterItems returns the main-mode footer items of the span fields: the
// legacy "x target", "n null" pair for a single target, field names and the
// tab and status keys when the schema has several fields or span statuses.
func (m annotModel) spanFooterItems() []string {
	name := m.active().Name
	if !m.multiSpan() {
		items := []string{"x " + name, "n null"}
		if m.sess.Schema().HasSpanStatuses() {
			items = append(items, "c status")
		}
		return items
	}
	items := []string{"tab/shift+tab field", "x select " + name, "n null " + name}
	if m.sess.Schema().HasSpanStatuses() {
		items = append(items, "c status")
	}
	return items
}

// spanModeFooterItems returns the span-mode footer items from the accept key on.
func (m annotModel) spanModeFooterItems() []string {
	if !m.multiSpan() {
		return []string{"enter accept", "n null", "esc cancel", "? help"}
	}
	name := m.active().Name
	return []string{"tab/shift+tab field", "enter set " + name, "n null " + name, "esc cancel", "? help"}
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
