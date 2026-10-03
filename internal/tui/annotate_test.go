package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/annotate"
)

// annotSchemaYAML is a Gidi-style schema fixture (tests may name Gidi types).
const annotSchemaYAML = `version: annotation-v1
types:
  expense: Money leaves the user with no expectation of principal repayment.
  income: Money received as salary, gift or sale proceeds.
  borrow: The user receives loan principal and now owes another party.
  lend: The user gives loan principal to another party and expects repayment.
  repayment_in: Another party repays money they previously owed the user.
  repayment_out: The user repays money they previously owed another party.
  transfer: Movement between the user's own accounts, wallets or savings.
  refund: Money returned to the user as reversal of an earlier expense.
null_target_types: [transfer]
statuses:
  complete: Type and target confidently determined.
  uncertain: The text does not establish the type or the target.
  skipped: Not a usable finance note.
trainable_statuses: [complete]
`

// annotQueue is the queue fixture, one Vietnamese note per record.
var annotQueue = []annotate.Item{
	{ID: "r1", Text: "Cho anh Nam mượn 2tr"},
	{ID: "r2", Text: "Ăn tối ở Pizza 4P hết 850k"},
	{ID: "r3", Text: "Chuyển 5tr từ MoMo sang tài khoản"},
	{ID: "r4", Text: "Mẹ cho 500k"},
	{ID: "r5", Text: "Tiền điện tháng 9"},
}

// annotTestModel opens a real session over temp schema/queue files and returns
// a model sized 120x40 plus the labels output path.
func annotTestModel(t *testing.T) (annotModel, string) {
	t.Helper()
	schemaPath, queuePath, outPath := writeAnnotFiles(t)
	s, err := annotate.Open(queuePath, schemaPath, outPath)
	if err != nil {
		t.Fatalf("annotate.Open: %v", err)
	}
	m, _ := newAnnotModel(s).update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, outPath
}

// annotRecheckModel opens a re-check session over the queue fixture against a labels
// file holding labels (raw JSONL), and returns a model sized 120x40 plus the labels path.
func annotRecheckModel(t *testing.T, labels string) (annotModel, string) {
	t.Helper()
	schemaPath, queuePath, labelsPath := writeAnnotFiles(t)
	if err := os.WriteFile(labelsPath, []byte(labels), 0o600); err != nil {
		t.Fatalf("write labels: %v", err)
	}
	s, err := annotate.OpenRecheck(queuePath, schemaPath, labelsPath)
	if err != nil {
		t.Fatalf("annotate.OpenRecheck: %v", err)
	}
	m, _ := newAnnotModel(s).update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, labelsPath
}

// writeAnnotFiles writes the schema and queue fixtures to a temp dir and returns
// their paths plus the (not yet created) labels path next to them.
func writeAnnotFiles(t *testing.T) (schemaPath, queuePath, labelsPath string) {
	t.Helper()
	dir := t.TempDir()
	schemaPath = filepath.Join(dir, "schema.yaml")
	queuePath = filepath.Join(dir, "queue.jsonl")
	labelsPath = filepath.Join(dir, "labels.jsonl")
	if err := os.WriteFile(schemaPath, []byte(annotSchemaYAML), 0o600); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	var q strings.Builder
	for _, it := range annotQueue {
		line, err := json.Marshal(queueLine{it.ID, it.Text})
		if err != nil {
			t.Fatalf("marshal queue: %v", err)
		}
		q.Write(line)
		q.WriteByte('\n')
	}
	if err := os.WriteFile(queuePath, []byte(q.String()), 0o600); err != nil {
		t.Fatalf("write queue: %v", err)
	}
	return schemaPath, queuePath, labelsPath
}

// queueLine is one queue JSONL line.
type queueLine struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// typed returns one rune key per rune of s.
func typed(s string) []tea.Msg {
	out := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		out = append(out, runeKey(r))
	}
	return out
}

// sendAnnot feeds msgs to the annotation model in order.
func sendAnnot(m annotModel, msgs ...tea.Msg) (annotModel, tea.Cmd) {
	var cmd tea.Cmd
	for _, msg := range msgs {
		m, cmd = m.update(msg)
	}
	return m, cmd
}

// keys concatenates key groups into one message list.
func keys(groups ...[]tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

var (
	enterKey = specialKey(tea.KeyEnter)
	escKey   = specialKey(tea.KeyEsc)
)

// readLabelLines returns the raw lines of the labels file.
func readLabelLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open labels: %v", err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan labels: %v", err)
	}
	return lines
}

// readLabels parses the labels file keyed by id, failing on a duplicate id:
// revising a label must rewrite its line, never append another. Members other than
// id, annotation_status, type, note and span_status are span fields.
func readLabels(t *testing.T, path string) map[string]annotate.Label {
	t.Helper()
	out := map[string]annotate.Label{}
	for _, line := range readLabelLines(t, path) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("parse label %q: %v", line, err)
		}
		l := annotate.Label{Spans: map[string]*annotate.Target{}}
		for key, val := range raw {
			var err error
			switch key {
			case "id":
				err = json.Unmarshal(val, &l.ID)
			case "annotation_status":
				err = json.Unmarshal(val, &l.Status)
			case "type":
				err = json.Unmarshal(val, &l.Type)
			case "note":
				err = json.Unmarshal(val, &l.Note)
			case "span_status":
				err = json.Unmarshal(val, &l.SpanStatus)
			default:
				var tg *annotate.Target
				err = json.Unmarshal(val, &tg)
				l.Spans[key] = tg
			}
			if err != nil {
				t.Fatalf("parse label %q member %s: %v", line, key, err)
			}
		}
		if _, dup := out[l.ID]; dup {
			t.Fatalf("labels file has id %q twice", l.ID)
		}
		out[l.ID] = l
	}
	return out
}

// runeOffset returns the code-point offset of sub in text.
func runeOffset(t *testing.T, text, sub string) int {
	t.Helper()
	i := strings.Index(text, sub)
	if i < 0 {
		t.Fatalf("%q not in %q", sub, text)
	}
	return len([]rune(text[:i]))
}

// isQuit reports whether cmd is tea.Quit. Only call it on commands expected
// to quit: other commands (status ticks) block while they run.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestAnnotatePickerSpanComplete(t *testing.T) {
	m, out := annotTestModel(t)

	m, _ = sendAnnot(m, keys([]tea.Msg{runeKey('t')}, typed("len"), []tea.Msg{enterKey})...)
	if got := m.sess.Draft(0).Type; got != "lend" {
		t.Fatalf("type after picker = %q, want lend", got)
	}
	m, _ = sendAnnot(m, typed("xww")...)
	if !strings.Contains(m.View(), `selection: "Nam" [8,11)`) {
		t.Errorf("span view lacks the selection line:\n%s", m.View())
	}
	m, _ = sendAnnot(m, enterKey)
	if m.mode != annotMain {
		t.Fatalf("mode after accepting span = %d, want main", m.mode)
	}
	if d := m.sess.Draft(0); d.Spans["target"] == nil || *d.Spans["target"] != (annotate.Target{Text: "Nam", Start: 8, End: 11}) {
		t.Fatalf("target = %+v, want Nam [8,11)", d.Spans["target"])
	}
	if !strings.Contains(m.View(), "unsaved draft") {
		t.Errorf("view lacks the unsaved-draft marker")
	}

	m, _ = sendAnnot(m, enterKey)
	want := `{"id":"r1","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11}}`
	if lines := readLabelLines(t, out); len(lines) != 1 || lines[0] != want {
		t.Fatalf("labels = %q, want [%s]", lines, want)
	}
	if m.sess.Cursor() != 1 {
		t.Fatalf("cursor after complete = %d, want 1", m.sess.Cursor())
	}

	// r2: digit type, then extend a word selection across two words.
	text := annotQueue[1].Text
	m, _ = sendAnnot(m, typed("1xwwwW")...)
	start := runeOffset(t, text, "Pizza 4P")
	if lo, hi := m.span.bounds(); lo != start || hi != start+7 {
		t.Fatalf("selection = [%d,%d], want [%d,%d]", lo, hi, start, start+7)
	}
	m, _ = sendAnnot(m, enterKey, enterKey)
	got := readLabels(t, out)["r2"]
	wantTarget := annotate.Target{Text: "Pizza 4P", Start: start, End: start + 8}
	if got.Status != annotate.StatusComplete || got.Type == nil || *got.Type != "expense" ||
		got.Spans["target"] == nil || *got.Spans["target"] != wantTarget {
		t.Fatalf("r2 label = %+v (target %+v), want complete expense %+v", got, got.Spans["target"], wantTarget)
	}
	if m.sess.Cursor() != 2 {
		t.Fatalf("cursor = %d, want 2", m.sess.Cursor())
	}
}

func TestAnnotateTypePickerKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.Msg
		want string
	}{
		{"fuzzy query then arrow", keys([]tea.Msg{runeKey('t')}, typed("rep"), []tea.Msg{specialKey(tea.KeyDown), enterKey}), "repayment_out"},
		{"j moves with empty query", keys(typed("tjj"), []tea.Msg{enterKey}), "borrow"},
		{"j is query text once typing", keys(typed("t"), typed("trj"), []tea.Msg{specialKey(tea.KeyBackspace), specialKey(tea.KeyCtrlN), enterKey}), "transfer"},
		{"digit picks schema type", typed("t8"), "refund"},
		{"esc cancels", keys(typed("t"), typed("inc"), []tea.Msg{escKey}), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := annotTestModel(t)
			m, _ = sendAnnot(m, tc.keys...)
			if m.mode != annotMain {
				t.Fatalf("mode = %d, want main", m.mode)
			}
			if got := m.sess.Draft(0).Type; got != tc.want {
				t.Fatalf("type = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnnotateSpanKeys(t *testing.T) {
	// r2 text: "Ăn tối ở Pizza 4P hết 850k"; first word "Ăn" = [0,1].
	text := annotQueue[1].Text
	pizza := runeOffset(t, text, "Pizza")
	n := len([]rune(text))
	tests := []struct {
		name   string
		keys   []tea.Msg
		lo, hi int
	}{
		{"starts on first word", nil, 0, 1},
		{"l collapses right of head", typed("l"), 2, 2},
		{"h collapses left of head", typed("h"), 0, 0},
		{"L grows head", typed("L"), 0, 2},
		{"shift+left shrinks head", []tea.Msg{specialKey(tea.KeyShiftLeft)}, 0, 0},
		{"w then b returns", typed("wwwb"), runeOffset(t, text, "ở"), runeOffset(t, text, "ở")},
		{"B extends back to word start", typed("wwwB"), runeOffset(t, text, "ở"), pizza + 4},
		{"$ last character", typed("$"), n - 1, n - 1},
		{"0 first character", typed("$0"), 0, 0},
		{"w on the first rune selects the first word", typed("$0w"), 0, 1},
		{"w inside a word selects that word", typed("hw"), 0, 1},
		{"w mid-word selects the whole word", typed("wwwhw"), pizza, pizza + 4},
		{"w on a whole word goes to the next", typed("w"), runeOffset(t, text, "tối"), runeOffset(t, text, "tối") + 2},
		{"b on the first rune selects the first word", typed("$0b"), 0, 1},
		{"W from a space starts at the next word", typed("lW"), runeOffset(t, text, "tối"), runeOffset(t, text, "tối") + 2},
		{"B from a space ends at the previous word", typed("lB"), 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := annotTestModel(t)
			m.sess.SetCursor(1)
			m, _ = sendAnnot(m, keys(typed("x"), tc.keys)...)
			if m.mode != annotSpan {
				t.Fatalf("mode = %d, want span", m.mode)
			}
			if lo, hi := m.span.bounds(); lo != tc.lo || hi != tc.hi {
				t.Fatalf("selection = [%d,%d], want [%d,%d]", lo, hi, tc.lo, tc.hi)
			}
		})
	}
}

func TestAnnotateSpanRejectsPaddedSelection(t *testing.T) {
	m, _ := annotTestModel(t)
	// "Cho" plus the following space is whitespace-padded.
	m, _ = sendAnnot(m, keys(typed("xL"), []tea.Msg{enterKey})...)
	if m.mode != annotSpan || !m.statusErr {
		t.Fatalf("mode = %d statusErr = %v, want span mode with an error", m.mode, m.statusErr)
	}
	if m.sess.Draft(0).Spans["target"] != nil {
		t.Fatalf("padded target was set: %+v", m.sess.Draft(0).Spans["target"])
	}
}

func TestAnnotateNullTarget(t *testing.T) {
	m, _ := annotTestModel(t)
	m, _ = sendAnnot(m, keys(typed("4x"), []tea.Msg{enterKey})...)
	if m.sess.Draft(0).Spans["target"] == nil {
		t.Fatal("target not set")
	}
	m, _ = sendAnnot(m, runeKey('n'))
	if m.sess.Draft(0).Spans["target"] != nil {
		t.Fatalf("n left target %+v", m.sess.Draft(0).Spans["target"])
	}
	if !strings.Contains(m.View(), "Target: null") {
		t.Errorf("view lacks Target: null")
	}

	// n inside span mode also nulls and leaves.
	m, _ = sendAnnot(m, keys(typed("x"), []tea.Msg{enterKey}, typed("xn"))...)
	if m.mode != annotMain || m.sess.Draft(0).Spans["target"] != nil {
		t.Fatalf("span n: mode %d target %+v, want main and null", m.mode, m.sess.Draft(0).Spans["target"])
	}
}

func TestAnnotateTransferClearsTarget(t *testing.T) {
	m, out := annotTestModel(t)
	m.sess.SetCursor(2)
	m, _ = sendAnnot(m, keys(typed("x"), []tea.Msg{enterKey})...)
	if m.sess.Draft(2).Spans["target"] == nil {
		t.Fatal("target not set")
	}
	m, _ = sendAnnot(m, runeKey('7'))
	if d := m.sess.Draft(2); d.Type != "transfer" || d.Spans["target"] != nil {
		t.Fatalf("draft = %+v, want transfer with null target", d)
	}
	if !strings.Contains(m.View(), "target cleared") {
		t.Errorf("view does not report the cleared target:\n%s", m.View())
	}
	m, _ = sendAnnot(m, runeKey('x'))
	if m.mode != annotMain || !m.statusErr || !strings.Contains(m.View(), "requires a null target") {
		t.Fatalf("x on transfer: mode %d err %v, want refusal", m.mode, m.statusErr)
	}
	sendAnnot(m, enterKey)
	got := readLabels(t, out)["r3"]
	if got.Status != annotate.StatusComplete || got.Type == nil || *got.Type != "transfer" || got.Spans["target"] != nil {
		t.Fatalf("r3 label = %+v, want complete transfer null target", got)
	}
}

func TestAnnotateUncertainWithoutType(t *testing.T) {
	m, out := annotTestModel(t)
	m.sess.SetCursor(3)
	sendAnnot(m, runeKey('u'))
	got, ok := readLabels(t, out)["r4"]
	if !ok || got.Status != annotate.StatusUncertain || got.Type != nil || got.Spans["target"] != nil {
		t.Fatalf("r4 label = %+v (present %v), want uncertain with null type/target", got, ok)
	}
}

func TestAnnotateCompleteWithoutTypeFails(t *testing.T) {
	m, out := annotTestModel(t)
	m, _ = sendAnnot(m, enterKey)
	if !m.statusErr || m.status == "" {
		t.Fatalf("status = %q err %v, want an error", m.status, m.statusErr)
	}
	if m.sess.Cursor() != 0 {
		t.Fatalf("cursor moved to %d", m.sess.Cursor())
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("labels file written: stat err %v", err)
	}
}

func TestAnnotateFilterPickerRevise(t *testing.T) {
	m, out := annotTestModel(t)
	m, _ = sendAnnot(m, keys(typed("4xww"), []tea.Msg{enterKey, enterKey})...)
	if m.sess.Cursor() != 1 {
		t.Fatalf("cursor = %d, want 1", m.sess.Cursor())
	}
	m, _ = sendAnnot(m, runeKey('f'))
	if m.mode != annotFilter {
		t.Fatalf("mode = %d, want filter", m.mode)
	}
	if v := m.View(); !strings.Contains(v, "unfinished  4") || !strings.Contains(v, "complete    1") {
		t.Errorf("filter picker lacks counts:\n%s", v)
	}
	m, _ = sendAnnot(m, runeKey('j'), enterKey)
	if m.sess.Filter() != annotate.FilterComplete || m.sess.Cursor() != 0 {
		t.Fatalf("filter %v cursor %d, want complete on r1", m.sess.Filter(), m.sess.Cursor())
	}
	m, _ = sendAnnot(m, runeKey('1'), enterKey)
	if !strings.Contains(m.View(), "all records in complete are done") {
		t.Errorf("view lacks the done notice:\n%s", m.View())
	}
	got := readLabels(t, out)["r1"]
	if got.Type == nil || *got.Type != "expense" || got.Spans["target"] == nil || got.Spans["target"].Text != "Nam" {
		t.Fatalf("revised r1 = %+v, want expense keeping target Nam", got)
	}
}

func TestAnnotateDirtyQuit(t *testing.T) {
	m, _ := annotTestModel(t)
	if _, cmd := sendAnnot(m, runeKey('q')); !isQuit(cmd) {
		t.Fatal("q on a clean record did not quit")
	}
	m, _ = sendAnnot(m, runeKey('4'), runeKey('q'))
	if !m.quitArmed || !strings.Contains(m.View(), "press q again") {
		t.Fatalf("first q over a draft did not warn:\n%s", m.View())
	}
	if _, cmd := sendAnnot(m, runeKey('q')); !isQuit(cmd) {
		t.Fatal("second q did not quit")
	}
	// Any other key disarms the warning.
	m, _ = sendAnnot(m, runeKey('d'), runeKey('a'))
	if m.quitArmed {
		t.Fatal("quit stayed armed after another key")
	}
	if _, cmd := sendAnnot(m, specialKey(tea.KeyCtrlC)); !isQuit(cmd) {
		t.Fatal("ctrl+c did not quit")
	}
}

func TestAnnotateEscDiscardsDraft(t *testing.T) {
	m, _ := annotTestModel(t)
	m, _ = sendAnnot(m, runeKey('4'), escKey)
	if m.sess.Dirty(0) || m.sess.Draft(0).Type != "" {
		t.Fatalf("draft survived esc: %+v", m.sess.Draft(0))
	}
}

// labelType returns the type of l, or "" when it is null.
func labelType(l annotate.Label) string {
	if l.Type == nil {
		return ""
	}
	return *l.Type
}

func TestAnnotateReviseComplete(t *testing.T) {
	m, out := annotTestModel(t)
	m, _ = sendAnnot(m, keys(typed("4xww"), []tea.Msg{enterKey, enterKey})...)
	if m.sess.Cursor() != 1 {
		t.Fatalf("cursor after complete = %d, want 1", m.sess.Cursor())
	}
	m, _ = sendAnnot(m, runeKey('a'))
	if m.sess.Cursor() != 0 {
		t.Fatalf("a from r2 went to %d, want the labeled r1", m.sess.Cursor())
	}
	v := m.View()
	for _, want := range []string{"Record 1 / 5 · ✓ complete", "Saved: complete", "Type: lend", `Target: "Nam" [8,11)`} {
		if !strings.Contains(v, want) {
			t.Errorf("revisited r1 view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "unsaved draft") {
		t.Errorf("clean revisit shows the unsaved-draft marker")
	}
	m, _ = sendAnnot(m, keys([]tea.Msg{runeKey('t')}, typed("bor"), []tea.Msg{enterKey})...)
	if v := m.View(); !strings.Contains(v, "unsaved draft") || !strings.Contains(v, "✓ complete") {
		t.Errorf("edited revisit lacks the draft marker or saved badge:\n%s", v)
	}
	m, _ = sendAnnot(m, enterKey)
	if m.status != "revised complete r1" {
		t.Errorf("status = %q, want %q", m.status, "revised complete r1")
	}
	labels := readLabels(t, out)
	got := labels["r1"]
	if len(labels) != 1 || got.Status != annotate.StatusComplete || labelType(got) != "borrow" ||
		got.Spans["target"] == nil || got.Spans["target"].Text != "Nam" {
		t.Fatalf("labels = %+v, want only r1 complete borrow keeping target Nam", labels)
	}
}

func TestAnnotateReviseUncertainToComplete(t *testing.T) {
	m, out := annotTestModel(t)
	m, _ = sendAnnot(m, runeKey('u'), runeKey('a'))
	if m.sess.Cursor() != 0 || !strings.Contains(m.View(), "? uncertain") {
		t.Fatalf("cursor %d, want r1 showing ? uncertain:\n%s", m.sess.Cursor(), m.View())
	}
	m, _ = sendAnnot(m, runeKey('1'), enterKey)
	labels := readLabels(t, out)
	if got := labels["r1"]; len(labels) != 1 || got.Status != annotate.StatusComplete || labelType(got) != "expense" {
		t.Fatalf("labels = %+v, want only r1 complete expense", labels)
	}
	if m.status != "revised complete r1" {
		t.Errorf("status = %q, want %q", m.status, "revised complete r1")
	}
}

func TestAnnotateReviseSkippedToUncertain(t *testing.T) {
	m, out := annotTestModel(t)
	m, _ = sendAnnot(m, runeKey('s'), runeKey('H'))
	if m.sess.Cursor() != 0 || !strings.Contains(m.View(), "– skipped") {
		t.Fatalf("cursor %d, want r1 showing – skipped:\n%s", m.sess.Cursor(), m.View())
	}
	m, _ = sendAnnot(m, runeKey('2'), runeKey('u'))
	labels := readLabels(t, out)
	if got := labels["r1"]; len(labels) != 1 || got.Status != annotate.StatusUncertain || labelType(got) != "income" {
		t.Fatalf("labels = %+v, want only r1 uncertain income", labels)
	}
}

func TestAnnotateUndo(t *testing.T) {
	m, out := annotTestModel(t)
	m, _ = sendAnnot(m, runeKey('4'), enterKey, runeKey('a'), runeKey('1'), runeKey('u'))
	if got := readLabels(t, out)["r1"]; got.Status != annotate.StatusUncertain || labelType(got) != "expense" {
		t.Fatalf("revised r1 = %+v, want uncertain expense", got)
	}
	m, _ = sendAnnot(m, runeKey('z'))
	if m.status != "undo: restored complete r1" || m.sess.Cursor() != 0 {
		t.Fatalf("status %q cursor %d, want restored complete on r1", m.status, m.sess.Cursor())
	}
	labels := readLabels(t, out)
	if got := labels["r1"]; len(labels) != 1 || got.Status != annotate.StatusComplete || labelType(got) != "lend" {
		t.Fatalf("labels after undo = %+v, want only r1 complete lend", labels)
	}
	m, _ = sendAnnot(m, runeKey('Z'))
	if m.status != "undo: removed label r1" {
		t.Fatalf("status = %q, want the label removed", m.status)
	}
	if labels := readLabels(t, out); len(labels) != 0 {
		t.Fatalf("labels after second undo = %+v, want none", labels)
	}
	if v := m.View(); !strings.Contains(v, "Status: unfinished") || strings.Contains(v, "Saved:") {
		t.Errorf("r1 still shows a saved label after undo:\n%s", v)
	}
	m, _ = sendAnnot(m, runeKey('z'))
	if m.status != "nothing to undo" || m.statusErr {
		t.Fatalf("status = %q err %v, want nothing to undo", m.status, m.statusErr)
	}
}

func TestAnnotateGoTo(t *testing.T) {
	m, _ := annotTestModel(t)
	m, _ = sendAnnot(m, runeKey(':'))
	if m.mode != annotGoto || !strings.Contains(m.View(), "enter go") {
		t.Fatalf("mode = %d, want the go-to prompt with its footer:\n%s", m.mode, m.View())
	}
	m, _ = sendAnnot(m, runeKey('3'), enterKey)
	if m.mode != annotMain || m.sess.Cursor() != 2 {
		t.Fatalf("mode %d cursor %d, want main on index 2", m.mode, m.sess.Cursor())
	}

	// q and ? are query text; a bad id keeps the prompt open with the error.
	m, _ = sendAnnot(m, keys(typed(":q?"), []tea.Msg{specialKey(tea.KeyBackspace), specialKey(tea.KeyBackspace)},
		typed("nope"), []tea.Msg{specialKey(tea.KeyDown), enterKey})...)
	if m.mode != annotGoto || !m.statusErr || !strings.Contains(m.View(), `go to: no record with id "nope"`) {
		t.Fatalf("bad id: mode %d err %v, want the prompt open with an error:\n%s", m.mode, m.statusErr, m.View())
	}
	if m.sess.Cursor() != 2 {
		t.Fatalf("bad id moved the cursor to %d", m.sess.Cursor())
	}
	m, _ = sendAnnot(m, escKey)
	if m.mode != annotMain || m.sess.Cursor() != 2 {
		t.Fatalf("esc: mode %d cursor %d, want main on index 2", m.mode, m.sess.Cursor())
	}

	m, _ = sendAnnot(m, runeKey('#'))
	if m.mode != annotGoto || len(m.gotoQuery) != 0 {
		t.Fatalf("# opened mode %d with query %q, want an empty prompt", m.mode, string(m.gotoQuery))
	}
	m, _ = sendAnnot(m, keys(typed("r5"), []tea.Msg{enterKey})...)
	if m.mode != annotMain || m.sess.Cursor() != 4 {
		t.Fatalf("id r5: mode %d cursor %d, want main on index 4", m.mode, m.sess.Cursor())
	}
}

func TestAnnotateNavigation(t *testing.T) {
	m, _ := annotTestModel(t)
	// Label r1 and r2: the unfinished filter now matches r3..r5 only.
	m, _ = sendAnnot(m, runeKey('4'), enterKey, runeKey('1'), enterKey)
	if m.sess.Cursor() != 2 {
		t.Fatalf("cursor = %d, want 2", m.sess.Cursor())
	}
	steps := []struct {
		key    tea.Msg
		cursor int
		status string
	}{
		{runeKey(']'), 3, ""},
		{runeKey('['), 2, ""},
		{runeKey('['), 2, "no previous unfinished record"},
		{runeKey('a'), 1, ""},
		{specialKey(tea.KeyLeft), 0, ""},
		{runeKey('h'), 0, "first record"},
		{runeKey(']'), 2, ""},
		{runeKey('G'), 4, ""},
		{runeKey(']'), 4, "no next unfinished record"},
		{runeKey('d'), 4, "last record"},
		{runeKey('g'), 0, ""},
		{runeKey('D'), 1, ""},
		{runeKey('L'), 2, ""},
		{specialKey(tea.KeyRight), 3, ""},
		{runeKey('A'), 2, ""},
	}
	for i, st := range steps {
		m.status = ""
		m, _ = sendAnnot(m, st.key)
		if m.sess.Cursor() != st.cursor || m.status != st.status {
			t.Fatalf("step %d (%v): cursor %d status %q, want %d %q", i, st.key, m.sess.Cursor(), m.status, st.cursor, st.status)
		}
	}
}

func TestAnnotateHelpOverlay(t *testing.T) {
	m, _ := annotTestModel(t)
	m, _ = sendAnnot(m, tea.WindowSizeMsg{Width: 160, Height: 60}, runeKey('?'))
	if m.mode != annotHelp {
		t.Fatalf("mode = %d, want help", m.mode)
	}
	v := m.View()
	for _, want := range []string{"Label", "Navigate", "Target span", "Type picker", "Other",
		"Null target", "Extend to word", "Fuzzy filter", "Discard draft", "Quit; twice if unsaved",
		"Undo last save", "Edit, then enter/u/s", "Previous, any status", "Prev/next in filter", "Go to position or id"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	for _, closeKey := range []tea.Msg{escKey, runeKey('?'), runeKey('q')} {
		m.mode = annotHelp
		m.helpReturn = annotMain
		if next, cmd := sendAnnot(m, closeKey); next.mode != annotMain || cmd != nil {
			t.Errorf("%v did not close help", closeKey)
		}
	}
	m.mode = annotMain
	m, _ = sendAnnot(m, runeKey('x'), runeKey('?'), escKey)
	if m.mode != annotSpan {
		t.Fatalf("help from span returned to %d, want span", m.mode)
	}
}

func TestAnnotateViewSizes(t *testing.T) {
	setups := map[string][]tea.Msg{
		"main":    nil,
		"span":    typed("x"),
		"types":   typed("t"),
		"filter":  typed("f"),
		"help":    typed("?"),
		"draft":   keys(typed("4x"), []tea.Msg{enterKey}),
		"goto":    typed(":12"),
		"revisit": keys(typed("4"), []tea.Msg{enterKey}, typed("a8")),
	}
	sizes := [][2]int{{80, 24}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {10, 4}, {20, 6}, {40, 10}, {200, 3}}
	for name, setup := range setups {
		for _, sz := range sizes {
			m, _ := annotTestModel(t)
			m, _ = sendAnnot(m, keys([]tea.Msg{tea.WindowSizeMsg{Width: sz[0], Height: sz[1]}}, setup)...)
			v := m.View()
			w, h := sz[0], sz[1]
			if w == 0 {
				w, h = 80, 24
			}
			lines := strings.Split(v, "\n")
			if len(lines) > h {
				t.Errorf("%s %dx%d: %d lines", name, sz[0], sz[1], len(lines))
			}
			if sz[0] == 80 {
				for _, l := range lines {
					if lw := lipgloss.Width(l); lw > 80 {
						t.Errorf("%s 80x24: line %d cells wide", name, lw)
					}
				}
			}
		}
	}
	m, _ := annotTestModel(t)
	m, _ = sendAnnot(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	v := m.View()
	for _, want := range []string{"Quet — annotate", "queue.jsonl → labels.jsonl", "Record 1 / 5",
		"remaining 5", "filter unfinished 1/5", "session 0 · 0.0/min", "Type: -", "Status: unfinished", "t type"} {
		if !strings.Contains(v, want) {
			t.Errorf("80x24 view lacks %q:\n%s", want, v)
		}
	}
}

func TestAnnotateRecheckHeader(t *testing.T) {
	labels := `{"id":"elsewhere","annotation_status":"skipped","type":null,"target":null}` + "\n" +
		`{"id":"r2","annotation_status":"uncertain","type":null,"target":null}` + "\n"
	m, _ := annotRecheckModel(t, labels)
	m, _ = sendAnnot(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	v := m.View()
	if lines := strings.Split(v, "\n"); len(lines) > 24 {
		t.Errorf("80x24 re-check view has %d lines", len(lines))
	}
	for _, want := range []string{"Quet — re-check", "queue.jsonl → labels.jsonl", "Re-check 1 / 5",
		"Labels: 2 total · 1 in current queue", "filter all", "Record 1 / 5"} {
		if !strings.Contains(v, want) {
			t.Errorf("80x24 re-check view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Quet — annotate") {
		t.Errorf("re-check view shows the annotate title:\n%s", v)
	}

	// Marking r1 moves to r2 (already labeled: re-check visits every record in order)
	// and the canonical file now holds one more label, in the queue.
	m, _ = sendAnnot(m, keys(typed("4x"), []tea.Msg{enterKey, enterKey})...)
	v = m.View()
	for _, want := range []string{"Re-check 2 / 5", "Labels: 3 total · 2 in current queue"} {
		if !strings.Contains(v, want) {
			t.Errorf("after marking, re-check view lacks %q:\n%s", want, v)
		}
	}

	// The last record does not wrap back to the first.
	m.sess.SetCursor(4)
	m, _ = sendAnnot(m, runeKey('s'))
	if m.sess.Cursor() != 4 || !strings.Contains(m.View(), "end of re-check in all") {
		t.Errorf("after marking the last record: cursor %d, view:\n%s", m.sess.Cursor(), m.View())
	}
}

func TestAnnotateFooterWraps(t *testing.T) {
	for _, sz := range [][2]int{{100, 30}, {80, 24}} {
		for _, status := range []string{"", "saved"} {
			m, _ := annotTestModel(t)
			m, _ = sendAnnot(m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			m.status = status
			v := m.View()
			for _, item := range m.footerItems() {
				if !strings.Contains(v, item) {
					t.Errorf("%dx%d status %q: view lacks footer item %q:\n%s", sz[0], sz[1], status, item, v)
				}
			}
			if !strings.Contains(v, "q quit") {
				t.Errorf("%dx%d: view lacks %q", sz[0], sz[1], "q quit")
			}
			lines := strings.Split(v, "\n")
			if len(lines) != sz[1] {
				t.Errorf("%dx%d status %q: %d lines, want %d", sz[0], sz[1], status, len(lines), sz[1])
			}
			for i, l := range lines {
				if lw := lipgloss.Width(l); lw > sz[0] {
					t.Errorf("%dx%d: line %d is %d cells wide", sz[0], sz[1], i, lw)
				}
			}
		}
	}
}

func TestWrapFooter(t *testing.T) {
	items := []string{"aa x", "bb y", "cc z"}
	tests := []struct {
		w, maxRows int
		want       []string
	}{
		{20, 3, []string{"aa x  bb y  cc z"}},
		{10, 3, []string{"aa x  bb y", "cc z"}},
		{9, 3, []string{"aa x", "bb y", "cc z"}},
		{9, 2, []string{"aa x", "bb y  cc z"}},
	}
	for _, tc := range tests {
		got := wrapFooter(items, tc.w, tc.maxRows)
		if len(got) != len(tc.want) {
			t.Errorf("wrapFooter(w=%d, rows=%d) = %q, want %d rows", tc.w, tc.maxRows, got, len(tc.want))
			continue
		}
		for i, l := range got {
			if lipgloss.Width(l) > tc.w {
				t.Errorf("wrapFooter(w=%d, rows=%d) row %d is %d cells wide", tc.w, tc.maxRows, i, lipgloss.Width(l))
			}
			// The overflow row is truncated; earlier rows hold whole items.
			if i < len(got)-1 || lipgloss.Width(tc.want[i]) <= tc.w {
				if !strings.Contains(l, tc.want[i]) {
					t.Errorf("wrapFooter(w=%d, rows=%d) row %d = %q, want %q", tc.w, tc.maxRows, i, l, tc.want[i])
				}
			}
		}
	}
}

func TestWrapRunes(t *testing.T) {
	tests := []struct {
		text string
		w    int
		want []string
	}{
		{"Cho anh Nam", 8, []string{"Cho anh ", "Nam"}},
		{"abcdef", 3, []string{"abc", "def"}},
		{"a\nb", 10, []string{"a\n", "b"}},
		{"中文字", 4, []string{"中文", "字"}},
		{"", 5, []string{""}},
	}
	for _, tc := range tests {
		runes := []rune(tc.text)
		var got []string
		for _, rg := range wrapRunes(runes, tc.w) {
			got = append(got, string(runes[rg[0]:rg[1]]))
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrapRunes(%q, %d) = %q, want %q", tc.text, tc.w, got, tc.want)
		}
	}
}
