package annotate

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Queue indices of testdata/queue.jsonl.
const (
	idxReal1       = 0 // "bo heo dat 50k"
	idxLend        = 1 // "cho Nam vay 500k"
	idxRepaymentIn = 2 // "Nam trả nợ tao 500k"
	idxExpense     = 3 // "ăn với Nam ở Pizza 4P 300k"
	idxTransfer    = 4 // "ck 5tr qua tk tiết kiệm"
	idxUncertain   = 5 // "Nam gửi tao 500k"
	idxReal2       = 6 // "con no Nam 300k tien an"
)

// fixture is a temp copy of the queue and Gidi schema fixtures plus an out path next to them.
type fixture struct{ dir, queue, schema, out string }

// newFixture copies testdata into a temp dir; schemaYAML replaces the Gidi schema when non-empty.
func newFixture(t *testing.T, schemaYAML string) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		dir:    dir,
		queue:  filepath.Join(dir, "queue.jsonl"),
		schema: filepath.Join(dir, "schema.yaml"),
		out:    filepath.Join(dir, "labels.jsonl"),
	}
	copyFile(t, "testdata/queue.jsonl", f.queue)
	if schemaYAML == "" {
		copyFile(t, gidiSchemaPath, f.schema)
	} else if err := os.WriteFile(f.schema, []byte(schemaYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) open(t *testing.T) *Session {
	t.Helper()
	s, err := Open(f.queue, f.schema, f.out)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// outLines returns the non-empty lines of the labels file.
func (f fixture) outLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func mustSetType(t *testing.T, s *Session, i int, typ string) {
	t.Helper()
	if _, err := s.SetType(i, typ); err != nil {
		t.Fatalf("SetType(%d,%q): %v", i, typ, err)
	}
}

func mustSetTarget(t *testing.T, s *Session, i, start, end int) {
	t.Helper()
	if err := s.SetTarget(i, start, end); err != nil {
		t.Fatalf("SetTarget(%d,%d,%d): %v", i, start, end, err)
	}
}

func mustMark(t *testing.T, s *Session, i int, status string) {
	t.Helper()
	if err := s.Mark(i, status); err != nil {
		t.Fatalf("Mark(%d,%q): %v", i, status, err)
	}
}

func TestSessionGidiCompatibility(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)

	// Marked out of queue order; the file is always written in queue order.
	mustMark(t, s, idxUncertain, StatusUncertain)

	mustSetType(t, s, idxExpense, "expense")
	mustSetTarget(t, s, idxExpense, 13, 21)
	mustMark(t, s, idxExpense, StatusComplete)

	mustSetType(t, s, idxLend, "lend")
	mustSetTarget(t, s, idxLend, 4, 7)
	mustMark(t, s, idxLend, StatusComplete)

	mustSetType(t, s, idxRepaymentIn, "repayment_in")
	mustSetTarget(t, s, idxRepaymentIn, 0, 3)
	mustMark(t, s, idxRepaymentIn, StatusComplete)

	mustSetType(t, s, idxTransfer, "transfer")
	mustMark(t, s, idxTransfer, StatusComplete)

	want := []string{
		`{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":4,"end":7}}`,
		`{"id":"case-repayment-in","annotation_status":"complete","type":"repayment_in","target":{"text":"Nam","start":0,"end":3}}`,
		`{"id":"case-expense","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21}}`,
		`{"id":"case-transfer","annotation_status":"complete","type":"transfer","target":null}`,
		`{"id":"case-uncertain","annotation_status":"uncertain","type":null,"target":null}`,
	}
	if got := f.outLines(t); !reflect.DeepEqual(got, want) {
		t.Errorf("labels file:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	l, ok := s.Label(idxUncertain)
	if !ok || l.Type != nil || l.Target != nil || l.Status != StatusUncertain {
		t.Errorf("uncertain label = %+v, %v", l, ok)
	}
	for _, i := range []int{idxLend, idxRepaymentIn, idxExpense, idxTransfer, idxUncertain} {
		if s.Dirty(i) {
			t.Errorf("record %d dirty after Mark", i)
		}
	}
}

func TestSessionTransferNullTarget(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	i := idxTransfer

	mustSetType(t, s, i, "lend")
	mustSetTarget(t, s, i, 3, 6) // "5tr"
	cleared, err := s.SetType(i, "transfer")
	if err != nil || !cleared {
		t.Fatalf("SetType(transfer) = %v, %v; want cleared", cleared, err)
	}
	if d := s.Draft(i); d != (Draft{Type: "transfer"}) {
		t.Errorf("draft = %+v, want transfer with null target", d)
	}
	if cleared, err := s.SetType(i, "transfer"); err != nil || cleared {
		t.Errorf("SetType(transfer) again = %v, %v; want not cleared", cleared, err)
	}
	if err := s.SetTarget(i, 3, 6); err == nil || !strings.Contains(err.Error(), "null target") {
		t.Errorf("SetTarget on transfer: err = %v", err)
	}
	if d := s.Draft(i); d.Target != nil {
		t.Errorf("refused SetTarget changed draft: %+v", d)
	}

	// A saved lend target is cleared from the draft (not the saved label) when switching to transfer.
	mustSetType(t, s, i, "lend")
	mustSetTarget(t, s, i, 3, 6)
	mustMark(t, s, i, StatusComplete)
	if cleared, _ := s.SetType(i, "transfer"); !cleared || !s.Dirty(i) {
		t.Errorf("switch saved lend to transfer: cleared=%v dirty=%v", cleared, s.Dirty(i))
	}
	if l, _ := s.Label(i); l.Target == nil {
		t.Error("saved label target lost before Mark")
	}
	mustMark(t, s, i, StatusComplete)
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{`{"id":"case-transfer","annotation_status":"complete","type":"transfer","target":null}`}) {
		t.Errorf("labels file = %q", got)
	}

	bad := Label{ID: "case-transfer", Status: StatusComplete, Type: new("transfer"), Target: &Target{Text: "5tr", Start: 3, End: 6}}
	if err := s.Schema().Validate(bad, s.Item(i).Text); err == nil {
		t.Error("Validate accepted transfer with a target")
	}
}

func TestSessionMarkRejections(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	i := idxLend

	if err := s.Mark(i, StatusComplete); err == nil || !strings.Contains(err.Error(), "type: required") {
		t.Errorf("complete without type: err = %v", err)
	}
	if err := s.Mark(i, "approved"); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Errorf("unknown status: err = %v", err)
	}
	if _, err := s.SetType(i, "other"); err == nil {
		t.Error("SetType accepted unknown type")
	}
	if _, err := s.SetType(i, ""); err == nil {
		t.Error("SetType accepted empty type")
	}
	if err := s.SetTarget(i, 3, 7); err == nil {
		t.Error("SetTarget accepted whitespace-padded span")
	}
	if err := s.SetTarget(i, 4, 99); err == nil {
		t.Error("SetTarget accepted out-of-range span")
	}
	if s.Dirty(i) {
		t.Errorf("failed edits left a draft: %+v", s.Draft(i))
	}
	if _, ok := s.Label(i); ok {
		t.Error("failed Mark saved a label")
	}
	if _, err := os.Stat(f.out); !os.IsNotExist(err) {
		t.Errorf("failed Mark wrote the labels file: %v", err)
	}
	if marked, _ := s.Speed(); marked != 0 {
		t.Errorf("marked = %d after failures", marked)
	}

	// uncertain and skipped keep the values they carry.
	mustSetType(t, s, i, "lend")
	mustSetTarget(t, s, i, 4, 7)
	mustMark(t, s, i, StatusUncertain)
	mustSetType(t, s, idxReal2, "borrow")
	mustMark(t, s, idxReal2, StatusSkipped)
	want := []string{
		`{"id":"case-lend","annotation_status":"uncertain","type":"lend","target":{"text":"Nam","start":4,"end":7}}`,
		`{"id":"baseline-01-efb9ecbae1cf","annotation_status":"skipped","type":"borrow","target":null}`,
	}
	if got := f.outLines(t); !reflect.DeepEqual(got, want) {
		t.Errorf("labels file = %q, want %q", got, want)
	}
	if s.Cursor() != 0 {
		t.Errorf("Mark moved the cursor to %d", s.Cursor())
	}
}

func TestSessionMarkUndeclaredRoleStatus(t *testing.T) {
	f := newFixture(t, "types: [a]\nstatuses: [complete]\n")
	s := f.open(t)
	for _, st := range []string{StatusUncertain, StatusSkipped} {
		if err := s.Mark(0, st); err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Errorf("Mark(%q): err = %v", st, err)
		}
	}
}

func TestSessionMarkWriteFailureRestoresLabel(t *testing.T) {
	f := newFixture(t, "")
	outDir := filepath.Join(f.dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(f.queue, f.schema, filepath.Join(outDir, "labels.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustSetType(t, s, idxLend, "lend")
	mustMark(t, s, idxLend, StatusComplete)
	if err := os.RemoveAll(outDir); err != nil {
		t.Fatal(err)
	}

	mustSetType(t, s, idxLend, "borrow")
	if err := s.Mark(idxLend, StatusUncertain); err == nil {
		t.Fatal("Mark succeeded without an output directory")
	}
	if l, _ := s.Label(idxLend); l.Status != StatusComplete || *l.Type != "lend" {
		t.Errorf("label not restored: %+v", l)
	}
	if !s.Dirty(idxLend) || s.Draft(idxLend).Type != "borrow" {
		t.Errorf("draft lost after failed write: %+v", s.Draft(idxLend))
	}

	mustSetType(t, s, idxExpense, "expense")
	if err := s.Mark(idxExpense, StatusComplete); err == nil {
		t.Fatal("Mark succeeded without an output directory")
	}
	if _, ok := s.Label(idxExpense); ok {
		t.Error("new label kept after failed write")
	}
	if c := s.Counts(); c.Complete != 1 || c.Remaining != 6 {
		t.Errorf("counts = %+v", c)
	}
	if marked, _ := s.Speed(); marked != 1 {
		t.Errorf("marked = %d, want 1", marked)
	}
}

func TestSessionResume(t *testing.T) {
	f := newFixture(t, "")
	queueBefore, _ := os.ReadFile(f.queue)
	// An existing note survives re-marking.
	if err := os.WriteFile(f.out, []byte(`{"id":"case-lend","annotation_status":"uncertain","type":null,"target":null,"note":"hỏi lại"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := f.open(t)
	if s.Cursor() != idxReal1 {
		t.Errorf("initial cursor = %d, want %d", s.Cursor(), idxReal1)
	}
	mustMark(t, s, idxReal1, StatusSkipped)
	mustSetType(t, s, idxLend, "lend")
	mustSetTarget(t, s, idxLend, 4, 7)
	mustMark(t, s, idxLend, StatusComplete)
	mustSetType(t, s, idxRepaymentIn, "repayment_in") // unsaved draft is not persisted

	r := f.open(t)
	if r.Cursor() != idxRepaymentIn {
		t.Errorf("resumed cursor = %d, want first unfinished %d", r.Cursor(), idxRepaymentIn)
	}
	for _, i := range []int{idxReal1, idxLend} {
		got, ok := r.Label(i)
		want, _ := s.Label(i)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("resumed label %d = %+v, want %+v", i, got, want)
		}
	}
	if l, _ := r.Label(idxLend); l.Note != "hỏi lại" {
		t.Errorf("note = %q, want preserved", l.Note)
	}
	if _, ok := r.Label(idxRepaymentIn); ok || r.Dirty(idxRepaymentIn) {
		t.Error("unsaved draft survived reopen")
	}
	if c := r.Counts(); c != (Counts{Total: 7, Complete: 1, Skipped: 1, Remaining: 5}) {
		t.Errorf("counts = %+v", c)
	}
	if want := `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":4,"end":7},"note":"hỏi lại"}`; f.outLines(t)[1] != want {
		t.Errorf("line 2 = %s, want %s", f.outLines(t)[1], want)
	}
	if queueAfter, _ := os.ReadFile(f.queue); !bytes.Equal(queueBefore, queueAfter) {
		t.Error("queue file changed")
	}
}

func TestSessionResumeAllDone(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	for i := range s.Len() {
		mustMark(t, s, i, StatusSkipped)
	}
	if r := f.open(t); r.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0 when nothing is unfinished", r.Cursor())
	}
}

func TestSessionReviseRecord(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	mustSetType(t, s, idxLend, "lend")
	mustSetTarget(t, s, idxLend, 4, 7)
	mustMark(t, s, idxLend, StatusComplete)
	mustMark(t, s, idxUncertain, StatusUncertain)

	if d := s.Draft(idxLend); d.Type != "lend" || d.Target == nil || s.Dirty(idxLend) {
		t.Fatalf("draft of saved label = %+v dirty=%v", d, s.Dirty(idxLend))
	}
	mustSetType(t, s, idxLend, "borrow")
	mustMark(t, s, idxLend, StatusComplete)

	want := []string{
		`{"id":"case-lend","annotation_status":"complete","type":"borrow","target":{"text":"Nam","start":4,"end":7}}`,
		`{"id":"case-uncertain","annotation_status":"uncertain","type":null,"target":null}`,
	}
	if got := f.outLines(t); !reflect.DeepEqual(got, want) {
		t.Errorf("labels file = %q, want %q", got, want)
	}
	if c := s.Counts(); c.Complete != 1 || c.Uncertain != 1 || c.Remaining != 5 {
		t.Errorf("counts = %+v", c)
	}
}

func TestSessionDraftsAndDirty(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	i := idxLend
	if d := s.Draft(i); d != (Draft{}) || s.Dirty(i) {
		t.Fatalf("fresh draft = %+v dirty=%v", d, s.Dirty(i))
	}
	mustSetType(t, s, i, "lend")
	if !s.Dirty(i) {
		t.Error("SetType did not dirty the record")
	}
	s.DiscardDraft(i)
	if s.Dirty(i) || s.Draft(i) != (Draft{}) {
		t.Error("DiscardDraft kept the draft")
	}

	mustSetType(t, s, i, "lend")
	mustSetTarget(t, s, i, 4, 7)
	mustMark(t, s, i, StatusComplete)
	mustSetType(t, s, i, "borrow")
	mustSetType(t, s, i, "lend")
	if s.Dirty(i) {
		t.Error("draft equal to saved label reported dirty")
	}
	s.ClearTarget(i)
	if !s.Dirty(i) || s.Draft(i).Target != nil {
		t.Errorf("ClearTarget: draft = %+v dirty=%v", s.Draft(i), s.Dirty(i))
	}
	mustSetTarget(t, s, i, 4, 7)
	if s.Dirty(i) {
		t.Error("equal target reported dirty")
	}
	mustSetTarget(t, s, i, 0, 3) // "cho"
	if !s.Dirty(i) {
		t.Error("changed target not dirty")
	}
	s.DiscardDraft(i)
	if d := s.Draft(i); d.Type != "lend" || *d.Target != (Target{Text: "Nam", Start: 4, End: 7}) {
		t.Errorf("DiscardDraft: draft = %+v, want saved values", d)
	}

	// Returned drafts and labels are copies.
	d := s.Draft(i)
	d.Target.Text = "mutated"
	l, _ := s.Label(i)
	l.Target.Text = "mutated"
	if s.Draft(i).Target.Text != "Nam" || s.Dirty(i) {
		t.Error("mutating a returned target changed session state")
	}
}

func TestOpenRejects(t *testing.T) {
	f := newFixture(t, "")
	queueBefore, _ := os.ReadFile(f.queue)
	link := filepath.Join(f.dir, "link.jsonl")
	if err := os.Symlink(f.queue, link); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(f.dir, "hard.jsonl")
	if err := os.Link(f.queue, hard); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, queue, schema, out, want string
	}{
		{"out is queue", f.queue, f.schema, f.queue, "queue file"},
		{"out is queue via ..", f.queue, f.schema, filepath.Join(f.dir, "x", "..", "queue.jsonl"), "queue file"},
		{"out symlinks to queue", f.queue, f.schema, link, "queue file"},
		{"out hard-links queue", f.queue, f.schema, hard, "queue file"},
		{"out parent missing", f.queue, f.schema, filepath.Join(f.dir, "missing", "labels.jsonl"), "does not exist"},
		{"missing schema", f.queue, filepath.Join(f.dir, "none.yaml"), f.out, "read schema"},
		{"missing queue", filepath.Join(f.dir, "none.jsonl"), f.schema, f.out, "read queue"},
		{"empty queue", writeFile(t, "empty.jsonl", "\n"), f.schema, f.out, "no records"},
		{"bad labels", f.queue, f.schema, writeFile(t, "labels.jsonl", "{}\n"), ":1: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(tt.queue, tt.schema, tt.out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
	if queueAfter, _ := os.ReadFile(f.queue); !bytes.Equal(queueBefore, queueAfter) {
		t.Error("queue file changed")
	}
}

func TestOpenAccessors(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	if s.QueuePath() != f.queue || s.OutPath() != f.out || s.Len() != 7 || s.Schema().Version != "annotation-v1" {
		t.Errorf("accessors: %q %q %d %q", s.QueuePath(), s.OutPath(), s.Len(), s.Schema().Version)
	}
	if s.Item(idxExpense) != (Item{ID: "case-expense", Text: "ăn với Nam ở Pizza 4P 300k"}) {
		t.Errorf("Item = %+v", s.Item(idxExpense))
	}
	if s.Filter() != FilterUnfinished || s.Cursor() != 0 {
		t.Errorf("filter=%v cursor=%d", s.Filter(), s.Cursor())
	}
}

// navSession opens a session with a custom extra status and labels: 1 complete, 2 uncertain, 4 skipped,
// 5 "later" (another schema status); 0, 3, 6 unfinished.
func navSession(t *testing.T) *Session {
	t.Helper()
	f := newFixture(t, "types: [lend, expense]\nstatuses: [complete, uncertain, skipped, later]\n")
	s := f.open(t)
	mustSetType(t, s, 1, "lend")
	mustMark(t, s, 1, StatusComplete)
	mustMark(t, s, 2, StatusUncertain)
	mustMark(t, s, 4, StatusSkipped)
	mustMark(t, s, 5, "later")
	return s
}

func TestSessionCountsAndMatches(t *testing.T) {
	s := navSession(t)
	if c := s.Counts(); c != (Counts{Total: 7, Complete: 1, Uncertain: 1, Skipped: 1, Other: 1, Remaining: 3}) {
		t.Errorf("counts = %+v", c)
	}
	want := map[Filter][]int{
		FilterUnfinished: {0, 3, 6},
		FilterComplete:   {1},
		FilterUncertain:  {2},
		FilterSkipped:    {4},
		FilterAll:        {0, 1, 2, 3, 4, 5, 6},
	}
	for f, idx := range want {
		var got []int
		for i := range s.Len() {
			if s.Matches(i, f) {
				got = append(got, i)
			}
		}
		if !reflect.DeepEqual(got, idx) {
			t.Errorf("%v matches %v, want %v", f, got, idx)
		}
	}
}

func TestSessionNavigation(t *testing.T) {
	s := navSession(t)
	steps := []struct {
		name string
		op   func() bool
		ok   bool
		want int
	}{
		{"next", s.Next, true, 3},
		{"next", s.Next, true, 6},
		{"next at end", s.Next, false, 6},
		{"prev", s.Prev, true, 3},
		{"prev", s.Prev, true, 0},
		{"prev at start", s.Prev, false, 0},
		{"last", s.Last, true, 6},
		{"first", s.First, true, 0},
	}
	for _, st := range steps {
		if ok := st.op(); ok != st.ok || s.Cursor() != st.want {
			t.Fatalf("%s: ok=%v cursor=%d, want ok=%v cursor=%d", st.name, ok, s.Cursor(), st.ok, st.want)
		}
	}

	if pos, n := s.FilterPosition(); pos != 1 || n != 3 {
		t.Errorf("FilterPosition = %d/%d, want 1/3", pos, n)
	}
	s.SetCursor(6)
	if pos, n := s.FilterPosition(); pos != 3 || n != 3 {
		t.Errorf("FilterPosition = %d/%d, want 3/3", pos, n)
	}
	s.SetCursor(1)
	if pos, n := s.FilterPosition(); pos != 0 || n != 3 {
		t.Errorf("FilterPosition on non-matching = %d/%d, want 0/3", pos, n)
	}
	s.SetCursor(99)
	if s.Cursor() != 6 {
		t.Errorf("SetCursor(99) = %d, want clamped 6", s.Cursor())
	}
	s.SetCursor(-3)
	if s.Cursor() != 0 {
		t.Errorf("SetCursor(-3) = %d, want clamped 0", s.Cursor())
	}

	// SetFilter: first match at/after the cursor, wrapping.
	filterSteps := []struct {
		cursor int
		f      Filter
		want   int
	}{
		{1, FilterUnfinished, 3},
		{3, FilterUnfinished, 3},
		{4, FilterComplete, 1},
		{1, FilterUncertain, 2},
		{2, FilterAll, 2},
		{5, FilterSkipped, 4},
	}
	for _, st := range filterSteps {
		s.SetCursor(st.cursor)
		s.SetFilter(st.f)
		if s.Filter() != st.f || s.Cursor() != st.want {
			t.Errorf("SetFilter(%v) from %d: filter=%v cursor=%d, want %d", st.f, st.cursor, s.Filter(), s.Cursor(), st.want)
		}
	}
	// Filter "all" navigation visits the other-status record.
	s.SetFilter(FilterAll)
	s.SetCursor(4)
	if !s.Next() || s.Cursor() != 5 {
		t.Errorf("all: Next from 4 = %d, want 5", s.Cursor())
	}
}

func TestSessionNoMatches(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	s.SetCursor(3)
	s.SetFilter(FilterComplete)
	if s.Filter() != FilterComplete || s.Cursor() != 3 {
		t.Errorf("SetFilter with no matches: filter=%v cursor=%d", s.Filter(), s.Cursor())
	}
	for name, op := range map[string]func() bool{"next": s.Next, "prev": s.Prev, "first": s.First, "last": s.Last, "advance": s.Advance} {
		if op() || s.Cursor() != 3 {
			t.Errorf("%s with no matches moved to %d", name, s.Cursor())
		}
	}
	if pos, n := s.FilterPosition(); pos != 0 || n != 0 {
		t.Errorf("FilterPosition = %d/%d, want 0/0", pos, n)
	}
}

func TestSessionAdvance(t *testing.T) {
	tests := []struct {
		name              string
		filter            Filter
		cursor            int
		ok                bool
		want              int
		markZeroUncertain bool
	}{
		{"unfinished next", FilterUnfinished, 3, true, 6, false},
		{"unfinished wraps", FilterUnfinished, 6, true, 0, false},
		{"unfinished from labeled", FilterUnfinished, 1, true, 3, false},
		{"all skips labeled", FilterAll, 1, true, 3, false},
		{"all wraps to unfinished", FilterAll, 6, true, 0, false},
		{"complete only cursor matches", FilterComplete, 1, false, 1, false},
		{"uncertain visits labeled", FilterUncertain, 2, true, 0, true},
		{"skipped from elsewhere", FilterSkipped, 6, true, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := navSession(t)
			if tt.markZeroUncertain {
				mustMark(t, s, 0, StatusUncertain)
			}
			s.SetFilter(tt.filter)
			s.SetCursor(tt.cursor)
			if ok := s.Advance(); ok != tt.ok || s.Cursor() != tt.want {
				t.Errorf("Advance = %v cursor %d, want %v cursor %d", ok, s.Cursor(), tt.ok, tt.want)
			}
		})
	}

	s := navSession(t)
	for _, i := range []int{0, 3, 6} {
		mustMark(t, s, i, StatusSkipped)
	}
	s.SetCursor(6)
	if s.Advance() || s.Cursor() != 6 {
		t.Errorf("Advance with everything done moved to %d", s.Cursor())
	}
}

func TestSessionSpeed(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := t0
	s.SetClock(func() time.Time { return now })

	check := func(wantMarked int, wantRate float64) {
		t.Helper()
		if marked, rate := s.Speed(); marked != wantMarked || rate != wantRate {
			t.Errorf("Speed = %d, %v; want %d, %v", marked, rate, wantMarked, wantRate)
		}
	}
	now = t0.Add(time.Minute)
	check(0, 0)
	now = t0
	mustMark(t, s, 0, StatusSkipped)
	check(1, 0)
	now = t0.Add(999 * time.Millisecond)
	check(1, 0)
	now = t0.Add(30 * time.Second)
	mustMark(t, s, 0, StatusUncertain) // re-marking counts
	check(2, 4)
	now = t0.Add(2 * time.Minute)
	check(2, 1)
}

func TestFilters(t *testing.T) {
	var names []string
	for _, f := range Filters() {
		names = append(names, f.String())
		got, err := ParseFilter(strings.ToUpper(f.String()))
		if err != nil || got != f {
			t.Errorf("ParseFilter(%q) = %v, %v", f.String(), got, err)
		}
	}
	if want := []string{"unfinished", "complete", "uncertain", "skipped", "all"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Filters() = %v, want %v", names, want)
	}
	if _, err := ParseFilter("done"); err == nil {
		t.Error("ParseFilter accepted an unknown filter")
	}
}
