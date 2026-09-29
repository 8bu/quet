package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/checks"
	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/review"
)

// testConfig mirrors the documented defaults without depending on config.Load.
func testConfig() config.Config {
	return config.Config{
		Review: config.Review{SkipReviewed: true},
		Checks: checks.Options{
			MaxChars:               160,
			RepeatedCharThreshold:  5,
			WeirdSymbolRatio:       0.35,
			TemplateMinOccurrences: 8,
		},
	}
}

func testFlagDefs() []config.FlagDef {
	return []config.FlagDef{
		{Name: "slang", Description: "Notable Vietnamese slang or informal wording."},
		{Name: "typo", Description: "Natural typo, abbreviation, or shorthand."},
	}
}

// copyCorpus copies the repository fixture into a temp dir so tests never write
// next to the fixture (the sidecar DB is created beside the copy).
func copyCorpus(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "corpus.jsonl"))
	if err != nil {
		t.Fatalf("read corpus fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "corpus.jsonl")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatalf("copy corpus fixture: %v", err)
	}
	return path
}

func openSession(t *testing.T, path string) *review.Session {
	t.Helper()
	s, err := review.Open(path, testConfig(), testFlagDefs())
	if err != nil {
		t.Fatalf("review.Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// testModel returns a model over a session opened from a temp copy of the
// fixture corpus, sized like a normal terminal.
func testModel(t *testing.T) model {
	t.Helper()
	m := newModel(openSession(t, copyCorpus(t)))
	next, _ := m.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return next
}

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func specialKey(kt tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: kt} }

// send feeds msgs to the model in order and returns the final model and cmd.
func send(m model, msgs ...tea.Msg) (model, tea.Cmd) {
	var cmd tea.Cmd
	for _, msg := range msgs {
		m, cmd = m.update(msg)
	}
	return m, cmd
}

// runPaletteCommand opens the palette and runs the named command.
func runPaletteCommand(t *testing.T, m model, name string) model {
	t.Helper()
	m.pal.open()
	m.mode = ModePalette
	for i, c := range m.commands() {
		if c.name != name {
			continue
		}
		m.pal.cursor = i
		next, _ := m.update(specialKey(tea.KeyEnter))
		return next
	}
	t.Fatalf("palette command %q not found", name)
	return m
}

func TestModeTransitions(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.Msg
		want Mode
	}{
		{"edit opens", []tea.Msg{runeKey('e')}, ModeEdit},
		{"esc cancels edit", []tea.Msg{runeKey('e'), specialKey(tea.KeyEsc)}, ModeReview},
		{"ctrl+s saves and returns to review", []tea.Msg{runeKey('e'), specialKey(tea.KeyCtrlS)}, ModeReview},
		{"help opens", []tea.Msg{runeKey('?')}, ModeHelp},
		{"help closes on any key", []tea.Msg{runeKey('?'), runeKey('x')}, ModeReview},
		{"palette opens", []tea.Msg{runeKey(':')}, ModePalette},
		{"palette closes on esc", []tea.Msg{runeKey(':'), specialKey(tea.KeyEsc)}, ModeReview},
		{"search opens", []tea.Msg{runeKey('/')}, ModeSearch},
		{"search closes on esc", []tea.Msg{runeKey('/'), specialKey(tea.KeyEsc)}, ModeReview},
		{"flags open", []tea.Msg{runeKey('f')}, ModeFlags},
		{"flags close on esc", []tea.Msg{runeKey('f'), specialKey(tea.KeyEsc)}, ModeReview},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(t)
			next, _ := send(m, tc.keys...)
			if next.mode != tc.want {
				t.Fatalf("mode = %s, want %s", next.mode, tc.want)
			}
		})
	}
}

func TestPaletteOnlyModes(t *testing.T) {
	tests := []struct {
		command string
		want    Mode
	}{
		{"Change filter", ModeFilter},
		{"Show duplicates", ModeDuplicates},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			m := testModel(t)
			next := runPaletteCommand(t, m, tc.command)
			if next.mode != tc.want {
				t.Fatalf("mode = %s, want %s", next.mode, tc.want)
			}
			if next.mode == ModeFilter {
				if _, rows, _ := next.overlayContent(60, 20); len(rows) == 0 {
					t.Error("filter overlay rendered no rows")
				}
			}
		})
	}
}

func TestReviewShortcutsInactiveInEditMode(t *testing.T) {
	m := testModel(t)
	cur := m.sess.Current()
	if cur < 0 {
		t.Fatal("expected a current record")
	}
	before := m.sess.State(cur).EffectiveStatus()

	next, _ := m.update(runeKey('e'))
	if next.mode != ModeEdit {
		t.Fatalf("mode = %s, want edit", next.mode)
	}
	next, _ = send(next, runeKey('w'), runeKey('?'), runeKey('f'), runeKey(' '))
	if next.mode != ModeEdit {
		t.Fatalf("mode = %s, want edit after typing review keys", next.mode)
	}
	if got := next.sess.State(cur).EffectiveStatus(); got != before {
		t.Errorf("status = %s, want %s (review shortcut leaked into edit mode)", got, before)
	}
	if text := next.editor.Value(); !strings.Contains(text, "w?f") {
		t.Errorf("editor value %q does not contain the typed keys", text)
	}

	next, _ = send(next, specialKey(tea.KeyEsc))
	if next.mode != ModeReview {
		t.Fatalf("mode = %s, want review after esc", next.mode)
	}
}

func TestReviewKeysDoNotFireInOtherModes(t *testing.T) {
	m := testModel(t)
	cur := m.sess.Current()
	before := m.sess.State(cur).EffectiveStatus()

	// help, palette, search, flags all ignore w; only the overlay key acts.
	modes := []struct {
		open tea.Msg
		mode Mode
	}{
		{runeKey('?'), ModeHelp},
		{runeKey(':'), ModePalette},
		{runeKey('/'), ModeSearch},
		{runeKey('f'), ModeFlags},
	}
	for _, tc := range modes {
		next, _ := m.update(tc.open)
		if next.mode != tc.mode {
			t.Fatalf("mode = %s, want %s", next.mode, tc.mode)
		}
		next, _ = next.update(runeKey('w'))
		if got := next.sess.State(cur).EffectiveStatus(); got != before {
			t.Errorf("w in %s changed status to %s", tc.mode, got)
		}
	}
}

func TestApproveShowsFeedbackAndAdvances(t *testing.T) {
	m := testModel(t)
	cur := m.sess.Current()
	if cur < 0 {
		t.Fatal("expected a current record")
	}
	next, _ := m.update(runeKey('w'))
	if got := next.sess.State(cur).EffectiveStatus(); got != review.Approved {
		t.Fatalf("status = %s, want approved", got)
	}
	if !strings.HasPrefix(next.status, "Approved") {
		t.Errorf("status message = %q, want it to start with Approved", next.status)
	}
	if next.mode != ModeReview {
		t.Errorf("mode = %s, want review", next.mode)
	}
}

func TestQuitFromReview(t *testing.T) {
	m := testModel(t)
	_, cmd := m.update(runeKey('q'))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q returned %T, want tea.QuitMsg", cmd())
	}
	if _, cmd := m.update(specialKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatal("ctrl+c returned no command")
	}
}

func TestFootersMatchSpec(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeReview, "w approve  s reject  a/d navigate  e edit  f flags  space review  ? help"},
		{ModeEdit, "ctrl+s save  esc cancel  ctrl+r revert"},
		{ModeFlags, "j/k move  space toggle  enter apply  esc close"},
		{ModePalette, "esc close  enter select"},
		{ModeSearch, "esc close  enter select"},
		{ModeFilter, "esc close  enter apply"},
		{ModeDuplicates, "esc close  enter select"},
		{ModeExport, "enter export  tab edit path  esc close"},
		{ModeHelp, "esc close"},
	}
	m := testModel(t)
	for _, tc := range tests {
		m.mode = tc.mode
		if got := m.footer(); got != tc.want {
			t.Errorf("footer(%s) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestViewRendersWithoutOverflow(t *testing.T) {
	sizes := []struct{ w, h int }{{40, 10}, {120, 40}, {20, 5}, {1, 1}}
	modes := []Mode{ModeReview, ModeEdit, ModeFlags, ModeHelp, ModePalette, ModeSearch, ModeFilter, ModeDuplicates, ModeExport}
	for _, size := range sizes {
		for _, mode := range modes {
			m := testModel(t)
			next, _ := m.update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			m = next
			m = enterMode(t, m, mode)
			out := m.View()
			if out == "" {
				t.Fatalf("empty view at %dx%d in %s mode", size.w, size.h, mode)
			}
			lines := strings.Split(out, "\n")
			if len(lines) > size.h {
				t.Errorf("%s at %dx%d: %d lines, want <= %d", mode, size.w, size.h, len(lines), size.h)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > size.w {
					t.Errorf("%s at %dx%d: line %d width %d, want <= %d", mode, size.w, size.h, i, w, size.w)
				}
			}
		}
	}
}

func TestFitRecordCollapsesToContent(t *testing.T) {
	base := computeLayout(120, 60) // recordH 42 (the max), detailsH 18
	tests := []struct {
		name        string
		l           layout
		rows        int
		wantRecordH int
	}{
		{"one line shrinks to borders plus one row", base, 1, 3},
		{"no rows still keeps one row", base, 0, 3},
		{"five lines", base, 5, 7},
		{"long text is capped at the max", base, 100, base.recordH},
		{"text exactly filling the max is unchanged", base, base.recordH - 2, base.recordH},
		{"no details panel: nothing to hand rows to", layout{rightW: 40, recordH: 5}, 1, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.l.fitRecord(tt.rows)
			if got.recordH != tt.wantRecordH {
				t.Errorf("recordH = %d, want %d", got.recordH, tt.wantRecordH)
			}
			if got.recordH+got.detailsH != tt.l.recordH+tt.l.detailsH {
				t.Errorf("column height changed: %d+%d, want %d", got.recordH, got.detailsH, tt.l.recordH+tt.l.detailsH)
			}
		})
	}
}

// enterMode switches m into mode the way the UI does.
func enterMode(t *testing.T, m model, mode Mode) model {
	t.Helper()
	switch mode {
	case ModeReview:
		return m
	case ModeEdit:
		if !m.openEditor() {
			t.Fatal("could not enter edit mode")
		}
		return m
	case ModeFlags:
		next, _ := m.openFlags()
		if next.mode != ModeFlags {
			t.Fatal("could not enter flags mode")
		}
		return next
	case ModeHelp:
		next, _ := m.update(runeKey('?'))
		return next
	case ModePalette:
		next, _ := m.update(runeKey(':'))
		return next
	case ModeSearch:
		next, _ := m.update(runeKey('/'))
		return next
	case ModeFilter:
		next := runPaletteCommand(t, m, "Change filter")
		if next.mode != ModeFilter {
			t.Fatal("could not enter filter mode")
		}
		return next
	case ModeDuplicates:
		next := runPaletteCommand(t, m, "Show duplicates")
		if next.mode != ModeDuplicates {
			t.Fatal("could not enter duplicates mode")
		}
		return next
	case ModeExport:
		next := runPaletteCommand(t, m, "Export to file")
		if next.mode != ModeExport {
			t.Fatal("could not enter export mode")
		}
		return next
	}
	t.Fatalf("unhandled mode %s", mode)
	return m
}

func TestDuplicatesOverlayListsMatches(t *testing.T) {
	m := testModel(t)
	// The fixture's note-002 and note-003 differ only by case and whitespace.
	next, _ := m.update(runeKey('d'))
	if got := next.sess.Current(); got != 1 {
		t.Fatalf("current = %d, want 1", got)
	}
	next = runPaletteCommand(t, next, "Show duplicates")
	if next.mode != ModeDuplicates {
		t.Fatalf("mode = %s, want duplicates", next.mode)
	}
	_, rows, sel := next.overlayContent(60, 20)
	if len(rows) == 0 || sel < 0 {
		t.Fatalf("overlay rows = %q, sel = %d", rows, sel)
	}
	if joined := strings.Join(rows, "\n"); !strings.Contains(joined, "#3") {
		t.Errorf("duplicate of record 2 not listed:\n%s", joined)
	}
	if idx, ok := next.dups.selected(); !ok || idx != 2 {
		t.Errorf("selected duplicate = %d (ok=%v), want 2", idx, ok)
	}
}

func TestBurstAndPasteAreNotCommands(t *testing.T) {
	m := testModel(t)
	cur := m.sess.Current()
	if cur < 0 {
		t.Fatal("expected a current record")
	}
	before := m.sess.State(cur).EffectiveStatus()

	// A burst of runes (fast typing) must not fire single-key commands.
	next, _ := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w s q")})
	if got := next.sess.State(cur).EffectiveStatus(); got != before {
		t.Errorf("burst changed status to %s", got)
	}
	if next.mode != ModeReview {
		t.Errorf("burst changed mode to %s", next.mode)
	}

	// Pasting the literal text "ctrl+s" while editing must insert it, not save.
	next, _ = next.update(runeKey('e'))
	if next.mode != ModeEdit {
		t.Fatalf("mode = %s, want edit", next.mode)
	}
	next, _ = next.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ctrl+s"), Paste: true})
	if next.mode != ModeEdit {
		t.Errorf("mode = %s, pasted text must not save the record", next.mode)
	}
	if !strings.Contains(next.editor.Value(), "ctrl+s") {
		t.Errorf("pasted text missing from editor value %q", next.editor.Value())
	}
}

func TestEmptyCorpus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty corpus: %v", err)
	}
	m := newModel(openSession(t, path))
	next, _ := m.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next

	if !strings.Contains(m.View(), "No records") {
		t.Errorf("view does not mention an empty corpus:\n%s", m.View())
	}
	if _, cmd := m.update(runeKey('q')); cmd == nil {
		t.Error("q must still quit on an empty corpus")
	}
	if next, _ := m.update(runeKey('?')); next.mode != ModeHelp {
		t.Error("? must still open help on an empty corpus")
	}
	if next, _ := m.update(runeKey('e')); next.mode != ModeReview {
		t.Error("e must not enter edit mode with no records")
	}
}
