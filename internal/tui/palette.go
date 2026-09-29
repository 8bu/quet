package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/export"
	"github.com/8bu/quet/internal/review"
)

// command is one entry of the command palette.
type command struct {
	name string
	keys string // the equivalent review-mode key, shown next to the name
	run  func(m model) (model, tea.Cmd)
}

// commands returns the palette's command table.
func (m model) commands() []command {
	return []command{
		{"Approve record", "w", func(m model) (model, tea.Cmd) {
			return m.applyStatus(review.Approved, "Approved")
		}},
		{"Reject record", "s", func(m model) (model, tea.Cmd) {
			return m.applyStatus(review.Rejected, "Rejected")
		}},
		{"Mark needs review", "space", func(m model) (model, tea.Cmd) {
			return m.applyStatus(review.NeedsReview, "Needs review")
		}},
		{"Undo", "z", func(m model) (model, tea.Cmd) { return m.undo() }},
		{"Edit record", "e", func(m model) (model, tea.Cmd) {
			if !m.openEditor() {
				return m.setStatus("No records")
			}
			return m, nil
		}},
		{"Revert edit", "ctrl+r", func(m model) (model, tea.Cmd) { return m.doRevert() }},
		{"Edit flags", "f", func(m model) (model, tea.Cmd) { return m.openFlags() }},
		{"Change filter", "", func(m model) (model, tea.Cmd) {
			m.filter.cursor = clampIndex(m.filter.cursor, len(m.facets.rows))
			m.mode = ModeFilter
			return m, nil
		}},
		{"Search", "/", func(m model) (model, tea.Cmd) {
			m.search.open()
			m.mode = ModeSearch
			return m, nil
		}},
		{"Show duplicates", "", func(m model) (model, tea.Cmd) {
			m.dups.open(m.sess)
			m.mode = ModeDuplicates
			return m, nil
		}},
		{"Export approved", "", func(m model) (model, tea.Cmd) { return m.runExportPreset("approved") }},
		{"Export rejected", "", func(m model) (model, tea.Cmd) { return m.runExportPreset("rejected") }},
		{"Export needs review", "", func(m model) (model, tea.Cmd) { return m.runExportPreset("needs_review") }},
		{"Export all with review metadata", "", func(m model) (model, tea.Cmd) { return m.runExportPreset("all") }},
		{"Export clean approved corpus", "", func(m model) (model, tea.Cmd) { return m.runExportPreset("clean") }},
		{"Export to file", "", func(m model) (model, tea.Cmd) {
			m.exp.open(m.sess)
			m.mode = ModeExport
			return m, nil
		}},
		{"Toggle skip reviewed", "", func(m model) (model, tea.Cmd) {
			m.sess.SkipReviewed = !m.sess.SkipReviewed
			return m.setStatus("Skip reviewed: %v", m.sess.SkipReviewed)
		}},
		{"Show help", "?", func(m model) (model, tea.Cmd) {
			m.mode = ModeHelp
			return m, nil
		}},
		{"Quit", "q", func(m model) (model, tea.Cmd) { return m, tea.Quit }},
	}
}

// fuzzyMatch reports whether every rune of query appears in s in order,
// case-insensitively.
func fuzzyMatch(s, query string) bool {
	q := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(q) == 0 {
		return true
	}
	i := 0
	for _, r := range strings.ToLower(s) {
		if r == q[i] {
			i++
			if i == len(q) {
				return true
			}
		}
	}
	return false
}

// filterCommands keeps the commands matching query.
func filterCommands(cmds []command, query string) []command {
	out := make([]command, 0, len(cmds))
	for _, c := range cmds {
		if fuzzyMatch(c.name, query) {
			out = append(out, c)
		}
	}
	return out
}

// paletteFiltered returns the commands matching the current palette query.
func (m model) paletteFiltered() []command {
	return filterCommands(m.commands(), m.pal.input.Value())
}

// doRevert clears the current record's edit from the palette.
func (m model) doRevert() (model, tea.Cmd) {
	done, err := m.revertEdit()
	if err != nil {
		return m.setStatus("Revert failed: %v", err)
	}
	if !done {
		return m.setStatus("No records")
	}
	return m.setStatus("Reverted to imported text")
}

// runExportPreset writes a named preset next to the corpus, without ever
// touching an existing file.
func (m model) runExportPreset(name string) (model, tea.Cmd) {
	preset, ok := findPreset(name)
	if !ok {
		return m.setStatus("Export preset %q unavailable", name)
	}
	out := export.DefaultPath(corpusPath(m.sess), preset.Suffix)
	n, err := export.ToFile(m.sess, out, preset.Options, false)
	if err != nil {
		return m.setStatus("Export failed: %v", err)
	}
	return m.setStatus("Wrote %d records to %s", n, out)
}

func findPreset(name string) (export.Preset, bool) {
	for _, p := range export.Presets() {
		if p.Name == name {
			return p, true
		}
	}
	return export.Preset{}, false
}
