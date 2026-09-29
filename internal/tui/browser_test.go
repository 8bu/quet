package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/review"
)

// browserTree builds a directory with folders, supported and unsupported
// files, hidden entries and one sidecar.
func browserTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"b", "A", ".git"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"x.jsonl", "y.JSON", "notes.txt", "x.jsonl.quet.db", "readme.md", ".hidden.jsonl", "b/inner.jsonl"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("{\"text\":\"a\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func entryNames(es []entry) []string {
	names := make([]string, len(es))
	for i, e := range es {
		names[i] = e.name
	}
	return names
}

func TestReadEntries(t *testing.T) {
	root := browserTree(t)

	got, err := readEntries(root, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"..", "A", "b", "notes.txt", "x.jsonl", "y.JSON"}
	if strings.Join(entryNames(got), ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v", entryNames(got), want)
	}
	for _, e := range got {
		if e.reviewed != (e.name == "x.jsonl") {
			t.Errorf("%s reviewed = %v", e.name, e.reviewed)
		}
	}

	withHidden, err := readEntries(root, true)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"..", ".git", "A", "b", ".hidden.jsonl", "notes.txt", "x.jsonl", "y.JSON"}
	if strings.Join(entryNames(withHidden), ",") != strings.Join(want, ",") {
		t.Fatalf("entries with hidden = %v, want %v", entryNames(withHidden), want)
	}

	top, err := readEntries(string(filepath.Separator), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) > 0 && top[0].name == parentName {
		t.Fatal("root directory lists a parent link")
	}
}

// press sends keys to a browser. When a key starts the gate, its command is run
// and the result delivered, the way the real program would.
func press(t *testing.T, m tea.Model, keys ...tea.KeyMsg) tea.Model {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = m.Update(k)
		if b, ok := m.(browser); ok && b.checking != "" && cmd != nil {
			m, _ = m.Update(cmd())
		}
	}
	return m
}

func TestBrowserNavigation(t *testing.T) {
	root := browserTree(t)
	b, err := newBrowser(root, func(string) (*review.Session, error) { return nil, errors.New("unused") })
	if err != nil {
		t.Fatal(err)
	}
	if got := b.entries[b.cursor].name; got != "A" {
		t.Fatalf("initial cursor on %q, want first entry after the parent link", got)
	}

	m := press(t, b, runeKey('s'), runeKey('d')) // down to b/, open it
	b = m.(browser)
	if b.dir != filepath.Join(root, "b") || b.entries[b.cursor].name != "inner.jsonl" {
		t.Fatalf("after entering b: dir %s cursor %q", b.dir, b.entries[b.cursor].name)
	}

	b = press(t, b, runeKey('a')).(browser)
	if b.dir != root || b.entries[b.cursor].name != "b" {
		t.Fatalf("after going up: dir %s cursor %q, want %s on b", b.dir, b.entries[b.cursor].name, root)
	}

	b = press(t, b, runeKey('.')).(browser)
	if b.entries[b.cursor].name != "b" || !strings.Contains(strings.Join(entryNames(b.entries), ","), ".git") {
		t.Fatalf("toggling hidden: cursor %q entries %v", b.entries[b.cursor].name, entryNames(b.entries))
	}
}

func TestBrowserGateFailureStaysInBrowser(t *testing.T) {
	root := browserTree(t)
	var opened []string
	b, err := newBrowser(root, func(path string) (*review.Session, error) {
		opened = append(opened, path)
		return nil, errors.New("no record has text")
	})
	if err != nil {
		t.Fatal(err)
	}
	m := press(t, b, runeKey('G'), specialKey(tea.KeyEnter))
	got, ok := m.(browser)
	if !ok {
		t.Fatalf("model after failed gate is %T, want browser", m)
	}
	if len(opened) != 1 || opened[0] != filepath.Join(root, "y.JSON") {
		t.Fatalf("gate called with %v", opened)
	}
	if !got.statusErr || !strings.Contains(got.status, "y.JSON") || !strings.Contains(got.status, "no record has text") {
		t.Fatalf("status = %q (err %v), want the gate error", got.status, got.statusErr)
	}
	if got.checking != "" {
		t.Fatal("browser still waiting for the gate")
	}
	// Keys work again after the failure.
	if got = press(t, got, runeKey('w')).(browser); got.entries[got.cursor].name != "x.jsonl" {
		t.Fatalf("cursor after failure and w = %q", got.entries[got.cursor].name)
	}
}

func TestBrowserOpensIntoReview(t *testing.T) {
	path := copyCorpus(t)
	b, err := newBrowser(filepath.Dir(path), func(p string) (*review.Session, error) {
		return review.Open(p, testConfig(), testFlagDefs())
	})
	if err != nil {
		t.Fatal(err)
	}
	sized, _ := b.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	b = sized.(browser)
	for i, e := range b.entries {
		if e.name == filepath.Base(path) {
			b.cursor = i
		}
	}
	m := press(t, b, specialKey(tea.KeyEnter))
	rm, ok := m.(model)
	if !ok {
		t.Fatalf("model after opening is %T, want review model", m)
	}
	t.Cleanup(func() { rm.sess.Close() })
	if rm.sess.Corpus.Path != path || rm.width != 120 || rm.height != 40 {
		t.Fatalf("review model: path %s size %dx%d", rm.sess.Corpus.Path, rm.width, rm.height)
	}
	if !strings.Contains(rm.status, "Opened") {
		t.Fatalf("status = %q, want an Opened confirmation", rm.status)
	}
}

func TestBrowserViewFitsTerminal(t *testing.T) {
	root := browserTree(t)
	long := strings.Repeat("malformed JSON on line 2 ", 20)
	for _, size := range []struct{ w, h int }{{1, 1}, {20, 5}, {40, 10}, {120, 40}} {
		for _, status := range []string{"", long} {
			b, err := newBrowser(root, nil)
			if err != nil {
				t.Fatal(err)
			}
			m, _ := b.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			if status != "" {
				m, _ = m.(browser).fail("%s", status)
			}
			lines := strings.Split(m.View(), "\n")
			if len(lines) > size.h {
				t.Errorf("%dx%d: %d lines", size.w, size.h, len(lines))
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > size.w {
					t.Errorf("%dx%d: line %d is %d cells wide", size.w, size.h, i, w)
				}
			}
		}
	}
}
