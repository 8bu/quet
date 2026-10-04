package annotate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Targets of multiQueue[mIdxPizza] ("ăn với Nam ở Pizza 4P 300k").
var (
	pizzaTarget  = Target{Text: "Pizza 4P", Start: 13, End: 21}
	amountTarget = Target{Text: "300k", Start: 22, End: 26}
)

// multiLabel returns a foreign label of the multi-span schema (no id, as a collaborator's label would be).
func multiLabel(status, typ string, target, value *Target, spanStatus map[string]string, note string) Label {
	l := Label{Status: status, Spans: map[string]*Target{"target": target, "value": value}, SpanStatus: spanStatus, Note: note}
	if typ != "" {
		l.Type = &typ
	}
	return l
}

func TestParseSchemaImplicitTarget(t *testing.T) {
	const head = "types: [a]\nstatuses: [complete]\n"
	for _, tt := range []struct {
		name, yaml string
		want       bool
	}{
		{"no spans key", head, true},
		{"null spans", head + "spans:\n", true},
		{"null_target_types only", head + "null_target_types: [a]\n", true},
		{"span list", head + "spans: [x]\n", false},
		{"span named target", head + "spans: [target]\n", false},
	} {
		if got := mustParseSchema(t, tt.yaml).ImplicitTarget; got != tt.want {
			t.Errorf("%s: ImplicitTarget = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestParseLabel(t *testing.T) {
	multi := mustParseSchema(t, multiSchemaYAML)
	implicit := mustParseSchema(t, "types: [a]\nstatuses: [complete]\n")

	got, err := ParseLabel(multi, []byte(`{"id":"m1","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21},"value":null,"span_status":{"value":"uncertain"},"note":"x"}`))
	if err != nil {
		t.Fatalf("ParseLabel: %v", err)
	}
	want := multiLabel("complete", "expense", &pizzaTarget, nil, map[string]string{"value": "uncertain"}, "x")
	want.ID = "m1"
	if !labelEqual(got, want) {
		t.Errorf("ParseLabel = %+v, want %+v", got, want)
	}
	if l, err := ParseLabel(implicit, []byte(`{"id":"a","annotation_status":"complete","type":null,"target":null}`)); err != nil || l.ID != "a" || l.Type != nil {
		t.Errorf("implicit ParseLabel = %+v, %v", l, err)
	}

	tests := []struct {
		name   string
		schema *Schema
		json   string
		want   string
	}{
		{"not an object", multi, `[1]`, ""},
		{"invalid json", multi, `{"id":`, ""},
		{"trailing data", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":null,"value":null} x`, ""},
		{"missing span", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":null}`, "value"},
		{"missing type", multi, `{"id":"m1","annotation_status":"skipped","target":null,"value":null}`, "type"},
		{"unknown field", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":null,"value":null,"extra":1}`, "extra"},
		{"empty id", multi, `{"id":"","annotation_status":"skipped","type":null,"target":null,"value":null}`, "id"},
		{"type not a string", multi, `{"id":"m1","annotation_status":"skipped","type":3,"target":null,"value":null}`, "type"},
		{"span with extra key", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":{"text":"a","start":0,"end":1,"x":1},"value":null}`, "target"},
		{"span offsets not integers", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":{"text":"a","start":0.5,"end":1},"value":null}`, "target"},
		{"span_status without declared statuses", implicit, `{"id":"a","annotation_status":"complete","type":null,"target":null,"span_status":{}}`, "span_status"},
		{"span_status value not a string", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":null,"value":null,"span_status":{"value":1}}`, "span_status"},
		{"note not a string", multi, `{"id":"m1","annotation_status":"skipped","type":null,"target":null,"value":null,"note":1}`, "note"},
		{"other schema's span", implicit, `{"id":"a","annotation_status":"complete","type":null,"value":null}`, "value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseLabel(tt.schema, []byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseLabel err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLabelsEqual(t *testing.T) {
	base := func() Label {
		return multiLabel("complete", "expense", &pizzaTarget, &amountTarget, map[string]string{"value": "complete"}, "n")
	}
	a, b := base(), base()
	a.ID, b.ID = "x", "y"
	if !LabelsEqual(a, b) {
		t.Error("labels differing only in id are not equal")
	}
	// Missing span equals null span; nil span status equals empty.
	c, d := base(), base()
	c.Spans = map[string]*Target{"target": &pizzaTarget, "value": &amountTarget}
	d.Spans = map[string]*Target{"target": {Text: "Pizza 4P", Start: 13, End: 21}, "value": &amountTarget, "extra": nil}
	if !LabelsEqual(c, d) {
		t.Error("a nil span entry differs from a missing one")
	}
	e, f := base(), base()
	e.SpanStatus, f.SpanStatus = nil, map[string]string{}
	if !LabelsEqual(e, f) {
		t.Error("nil span status differs from empty")
	}

	mutations := map[string]func(*Label){
		"status":      func(l *Label) { l.Status = "uncertain" },
		"type":        func(l *Label) { l.Type = new("income") },
		"type nil":    func(l *Label) { l.Type = nil },
		"span moved":  func(l *Label) { l.Spans["target"] = &Target{Text: "Pizza 4P", Start: 14, End: 22} },
		"span nil":    func(l *Label) { l.Spans["target"] = nil },
		"span status": func(l *Label) { l.SpanStatus = map[string]string{"value": "uncertain"} },
		"span status dropped": func(l *Label) {
			l.SpanStatus = nil
		},
		"note": func(l *Label) { l.Note = "other" },
	}
	for name, mutate := range mutations {
		m := base()
		mutate(&m)
		if LabelsEqual(base(), m) || LabelsEqual(m, base()) {
			t.Errorf("labels differing in %s compare equal", name)
		}
	}
}

func TestSaveLabelAppliesRulesAndDefaults(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)

	// Default span status: the value span gets its first listed status; the given note is kept; the id is ignored.
	in := multiLabel("complete", "expense", &pizzaTarget, &amountTarget, nil, "from alice")
	in.ID = "someone-else"
	if err := s.SaveLabel(mIdxPizza, in); err != nil {
		t.Fatalf("SaveLabel: %v", err)
	}
	got, ok := s.Label(mIdxPizza)
	want := multiLabel("complete", "expense", &pizzaTarget, &amountTarget, map[string]string{"value": "complete"}, "from alice")
	want.ID = "m1"
	if !ok || !labelEqual(got, want) {
		t.Fatalf("Label = %+v, %v; want %+v", got, ok, want)
	}
	if !strings.Contains(fileString(t, f.out), `"id":"m1"`) || strings.Contains(fileString(t, f.out), "someone-else") {
		t.Errorf("labels file: %s", fileString(t, f.out))
	}

	// A given span status is kept; the note replaces the previous one, "" meaning none.
	in = multiLabel("complete", "expense", &pizzaTarget, &amountTarget, map[string]string{"value": "uncertain"}, "")
	if err := s.SaveLabel(mIdxPizza, in); err != nil {
		t.Fatalf("SaveLabel: %v", err)
	}
	got, _ = s.Label(mIdxPizza)
	if got.SpanStatus["value"] != "uncertain" || got.Note != "" {
		t.Errorf("span status %v note %q, want uncertain and no note", got.SpanStatus, got.Note)
	}

	// null_for_types: a span that must be null for the type is forced null together with its span status.
	in = multiLabel("complete", "gift", &Target{Text: "Nam", Start: 5, End: 8}, &amountTarget, map[string]string{"value": "uncertain"}, "")
	if err := s.SaveLabel(mIdxGift, in); err != nil {
		t.Fatalf("SaveLabel gift: %v", err)
	}
	got, _ = s.Label(mIdxGift)
	if got.Spans["value"] != nil || len(got.SpanStatus) != 0 || got.Spans["target"] == nil {
		t.Errorf("gift label = %+v, want value and its status dropped, target kept", got)
	}

	// null_label_statuses: type, spans and span statuses are dropped.
	in = multiLabel("skipped", "expense", &pizzaTarget, &amountTarget, map[string]string{"value": "complete"}, "meh")
	if err := s.SaveLabel(mIdxSkipped, in); err != nil {
		t.Fatalf("SaveLabel skipped: %v", err)
	}
	got, _ = s.Label(mIdxSkipped)
	if got.Type != nil || got.Spans["target"] != nil || got.Spans["value"] != nil || len(got.SpanStatus) != 0 || got.Note != "meh" {
		t.Errorf("skipped label = %+v", got)
	}

	// The drafts of the record are dropped and the session counts the saves.
	if err := s.SetSpan(mIdxTransfer, "value", 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLabel(mIdxTransfer, multiLabel("uncertain", "", nil, nil, nil, "")); err != nil {
		t.Fatalf("SaveLabel uncertain: %v", err)
	}
	if s.Dirty(mIdxTransfer) {
		t.Error("draft survived SaveLabel")
	}
	if marked, _ := s.Speed(); marked != 5 {
		t.Errorf("marked = %d, want 5", marked)
	}

	// The file on disk loads back identically.
	loaded, err := LoadLabels(f.out, s.Schema(), multiQueue)
	if err != nil || len(loaded) != 4 {
		t.Fatalf("LoadLabels = %d labels, %v", len(loaded), err)
	}
}

func TestSaveLabelErrorsChangeNothing(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	good := multiLabel("complete", "expense", &pizzaTarget, &amountTarget, nil, "")
	if err := s.SaveLabel(mIdxPizza, good); err != nil {
		t.Fatal(err)
	}
	before := fileString(t, f.out)

	bad := map[string]Label{
		"undeclared status": multiLabel("later", "expense", &pizzaTarget, &amountTarget, nil, ""),
		"undeclared type":   multiLabel("complete", "bogus", &pizzaTarget, &amountTarget, nil, ""),
		"wrong span slice":  multiLabel("complete", "expense", &Target{Text: "Pizza 4P", Start: 12, End: 20}, nil, nil, ""),
		"unknown span":      {Status: "complete", Type: new("expense"), Spans: map[string]*Target{"target": &pizzaTarget}, SpanStatus: map[string]string{"ghost": "complete"}},
		"span status value": multiLabel("complete", "expense", &pizzaTarget, &amountTarget, map[string]string{"value": "zzz"}, ""),
	}
	for name, l := range bad {
		if err := s.SaveLabel(mIdxPizza, l); err == nil {
			t.Errorf("%s: SaveLabel succeeded", name)
		}
	}
	if err := s.SaveLabel(len(multiQueue), good); err == nil {
		t.Error("SaveLabel accepted an index past the queue")
	}
	if fileString(t, f.out) != before {
		t.Error("a failed SaveLabel changed the file")
	}
	if cur := mustLabel(t, s, mIdxPizza); cur.Status != "complete" || cur.Note != "" {
		t.Errorf("a failed SaveLabel changed the label: %+v", cur)
	}
	// Exactly one undo entry (the first save) exists.
	if _, _, ok, err := s.Undo(); !ok || err != nil {
		t.Fatalf("Undo = %v, %v", ok, err)
	}
	if s.CanUndo() {
		t.Error("failed SaveLabel calls pushed undo entries")
	}
}

// mustLabel returns the saved label of record i.
func mustLabel(t *testing.T, s *Session, i int) Label {
	t.Helper()
	l, ok := s.Label(i)
	if !ok {
		t.Fatalf("record %d has no label", i)
	}
	return l
}

func TestSaveLabelsAllOrNothing(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	ok1 := multiLabel("complete", "expense", &pizzaTarget, &amountTarget, nil, "")
	ok2 := multiLabel("uncertain", "", nil, nil, nil, "")
	invalid := multiLabel("complete", "expense", &Target{Text: "nope", Start: 0, End: 4}, nil, nil, "")

	err := s.SaveLabels(map[int]Label{mIdxPizza: ok1, mIdxNFD: invalid, mIdxGift: ok2})
	if err == nil || !strings.Contains(err.Error(), "record m2") {
		t.Fatalf("SaveLabels err = %v, want one naming record m2", err)
	}
	if _, statErr := os.Stat(f.out); !os.IsNotExist(statErr) {
		t.Errorf("a failed SaveLabels created the labels file (stat err %v)", statErr)
	}
	if s.LabelsInQueue() != 0 || s.CanUndo() {
		t.Errorf("a failed SaveLabels left %d labels, CanUndo=%v", s.LabelsInQueue(), s.CanUndo())
	}
	if err := s.SaveLabels(map[int]Label{99: ok1}); err == nil {
		t.Error("SaveLabels accepted an index past the queue")
	}
	if err := s.SaveLabels(nil); err != nil || s.CanUndo() {
		t.Errorf("SaveLabels(nil) = %v, CanUndo=%v; want a no-op", err, s.CanUndo())
	}

	// A write error restores the in-memory state: remove the output directory under the session.
	if err := os.MkdirAll(filepath.Join(f.dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(f.dir, "sub", "labels.jsonl")
	g, err := Open(f.queue, f.schema, gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.SaveLabel(mIdxPizza, ok1); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	if err := g.SaveLabels(map[int]Label{mIdxNFD: ok2, mIdxGift: ok2}); err == nil {
		t.Fatal("SaveLabels succeeded without an output directory")
	}
	if g.LabelsInQueue() != 1 || g.Dirty(mIdxNFD) {
		t.Errorf("a failed write left %d labels", g.LabelsInQueue())
	}
	if _, has := g.Label(mIdxNFD); has {
		t.Error("a failed write kept the in-memory label")
	}
	if marked, _ := g.Speed(); marked != 1 {
		t.Errorf("marked = %d after a failed write, want 1", marked)
	}
	if _, _, ok, err := g.Undo(); ok || err == nil {
		// The first save's entry is still on the stack; undoing it needs the directory, which is gone.
		t.Errorf("Undo after the directory vanished = ok %v, err %v; want the write error", ok, err)
	}
}

func TestSaveLabelsOneUndoEntryNormalSession(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	if err := s.SaveLabel(mIdxSkipped, multiLabel("uncertain", "", nil, nil, nil, "first")); err != nil {
		t.Fatal(err)
	}
	afterFirst := fileString(t, f.out)

	batch := map[int]Label{
		mIdxSkipped: multiLabel("skipped", "", nil, nil, nil, "replaced"), // replaces the saved label
		mIdxPizza:   multiLabel("complete", "expense", &pizzaTarget, &amountTarget, nil, "p"),
		mIdxGift:    multiLabel("uncertain", "", nil, nil, nil, "g"),
	}
	if err := s.SaveLabels(batch); err != nil {
		t.Fatalf("SaveLabels: %v", err)
	}
	if s.LabelsInQueue() != 3 {
		t.Fatalf("LabelsInQueue = %d, want 3", s.LabelsInQueue())
	}
	if got := fileString(t, f.out); strings.Count(got, "\n") != 3 {
		t.Errorf("labels file has %d lines, want 3:\n%s", strings.Count(got, "\n"), got)
	}
	if marked, _ := s.Speed(); marked != 4 {
		t.Errorf("marked = %d, want 4", marked)
	}

	idx, desc, ok, err := s.Undo()
	if err != nil || !ok || idx != mIdxPizza || desc != "reverted 3 labels" {
		t.Fatalf("Undo = %d, %q, %v, %v", idx, desc, ok, err)
	}
	if s.Cursor() != mIdxPizza {
		t.Errorf("cursor = %d, want %d", s.Cursor(), mIdxPizza)
	}
	if got := fileString(t, f.out); got != afterFirst {
		t.Errorf("one Undo did not revert the whole batch:\n%s\nwant\n%s", got, afterFirst)
	}
	if marked, _ := s.Speed(); marked != 1 {
		t.Errorf("marked = %d after undo, want 1", marked)
	}
	// The single-record entry keeps its message.
	idx, desc, ok, err = s.Undo()
	if err != nil || !ok || idx != mIdxSkipped || desc != "removed label" {
		t.Fatalf("second Undo = %d, %q, %v, %v", idx, desc, ok, err)
	}
	if s.CanUndo() || s.LabelsInQueue() != 0 {
		t.Error("undo stack not empty")
	}
}

// recheckBatch replaces the labels of subset queue indices 0 (record 3), 3 (record 21, hand-formatted) and 5
// (record 33) and labels indices 1 and 2 (records 9 and 15) when they were unlabeled.
func recheckBatch() map[int]Label {
	batch := map[int]Label{}
	for _, k := range []int{0, 1, 2, 3, 5} {
		batch[k] = Label{Status: StatusUncertain, Note: "batch " + recheckID(recheckSubsetRecords()[k])}
	}
	return batch
}

func TestSaveLabelsUndoIsByteIdenticalInRecheck(t *testing.T) {
	f := newRecheckFixture(t, 9, 15) // records 9 and 15 (queue indices 1 and 2) are unlabeled
	s := f.open(t)
	if s.LabelsInQueue() != recheckSubset-2 {
		t.Fatalf("LabelsInQueue = %d", s.LabelsInQueue())
	}
	if err := s.SaveLabels(recheckBatch()); err != nil {
		t.Fatalf("SaveLabels: %v", err)
	}
	after := f.read(t)
	if bytes.Equal(after, f.original) {
		t.Fatal("SaveLabels did not change the file")
	}
	if !bytes.Contains(after, []byte(`"note":"batch r021"`)) {
		t.Errorf("replaced hand-formatted line missing:\n%s", after)
	}

	idx, desc, ok, err := s.Undo()
	if err != nil || !ok || idx != 0 || desc != "reverted 5 labels" {
		t.Fatalf("Undo = %d, %q, %v, %v", idx, desc, ok, err)
	}
	if got := f.read(t); !bytes.Equal(got, f.original) {
		t.Errorf("Undo of the batch is not byte-identical:\n%s", got)
	}
	if s.CanUndo() {
		t.Error("one batch left more than one undo entry")
	}
}

func TestOpenRecheckItemsMergesReplacedAndAppended(t *testing.T) {
	f := newRecheckFixture(t, 9, 15)
	schema, err := ParseSchema([]byte(recheckSchema))
	if err != nil {
		t.Fatal(err)
	}
	items, err := LoadQueue(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenRecheckItems(schema, items, f.labels)
	if err != nil {
		t.Fatalf("OpenRecheckItems: %v", err)
	}
	if !s.Recheck() || s.QueuePath() != "" || s.SchemaPath() != "" || s.OutPath() != f.labels || s.Filter() != FilterAll || s.Len() != recheckSubset {
		t.Errorf("Recheck=%v QueuePath=%q SchemaPath=%q OutPath=%q Filter=%v Len=%d", s.Recheck(), s.QueuePath(), s.SchemaPath(), s.OutPath(), s.Filter(), s.Len())
	}
	if s.LabelsTotal() != recheckFull-2 {
		t.Errorf("LabelsTotal = %d, want %d", s.LabelsTotal(), recheckFull-2)
	}

	batch := recheckBatch()
	if err := s.SaveLabels(batch); err != nil {
		t.Fatalf("SaveLabels: %v", err)
	}

	// Expected: original lines in place (replaced ones re-encoded, the rest byte for byte), then the labels of the
	// unlabeled records appended in queue order.
	subset := recheckSubsetRecords()
	replacedAt := map[string]int{}
	for k := range batch {
		replacedAt[recheckID(subset[k])] = k
	}
	var want []string
	for _, line := range fileLines(t, f.original) {
		id := lineIDs(t, []string{line})[0]
		if k, replaced := replacedAt[id]; replaced {
			var buf bytes.Buffer
			if err := encodeLabel(&buf, schema, id, batch[k]); err != nil {
				t.Fatal(err)
			}
			line = strings.TrimSuffix(buf.String(), "\n")
			delete(replacedAt, id)
		}
		want = append(want, line)
	}
	for _, k := range []int{1, 2} {
		var buf bytes.Buffer
		if err := encodeLabel(&buf, schema, recheckID(subset[k]), batch[k]); err != nil {
			t.Fatal(err)
		}
		want = append(want, strings.TrimSuffix(buf.String(), "\n"))
		delete(replacedAt, recheckID(subset[k]))
	}
	if len(replacedAt) != 0 {
		t.Fatalf("test bug: unplaced ids %v", replacedAt)
	}
	if got := string(f.read(t)); got != strings.Join(want, "\n")+"\n" {
		t.Errorf("merged file differs:\n%s\nwant\n%s", got, strings.Join(want, "\n")+"\n")
	}
	if s.LabelsTotal() != recheckFull {
		t.Errorf("LabelsTotal = %d, want %d", s.LabelsTotal(), recheckFull)
	}
}

func TestOpenRecheckItemsErrors(t *testing.T) {
	f := newRecheckFixture(t)
	schema := mustParseSchema(t, recheckSchema)
	items, err := LoadQueue(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRecheckItems(schema, nil, f.labels); err == nil || !strings.Contains(err.Error(), "no records") {
		t.Errorf("empty queue: %v", err)
	}
	if _, err := OpenRecheckItems(schema, items, filepath.Join(f.dir, "missing", "labels.jsonl")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing directory: %v", err)
	}
	if _, err := OpenRecheckItems(schema, items, filepath.Join(f.dir, "nope.jsonl")); err == nil {
		t.Error("missing labels file accepted")
	}
	// The labels file may not be modified underneath the session.
	s, err := OpenRecheckItems(schema, items, f.labels)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.labels, append(f.original, []byte(`{"id": "zz", "annotation_status": "skipped", "type": null, "target": null}`+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := f.read(t)
	if err := s.SaveLabels(recheckBatch()); err == nil {
		t.Error("SaveLabels wrote over a file changed underneath")
	}
	if !bytes.Equal(f.read(t), changed) || s.CanUndo() {
		t.Error("a refused SaveLabels changed state")
	}
}

func TestOpenRecheckItemsLoadsProposals(t *testing.T) {
	f := newFixture(t, "")
	schema := loadGidiSchema(t)
	items, err := LoadQueue(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenRecheckItems(schema, items, f.out)
	if err != nil {
		t.Fatalf("OpenRecheckItems: %v", err)
	}
	if s.ProposalsSource() != "" {
		t.Errorf("ProposalsSource = %q without proposals", s.ProposalsSource())
	}
	path := writeProposals(t, f.dir, propLend)
	if _, err := s.LoadProposals(path); err != nil {
		t.Fatalf("LoadProposals: %v", err)
	}
	if s.ProposalsSource() != path {
		t.Errorf("ProposalsSource = %q, want %q", s.ProposalsSource(), path)
	}
	if _, err := s.LoadProposals(f.out); err == nil {
		t.Error("second LoadProposals accepted")
	}
}

func TestSessionIndexOfAndSchemaPath(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	if s.SchemaPath() != f.schema {
		t.Errorf("SchemaPath = %q, want %q", s.SchemaPath(), f.schema)
	}
	for i := range s.Len() {
		if got, ok := s.IndexOf(s.Item(i).ID); !ok || got != i {
			t.Errorf("IndexOf(%q) = %d, %v; want %d", s.Item(i).ID, got, ok, i)
		}
	}
	if _, ok := s.IndexOf("no-such-id"); ok {
		t.Error("IndexOf found an unknown id")
	}
	if _, ok := s.IndexOf(""); ok {
		t.Error("IndexOf found the empty id")
	}
}
