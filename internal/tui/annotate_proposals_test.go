package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
)

// annotProposalsJSONL returns the proposals fixture over the queue fixture: r1 a valid
// lend proposal with a target, confidence and reason; r2 an undeclared type; r3 a
// valid uncertain transfer; r4 and r5 none; and "elsewhere", which is not in the queue.
func annotProposalsJSONL(t *testing.T) string {
	t.Helper()
	text := annotQueue[0].Text
	start := runeOffset(t, text, "2tr")
	lines := []string{
		fmt.Sprintf(`{"id":"r1","annotation_status":"complete","type":"lend","target":{"text":"2tr","start":%d,"end":%d},`+
			`"confidence":0.874,"reason":"Cho mượn is lending","note":"checked","extra":true}`, start, start+3),
		`{"id":"r2","annotation_status":"complete","type":"bogus","target":null,"confidence":0.5}`,
		`{"id":"r3","annotation_status":"uncertain","type":"transfer","target":null}`,
		`{"id":"elsewhere","annotation_status":"complete","type":"lend","target":null}`,
	}
	return strings.Join(lines, "\n") + "\n"
}

// annotProposalModel opens a session over the queue fixture with the proposals in body,
// and returns a model sized 120x40 plus the labels path.
func annotProposalModel(t *testing.T, body string) (annotModel, string) {
	t.Helper()
	schemaPath, queuePath, outPath := writeAnnotFiles(t)
	s, err := annotate.Open(queuePath, schemaPath, outPath)
	if err != nil {
		t.Fatalf("annotate.Open: %v", err)
	}
	proposals := filepath.Join(filepath.Dir(outPath), "proposals.jsonl")
	if err := os.WriteFile(proposals, []byte(body), 0o600); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if _, err := s.LoadProposals(proposals); err != nil {
		t.Fatalf("LoadProposals: %v", err)
	}
	m, _ := newAnnotModel(s).update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, outPath
}

func TestAnnotateProposalBlockIsDistinctFromCurrent(t *testing.T) {
	m, _ := annotProposalModel(t, annotProposalsJSONL(t))
	v := m.View()
	for _, want := range []string{"Proposal — not accepted", "Current", "Confidence: 0.87",
		"Reason: Cho mượn is lending", "Note: checked", `Target: "2tr"`, "Type: lend", "Status: complete"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if cur, prop := strings.Index(v, "Current"), strings.Index(v, "Proposal — not accepted"); cur < 0 || prop < cur {
		t.Errorf("want the Current section before the proposal block (Current at %d, proposal at %d):\n%s", cur, prop, v)
	}
	// The record itself is not labeled: the current section shows it unfinished, and the
	// proposal's values are not presented as saved.
	current := v[strings.Index(v, "Current"):strings.Index(v, "Proposal — not accepted")]
	if !strings.Contains(current, "Status: unfinished") || strings.Contains(current, "Saved:") {
		t.Errorf("current section should show the unlabeled record:\n%s", current)
	}
	if strings.Contains(v, "matches current") || strings.Contains(v, "invalid") {
		t.Errorf("unlabeled record with a valid proposal shows a match/invalid marker:\n%s", v)
	}
}

func TestAnnotateProposalHeaderAndStartupStatus(t *testing.T) {
	m, _ := annotProposalModel(t, annotProposalsJSONL(t))
	want := "Proposals: 3 for queue · 1 ignored (not in queue)"
	if m.status != want {
		t.Errorf("startup status = %q, want %q", m.status, want)
	}
	if v := m.View(); strings.Count(v, want) != 2 {
		t.Errorf("want the summary in both the header and the status row:\n%s", v)
	}
	if m.Init() == nil {
		t.Error("Init scheduled no expiry for the startup status")
	}

	// Without ignored ids the ignored part is omitted.
	body := `{"id":"r1","annotation_status":"skipped"}` + "\n"
	m, _ = annotProposalModel(t, body)
	if want := "Proposals: 1 for queue"; m.status != want {
		t.Errorf("startup status = %q, want %q", m.status, want)
	}
}

func TestAnnotateProposalMatchAndInvalidMarkers(t *testing.T) {
	m, _ := annotProposalModel(t, annotProposalsJSONL(t))
	// r3: label it the same as its proposal, then the block says so.
	m.sess.SetCursor(2)
	m, _ = sendAnnot(m, runeKey('P'), runeKey('u'))
	m.sess.SetCursor(2)
	v := m.View()
	if !strings.Contains(v, "✓ matches current") {
		t.Errorf("r3 view lacks the match marker:\n%s", v)
	}
	// r2: undeclared type.
	m.sess.SetCursor(1)
	v = m.View()
	if !strings.Contains(v, "⚠ invalid:") || !strings.Contains(v, "bogus") {
		t.Errorf("r2 view lacks the invalid marker naming the type:\n%s", v)
	}
}

func TestAnnotateProposalFooterOnlyWithProposal(t *testing.T) {
	m, _ := annotProposalModel(t, annotProposalsJSONL(t))
	if f := strings.Join(m.footerItems(), " | "); !strings.Contains(f, "p accept proposal") || !strings.Contains(f, "P edit proposal") {
		t.Errorf("footer on a record with a proposal = %q", f)
	}
	if !strings.Contains(m.View(), "p accept proposal") {
		t.Errorf("rendered footer lacks p/P:\n%s", m.View())
	}
	m.sess.SetCursor(3) // r4 has no proposal
	if f := strings.Join(m.footerItems(), " | "); strings.Contains(f, "proposal") {
		t.Errorf("footer on a record without a proposal = %q", f)
	}
	v := m.View()
	if strings.Contains(v, "Proposal — not accepted") || strings.Contains(v, "Current") {
		t.Errorf("record without a proposal shows a proposal block:\n%s", v)
	}
}

func TestAnnotateProposalAcceptSavesAndAdvances(t *testing.T) {
	m, out := annotProposalModel(t, annotProposalsJSONL(t))
	m, _ = sendAnnot(m, runeKey('p'))
	if m.statusErr || m.status != "accepted proposal complete r1" {
		t.Errorf("status = %q (error %v)", m.status, m.statusErr)
	}
	if got := m.sess.Item(m.sess.Cursor()).ID; got != "r2" {
		t.Errorf("cursor on %s, want r2 (advanced like a mark)", got)
	}
	lines := readLabelLines(t, out)
	if len(lines) != 1 {
		t.Fatalf("labels file has %d lines, want 1: %q", len(lines), lines)
	}
	for _, leaked := range []string{"confidence", "reason", "0.87", "extra"} {
		if strings.Contains(lines[0], leaked) {
			t.Errorf("label line %q contains %q", lines[0], leaked)
		}
	}
	l := readLabels(t, out)["r1"]
	if l.Status != annotate.StatusComplete || labelType(l) != "lend" || l.Spans["target"] == nil || l.Spans["target"].Text != "2tr" {
		t.Errorf("saved label = %+v, want complete lend over 2tr", l)
	}
	if m.sess.Dirty(0) {
		t.Error("r1 still has a draft after accepting")
	}
	// Accepting is undoable like a mark.
	m, _ = sendAnnot(m, runeKey('z'))
	if _, ok := m.sess.Label(0); ok {
		t.Error("undo left the accepted label in place")
	}
}

func TestAnnotateProposalAcceptInvalidWritesNothing(t *testing.T) {
	m, out := annotProposalModel(t, annotProposalsJSONL(t))
	m.sess.SetCursor(1)
	m, _ = sendAnnot(m, runeKey('p'))
	if !m.statusErr || !strings.Contains(m.status, "bogus") {
		t.Errorf("status = %q (error %v), want the validation error naming the type", m.status, m.statusErr)
	}
	if m.sess.Cursor() != 1 {
		t.Errorf("cursor moved to %d after a refused accept", m.sess.Cursor())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("labels file written after a refused accept (stat error %v)", err)
	}
	if _, ok := m.sess.Label(1); ok {
		t.Error("refused accept left a label")
	}
}

func TestAnnotateProposalKeysWithoutProposal(t *testing.T) {
	m, out := annotProposalModel(t, annotProposalsJSONL(t))
	m.sess.SetCursor(3)
	for _, key := range []rune{'p', 'P'} {
		next, _ := sendAnnot(m, runeKey(key))
		if !next.statusErr || next.status != "no proposal for r4" {
			t.Errorf("%c: status = %q (error %v), want \"no proposal for r4\"", key, next.status, next.statusErr)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("labels file written (stat error %v)", err)
	}
}

func TestAnnotateProposalLoadIntoDraftThenEdit(t *testing.T) {
	m, out := annotProposalModel(t, annotProposalsJSONL(t))
	m, _ = sendAnnot(m, runeKey('P'))
	if m.statusErr || m.status != "proposal loaded into draft — edit, then enter/u/s to save" {
		t.Errorf("status = %q (error %v)", m.status, m.statusErr)
	}
	d := m.sess.Draft(0)
	if d.Type != "lend" || d.Spans["target"] == nil || d.Spans["target"].Text != "2tr" || !m.sess.Dirty(0) {
		t.Errorf("draft after P = %+v (dirty %v), want lend over 2tr", d, m.sess.Dirty(0))
	}
	if m.sess.Cursor() != 0 {
		t.Errorf("cursor moved to %d by P", m.sess.Cursor())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("P wrote the labels file (stat error %v)", err)
	}

	// Change the type (borrow is schema type 3), then save with enter.
	m, _ = sendAnnot(m, runeKey('3'), enterKey)
	l := readLabels(t, out)["r1"]
	if l.Status != annotate.StatusComplete || labelType(l) != "borrow" || l.Spans["target"] == nil || l.Spans["target"].Text != "2tr" {
		t.Errorf("saved label = %+v, want complete borrow over 2tr", l)
	}
	if strings.Contains(strings.Join(readLabelLines(t, out), "\n"), "confidence") {
		t.Error("confidence reached the labels file")
	}
}

func TestAnnotateProposalFiltersAndHelp(t *testing.T) {
	m, _ := annotProposalModel(t, annotProposalsJSONL(t))
	m, _ = sendAnnot(m, runeKey('f'))
	v := m.View()
	for _, want := range []string{"proposed", "unproposed", "proposed uncertain"} {
		if !strings.Contains(v, want) {
			t.Errorf("filter picker lacks %q:\n%s", want, v)
		}
	}
	// Apply "proposed uncertain": only r3 matches.
	filters := m.sess.Filters()
	m, _ = sendAnnot(m, keys(func() []tea.Msg {
		var msgs []tea.Msg
		for range len(filters) - 1 {
			msgs = append(msgs, runeKey('j'))
		}
		return append(msgs, enterKey)
	}())...)
	if got := m.sess.Filter(); got != annotate.FilterProposedUncertain {
		t.Fatalf("filter = %v, want proposed uncertain", got)
	}
	if _, n := m.sess.FilterPosition(); n != 1 {
		t.Errorf("proposed uncertain matches %d records, want 1 (r3)", n)
	}

	m, _ = sendAnnot(m, tea.WindowSizeMsg{Width: 160, Height: 60}, runeKey('?'))
	v = m.View()
	for _, want := range []string{"Proposals", "Accept and save", "Load, then edit", "Suggestions only", "Not ground truth", "p/enter/u/s only"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
}

func TestAnnotateProposalViewSizes(t *testing.T) {
	sizes := [][2]int{{80, 24}, {0, 0}, {1, 1}, {3, 3}, {10, 4}, {20, 6}, {40, 10}, {200, 3}, {60, 16}}
	for _, setup := range [][]tea.Msg{nil, typed("f"), typed("?"), typed("P")} {
		for _, sz := range sizes {
			m, _ := annotProposalModel(t, annotProposalsJSONL(t))
			m, _ = sendAnnot(m, keys([]tea.Msg{tea.WindowSizeMsg{Width: sz[0], Height: sz[1]}}, setup)...)
			h := sz[1]
			if sz[0] == 0 {
				h = 24
			}
			if lines := strings.Split(m.View(), "\n"); len(lines) > h {
				t.Errorf("%dx%d after %v: %d lines", sz[0], sz[1], setup, len(lines))
			}
		}
	}
}

// TestAnnotateNoProposalsSessionUnchanged pins that a session without --proposals
// shows no proposal UI and ignores p/P.
func TestAnnotateNoProposalsSessionUnchanged(t *testing.T) {
	m, out := annotTestModel(t)
	v := m.View()
	for _, unwanted := range []string{"Proposal", "proposal", "Current", "p accept"} {
		if strings.Contains(v, unwanted) {
			t.Errorf("no-proposals view contains %q:\n%s", unwanted, v)
		}
	}
	for _, key := range []rune{'p', 'P'} {
		next, cmd := sendAnnot(m, runeKey(key))
		if next.status != "" || cmd != nil || next.sess.Cursor() != 0 || next.sess.Dirty(0) {
			t.Errorf("%c changed a no-proposals session: status %q", key, next.status)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("labels file written (stat error %v)", err)
	}
	if m.Init() != nil {
		t.Error("Init scheduled a command without proposals")
	}

	m, _ = sendAnnot(m, runeKey('f'))
	v = m.View()
	if strings.Contains(v, "proposed") {
		t.Errorf("filter picker lists proposal filters without proposals:\n%s", v)
	}
	for _, want := range []string{"unfinished", "complete", "uncertain", "skipped"} {
		if !strings.Contains(v, want) {
			t.Errorf("filter picker lacks %q:\n%s", want, v)
		}
	}

	m, _ = sendAnnot(m, escKey, tea.WindowSizeMsg{Width: 160, Height: 60}, runeKey('?'))
	if v := m.View(); strings.Contains(v, "Proposals") {
		t.Errorf("help shows the Proposals group without proposals:\n%s", v)
	}
}
