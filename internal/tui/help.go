package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// helpItem is one key/description pair in the help overlay.
type helpItem struct {
	keys string
	desc string
}

// helpGroup is one titled group of help items.
type helpGroup struct {
	title string
	items []helpItem
}

// helpGroups returns the keyboard reference in the exact spec grouping.
func helpGroups() []helpGroup {
	return []helpGroup{
		{"Review", []helpItem{
			{"w", "Approve"},
			{"s", "Reject"},
			{"space", "Needs review"},
			{"z", "Undo"},
		}},
		{"Navigation", []helpItem{
			{"a/h", "Previous"},
			{"d/l", "Next"},
			{"g", "First"},
			{"G", "Last"},
		}},
		{"Record", []helpItem{
			{"e", "Edit"},
			{"f", "Flags"},
			{"/", "Search"},
		}},
		{"Application", []helpItem{
			{"tab", "Change panel"},
			{":", "Commands"},
			{"esc", "Back"},
			{"q", "Quit"},
		}},
	}
}

// helpGroupRows renders one group as headed rows of at most w cells.
func helpGroupRows(g helpGroup, w int) []string {
	if w < 1 {
		return nil
	}
	keyW := 0
	for _, it := range g.items {
		keyW = max(keyW, lipgloss.Width(it.keys))
	}
	rows := []string{styleHelpGroup.Render(truncateLine(g.title, w))}
	for _, it := range g.items {
		pad := strings.Repeat(" ", keyW-lipgloss.Width(it.keys)+2)
		line := "  " + styleHelpKey.Render(it.keys) + pad + styleHelpDesc.Render(it.desc)
		rows = append(rows, truncateLine(line, w))
	}
	return rows
}

// layoutHelpGroups arranges the groups into cols columns of colW cells.
func layoutHelpGroups(groups []helpGroup, cols, colW int) []string {
	columns := make([][]string, cols)
	for i, g := range groups {
		col := i % cols
		if len(columns[col]) > 0 {
			columns[col] = append(columns[col], "")
		}
		columns[col] = append(columns[col], helpGroupRows(g, colW)...)
	}
	height := 0
	for _, c := range columns {
		height = max(height, len(c))
	}
	rows := make([]string, 0, height)
	for i := range height {
		parts := make([]string, 0, cols)
		for _, c := range columns {
			line := ""
			if i < len(c) {
				line = c[i]
			}
			parts = append(parts, padLine(line, colW))
		}
		rows = append(rows, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	return rows
}

// helpRows lays the help reference out in the fewest columns that fit maxRows.
func helpRows(w, maxRows int) []string {
	if w < 1 || maxRows < 1 {
		return nil
	}
	groups := helpGroups()
	colW := min(32, w)
	maxCols := min(max(w/(colW+2), 1), len(groups))
	best := layoutHelpGroups(groups, maxCols, colW)
	for cols := 1; cols < maxCols; cols++ {
		if rows := layoutHelpGroups(groups, cols, colW); len(rows) <= maxRows {
			return rows
		}
	}
	return best
}
