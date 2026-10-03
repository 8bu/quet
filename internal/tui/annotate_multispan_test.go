package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/annotate"
)

// multiSchemaYAML declares two span fields: target (null for transfers) and value (with
// per-span statuses, complete by default).
const multiSchemaYAML = `version: expense-v1
types:
  expense: Money the writer paid out.
  income: Money the writer received.
  transfer: Money moved between the writer's own accounts; there is no counterparty.
statuses:
  complete: Type and every span confidently determined.
  uncertain: The text does not settle the type or a span.
  skipped: Not a money note.
null_label_statuses: [skipped]
spans:
  target:
    description: Counterparty.
    null_for_types: [transfer]
  value:
    description: Monetary amount.
    statuses: [complete, uncertain]
`

// multiQueue is the multi-span queue fixture.
var multiQueue = []annotate.Item{
	{ID: "ms-001", Text: "Mua sữa ở Vinamilk hết 500k"},
	{ID: "ms-002", Text: "Chuyển khoản 2tr cho mẹ"},
	{ID: "ms-003", Text: "asdf test test"},
}

var (
	tabKey      = specialKey(tea.KeyTab)
	shiftTabKey = specialKey(tea.KeyShiftTab)
)

// multiSpanModel opens a session over multiSchemaYAML and multiQueue and returns a model
// sized 120x40 plus the labels path.
func multiSpanModel(t *testing.T) (annotModel, string) {
	t.Helper()
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.yaml")
	queuePath := filepath.Join(dir, "queue.jsonl")
	outPath := filepath.Join(dir, "labels.jsonl")
	if err := os.WriteFile(schemaPath, []byte(multiSchemaYAML), 0o600); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	var q strings.Builder
	for _, it := range multiQueue {
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
	s, err := annotate.Open(queuePath, schemaPath, outPath)
	if err != nil {
		t.Fatalf("annotate.Open: %v", err)
	}
	m, _ := newAnnotModel(s).update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, outPath
}

// selectTarget presses x then moves to the word "Vinamilk" of the first record and accepts.
func selectTarget() []tea.Msg {
	return keys(typed("xwww"), []tea.Msg{enterKey})
}

// selectValue presses x then selects the last word "500k" of the first record and accepts.
func selectValue() []tea.Msg {
	return keys(typed("x$b"), []tea.Msg{enterKey})
}

func TestAnnotateMultiSpanLabelsEndToEnd(t *testing.T) {
	m, out := multiSpanModel(t)

	m, _ = sendAnnot(m, runeKey('1'))
	m, _ = sendAnnot(m, selectTarget()...)
	if want := `target "Vinamilk" [10,18)`; m.status != want || m.statusErr {
		t.Fatalf("status after target = %q (err %v), want %q", m.status, m.statusErr, want)
	}
	m, _ = sendAnnot(m, tabKey)
	if m.activeSpan != 1 {
		t.Fatalf("activeSpan after tab = %d, want 1", m.activeSpan)
	}
	m, _ = sendAnnot(m, selectValue()...)
	if want := `value "500k" [23,27)`; m.status != want || m.statusErr {
		t.Fatalf("status after value = %q (err %v), want %q", m.status, m.statusErr, want)
	}
	m, _ = sendAnnot(m, runeKey('c'))
	if want := "value status: uncertain"; m.status != want || m.statusErr {
		t.Fatalf("status after c = %q (err %v), want %q", m.status, m.statusErr, want)
	}
	v := m.View()
	for _, want := range []string{`Target: "Vinamilk" [10,18)`, `Value: "500k" [23,27) · uncertain`, "unsaved draft"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "(default)") {
		t.Errorf("view shows (default) after c set the status:\n%s", v)
	}

	m, _ = sendAnnot(m, enterKey)
	if want := "complete ms-001"; m.status != want {
		t.Fatalf("status after enter = %q, want %q", m.status, want)
	}
	if m.sess.Cursor() != 1 {
		t.Fatalf("cursor = %d, want 1 (advanced)", m.sess.Cursor())
	}
	lines := readLabelLines(t, out)
	want := `{"id":"ms-001","annotation_status":"complete","type":"expense",` +
		`"target":{"text":"Vinamilk","start":10,"end":18},` +
		`"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"uncertain"}}`
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("labels = %q, want [%q]", lines, want)
	}
}

func TestAnnotateMultiSpanSecondRecordWithDefaultStatus(t *testing.T) {
	m, out := multiSpanModel(t)
	m.sess.SetCursor(1)
	// A transfer has no counterparty: the value is the only span, left on its default status.
	m, _ = sendAnnot(m, runeKey('3'))
	m, _ = sendAnnot(m, tabKey)
	m, _ = sendAnnot(m, keys(typed("xww"), []tea.Msg{enterKey})...)
	if want := `value "2tr" [13,16)`; m.status != want {
		t.Fatalf("status = %q, want %q", m.status, want)
	}
	if v := m.View(); !strings.Contains(v, `Value: "2tr" [13,16) · complete (default)`) {
		t.Errorf("view lacks the default status:\n%s", v)
	}
	m, _ = sendAnnot(m, enterKey)
	lines := readLabelLines(t, out)
	want := `{"id":"ms-002","annotation_status":"complete","type":"transfer","target":null,` +
		`"value":{"text":"2tr","start":13,"end":16},"span_status":{"value":"complete"}}`
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("labels = %q, want [%q]", lines, want)
	}
}

func TestAnnotateTabCyclesAndWraps(t *testing.T) {
	m, _ := multiSpanModel(t)
	m, _ = sendAnnot(m, tabKey)
	if m.activeSpan != 1 {
		t.Fatalf("tab: activeSpan = %d, want 1", m.activeSpan)
	}
	m, _ = sendAnnot(m, tabKey)
	if m.activeSpan != 0 {
		t.Fatalf("tab wrap: activeSpan = %d, want 0", m.activeSpan)
	}
	m, _ = sendAnnot(m, shiftTabKey)
	if m.activeSpan != 1 {
		t.Fatalf("shift+tab wrap: activeSpan = %d, want 1", m.activeSpan)
	}
	// The active field survives moving to another record.
	m, _ = sendAnnot(m, runeKey('d'))
	if m.activeSpan != 1 {
		t.Fatalf("activeSpan after next record = %d, want 1", m.activeSpan)
	}
	if v := m.View(); !strings.Contains(v, "▸ Value:") || strings.Contains(v, "▸ Target:") {
		t.Errorf("active marker should sit on Value:\n%s", v)
	}
}

func TestAnnotateTabNoopOnLegacySchema(t *testing.T) {
	m, _ := annotTestModel(t)
	before := m.View()
	m, cmd := sendAnnot(m, tabKey)
	if m.activeSpan != 0 || m.status != "" || cmd != nil {
		t.Fatalf("tab: activeSpan %d status %q cmd %v, want no-op", m.activeSpan, m.status, cmd != nil)
	}
	m, _ = sendAnnot(m, shiftTabKey)
	if m.activeSpan != 0 || m.status != "" {
		t.Fatalf("shift+tab: activeSpan %d status %q, want no-op", m.activeSpan, m.status)
	}
	// c is not bound on a legacy schema either.
	m, cmd = sendAnnot(m, runeKey('c'))
	if m.status != "" || cmd != nil {
		t.Fatalf("c: status %q, want no-op", m.status)
	}
	if after := m.View(); after != before {
		t.Errorf("view changed after tab/shift+tab/c on a legacy schema:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestAnnotateMultiSpanNullClearsOnlyActiveField(t *testing.T) {
	m, _ := multiSpanModel(t)
	m, _ = sendAnnot(m, keys(typed("1"), selectTarget(), []tea.Msg{tabKey}, selectValue())...)
	d := m.sess.Draft(0)
	if d.Spans["target"] == nil || d.Spans["value"] == nil {
		t.Fatalf("draft = %+v, want both spans set", d)
	}
	m, _ = sendAnnot(m, runeKey('n'))
	if want := "value: null"; m.status != want {
		t.Fatalf("status = %q, want %q", m.status, want)
	}
	d = m.sess.Draft(0)
	if d.Spans["value"] != nil || d.Spans["target"] == nil || d.Spans["target"].Text != "Vinamilk" {
		t.Fatalf("draft after n = %+v, want value null and target kept", d)
	}
	if v := m.View(); !strings.Contains(v, "Value: null") || !strings.Contains(v, `Target: "Vinamilk" [10,18)`) {
		t.Errorf("view after n:\n%s", v)
	}

	// n inside span mode nulls the active field only and leaves the mode.
	m, _ = sendAnnot(m, shiftTabKey)
	m, _ = sendAnnot(m, typed("xn")...)
	if m.mode != annotMain || m.status != "target: null" {
		t.Fatalf("span n: mode %d status %q, want main and %q", m.mode, m.status, "target: null")
	}
	d = m.sess.Draft(0)
	if d.Spans["target"] != nil {
		t.Fatalf("target = %+v, want null", d.Spans["target"])
	}
}

func TestAnnotateCycleStatusErrorsWithoutStatuses(t *testing.T) {
	m, _ := multiSpanModel(t)
	m, _ = sendAnnot(m, runeKey('c'))
	if want := "span target declares no statuses"; m.status != want || !m.statusErr {
		t.Fatalf("c on target: status %q (err %v), want error %q", m.status, m.statusErr, want)
	}
	m, _ = sendAnnot(m, tabKey)
	m, _ = sendAnnot(m, runeKey('c'))
	if want := "value status: uncertain"; m.status != want || m.statusErr {
		t.Fatalf("c on value: status %q (err %v), want %q", m.status, m.statusErr, want)
	}
	m, _ = sendAnnot(m, runeKey('c'))
	if want := "value status: complete"; m.status != want {
		t.Fatalf("c wraps: status %q, want %q", m.status, want)
	}
	// Back on the default, the unedited draft is clean again.
	if m.sess.Dirty(0) {
		t.Errorf("draft dirty after cycling the span status back to its default")
	}
}

func TestAnnotateNullForTypeClearsSpan(t *testing.T) {
	m, _ := multiSpanModel(t)
	m, _ = sendAnnot(m, keys(typed("1"), selectTarget(), []tea.Msg{tabKey}, selectValue())...)
	m, _ = sendAnnot(m, runeKey('3'))
	if want := "type transfer · target cleared (transfer requires a null target)"; m.status != want || m.statusErr {
		t.Fatalf("status = %q (err %v), want %q", m.status, m.statusErr, want)
	}
	d := m.sess.Draft(0)
	if d.Type != "transfer" || d.Spans["target"] != nil || d.Spans["value"] == nil {
		t.Fatalf("draft = %+v, want transfer with null target and the value kept", d)
	}

	// The target field cannot be selected for a transfer.
	m, _ = sendAnnot(m, shiftTabKey)
	m, _ = sendAnnot(m, runeKey('x'))
	if want := "type transfer requires a null target"; m.mode != annotMain || m.status != want || !m.statusErr {
		t.Fatalf("x: mode %d status %q (err %v), want main and error %q", m.mode, m.status, m.statusErr, want)
	}
}

func TestAnnotateSpanModeTabSwitchesFieldKeepingSelection(t *testing.T) {
	m, _ := multiSpanModel(t)
	m, _ = sendAnnot(m, keys(typed("1"), selectTarget())...)
	m, _ = sendAnnot(m, typed("x")...)
	if m.mode != annotSpan {
		t.Fatalf("mode = %d, want span", m.mode)
	}
	sel := m.span
	v := m.View()
	for _, want := range []string{"selecting target", `selection (target): "Vinamilk" [10,18)`} {
		if !strings.Contains(v, want) {
			t.Errorf("span view lacks %q:\n%s", want, v)
		}
	}

	m, _ = sendAnnot(m, tabKey)
	if m.mode != annotSpan || m.activeSpan != 1 {
		t.Fatalf("after tab: mode %d active %d, want span mode on field 1", m.mode, m.activeSpan)
	}
	if lo, hi := m.span.bounds(); m.span.head != sel.head || m.span.anchor != sel.anchor || lo != 10 || hi != 17 {
		t.Errorf("selection changed on tab: %+v, was %+v", m.span, sel)
	}
	v = m.View()
	for _, want := range []string{"selecting value", `selection (value): "Vinamilk" [10,18)`, "enter set value"} {
		if !strings.Contains(v, want) {
			t.Errorf("view after tab lacks %q:\n%s", want, v)
		}
	}

	// enter writes the active (value) field; the target keeps its span.
	m, _ = sendAnnot(m, enterKey)
	d := m.sess.Draft(0)
	if m.mode != annotMain || d.Spans["value"] == nil || d.Spans["value"].Text != "Vinamilk" ||
		d.Spans["target"] == nil || d.Spans["target"].Text != "Vinamilk" {
		t.Fatalf("mode %d draft %+v, want both fields over Vinamilk", m.mode, d)
	}
	if want := `value "Vinamilk" [10,18)`; m.status != want {
		t.Errorf("status = %q, want %q", m.status, want)
	}

	// esc cancels without writing.
	m, _ = sendAnnot(m, keys(typed("x"), typed("l"), []tea.Msg{escKey})...)
	if m.mode != annotMain || m.sess.Draft(0).Spans["value"].Text != "Vinamilk" {
		t.Fatalf("esc: mode %d draft %+v, want main and value untouched", m.mode, m.sess.Draft(0))
	}
}

func TestAnnotateMultiSpanRendering(t *testing.T) {
	m, _ := multiSpanModel(t)
	v := m.View()
	for _, want := range []string{"▸ Target: null", "  Value: null · complete (default)", "Type: -"} {
		if !strings.Contains(v, want) {
			t.Errorf("fresh view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "selecting") {
		t.Errorf("main view mentions selecting:\n%s", v)
	}

	// Overlapping fields: both are rendered, each with its own label and offsets.
	m, _ = sendAnnot(m, keys(typed("1"), selectTarget(), []tea.Msg{tabKey}, typed("xwww"), []tea.Msg{enterKey})...)
	d := m.sess.Draft(0)
	if d.Spans["target"] == nil || d.Spans["value"] == nil {
		t.Fatalf("draft = %+v", d)
	}
	v = m.View()
	for _, want := range []string{`Target: "Vinamilk" [10,18)`, `Value: "Vinamilk" [10,18) · complete (default)`} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

func TestAnnotateMultiSpanFooterAndHelp(t *testing.T) {
	m, _ := multiSpanModel(t)
	footer := strings.Join(m.footerItems(), "|")
	for _, want := range []string{"tab/shift+tab field", "x select target", "n null target", "c status"} {
		if !strings.Contains(footer, want) {
			t.Errorf("multi-span footer lacks %q: %s", want, footer)
		}
	}
	m, _ = sendAnnot(m, tabKey)
	if footer = strings.Join(m.footerItems(), "|"); !strings.Contains(footer, "x select value") {
		t.Errorf("footer does not follow the active field: %s", footer)
	}
	help := helpText(annotHelpGroups(m.sess.Schema(), false))
	for _, want := range []string{"tab Next field", "shift+tab Previous field", "Select active span", "Null active field", "Cycle active status"} {
		if !strings.Contains(help, want) {
			t.Errorf("multi-span help lacks %q:\n%s", want, help)
		}
	}

	// A legacy schema keeps the footer and help it always had.
	l, _ := annotTestModel(t)
	footer = strings.Join(l.footerItems(), "|")
	if !strings.HasPrefix(footer, "t type|x target|n null|enter complete|") || strings.Contains(footer, "tab") || strings.Contains(footer, "c status") {
		t.Errorf("legacy footer changed: %s", footer)
	}
	help = helpText(annotHelpGroups(l.sess.Schema(), false))
	for _, want := range []string{"Select target span", "Null target", "Target span", "Accept target"} {
		if !strings.Contains(help, want) {
			t.Errorf("legacy help lacks %q:\n%s", want, help)
		}
	}
	for _, unwanted := range []string{"tab", "active field", "Cycle"} {
		if strings.Contains(help, unwanted) {
			t.Errorf("legacy help mentions %q:\n%s", unwanted, help)
		}
	}
	m, _ = sendAnnot(l, typed("x")...)
	if footer = strings.Join(m.footerItems(), "|"); !strings.Contains(footer, "enter accept|n null|esc cancel") || strings.Contains(footer, "tab") {
		t.Errorf("legacy span-mode footer changed: %s", footer)
	}
}

// helpText flattens help groups into one string.
func helpText(groups []helpGroup) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(g.title + "\n")
		for _, it := range g.items {
			b.WriteString(it.keys + " " + it.desc + "\n")
		}
	}
	return b.String()
}

func TestAnnotateMultiSpanProposalBlock(t *testing.T) {
	m, out := multiSpanModel(t)
	proposals := filepath.Join(filepath.Dir(out), "proposals.jsonl")
	body := `{"id":"ms-001","annotation_status":"complete","type":"expense",` +
		`"target":{"text":"Vinamilk","start":10,"end":18},` +
		`"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"uncertain"},"confidence":0.9}` + "\n"
	if err := os.WriteFile(proposals, []byte(body), 0o600); err != nil {
		t.Fatalf("write proposals: %v", err)
	}
	if _, err := m.sess.LoadProposals(proposals); err != nil {
		t.Fatalf("LoadProposals: %v", err)
	}
	v := m.View()
	cur, prop := strings.Index(v, "Current"), strings.Index(v, "Proposal — not accepted")
	if cur < 0 || prop < cur {
		t.Fatalf("want Current before the proposal block:\n%s", v)
	}
	if cur := v[cur:prop]; !strings.Contains(cur, "Value: null · complete (default)") {
		t.Errorf("current section lacks the draft value:\n%s", cur)
	}
	for _, want := range []string{`Target: "Vinamilk" [10,18)`, `Value: "500k" [23,27) · uncertain`} {
		if !strings.Contains(v[prop:], want) {
			t.Errorf("proposal block lacks %q:\n%s", want, v[prop:])
		}
	}

	m, _ = sendAnnot(m, runeKey('p'))
	lines := readLabelLines(t, out)
	want := `{"id":"ms-001","annotation_status":"complete","type":"expense",` +
		`"target":{"text":"Vinamilk","start":10,"end":18},` +
		`"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"uncertain"}}`
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("labels = %q, want [%q]", lines, want)
	}
}

func TestSpanMarksOverlapActiveOnTop(t *testing.T) {
	tag := func(open, closer string) lipgloss.Style {
		return lipgloss.NewStyle().Transform(func(s string) string { return open + s + closer })
	}
	other, active, head := tag("<", ">"), tag("{", "}"), tag("(", ")")
	rows, focus := selectionRows([]rune("abcdef"), 20, []spanMark{{1, 4, other}, {3, 5, active}}, -1, head, 3)
	if want := "a<bc>{def}"; len(rows) != 1 || rows[0] != want || focus != 0 {
		t.Errorf("rows = %q focus %d, want [%q] 0 (active field on top)", rows, focus, want)
	}
	// A head rune is drawn over every mark.
	rows, _ = selectionRows([]rune("abcdef"), 20, []spanMark{{1, 4, other}, {3, 5, active}}, 4, head, -1)
	if want := "a<bc>{d}(e){f}"; len(rows) != 1 || rows[0] != want {
		t.Errorf("rows with head = %q, want [%q]", rows, want)
	}
}
