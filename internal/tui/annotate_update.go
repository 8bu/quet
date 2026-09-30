package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
)

// update is the single message entry point. Keys are dispatched per mode, so
// main-mode shortcuts can never fire in another mode.
func (m annotModel) update(msg tea.Msg) (annotModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case statusExpireMsg:
		if msg.seq == m.statusSeq {
			m.status, m.statusErr = "", false
		}
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// updateKey dispatches a key to the current mode. ctrl+c always quits.
func (m annotModel) updateKey(k tea.KeyMsg) (annotModel, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	// Pasted text and bursts of runes are never commands; only the type
	// picker's query and the go-to prompt accept them.
	if k.Paste || (k.Type == tea.KeyRunes && len(k.Runes) > 1) {
		switch m.mode {
		case annotTypes:
			m.types.query = append(m.types.query, k.Runes...)
			m.types.cursor = 0
		case annotGoto:
			m.gotoQuery = append(m.gotoQuery, k.Runes...)
		}
		return m, nil
	}
	switch m.mode {
	case annotSpan:
		return m.updateSpan(k.String())
	case annotTypes:
		return m.updateTypes(k)
	case annotFilter:
		return m.updateFilter(k.String())
	case annotHelp:
		return m.updateHelp(k.String())
	case annotGoto:
		return m.updateGoto(k)
	default:
		return m.updateMain(k.String())
	}
}

// setStatus records a transient status message and schedules its expiry.
func (m annotModel) setStatus(format string, args ...any) (annotModel, tea.Cmd) {
	m.status = fmt.Sprintf(format, args...)
	m.statusErr = false
	m.statusSeq++
	seq := m.statusSeq
	return m, tea.Tick(statusTTL, func(time.Time) tea.Msg { return statusExpireMsg{seq: seq} })
}

// setError is setStatus for errors: the status row renders it in styleError.
func (m annotModel) setError(format string, args ...any) (annotModel, tea.Cmd) {
	m, cmd := m.setStatus(format, args...)
	m.statusErr = true
	return m, cmd
}

// updateMain handles annotMain: labeling, navigation and mode entries.
func (m annotModel) updateMain(key string) (annotModel, tea.Cmd) {
	armed := m.quitArmed
	m.quitArmed = false
	switch key {
	case "q":
		if !armed && m.sess.Len() > 0 && m.sess.Dirty(m.sess.Cursor()) {
			m.quitArmed = true
			return m.setError("unsaved draft — press q again to quit (enter/u/s save, esc discards)")
		}
		return m, tea.Quit
	case "?":
		return m.openHelp(), nil
	case "f":
		return m.openFilter(), nil
	}
	if m.sess.Len() == 0 {
		return m, nil
	}
	i := m.sess.Cursor()
	switch key {
	case "enter":
		return m.mark(annotate.StatusComplete)
	case "u":
		return m.mark(annotate.StatusUncertain)
	case "s":
		return m.mark(annotate.StatusSkipped)
	case "t":
		return m.openTypes(), nil
	case "p", "P":
		if !m.sess.HasProposals() {
			return m, nil
		}
		if key == "p" {
			return m.acceptProposal()
		}
		return m.applyProposal()
	case "x":
		return m.openSpan()
	case "n":
		m.sess.ClearTarget(i)
		return m.setStatus("target: null")
	case "esc":
		if !m.sess.Dirty(i) {
			return m, nil
		}
		m.sess.DiscardDraft(i)
		return m.setStatus("draft discarded")
	case "a", "A", "h", "H", "left":
		if !m.sess.Prev() {
			return m.setStatus("first record")
		}
		return m, nil
	case "d", "D", "l", "L", "right":
		if !m.sess.Next() {
			return m.setStatus("last record")
		}
		return m, nil
	case "[":
		if !m.sess.PrevMatch() {
			return m.setStatus("no previous %s record", m.sess.Filter())
		}
		return m, nil
	case "]":
		if !m.sess.NextMatch() {
			return m.setStatus("no next %s record", m.sess.Filter())
		}
		return m, nil
	case "g":
		m.sess.First()
		return m, nil
	case "G":
		m.sess.Last()
		return m, nil
	case "z", "Z":
		return m.undo()
	case ":", "#":
		m.gotoQuery = nil
		m.mode = annotGoto
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n := int(key[0] - '0')
		types := m.sess.Schema().Types
		if n > len(types) {
			return m.setError("no type %d: the schema declares %d", n, len(types))
		}
		return m.applyType(types[n-1].Name)
	}
	return m, nil
}

// mark saves the current draft with status, then advances to the next record
// left to do in the filter (in a re-check, the next record in the filter, without
// wrapping). Re-marking a labeled record reports a revision.
func (m annotModel) mark(status string) (annotModel, tea.Cmd) {
	if !m.sess.Schema().HasStatus(status) {
		return m.setError("status %q is not declared in the schema", status)
	}
	i := m.sess.Cursor()
	id := m.sess.Item(i).ID
	_, had := m.sess.Label(i)
	if err := m.sess.Mark(i, status); err != nil {
		return m.setError("%s %s: %v", status, id, err)
	}
	what := status
	if had {
		what = "revised " + status
	}
	return m.afterMark(what, id)
}

// acceptProposal saves the current record's proposal as its label and then behaves
// like a successful mark: it advances and reports what was saved. A missing or
// invalid proposal is reported and nothing is written.
func (m annotModel) acceptProposal() (annotModel, tea.Cmd) {
	i := m.sess.Cursor()
	id := m.sess.Item(i).ID
	p, ok := m.sess.Proposal(i)
	if !ok {
		return m.setError("no proposal for %s", id)
	}
	if err := m.sess.AcceptProposal(i); err != nil {
		return m.setError("accept proposal %s: %v", id, err)
	}
	return m.afterMark("accepted proposal "+p.Status, id)
}

// applyProposal loads the current record's proposal into its draft without saving.
func (m annotModel) applyProposal() (annotModel, tea.Cmd) {
	i := m.sess.Cursor()
	id := m.sess.Item(i).ID
	if _, ok := m.sess.Proposal(i); !ok {
		return m.setError("no proposal for %s", id)
	}
	if err := m.sess.ApplyProposal(i); err != nil {
		return m.setError("load proposal %s: %v", id, err)
	}
	return m.setStatus("proposal loaded into draft — edit, then enter/u/s to save")
}

// afterMark moves on after a successful save described by what: to the next record
// left to do in the filter, or reports the end of the filter (or of the re-check).
func (m annotModel) afterMark(what, id string) (annotModel, tea.Cmd) {
	if !m.sess.Advance() {
		if m.sess.Recheck() {
			return m.setStatus("%s %s · end of re-check in %s", what, id, m.sess.Filter())
		}
		return m.setStatus("%s %s · all records in %s are done", what, id, m.sess.Filter())
	}
	return m.setStatus("%s %s", what, id)
}

// undo reverts the session's most recent mark and reports what it restored.
func (m annotModel) undo() (annotModel, tea.Cmd) {
	i, desc, ok, err := m.sess.Undo()
	if err != nil {
		return m.setError("undo: %v", err)
	}
	if !ok {
		return m.setStatus("nothing to undo")
	}
	return m.setStatus("undo: %s %s", desc, m.sess.Item(i).ID)
}

// applyType sets the current draft's type, reporting a target it cleared.
func (m annotModel) applyType(name string) (annotModel, tea.Cmd) {
	cleared, err := m.sess.SetType(m.sess.Cursor(), name)
	if err != nil {
		return m.setError("type: %v", err)
	}
	if cleared {
		return m.setStatus("type %s · target cleared (%s requires a null target)", name, name)
	}
	return m.setStatus("type %s", name)
}

// openHelp shows the help overlay, returning to the current mode on close.
func (m annotModel) openHelp() annotModel {
	m.helpReturn = m.mode
	m.helpScroll = 0
	m.mode = annotHelp
	return m
}

// openTypes enters the type picker on the draft's current type.
func (m annotModel) openTypes() annotModel {
	m.types = typeChooser{}
	cur := m.sess.Draft(m.sess.Cursor()).Type
	for i, t := range m.sess.Schema().Types {
		if t.Name == cur {
			m.types.cursor = i
		}
	}
	m.mode = annotTypes
	return m
}

// openFilter enters the filter picker on the active filter, counting the
// records each filter matches.
func (m annotModel) openFilter() annotModel {
	filters := m.sess.Filters()
	m.filters = filterChooser{counts: make([]int, len(filters))}
	for fi, f := range filters {
		if f == m.sess.Filter() {
			m.filters.cursor = fi
		}
		for i := range m.sess.Len() {
			if m.sess.Matches(i, f) {
				m.filters.counts[fi]++
			}
		}
	}
	m.mode = annotFilter
	return m
}

// openSpan enters span mode, refusing null-target types and empty texts.
func (m annotModel) openSpan() (annotModel, tea.Cmd) {
	i := m.sess.Cursor()
	d := m.sess.Draft(i)
	if d.Type != "" && m.sess.Schema().NullTarget(d.Type) {
		return m.setError("type %s requires a null target", d.Type)
	}
	runes := []rune(m.sess.Item(i).Text)
	if len(runes) == 0 {
		return m.setError("record %s has no text to select", m.sess.Item(i).ID)
	}
	m.span = newSpanSel(runes, d.Target)
	m.mode = annotSpan
	return m, nil
}

// updateSpan handles annotSpan: selection movement, accept, null and cancel.
func (m annotModel) updateSpan(key string) (annotModel, tea.Cmd) {
	moved := true
	switch key {
	case "esc":
		m.mode = annotMain
		return m, nil
	case "?":
		return m.openHelp(), nil
	case "enter":
		i := m.sess.Cursor()
		lo, hi := m.span.bounds()
		if err := m.sess.SetTarget(i, lo, hi+1); err != nil {
			return m.setError("target: %v", err)
		}
		m.mode = annotMain
		return m.setStatus("target %q [%d,%d)", string(m.span.runes[lo:hi+1]), lo, hi+1)
	case "n":
		m.sess.ClearTarget(m.sess.Cursor())
		m.mode = annotMain
		return m.setStatus("target: null")
	case "h", "left":
		m.span.move(-1)
	case "l", "right":
		m.span.move(1)
	case "H", "shift+left":
		m.span.extend(-1)
	case "L", "shift+right":
		m.span.extend(1)
	case "w":
		moved = m.span.nextWord()
	case "b":
		moved = m.span.prevWord()
	case "W":
		moved = m.span.extendNextWord()
	case "B":
		moved = m.span.extendPrevWord()
	case "0", "home":
		m.span.collapse(0)
	case "$", "end":
		m.span.collapse(len(m.span.runes) - 1)
	}
	if !moved {
		return m.setStatus("no more words")
	}
	return m, nil
}

// updateTypes handles annotTypes: fuzzy query, movement, digit pick, select.
func (m annotModel) updateTypes(k tea.KeyMsg) (annotModel, tea.Cmd) {
	types := m.sess.Schema().Types
	matches := m.types.matches(types)
	key := k.String()
	switch key {
	case "esc":
		m.mode = annotMain
		return m, nil
	case "enter":
		if m.types.cursor < 0 || m.types.cursor >= len(matches) {
			return m.setError("no type matches %q", string(m.types.query))
		}
		m.mode = annotMain
		return m.applyType(types[matches[m.types.cursor]].Name)
	case "up", "ctrl+p":
		m.types.cursor = clampIndex(m.types.cursor-1, len(matches))
		return m, nil
	case "down", "ctrl+n":
		m.types.cursor = clampIndex(m.types.cursor+1, len(matches))
		return m, nil
	case "backspace":
		if len(m.types.query) > 0 {
			m.types.query = m.types.query[:len(m.types.query)-1]
			m.types.cursor = 0
		}
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		n := int(key[0] - '0')
		if n > len(types) {
			return m.setError("no type %d: the schema declares %d", n, len(types))
		}
		m.mode = annotMain
		return m.applyType(types[n-1].Name)
	}
	if len(m.types.query) == 0 {
		switch key {
		case "j":
			m.types.cursor = clampIndex(m.types.cursor+1, len(matches))
			return m, nil
		case "k":
			m.types.cursor = clampIndex(m.types.cursor-1, len(matches))
			return m, nil
		}
	}
	if k.Type == tea.KeyRunes {
		m.types.query = append(m.types.query, k.Runes...)
		m.types.cursor = 0
	}
	return m, nil
}

// updateFilter handles annotFilter: j/k move, enter applies, esc closes.
func (m annotModel) updateFilter(key string) (annotModel, tea.Cmd) {
	filters := m.sess.Filters()
	switch key {
	case "esc":
		m.mode = annotMain
		return m, nil
	case "j", "down":
		m.filters.cursor = clampIndex(m.filters.cursor+1, len(filters))
		return m, nil
	case "k", "up":
		m.filters.cursor = clampIndex(m.filters.cursor-1, len(filters))
		return m, nil
	case "enter":
		m.mode = annotMain
		if m.filters.cursor < 0 || m.filters.cursor >= len(filters) {
			return m, nil
		}
		f := filters[m.filters.cursor]
		m.sess.SetFilter(f)
		if pos, n := m.sess.FilterPosition(); pos > 0 {
			return m.setStatus("filter %s %d/%d", f, pos, n)
		}
		return m.setStatus("filter %s: no matching records", f)
	}
	return m, nil
}

// updateGoto handles annotGoto: edit the query, enter jumps (the prompt stays
// open on a bad query), esc cancels. Non-printable keys are ignored.
func (m annotModel) updateGoto(k tea.KeyMsg) (annotModel, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = annotMain
		return m, nil
	case "enter":
		i, err := m.sess.Find(string(m.gotoQuery))
		if err != nil {
			return m.setError("go to: %v", err)
		}
		m.sess.SetCursor(i)
		m.mode = annotMain
		return m.setStatus("record %d / %d · %s", i+1, m.sess.Len(), m.sess.Item(i).ID)
	case "backspace":
		if len(m.gotoQuery) > 0 {
			m.gotoQuery = m.gotoQuery[:len(m.gotoQuery)-1]
		}
		return m, nil
	}
	switch k.Type {
	case tea.KeyRunes:
		m.gotoQuery = append(m.gotoQuery, k.Runes...)
	case tea.KeySpace:
		m.gotoQuery = append(m.gotoQuery, ' ')
	}
	return m, nil
}

// updateHelp handles annotHelp: j/k scroll, esc/?/q close.
func (m annotModel) updateHelp(key string) (annotModel, tea.Cmd) {
	switch key {
	case "esc", "?", "q":
		m.mode = m.helpReturn
	case "j", "down":
		m.helpScroll = min(m.helpScroll+1, m.helpMaxScroll())
	case "k", "up":
		m.helpScroll = max(0, m.helpScroll-1)
	}
	return m, nil
}
