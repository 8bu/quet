package tui

import (
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
)

// RunAnnotate starts the full-screen annotation TUI (alt screen) over s and
// blocks until quit.
func RunAnnotate(s *annotate.Session) error {
	_, err := tea.NewProgram(newAnnotModel(s), tea.WithAltScreen()).Run()
	return err
}

// annotMode is the annotation TUI's current input mode. Every mode owns its
// keys: main-mode shortcuts never fire in another mode.
type annotMode int

// Annotation modes.
const (
	// annotMain labels and navigates records.
	annotMain annotMode = iota
	// annotSpan selects the target span in the record text.
	annotSpan
	// annotTypes picks the draft type from the schema.
	annotTypes
	// annotFilter picks the active record filter.
	annotFilter
	// annotHelp shows the keyboard reference overlay.
	annotHelp
)

// annotModel is the Bubble Tea model of annotation mode. It is separate from
// the review model and only talks to the annotate.Session API.
type annotModel struct {
	sess *annotate.Session

	mode       annotMode
	helpReturn annotMode // mode restored when the help overlay closes
	helpScroll int       // first help row shown

	width, height int

	status    string
	statusErr bool
	statusSeq int

	quitArmed bool // q was pressed once over an unsaved draft

	types   typeChooser
	filters filterChooser
	span    spanSel
}

// newAnnotModel returns the annotation model over s.
func newAnnotModel(s *annotate.Session) annotModel {
	return annotModel{sess: s, mode: annotMain, width: 100, height: 30}
}

// Init implements tea.Model.
func (m annotModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m annotModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next, cmd
}

// View implements tea.Model.
func (m annotModel) View() string { return m.view() }

// typeChooser is the state of the type picker: a fuzzy query and the cursor
// among the matching schema types.
type typeChooser struct {
	query  []rune
	cursor int
}

// matches returns the schema indices of the types whose name fuzzy-matches
// the query, in schema order.
func (p typeChooser) matches(types []annotate.TypeDef) []int {
	out := make([]int, 0, len(types))
	q := string(p.query)
	for i, t := range types {
		if fuzzyMatch(t.Name, q) {
			out = append(out, i)
		}
	}
	return out
}

// filterChooser is the state of the filter picker: the cursor into
// annotate.Filters() and the per-filter record counts taken when it opened.
type filterChooser struct {
	cursor int
	counts []int
}

// spanSel is the span-mode selection over one record's runes: anchor..head,
// both inclusive, so the selection always covers at least one rune.
type spanSel struct {
	runes        []rune
	words        [][2]int // [start,end) rune ranges of the words, in order
	anchor, head int
}

// newSpanSel starts a selection over runes (non-empty) on target when it fits
// the text, else on the first word, else on the first rune.
func newSpanSel(runes []rune, target *annotate.Target) spanSel {
	s := spanSel{runes: runes, words: wordRanges(runes)}
	switch {
	case target != nil && target.Start >= 0 && target.Start < target.End && target.End <= len(runes):
		s.anchor, s.head = target.Start, target.End-1
	case len(s.words) > 0:
		s.anchor, s.head = s.words[0][0], s.words[0][1]-1
	}
	return s
}

// bounds returns the selection as an inclusive rune range lo..hi.
func (s spanSel) bounds() (lo, hi int) { return min(s.anchor, s.head), max(s.anchor, s.head) }

// clamp limits a rune index to the text.
func (s spanSel) clamp(i int) int { return max(0, min(i, len(s.runes)-1)) }

// collapse selects the single rune at i.
func (s *spanSel) collapse(i int) {
	i = s.clamp(i)
	s.anchor, s.head = i, i
}

// move collapses the selection one rune left or right of the head.
func (s *spanSel) move(delta int) { s.collapse(s.head + delta) }

// extend moves the head by delta runes, keeping the anchor.
func (s *spanSel) extend(delta int) { s.head = s.clamp(s.head + delta) }

// nextWord selects the first whole word starting after the selection. It
// reports whether there was one.
func (s *spanSel) nextWord() bool {
	_, hi := s.bounds()
	for _, w := range s.words {
		if w[0] > hi {
			s.anchor, s.head = w[0], w[1]-1
			return true
		}
	}
	return false
}

// prevWord selects the last whole word starting before the selection. It
// reports whether there was one.
func (s *spanSel) prevWord() bool {
	lo, _ := s.bounds()
	for i := len(s.words) - 1; i >= 0; i-- {
		if w := s.words[i]; w[0] < lo {
			s.anchor, s.head = w[0], w[1]-1
			return true
		}
	}
	return false
}

// extendNextWord grows the selection forward to the end of the next word
// after it, anchoring at its start. It reports whether there was one.
func (s *spanSel) extendNextWord() bool {
	lo, hi := s.bounds()
	for _, w := range s.words {
		if w[1]-1 > hi {
			s.anchor, s.head = lo, w[1]-1
			return true
		}
	}
	return false
}

// extendPrevWord grows the selection backward to the start of the previous
// word before it, anchoring at its end. It reports whether there was one.
func (s *spanSel) extendPrevWord() bool {
	lo, hi := s.bounds()
	for i := len(s.words) - 1; i >= 0; i-- {
		if w := s.words[i]; w[0] < lo {
			s.anchor, s.head = hi, w[0]
			return true
		}
	}
	return false
}

// isWordRune reports whether r belongs to a word: letters, digits and marks.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

// wordRanges returns the [start,end) rune ranges of every maximal run of word
// runes in runes.
func wordRanges(runes []rune) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range runes {
		switch {
		case isWordRune(r) && start < 0:
			start = i
		case !isWordRune(r) && start >= 0:
			out = append(out, [2]int{start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(runes)})
	}
	return out
}
