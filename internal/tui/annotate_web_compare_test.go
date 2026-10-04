package tui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
)

// localLabel builds a complete local label of the target span for the queue fixture record i.
func localLabel(t *testing.T, i int, typ, target string) annotate.Label {
	t.Helper()
	text := annotQueue[i].Text
	l := annotate.Label{Status: annotate.StatusComplete, Type: &typ, Spans: map[string]*annotate.Target{"target": nil}}
	if target != "" {
		start := runeOffset(t, text, target)
		l.Spans["target"] = &annotate.Target{Text: target, Start: start, End: start + len([]rune(target))}
	}
	return l
}

// referenceLabels returns the labels file bytes a normal annotation session writes after marking record i of
// the queue fixture complete with type typ and target (the independent oracle of the compare tests).
func referenceLabels(t *testing.T, m annotModel, marks ...refMark) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "ref.jsonl")
	s, err := annotate.Open(m.sess.QueuePath(), m.sess.SchemaPath(), out)
	if err != nil {
		t.Fatal(err)
	}
	for _, mk := range marks {
		if _, err := s.SetType(mk.index, mk.typ); err != nil {
			t.Fatal(err)
		}
		if mk.target != "" {
			start := runeOffset(t, annotQueue[mk.index].Text, mk.target)
			if err := s.SetSpan(mk.index, "target", start, start+len([]rune(mk.target))); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Mark(mk.index, annotate.StatusComplete); err != nil {
			t.Fatal(err)
		}
	}
	return readFile(t, out)
}

// refMark is one complete label of referenceLabels.
type refMark struct {
	index  int
	typ    string
	target string
}

func TestCompareShowsCandidatesGroupedAndIgnoredCount(t *testing.T) {
	m, f, _ := openCompareModel(t)
	if got := f.callList(); !slices.Contains(got, "GET /api/admin/projects/queue/labels") {
		t.Errorf("labels not pulled: %v", got)
	}
	if m.sess.Cursor() != 0 {
		t.Fatalf("cursor = %d, want the first disagreement r1", m.sess.Cursor())
	}
	rows := m.remoteCandidates(0)
	if len(rows) != 2 || !slices.Equal(rows[0].users, []string{"alice", "bob"}) || !slices.Equal(rows[1].users, []string{"carol"}) {
		t.Fatalf("r1 candidates = %+v", rows)
	}
	v := m.View()
	for _, want := range []string{"Quet — compare", "origin/queue", "alice, bob, carol", "Compare 1 / 5 · to decide",
		"1 alice, bob · ✓ complete · lend", "2 carol · ✓ complete · borrow", "L Local · not labelled",
		"Cho anh Nam mượn 2tr", "ID: r1", "ignored 2 (not in queue)", "to decide 1", "unanimous 3", "1-9 pick"} {
		if !strings.Contains(v, want) {
			t.Errorf("compare view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "x1") || strings.Contains(v, "zz9") {
		t.Errorf("ignored ids shown:\n%s", v)
	}
	m, _ = m.update(webKey("esc"))
	if m.mode != annotMain {
		t.Errorf("esc left mode %d", m.mode)
	}
}

func TestComparePickWritesLabelAndUndoes(t *testing.T) {
	m, _, labelsPath := openCompareModel(t)
	m, _ = webPress(t, m, "1") // alice, bob: lend
	if got, want := readFile(t, labelsPath), referenceLabels(t, m, refMark{0, "lend", "anh Nam"}); got != want {
		t.Errorf("labels file after picking 1:\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(m.status, "saved alice, bob's label for r1") {
		t.Errorf("status = %q", m.status)
	}
	if l, _ := m.sess.Label(0); l.Type == nil || *l.Type != "lend" {
		t.Errorf("local label = %+v", l)
	}
	if v := m.View(); !strings.Contains(v, "(same as Local)") {
		t.Errorf("the picked candidate is not marked:\n%s", v)
	}
	before := readFile(t, labelsPath)
	m, _ = webPress(t, m, "1") // already the saved label: no new write
	if !strings.Contains(m.status, "already has alice, bob's label") || readFile(t, labelsPath) != before {
		t.Errorf("re-picking rewrote or misreported: %q", m.status)
	}

	m, _ = webPress(t, m, "2") // carol: borrow replaces it
	if got, want := readFile(t, labelsPath), referenceLabels(t, m, refMark{0, "borrow", "anh Nam"}); got != want {
		t.Errorf("labels file after picking 2:\n%s\nwant\n%s", got, want)
	}
	m, _ = webPress(t, m, "z")
	if got, want := readFile(t, labelsPath), referenceLabels(t, m, refMark{0, "lend", "anh Nam"}); got != want {
		t.Errorf("labels file after z:\n%s\nwant\n%s", got, want)
	}
	m, _ = webPress(t, m, "3")
	if !m.statusErr || !strings.Contains(m.status, "no candidate 3") {
		t.Errorf("status = %q", m.status)
	}
}

func TestCompareInvalidCandidateCannotBePicked(t *testing.T) {
	m, _, labelsPath := openCompareModel(t)
	m, _ = webPress(t, m, "d", "d") // r1 → r2 → r3
	if m.sess.Cursor() != 2 {
		t.Fatalf("cursor = %d, want r3", m.sess.Cursor())
	}
	rows := m.remoteCandidates(2)
	if len(rows) != 2 || rows[0].invalid != "" || rows[1].invalid == "" {
		t.Fatalf("r3 candidates = %+v", rows)
	}
	v := m.View()
	for _, want := range []string{"⚠ carol — invalid:", "1 alice · ✓ complete · transfer"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	m, _ = webPress(t, m, "2")
	if !m.statusErr || !strings.Contains(m.status, "invalid and cannot be picked") {
		t.Errorf("status = %q", m.status)
	}
	if labelsPathExists(labelsPath) {
		t.Errorf("an invalid candidate wrote the labels file:\n%s", readFile(t, labelsPath))
	}
	m, _ = webPress(t, m, "1") // the valid one is pickable
	if _, ok := m.sess.Label(2); !ok || !labelsPathExists(labelsPath) {
		t.Errorf("the valid candidate was not saved: %q", m.status)
	}
}

func TestCompareKindsAndCounts(t *testing.T) {
	m, _, _ := linkedModel(t)
	// Local: r2 equals the remote label (agreed), r4 differs from it (disagreement).
	if err := m.sess.SaveLabel(1, localLabel(t, 1, "expense", "Pizza 4P")); err != nil {
		t.Fatal(err)
	}
	if err := m.sess.SaveLabel(3, localLabel(t, 3, "refund", "Mẹ")); err != nil {
		t.Fatal(err)
	}
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	want := []compareKind{compareDisagree, compareAgreed, compareUnanimous, compareDisagree, compareNone}
	for i, w := range want {
		if got := m.compareKind(i); got != w {
			t.Errorf("record r%d: kind %d, want %d", i+1, got, w)
		}
	}
	if c := m.compareCounts(); c != (compareTotals{disagree: 2, unanimous: 1, agreed: 1, invalid: 1}) {
		t.Errorf("counts = %+v", c)
	}
	if v := m.View(); !strings.Contains(v, "to decide 2 · unanimous 1 · agreed 1 · ⚠ invalid 1") {
		t.Errorf("header lacks the counts:\n%s", v)
	}
}

func TestCompareDisagreementNavigation(t *testing.T) {
	m, _, _ := linkedModel(t)
	if err := m.sess.SaveLabel(1, localLabel(t, 1, "expense", "Pizza 4P")); err != nil {
		t.Fatal(err)
	}
	if err := m.sess.SaveLabel(3, localLabel(t, 3, "refund", "Mẹ")); err != nil {
		t.Fatal(err)
	}
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	cursor := func() int { return m.sess.Cursor() }

	if cursor() != 0 {
		t.Fatalf("starts on r%d, want the first disagreement r1", cursor()+1)
	}
	m, _ = webPress(t, m, "]")
	if cursor() != 3 {
		t.Fatalf("] → r%d, want r4", cursor()+1)
	}
	m, _ = webPress(t, m, "]")
	if cursor() != 3 || !strings.Contains(m.status, "no next disagreement") {
		t.Errorf("] at the last disagreement: r%d %q", cursor()+1, m.status)
	}
	m, _ = webPress(t, m, "[")
	if cursor() != 0 {
		t.Fatalf("[ → r%d, want r1", cursor()+1)
	}
	m, _ = webPress(t, m, "[")
	if cursor() != 0 || !strings.Contains(m.status, "no previous disagreement") {
		t.Errorf("[ at the first disagreement: r%d %q", cursor()+1, m.status)
	}

	// a/d visit the records that have remote labels (r1-r4), not r5.
	var visited []int
	for range 3 {
		m, _ = webPress(t, m, "d")
		visited = append(visited, cursor())
	}
	if !slices.Equal(visited, []int{1, 2, 3}) {
		t.Errorf("d visited %v, want [1 2 3]", visited)
	}
	m, _ = webPress(t, m, "d")
	if cursor() != 3 || !strings.Contains(m.status, "no next record with remote labels") {
		t.Errorf("d past the last: r%d %q", cursor()+1, m.status)
	}
	m, _ = webPress(t, m, "a")
	if cursor() != 2 {
		t.Errorf("a → r%d, want r3", cursor()+1)
	}
}

func TestCompareBulkAcceptPreviewApplyAndOneUndo(t *testing.T) {
	m, _, labelsPath := openCompareModel(t)
	m, _ = webPress(t, m, "A")
	if !m.web.compare.previewing || len(m.web.compare.preview) != 3 {
		t.Fatalf("no preview of 3 records (previewing %v, %v)", m.web.compare.previewing, m.web.compare.preview)
	}
	v := m.View()
	for _, want := range []string{"Accept 3 unanimous records", "r2", "r3", "r4", `target="Pizza 4P"`, "complete · transfer", "enter accept 3"} {
		if !strings.Contains(v, want) {
			t.Errorf("preview lacks %q:\n%s", want, v)
		}
	}
	for _, banned := range []string{"r1 ", "r5"} {
		if strings.Contains(v, banned) {
			t.Errorf("preview lists %q:\n%s", banned, v)
		}
	}
	if labelsPathExists(labelsPath) {
		t.Fatal("the preview wrote the labels file")
	}
	m, _ = webPress(t, m, "esc")
	if m.web.compare.previewing || m.mode != annotCompare || labelsPathExists(labelsPath) {
		t.Fatalf("esc did not cancel the preview (mode %d)", m.mode)
	}

	m, _ = webPress(t, m, "A", "enter")
	want := referenceLabels(t, m, refMark{1, "expense", "Pizza 4P"}, refMark{2, "transfer", ""}, refMark{3, "income", "Mẹ"})
	if got := readFile(t, labelsPath); got != want {
		t.Errorf("labels file after accepting:\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(m.status, "accepted 3 unanimous labels") || m.web.compare.previewing {
		t.Errorf("status = %q previewing %v", m.status, m.web.compare.previewing)
	}
	if c := m.compareCounts(); c.unanimous != 0 || c.agreed != 3 {
		t.Errorf("counts after accepting = %+v", c)
	}
	m, _ = webPress(t, m, "A")
	if !strings.Contains(m.status, "no unanimous records") {
		t.Errorf("A with nothing to accept: %q", m.status)
	}

	m, _ = webPress(t, m, "z") // one z undoes the whole batch
	if lines := readLabelLines(t, labelsPath); len(lines) != 0 {
		t.Errorf("labels after one undo = %v, want none", lines)
	}
	if !strings.Contains(m.status, "reverted 3 labels") {
		t.Errorf("status = %q", m.status)
	}
	if c := m.compareCounts(); c.unanimous != 3 || c.agreed != 0 {
		t.Errorf("counts after undo = %+v", c)
	}
	m, _ = webPress(t, m, "z")
	if !strings.Contains(m.status, "nothing to undo") {
		t.Errorf("second z: %q", m.status)
	}
}

func TestCompareWithoutLinkOpensLinkFlowThenPulls(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.compareFixture()
	saveFakeRemote(t, f)
	m, _ := annotTestModel(t)
	m, _ = webPress(t, m, "w", "c")
	if m.mode != annotWebRemotes {
		t.Fatalf("mode = %d, want the remote picker first", m.mode)
	}
	m, cmd := webPress(t, m, "enter")
	m = runWeb(t, m, cmd)
	m, cmd = webPress(t, m, "enter") // project queue → pull
	if m.mode != annotWebBusy || !strings.Contains(m.status, "pulling") {
		t.Fatalf("no pulling status (mode %d): %q", m.mode, m.status)
	}
	m = runWeb(t, m, cmd)
	if m.mode != annotCompare {
		t.Errorf("mode = %d, want compare", m.mode)
	}
}

func TestCompareNoLabelsReportsAndStaysOnMenu(t *testing.T) {
	m, f, _ := linkedModel(t)
	f.project("queue").labels = nil
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	if m.mode != annotWebMenu || !strings.Contains(m.web.note, "no collaborator labels in origin/queue yet") {
		t.Errorf("mode %d note %q", m.mode, m.web.note)
	}
}

func TestCompareRendersOnSmallScreens(t *testing.T) {
	m, _, _ := openCompareModel(t)
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 14}, {40, 8}, {20, 5}, {8, 2}} {
		m, _ = m.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		m.web.compare.previewing, m.web.compare.preview = true, m.unanimousRecords()
		_ = m.View()
		m.web.compare.previewing = false
	}
}
