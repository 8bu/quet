package tui

import (
	"fmt"
	"time"
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
	// annotGoto reads a queue position or record id to jump to.
	annotGoto
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

	activeSpan int // index into Schema().Spans of the field x, n, c and enter act on

	gotoQuery []rune // annotGoto input: a 1-based queue position or a record id
}

// newAnnotModel returns the annotation model over s. A session with proposals starts
// with a status line summarising them.
func newAnnotModel(s *annotate.Session) annotModel {
	m := annotModel{sess: s, mode: annotMain, width: 100, height: 30}
	if s.HasProposals() {
		m.status = proposalSummary(s)
		m.statusSeq = 1
	}
	return m
}

// proposalSummary says how many proposals apply to the queue and how many ids of the
// proposals file were ignored because they are not in it.
func proposalSummary(s *annotate.Session) string {
	summary := fmt.Sprintf("Proposals: %d for queue", s.ProposalCount())
	if n := len(s.IgnoredProposals()); n > 0 {
		summary += fmt.Sprintf(" · %d ignored (not in queue)", n)
	}
	return summary
}

// Init implements tea.Model: a startup status line is cleared after the usual delay.
func (m annotModel) Init() tea.Cmd {
	if m.status == "" {
		return nil
	}
	seq := m.statusSeq
	return tea.Tick(statusTTL, func(time.Time) tea.Msg { return statusExpireMsg{seq: seq} })
}

// Update implements tea.Model.
func (m annotModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next, cmd
}

// View implements tea.Model.
func (m annotModel) View() string { return m.view() }

// multiSpan reports whether the schema declares more than one span field.
func (m annotModel) multiSpan() bool { return len(m.sess.Schema().Spans) > 1 }

// spanUI reports whether the schema needs the multi-field wording and keys: it
// declares several span fields or per-span statuses. Without that the
// annotation screen reads exactly as for a single implicit target.
func (m annotModel) spanUI() bool {
	return m.multiSpan() || m.sess.Schema().HasSpanStatuses()
}

// active returns the span field the field keys act on.
func (m annotModel) active() annotate.SpanDef { return m.sess.Schema().Spans[m.activeSpan] }

// cycleSpan moves the active field by delta (+1 tab, -1 shift+tab), wrapping. It
// is a no-op with a single span field.
func (m annotModel) cycleSpan(delta int) annotModel {
	n := len(m.sess.Schema().Spans)
	m.activeSpan = ((m.activeSpan+delta)%n + n) % n
	return m
}

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
// m.sess.Filters() and the per-filter record counts taken when it opened.
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

// wordAt returns the index in s.words of the word containing rune i, or -1
// when i is not on a word rune.
func (s spanSel) wordAt(i int) int {
	for k, w := range s.words {
		if w[0] <= i && i < w[1] {
			return k
		}
	}
	return -1
}

// selectWord selects word k exactly and reports whether the selection changed.
func (s *spanSel) selectWord(k int) bool {
	w := s.words[k]
	if lo, hi := s.bounds(); lo == w[0] && hi == w[1]-1 {
		return false
	}
	s.anchor, s.head = w[0], w[1]-1
	return true
}

// nextWord selects the word under the selection's end when it is not already
// exactly selected, else the first whole word starting after the selection. It
// reports whether the selection moved.
func (s *spanSel) nextWord() bool {
	_, hi := s.bounds()
	if k := s.wordAt(hi); k >= 0 && s.selectWord(k) {
		return true
	}
	for k, w := range s.words {
		if w[0] > hi {
			return s.selectWord(k)
		}
	}
	return false
}

// prevWord selects the word under the selection's start when it is not
// already exactly selected, else the last whole word starting before the
// selection. It reports whether the selection moved.
func (s *spanSel) prevWord() bool {
	lo, _ := s.bounds()
	if k := s.wordAt(lo); k >= 0 && s.selectWord(k) {
		return true
	}
	for k := len(s.words) - 1; k >= 0; k-- {
		if s.words[k][0] < lo {
			return s.selectWord(k)
		}
	}
	return false
}

// extendNextWord grows the selection forward to the end of the next word
// after it, anchoring at its start. From a single non-word rune (a space or
// punctuation) it selects the next word instead, so the span never starts on
// that rune. It reports whether there was one.
func (s *spanSel) extendNextWord() bool {
	lo, hi := s.bounds()
	if lo == hi && !isWordRune(s.runes[lo]) {
		return s.nextWord()
	}
	for _, w := range s.words {
		if w[1]-1 > hi {
			s.anchor, s.head = lo, w[1]-1
			return true
		}
	}
	return false
}

// extendPrevWord grows the selection backward to the start of the previous
// word before it, anchoring at its end. From a single non-word rune it selects
// the previous word instead, so the span never ends on that rune. It reports
// whether there was one.
func (s *spanSel) extendPrevWord() bool {
	lo, hi := s.bounds()
	if lo == hi && !isWordRune(s.runes[lo]) {
		return s.prevWord()
	}
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
