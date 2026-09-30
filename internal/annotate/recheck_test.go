package annotate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recheckSchema is a generic schema for re-check tests: gamma must have a null target; later is another status.
const recheckSchema = "types: [alpha, beta, gamma]\nstatuses: [complete, uncertain, skipped, later]\nnull_target_types: [gamma]\n"

// Sizes of the re-check fixture: the full corpus and the subset queue (records 3, 9, …, 135).
const (
	recheckFull   = 140
	recheckSubset = 23
)

// recheckID and recheckText are record i of the full corpus.
func recheckID(i int) string   { return fmt.Sprintf("r%03d", i) }
func recheckText(i int) string { return fmt.Sprintf("Nam ăn phở số %03d", i) }

// recheckLabel is the fixture label of record i: a mix of statuses, targets, null-target types and notes.
func recheckLabel(t *testing.T, i int) Label {
	t.Helper()
	l := Label{ID: recheckID(i)}
	switch i % 4 {
	case 0:
		target, err := SpanTarget(recheckText(i), 7, 10) // "phở"
		if err != nil {
			t.Fatal(err)
		}
		l.Status, l.Type, l.Target = StatusComplete, new("alpha"), &target
	case 1:
		l.Status, l.Type = StatusComplete, new("gamma")
	case 2:
		l.Status, l.Note = StatusUncertain, "khó <&> hiểu"
	case 3:
		l.Status, l.Type = "later", new("beta")
	}
	return l
}

// recheckLine is the labels file line of record i. Every 7th line is hand-formatted (spaces, escaped non-ASCII) so
// that verbatim preservation differs observably from re-encoding.
func recheckLine(t *testing.T, i int) string {
	t.Helper()
	if i%7 == 0 {
		return fmt.Sprintf(`{"id": "%s", "annotation_status": "skipped", "type": null, "target": null, "note": "\u0103n"}`, recheckID(i))
	}
	var buf bytes.Buffer
	if err := encodeLabel(newLabelEncoder(&buf), recheckID(i), recheckLabel(t, i)); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// recheckFixture is a temp dir with the generic schema, a subset queue and a canonical labels file.
type recheckFixture struct {
	dir, queue, schema, labels string
	original                   []byte // labels file content as written
}

// newRecheckFixture writes the subset queue and a labels file holding every full-corpus record except unlabeled,
// in a shuffled (non-queue) order.
func newRecheckFixture(t *testing.T, unlabeled ...int) recheckFixture {
	t.Helper()
	dir := t.TempDir()
	f := recheckFixture{
		dir:    dir,
		queue:  filepath.Join(dir, "recheck.jsonl"),
		schema: filepath.Join(dir, "schema.yaml"),
		labels: filepath.Join(dir, "labels.jsonl"),
	}
	var queue bytes.Buffer
	for _, i := range recheckSubsetRecords() {
		fmt.Fprintf(&queue, `{"id":%q,"text":%q,"source":"x"}`+"\n", recheckID(i), recheckText(i))
	}
	skip := map[int]bool{}
	for _, i := range unlabeled {
		skip[i] = true
	}
	var labels bytes.Buffer
	for k := range recheckFull {
		if i := k * 37 % recheckFull; !skip[i] {
			labels.WriteString(recheckLine(t, i) + "\n")
		}
	}
	f.original = labels.Bytes()
	for path, data := range map[string][]byte{f.queue: queue.Bytes(), f.schema: []byte(recheckSchema), f.labels: f.original} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// recheckSubsetRecords returns the full-corpus record numbers of the subset queue, in queue order.
func recheckSubsetRecords() []int {
	var recs []int
	for k := range recheckSubset {
		recs = append(recs, 3+6*k)
	}
	return recs
}

func (f recheckFixture) open(t *testing.T) *Session {
	t.Helper()
	s, err := OpenRecheck(f.queue, f.schema, f.labels)
	if err != nil {
		t.Fatalf("OpenRecheck: %v", err)
	}
	return s
}

// read returns the labels file content.
func (f recheckFixture) read(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(f.labels)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fileLines splits file content into lines, requiring a trailing newline.
func fileLines(t *testing.T, data []byte) []string {
	t.Helper()
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("labels file does not end with a newline: %q", data)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// lineIDs returns the id of every line, failing on duplicates.
func lineIDs(t *testing.T, lines []string) []string {
	t.Helper()
	seen := map[string]bool{}
	var ids []string
	for n, line := range lines {
		var v struct{ ID string }
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line %d: %v", n+1, err)
		}
		if seen[v.ID] {
			t.Fatalf("duplicate id %q", v.ID)
		}
		seen[v.ID] = true
		ids = append(ids, v.ID)
	}
	return ids
}

func TestOpenRecheckSubset(t *testing.T) {
	f := newRecheckFixture(t)
	s := f.open(t)
	if !s.Recheck() || s.Len() != recheckSubset || s.LabelsTotal() != recheckFull || s.LabelsInQueue() != recheckSubset {
		t.Fatalf("Recheck=%v Len=%d LabelsTotal=%d LabelsInQueue=%d", s.Recheck(), s.Len(), s.LabelsTotal(), s.LabelsInQueue())
	}
	if s.OutPath() != f.labels || s.Filter() != FilterAll || s.Cursor() != 0 {
		t.Errorf("OutPath=%q Filter=%v Cursor=%d", s.OutPath(), s.Filter(), s.Cursor())
	}
	for k, i := range recheckSubsetRecords() {
		want := recheckLabel(t, i)
		if i%7 == 0 {
			want = Label{ID: recheckID(i), Status: StatusSkipped, Note: "ăn"}
		}
		if got, ok := s.Label(k); !ok || !labelEqual(got, want) {
			t.Errorf("Label(%d) = %+v, %v; want %+v", k, got, ok, want)
		}
	}

	// A normal session refuses the same canonical file: its ids are not all in the queue.
	if _, err := Open(f.queue, f.schema, f.labels); err == nil || !strings.Contains(err.Error(), "is not in the queue") {
		t.Errorf("Open err = %v, want an id not in the queue", err)
	}
}

func TestRecheckMarkRewritesOnlyEditedLine(t *testing.T) {
	f := newRecheckFixture(t)
	s := f.open(t)
	origLines := fileLines(t, f.original)

	// Re-marking a record with unchanged values keeps the file byte-identical, even a hand-formatted line.
	const same = 11 // record 69 (69%4 = 1: complete gamma)
	mustMark(t, s, same, StatusComplete)
	if got := f.read(t); !bytes.Equal(got, f.original) {
		t.Fatalf("unchanged mark rewrote the file:\n%s", got)
	}

	const k = 2 // record 15 (15%4 = 3: later beta)
	id := recheckID(recheckSubsetRecords()[k])
	mustSetType(t, s, k, "alpha")
	mustSetTarget(t, s, k, 7, 10)
	mustMark(t, s, k, StatusComplete)

	lines := fileLines(t, f.read(t))
	if len(lines) != recheckFull {
		t.Fatalf("file has %d lines, want %d", len(lines), recheckFull)
	}
	ids, origIDs := lineIDs(t, lines), lineIDs(t, origLines)
	changed := 0
	for n, line := range lines {
		if ids[n] != origIDs[n] {
			t.Fatalf("line %d: id %q, want %q (order changed)", n+1, ids[n], origIDs[n])
		}
		if line == origLines[n] {
			continue
		}
		changed++
		if ids[n] != id {
			t.Errorf("line %d (%s) changed:\n%s\nwas\n%s", n+1, ids[n], line, origLines[n])
		}
		want := `{"id":"r015","annotation_status":"complete","type":"alpha","target":{"text":"phở","start":7,"end":10}}`
		if line != want {
			t.Errorf("edited line = %s, want %s", line, want)
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want 1", changed)
	}
	if s.LabelsTotal() != recheckFull || s.LabelsInQueue() != recheckSubset {
		t.Errorf("LabelsTotal=%d LabelsInQueue=%d", s.LabelsTotal(), s.LabelsInQueue())
	}

	// Undo restores the original bytes of the line.
	for s.CanUndo() {
		if _, _, ok, err := s.Undo(); !ok || err != nil {
			t.Fatalf("Undo: %v, %v", ok, err)
		}
	}
	if got := f.read(t); !bytes.Equal(got, f.original) {
		t.Errorf("file after undo differs from the original:\n%s", got)
	}
}

func TestRecheckHandFormattedLineRewrittenAndRestored(t *testing.T) {
	f := newRecheckFixture(t)
	s := f.open(t)
	const k = 3 // record 21 (21%7 = 0: hand-formatted skipped line with an escaped note)
	mustSetType(t, s, k, "beta")
	mustMark(t, s, k, StatusSkipped)
	got := string(f.read(t))
	want := `{"id":"r021","annotation_status":"skipped","type":"beta","target":null,"note":"ăn"}` + "\n"
	if !strings.Contains(got, want) || strings.Count(got, `"r021"`) != 1 {
		t.Errorf("re-encoded line %q not found once in:\n%s", want, got)
	}
	if _, _, ok, err := s.Undo(); !ok || err != nil {
		t.Fatalf("Undo: %v, %v", ok, err)
	}
	if got := f.read(t); !bytes.Equal(got, f.original) {
		t.Errorf("file after undo differs from the original:\n%s", got)
	}
}

func TestRecheckNewLabelAppendsAndUndoRemoves(t *testing.T) {
	// Records 9 and 15 (subset indices 1 and 2) have no label.
	f := newRecheckFixture(t, 9, 15)
	s := f.open(t)
	if s.LabelsTotal() != recheckFull-2 || s.LabelsInQueue() != recheckSubset-2 {
		t.Fatalf("LabelsTotal=%d LabelsInQueue=%d", s.LabelsTotal(), s.LabelsInQueue())
	}
	if _, ok := s.Label(1); ok {
		t.Fatal("record 9 is labeled")
	}
	mustSetType(t, s, 2, "gamma")
	mustMark(t, s, 2, StatusComplete)
	mustMark(t, s, 1, StatusSkipped)

	origLines := fileLines(t, f.original)
	lines := fileLines(t, f.read(t))
	lineIDs(t, lines)
	if len(lines) != len(origLines)+2 {
		t.Fatalf("file has %d lines, want %d", len(lines), len(origLines)+2)
	}
	for n, line := range origLines {
		if lines[n] != line {
			t.Fatalf("line %d changed:\n%s\nwas\n%s", n+1, lines[n], line)
		}
	}
	appended := lines[len(origLines):]
	want := []string{
		`{"id":"r015","annotation_status":"complete","type":"gamma","target":null}`,
		`{"id":"r009","annotation_status":"skipped","type":null,"target":null}`,
	}
	if strings.Join(appended, "\n") != strings.Join(want, "\n") {
		t.Errorf("appended =\n%s\nwant\n%s", strings.Join(appended, "\n"), strings.Join(want, "\n"))
	}
	if s.LabelsTotal() != recheckFull || s.LabelsInQueue() != recheckSubset {
		t.Errorf("LabelsTotal=%d LabelsInQueue=%d", s.LabelsTotal(), s.LabelsInQueue())
	}

	for range 2 {
		if _, desc, ok, err := s.Undo(); !ok || err != nil || desc != "removed label" {
			t.Fatalf("Undo: %q, %v, %v", desc, ok, err)
		}
	}
	if got := f.read(t); !bytes.Equal(got, f.original) {
		t.Errorf("file after undo differs from the original:\n%s", got)
	}
	if s.LabelsTotal() != recheckFull-2 {
		t.Errorf("LabelsTotal = %d after undo", s.LabelsTotal())
	}
}

func TestRecheckRefusesExternalChange(t *testing.T) {
	f := newRecheckFixture(t)
	s := f.open(t)
	external := append(bytes.Clone(f.original), []byte(`{"id":"x999","annotation_status":"skipped","type":null,"target":null}`+"\n")...)
	if err := os.WriteFile(f.labels, external, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Label(0)
	mustSetType(t, s, 0, "alpha")
	err := s.Mark(0, StatusComplete)
	if err == nil || !strings.Contains(err.Error(), "changed by another program") {
		t.Fatalf("Mark err = %v, want refusal", err)
	}
	if got := f.read(t); !bytes.Equal(got, external) {
		t.Errorf("file was modified:\n%s", got)
	}
	if got, _ := s.Label(0); !labelEqual(got, before) || !s.Dirty(0) {
		t.Errorf("label not restored: %+v (dirty=%v)", got, s.Dirty(0))
	}
	if s.CanUndo() || s.LabelsTotal() != recheckFull {
		t.Errorf("CanUndo=%v LabelsTotal=%d", s.CanUndo(), s.LabelsTotal())
	}
}

func TestRecheckWriteFailureLeavesFileIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newRecheckFixture(t)
	s := f.open(t)
	if err := os.Chmod(f.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f.dir, 0o755) })

	before, _ := s.Label(4)
	mustSetType(t, s, 4, "beta")
	if err := s.Mark(4, StatusUncertain); err == nil {
		t.Fatal("Mark succeeded in a read-only directory")
	}
	if got := f.read(t); !bytes.Equal(got, f.original) {
		t.Error("labels file changed by a failed write")
	}
	if entries, _ := os.ReadDir(f.dir); len(entries) != 3 {
		t.Errorf("temp files left behind: %v", entries)
	}
	if got, _ := s.Label(4); !labelEqual(got, before) {
		t.Errorf("label not restored: %+v", got)
	}

	// The baseline is untouched, so the retry succeeds once the directory is writable.
	if err := os.Chmod(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustMark(t, s, 4, StatusUncertain)
	if lines := fileLines(t, f.read(t)); len(lines) != recheckFull {
		t.Errorf("file has %d lines after retry", len(lines))
	}
}

func TestOpenRecheckRejects(t *testing.T) {
	const good = `{"id":"z1","annotation_status":"skipped","type":null,"target":null}`
	tests := []struct {
		name, extra, want string
	}{
		{"duplicate outside queue", good + "\n\n" + good, `duplicate id "z1" (first on line 141)`},
		{"duplicate queue id", recheckLine(t, 3), `:141: duplicate id "r003" (first on line `},
		{"undeclared type outside queue", `{"id":"z1","annotation_status":"complete","type":"delta","target":null}`, `:141: id "z1": type: "delta" is not one of`},
		{"undeclared status outside queue", `{"id":"z1","annotation_status":"done","type":null,"target":null}`, `annotation_status: "done"`},
		{"complete without type outside queue", `{"id":"z1","annotation_status":"complete","type":null,"target":null}`, "type: required"},
		{"null-target type with target outside queue", `{"id":"z1","annotation_status":"complete","type":"gamma","target":{"text":"ab","start":0,"end":2}}`, `target: must be null for type "gamma"`},
		{"target length mismatch outside queue", `{"id":"z1","annotation_status":"complete","type":"alpha","target":{"text":"phở","start":0,"end":4}}`, "has 3 code points"},
		{"negative start outside queue", `{"id":"z1","annotation_status":"complete","type":"alpha","target":{"text":"ab","start":-2,"end":0}}`, "is negative"},
		{"unknown key outside queue", `{"id":"z1","annotation_status":"skipped","type":null,"target":null,"x":1}`, `unknown field(s) ["x"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRecheckFixture(t)
			if err := os.WriteFile(f.labels, append(bytes.Clone(f.original), tt.extra+"\n"...), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := OpenRecheck(f.queue, f.schema, f.labels)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), f.labels) {
				t.Fatalf("err = %v, want containing %q and the path", err, tt.want)
			}
		})
	}

	t.Run("invalid target of queue id", func(t *testing.T) {
		f := newRecheckFixture(t, 3)
		// Structurally sane but not the record text: only queue ids are checked against their text.
		line := `{"id":"r003","annotation_status":"complete","type":"alpha","target":{"text":"xyz","start":0,"end":3}}`
		if err := os.WriteFile(f.labels, append(bytes.Clone(f.original), line+"\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRecheck(f.queue, f.schema, f.labels); err == nil || !strings.Contains(err.Error(), `id "r003": target: text[0:3] is "Nam"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("missing labels file", func(t *testing.T) {
		f := newRecheckFixture(t)
		missing := filepath.Join(f.dir, "typo.jsonl")
		_, err := OpenRecheck(f.queue, f.schema, missing)
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err = %v, want missing file", err)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Errorf("missing labels file was created: %v", err)
		}
	})

	t.Run("labels file is the queue", func(t *testing.T) {
		f := newRecheckFixture(t)
		if _, err := OpenRecheck(f.queue, f.schema, f.queue); err == nil || !strings.Contains(err.Error(), "is the queue file") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRecheckAdvance(t *testing.T) {
	f := newRecheckFixture(t)
	s := f.open(t)
	if !s.Advance() || s.Cursor() != 1 {
		t.Fatalf("Advance from 0: cursor %d, want 1 (labeled records are visited)", s.Cursor())
	}
	s.SetFilter(FilterComplete)
	// Cursor 1 = record 9 (complete gamma) matches. Next complete (record%4 in {0,1}, not hand-formatted %7 == 0):
	// 15 later, 21 skipped, 27 later, 33 complete → index 5.
	if !s.Advance() || s.Cursor() != 5 {
		t.Errorf("Advance with complete filter: cursor %d, want 5", s.Cursor())
	}
	s.SetFilter(FilterAll)
	s.Last()
	if s.Advance() || s.Cursor() != recheckSubset-1 {
		t.Errorf("Advance at the end moved to %d", s.Cursor())
	}
}
