package tui

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/export"
	"github.com/8bu/quet/internal/review"
)

// statusTTL is how long a transient status message stays on screen.
const statusTTL = 2 * time.Second

// statusExpireMsg clears the status message it was scheduled for.
type statusExpireMsg struct{ seq int }

// update is the single message entry point. Keys are dispatched per mode, so
// review-mode shortcuts can never fire in another mode.
func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case statusExpireMsg:
		if msg.seq == m.statusSeq {
			m.status = ""
		}
		return m, nil
	case editorDoneMsg:
		return m.editorDone(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m model) updateKey(k tea.KeyMsg) (model, tea.Cmd) {
	// Pasted text and bursts of runes are never commands: a burst can only be
	// fast typing or a paste, and matching it against names like "esc" or
	// "ctrl+s" would fire an action the user never asked for. Route them to the
	// active text widget instead.
	if k.Paste || (k.Type == tea.KeyRunes && len(k.Runes) > 1) {
		return m.routeText(k)
	}
	switch m.mode {
	case ModeEdit:
		return m.updateEdit(k)
	case ModeFlags:
		return m.updateFlags(k)
	case ModeHelp:
		return m.updateHelp(k)
	case ModePalette:
		return m.updatePalette(k)
	case ModeSearch:
		return m.updateSearch(k)
	case ModeFilter:
		return m.updateFilter(k)
	case ModeDuplicates:
		return m.updateDuplicates(k)
	case ModeExport:
		return m.updateExport(k)
	case ModeCreateFlags:
		return m.updateCreateFlags(k)
	default:
		return m.updateReview(k)
	}
}

// routeText sends non-command input to the active text widget, if any.
func (m model) routeText(k tea.KeyMsg) (model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.mode {
	case ModeEdit:
		m.editor, cmd = m.editor.Update(k)
	case ModeSearch:
		before := m.search.input.Value()
		m.search.input, cmd = m.search.input.Update(k)
		if m.search.input.Value() != before {
			m.search.refresh(m.sess)
		}
	case ModePalette:
		m.pal.input, cmd = m.pal.input.Update(k)
		m.pal.cursor = clampIndex(m.pal.cursor, len(m.paletteFiltered()))
	case ModeExport:
		if m.exp.pathFocus {
			m.exp.path, cmd = m.exp.path.Update(k)
			m.exp.pathTouched = true
		}
	}
	return m, cmd
}

// setStatus records a transient status message and schedules its expiry.
func (m model) setStatus(format string, args ...any) (model, tea.Cmd) {
	m.status = fmt.Sprintf(format, args...)
	m.statusSeq++
	seq := m.statusSeq
	return m, tea.Tick(statusTTL, func(time.Time) tea.Msg { return statusExpireMsg{seq: seq} })
}

// updateReview handles ModeReview: navigation, review actions and mode entries.
func (m model) updateReview(k tea.KeyMsg) (model, tea.Cmd) {
	switch key := k.String(); key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.mode = ModeHelp
		return m, nil
	case ":":
		m.pal.open()
		m.mode = ModePalette
		return m, nil
	case "/":
		m.search.open()
		m.mode = ModeSearch
		return m, nil
	case "f", "F":
		return m.openFlags()
	case "c":
		return m.editConfig()
	case "e":
		if !m.openEditor() {
			return m.setStatus("No records")
		}
		return m, nil
	case "tab":
		m.focus = (m.focus + 1) % panelCount
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + panelCount - 1) % panelCount
		return m, nil
	case "1":
		m.focus = panelCorpus
		return m, nil
	case "2":
		m.focus = panelRecord
		return m, nil
	case "3":
		m.focus = panelDetails
		return m, nil
	case "esc":
		if m.sess.Filter().Kind != review.FilterAll {
			m.sess.SetFilter(review.Filter{Kind: review.FilterAll})
			m.refreshFacets()
			return m.setStatus("Filter: all")
		}
		m.focus = panelCorpus
		return m, nil
	case "w":
		return m.applyStatus(review.Approved, "Approved")
	case "s":
		return m.applyStatus(review.Rejected, "Rejected")
	case " ":
		return m.applyStatus(review.NeedsReview, "Needs review")
	case "z":
		return m.undo()
	case "a", "h", "left":
		m.sess.Prev()
		return m, nil
	case "d", "l", "right":
		m.sess.Next()
		return m, nil
	case "j", "down":
		if m.focus == panelCorpus {
			m.facets.move(1)
			return m, nil
		}
		m.sess.Next()
		return m, nil
	case "k", "up":
		if m.focus == panelCorpus {
			m.facets.move(-1)
			return m, nil
		}
		m.sess.Prev()
		return m, nil
	case "g", "home":
		m.sess.First()
		return m, nil
	case "G", "end":
		m.sess.Last()
		return m, nil
	case "enter":
		if m.focus == panelCorpus {
			return m.applyFacet()
		}
		return m, nil
	}
	return m, nil
}

// applyStatus persists a review status and reports it; the session auto-advances.
func (m model) applyStatus(st review.ReviewStatus, label string) (model, tea.Cmd) {
	if m.sess.Current() < 0 {
		return m.setStatus("No records")
	}
	before, _ := m.sess.Position()
	if _, err := m.sess.SetStatus(st); err != nil {
		return m.setStatus("Error: %v", err)
	}
	m.refreshFacets()
	pos, n := m.sess.Position()
	if n == 0 {
		return m.setStatus("%s", label)
	}
	switch {
	case m.sess.Counts().Unreviewed == 0:
		return m.setStatus("%s  %d/%d  all reviewed", label, pos+1, n)
	case pos < before:
		return m.setStatus("%s  %d/%d  wrapped to first unreviewed", label, pos+1, n)
	}
	return m.setStatus("%s  %d/%d", label, pos+1, n)
}

// applyFacet applies the filter row highlighted in the corpus panel.
func (m model) applyFacet() (model, tea.Cmd) {
	row, ok := m.facets.current()
	if !ok {
		return m, nil
	}
	n := m.sess.SetFilter(row.f)
	return m.setStatus("Filter: %s  %d records", row.label, n)
}

func (m model) undo() (model, tea.Cmd) {
	ok, idx, err := m.sess.Undo()
	if err != nil {
		return m.setStatus("Undo failed: %v", err)
	}
	if !ok {
		return m.setStatus("Nothing to undo")
	}
	m.refreshFacets()
	if r := m.sess.Record(idx); r != nil {
		return m.setStatus("Undo  %s", r.ID)
	}
	return m.setStatus("Undo")
}

// openFlags enters ModeFlags. With config support and no flags file it offers
// to create one instead.
func (m model) openFlags() (model, tea.Cmd) {
	if m.opt.Settings != nil && len(m.sess.FlagDefs) == 0 && m.sess.FlagsPath == "" {
		return m.promptCreateFlags()
	}
	if m.sess.Current() < 0 {
		return m.setStatus("No records")
	}
	if !m.flags.open(m.sess) {
		if m.opt.Settings != nil && m.sess.FlagsPath != "" {
			return m.setStatus("No flags in %s — use \":\" Edit flags file", m.sess.FlagsPath)
		}
		return m.setStatus("No manual flags defined")
	}
	m.mode = ModeFlags
	return m, nil
}

// openEditor enters ModeEdit on the current record; it reports success.
func (m *model) openEditor() bool {
	cur := m.sess.Current()
	if cur < 0 {
		return false
	}
	m.editor.SetValue(m.sess.FinalText(cur))
	m.editor.CursorEnd()
	m.resize()
	m.editor.Focus()
	m.mode = ModeEdit
	return true
}

// updateEdit handles ModeEdit. Only ctrl+s saves; every other key is text.
func (m model) updateEdit(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.editor.Blur()
		m.mode = ModeReview
		return m, nil
	case "ctrl+s":
		cur := m.sess.Current()
		m.editor.Blur()
		m.mode = ModeReview
		if cur < 0 {
			return m, nil
		}
		if err := m.sess.SaveEdit(m.editor.Value()); err != nil {
			return m.setStatus("Save failed: %v", err)
		}
		m.refreshFacets()
		return m.setStatus("Saved")
	case "ctrl+r":
		cur := m.sess.Current()
		done, err := m.revertEdit()
		if err != nil {
			return m.setStatus("Revert failed: %v", err)
		}
		if !done {
			return m, nil
		}
		m.editor.SetValue(m.sess.FinalText(cur))
		m.editor.CursorEnd()
		return m.setStatus("Reverted to imported text")
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(k)
	return m, cmd
}

// revertEdit clears the current record's edit; it reports whether it did
// anything.
func (m *model) revertEdit() (bool, error) {
	if m.sess.Current() < 0 {
		return false, nil
	}
	if err := m.sess.RevertEdit(); err != nil {
		return false, err
	}
	m.refreshFacets()
	return true, nil
}

// updateFlags handles ModeFlags: j/k move, space toggles, enter applies.
func (m model) updateFlags(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeReview
		return m, nil
	case "j", "down":
		m.flags.move(1)
		return m, nil
	case "k", "up":
		m.flags.move(-1)
		return m, nil
	case " ":
		m.flags.toggle()
		return m, nil
	case "enter":
		return m.applyFlags()
	}
	return m, nil
}

func (m model) applyFlags() (model, tea.Cmd) {
	cur := m.sess.Current()
	m.mode = ModeReview
	if cur < 0 {
		return m, nil
	}
	names := m.flags.selectedNames()
	if err := m.sess.SetManualFlags(names); err != nil {
		return m.setStatus("Flags failed: %v", err)
	}
	m.refreshFacets()
	if len(names) == 0 {
		return m.setStatus("Flags: none")
	}
	return m.setStatus("Flags: %s", listOrDash(names))
}

// updateFilter handles ModeFilter: j/k move, enter applies, esc closes.
func (m model) updateFilter(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeReview
		return m, nil
	case "j", "down":
		m.filter = m.filter.move(1, len(m.facets.rows))
		return m, nil
	case "k", "up":
		m.filter = m.filter.move(-1, len(m.facets.rows))
		return m, nil
	case "enter", " ":
		row, ok := m.filterRow()
		m.mode = ModeReview
		if !ok {
			return m, nil
		}
		m.sess.SetFilter(row.f)
		m.refreshFacets()
		return m.setStatus("Filter: %s", row.label)
	}
	return m, nil
}

func (m model) filterRow() (filterRow, bool) {
	if m.filter.cursor < 0 || m.filter.cursor >= len(m.facets.rows) {
		return filterRow{}, false
	}
	return m.facets.rows[m.filter.cursor], true
}

// updateSearch handles ModeSearch: typing searches, enter jumps to the result.
func (m model) updateSearch(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeReview
		return m, nil
	case "enter":
		idx, ok := m.search.selected()
		m.mode = ModeReview
		if !ok {
			return m, nil
		}
		m.sess.Goto(idx)
		m.refreshFacets()
		return m, nil
	case "up", "ctrl+p":
		m.search.move(-1)
		return m, nil
	case "down", "ctrl+n":
		m.search.move(1)
		return m, nil
	}
	before := m.search.input.Value()
	var cmd tea.Cmd
	m.search.input, cmd = m.search.input.Update(k)
	if m.search.input.Value() != before {
		m.search.refresh(m.sess)
	}
	return m, cmd
}

// updateDuplicates handles ModeDuplicates: j/k move, enter jumps to the match.
func (m model) updateDuplicates(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeReview
		return m, nil
	case "j", "down":
		m.dups.move(1)
		return m, nil
	case "k", "up":
		m.dups.move(-1)
		return m, nil
	case "enter":
		idx, ok := m.dups.selected()
		m.mode = ModeReview
		if !ok {
			return m, nil
		}
		m.sess.Goto(idx)
		m.refreshFacets()
		return m, nil
	}
	return m, nil
}

// updatePalette handles ModePalette: typing filters, enter runs the command.
func (m model) updatePalette(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = ModeReview
		return m, nil
	case "enter":
		cmd, ok := m.selectedCommand()
		m.mode = ModeReview
		if !ok {
			return m, nil
		}
		return cmd.run(m)
	case "up", "ctrl+p":
		m.pal.move(-1, len(m.paletteFiltered()))
		return m, nil
	case "down", "ctrl+n":
		m.pal.move(1, len(m.paletteFiltered()))
		return m, nil
	}
	before := m.pal.input.Value()
	var cmd tea.Cmd
	m.pal.input, cmd = m.pal.input.Update(k)
	if m.pal.input.Value() != before {
		m.pal.cursor = clampIndex(m.pal.cursor, len(m.paletteFiltered()))
	}
	return m, cmd
}

func (m model) selectedCommand() (command, bool) {
	cmds := m.paletteFiltered()
	if m.pal.cursor < 0 || m.pal.cursor >= len(cmds) {
		return command{}, false
	}
	return cmds[m.pal.cursor], true
}

// updateExport handles ModeExport: pick a preset, edit the path, write the file.
func (m model) updateExport(k tea.KeyMsg) (model, tea.Cmd) {
	key := k.String()
	if m.exp.pending {
		switch key {
		case "y":
			return m.runExport(true)
		case "n", "esc":
			m.exp.pending = false
			return m.setStatus("Export cancelled")
		}
		return m, nil
	}
	switch key {
	case "esc":
		m.exp.path.Blur()
		m.mode = ModeReview
		return m, nil
	case "tab":
		m.exp.pathFocus = !m.exp.pathFocus
		if m.exp.pathFocus {
			m.exp.path.Focus()
		} else {
			m.exp.path.Blur()
		}
		return m, nil
	case "enter":
		return m.runExport(false)
	}
	if m.exp.pathFocus {
		before := m.exp.path.Value()
		var cmd tea.Cmd
		m.exp.path, cmd = m.exp.path.Update(k)
		if m.exp.path.Value() != before {
			m.exp.pathTouched = true
		}
		return m, cmd
	}
	switch key {
	case "j", "down":
		m.exp.movePreset(m.sess, 1)
	case "k", "up":
		m.exp.movePreset(m.sess, -1)
	}
	return m, nil
}

// runExport writes the selected preset, asking before overwriting.
func (m model) runExport(force bool) (model, tea.Cmd) {
	preset, ok := m.exp.current()
	if !ok {
		return m.setStatus("No export preset available")
	}
	out := m.exp.outputPath()
	if out == "" {
		return m.setStatus("No output path")
	}
	if !force {
		if _, err := os.Stat(out); err == nil {
			m.exp.pending = true
			return m.setStatus("Overwrite %s? (y/n)", out)
		}
	}
	n, err := export.ToFile(m.sess, out, preset.Options, force)
	if err != nil {
		return m.setStatus("Export failed: %v", err)
	}
	m.exp.pending = false
	return m.setStatus("Wrote %d records to %s", n, out)
}

// updateHelp closes the help overlay on any key (ctrl+c still quits).
func (m model) updateHelp(k tea.KeyMsg) (model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.mode = ModeReview
	return m, nil
}
