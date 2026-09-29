package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/review"
)

// Mode is the TUI's current input mode. Every mode owns its keys: review-mode
// shortcuts never fire in another mode.
type Mode int

// TUI modes.
const (
	// ModeReview is the default mode: navigate, approve, reject, flag.
	ModeReview Mode = iota
	// ModeEdit edits the current record's text.
	ModeEdit
	// ModeFlags picks the manual flags of the current record.
	ModeFlags
	// ModeHelp shows the keyboard reference overlay.
	ModeHelp
	// ModePalette shows the searchable command palette.
	ModePalette
	// ModeSearch searches the corpus.
	ModeSearch
	// ModeFilter picks the active filter.
	ModeFilter
	// ModeDuplicates lists the current record's duplicate group.
	ModeDuplicates
	// ModeExport writes an export preset to a file.
	ModeExport
)

// String returns the mode's name.
func (m Mode) String() string {
	switch m {
	case ModeEdit:
		return "edit"
	case ModeFlags:
		return "flags"
	case ModeHelp:
		return "help"
	case ModePalette:
		return "palette"
	case ModeSearch:
		return "search"
	case ModeFilter:
		return "filter"
	case ModeDuplicates:
		return "duplicates"
	case ModeExport:
		return "export"
	default:
		return "review"
	}
}

// panel identifies one of the three main panels.
type panel int

const (
	panelCorpus panel = iota
	panelRecord
	panelDetails
	panelCount
)

func (p panel) title() string {
	switch p {
	case panelRecord:
		return "2 Record"
	case panelDetails:
		return "3 Details"
	default:
		return "1 Corpus"
	}
}

// model is the Bubble Tea model. Only the current record and its neighbours are
// ever rendered; the view is recomputed on demand.
type model struct {
	sess *review.Session

	mode  Mode
	focus panel

	width, height int

	status    string
	statusSeq int

	editor textarea.Model

	facets facetList
	flags  flagsPicker
	filter filterPicker
	search searchPicker
	dups   dupPicker
	exp    exportPicker
	pal    palettePicker

	quitErr error
}

func newModel(s *review.Session) model {
	ed := textarea.New()
	ed.Prompt = ""
	ed.Placeholder = ""
	ed.ShowLineNumbers = false
	ed.Blur()

	m := model{
		sess:   s,
		mode:   ModeReview,
		focus:  panelRecord,
		width:  100,
		height: 30,
		editor: ed,
		pal:    newPalettePicker(),
		search: newSearchPicker(),
		exp:    newExportPicker(),
	}
	m.refreshFacets()
	m.resize()
	return m
}

// Init implements tea.Model.
func (m model) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next, cmd
}

// View implements tea.Model.
func (m model) View() string { return m.view() }

// refreshFacets rebuilds the filter/facet list (with counts) shown in the corpus
// panel and the filter picker. It is called after every mutation, never per
// keystroke.
func (m *model) refreshFacets() {
	m.facets.rows = buildFilterRows(m.sess)
	m.facets.clampCursor()
	m.filter.clamp(len(m.facets.rows))
}

// resize re-lays out widgets that need explicit sizes.
func (m *model) resize() {
	w, h := m.recordInner()
	if w > 0 {
		m.editor.SetWidth(w)
	}
	if h > 0 {
		m.editor.SetHeight(h)
	}
	inner := m.overlayInnerWidth()
	m.pal.input.Width = max(inner-3, 1)
	m.search.input.Width = max(inner-3, 1)
	m.exp.path.Width = max(inner-3, 1)
}

// overlayInnerWidth returns the content width available inside an overlay panel.
func (m model) overlayInnerWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	if w > 64 {
		w = 64
	}
	return max(w-2, 1)
}

// layout is the fixed geometry of the main screen's body.
type layout struct {
	leftW, rightW     int
	recordH, detailsH int
}

// computeLayout splits a w x h body into the corpus column and the stacked
// record/details column. It never returns negative sizes.
func computeLayout(w, h int) layout {
	l := layout{leftW: w / 3}
	if l.leftW < 20 {
		l.leftW = 20
	}
	if l.leftW > 34 {
		l.leftW = 34
	}
	if l.leftW > w-10 {
		l.leftW = w / 2
	}
	if l.leftW < 1 {
		l.leftW = 1
	}
	if l.leftW > w {
		l.leftW = w
	}
	l.rightW = w - l.leftW

	if h < 6 {
		l.recordH = h
		return l
	}
	l.recordH = h * 7 / 10
	if l.recordH < 3 {
		l.recordH = 3
	}
	l.detailsH = h - l.recordH
	if l.detailsH < 3 {
		l.detailsH = 3
		l.recordH = h - l.detailsH
		if l.recordH < 1 {
			l.recordH, l.detailsH = h, 0
		}
	}
	return l
}

// bodyDimensions returns the drawing width and the body height (screen minus
// footer and the optional status row).
func (m model) bodyDimensions() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	statusRows := 0
	if m.status != "" && h >= 3 {
		statusRows = 1
	}
	bh := h - 1 - statusRows
	if bh < 0 {
		bh = 0
	}
	return w, bh
}

// recordInner returns the inner size of the record panel (inside its border).
func (m model) recordInner() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	l := computeLayout(w, h-1)
	return l.rightW - 2, l.recordH - 2
}

// windowRange returns the [start,end) slice of n items that keeps cursor
// visible in a window of max rows.
func windowRange(n, cursor, max int) (int, int) {
	if n <= 0 || max <= 0 {
		return 0, 0
	}
	if max > n {
		max = n
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	start := cursor - max/2
	if start < 0 {
		start = 0
	}
	if start+max > n {
		start = n - max
	}
	return start, start + max
}

// listRows renders the visible window of a list, marking the cursor row with a
// "> " prefix. It returns the rows and the index of the cursor row, or -1.
func listRows(total, cursor, max int, render func(i int) string) ([]string, int) {
	if total <= 0 || max <= 0 {
		return nil, -1
	}
	start, end := windowRange(total, cursor, max)
	rows := make([]string, 0, end-start)
	sel := -1
	for i := start; i < end; i++ {
		marker := "  "
		if i == cursor {
			marker = "> "
			sel = len(rows)
		}
		rows = append(rows, marker+render(i))
	}
	return rows, sel
}

// padLine truncates or space-pads s to exactly w cells.
func padLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	if d := w - lipgloss.Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}

// truncateLine shortens s to at most w cells.
func truncateLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// fitRows clips rows to ih rows of exactly iw cells, styling row sel.
func fitRows(rows []string, iw, ih, sel int) []string {
	if ih <= 0 {
		return nil
	}
	out := make([]string, 0, ih)
	for i := range ih {
		row := ""
		if i < len(rows) {
			row = rows[i]
		}
		if i == sel {
			out = append(out, styleSelected.Width(iw).MaxWidth(iw).Render(row))
			continue
		}
		out = append(out, padLine(row, iw))
	}
	return out
}

// wrapRows word-wraps each logical line to at most w cells.
func wrapRows(lines []string, w int) []string {
	if w <= 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(lipgloss.NewStyle().Width(w).Render(line), "\n")...)
	}
	return out
}

// box draws a bordered panel of exactly w x h cells with an optional title in
// the top border. It degrades to clipped, borderless rows on tiny sizes.
func box(title string, focused bool, w, h int, rows []string, sel int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	if w < 4 || h < 3 {
		return strings.Join(fitRows(rows, w, h, sel), "\n")
	}
	bs, border := borderBlurred, styleBorderBlurred
	if focused {
		bs, border = borderFocused, styleBorderFocused
	}
	iw, ih := w-2, h-2
	fitted := fitRows(rows, iw, ih, sel)
	lines := make([]string, 0, h)
	lines = append(lines, borderTop(bs, border, title, w))
	for _, row := range fitted {
		lines = append(lines, border.Render(bs.v)+row+border.Render(bs.v))
	}
	lines = append(lines, border.Render(bs.bl+strings.Repeat(bs.h, iw)+bs.br))
	return strings.Join(lines, "\n")
}

// borderTop draws a panel's top border, embedding the title when it fits. The
// result is exactly w cells wide.
func borderTop(bs borderSet, border lipgloss.Style, title string, w int) string {
	plain := border.Render(bs.tl + strings.Repeat(bs.h, w-2) + bs.tr)
	if title == "" {
		return plain
	}
	label := " " + title + " "
	lw := lipgloss.Width(label)
	fill := w - 3 - lw
	if fill < 1 {
		return plain
	}
	return border.Render(bs.tl+bs.h) + styleTitle.Render(label) +
		border.Render(strings.Repeat(bs.h, fill)+bs.tr)
}

// squeeze collapses all whitespace runs in s into single spaces.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

// snippet renders a single-line preview of text clipped to w cells.
func snippet(text string, w int) string { return truncateLine(squeeze(text), w) }

// listOrDash renders names as a comma-separated list, or "-" when empty.
func listOrDash(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ", ")
}

// metaLine renders one "key: value" detail line.
func metaLine(key, value string) string {
	if value == "" {
		value = "-"
	}
	return key + ": " + value
}

// counterLine right-aligns a counter inside iw cells.
func counterLine(label string, n, iw int) string {
	num := strconv.Itoa(n)
	pad := iw - lipgloss.Width(label) - len(num)
	if pad < 1 {
		pad = 1
	}
	return label + strings.Repeat(" ", pad) + num
}
