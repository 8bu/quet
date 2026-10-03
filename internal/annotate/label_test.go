package annotate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSpanTargetUnicodeOffsets(t *testing.T) {
	tests := []struct {
		text       string
		start, end int
		want       string
	}{
		{"ăn với Nam ở Pizza 4P 300k", 13, 21, "Pizza 4P"},
		{"cho thg Nam mượn 2 củ", 8, 11, "Nam"},
		{"cho Nam vay 500k", 4, 7, "Nam"},
		{"ck 5tr qua tk tiết kiệm", 14, 23, "tiết kiệm"},
		{"cho thg Nam mượn 2 củ", 12, 16, "mượn"},
		{"cho thg Nam mượn 2 củ", 19, 21, "củ"},
		{"a", 0, 1, "a"},
	}
	for _, tt := range tests {
		got, err := SpanTarget(tt.text, tt.start, tt.end)
		if err != nil {
			t.Fatalf("SpanTarget(%q,%d,%d): %v", tt.text, tt.start, tt.end, err)
		}
		if want := (Target{Text: tt.want, Start: tt.start, End: tt.end}); got != want {
			t.Errorf("SpanTarget(%q,%d,%d) = %+v, want %+v", tt.text, tt.start, tt.end, got, want)
		}
	}
	// Code-point offsets differ from byte offsets once non-ASCII precedes the span.
	if b := strings.Index("ăn với Nam ở Pizza 4P 300k", "Pizza 4P"); b != 18 {
		t.Fatalf("fixture: byte offset of Pizza 4P = %d, want 18 (NFC)", b)
	}
	if b := strings.Index("cho thg Nam mượn 2 củ", "củ"); b != 22 {
		t.Fatalf("fixture: byte offset of củ = %d, want 22 (NFC)", b)
	}
}

func TestSpanTargetRejects(t *testing.T) {
	const text = "ăn với Nam ở Pizza 4P 300k" // 26 runes
	tests := []struct {
		name       string
		start, end int
		want       string
	}{
		{"empty", 13, 13, "is empty"},
		{"reversed", 21, 13, "is empty"},
		{"negative start", -1, 3, "negative"},
		{"end beyond runes", 20, 27, "beyond the text length 26"},
		{"leading space", 12, 21, "whitespace"},
		{"trailing space", 13, 22, "whitespace"},
		{"only space", 12, 13, "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SpanTarget(text, tt.start, tt.end)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadQueue(t *testing.T) {
	items, err := LoadQueue("testdata/queue.jsonl")
	if err != nil {
		t.Fatalf("LoadQueue: %v", err)
	}
	if len(items) != 7 {
		t.Fatalf("len = %d, want 7 (blank line skipped)", len(items))
	}
	if items[1] != (Item{ID: "case-lend", Text: "cho Nam vay 500k"}) {
		t.Errorf("items[1] = %+v", items[1])
	}
	if items[6].ID != "baseline-01-efb9ecbae1cf" {
		t.Errorf("items[6] = %+v", items[6])
	}
}

func TestLoadQueueErrors(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{"invalid json", "{\"id\":\"a\",\"text\":\"x\"}\n{nope\n", ":2: invalid JSON"},
		{"missing id", "\n{\"text\":\"x\"}\n", `:2: missing field "id"`},
		{"missing text", "{\"id\":\"a\"}\n", `:1: missing field "text"`},
		{"numeric id", "{\"id\":1,\"text\":\"x\"}\n", ":1: id: expected a string"},
		{"null text", "{\"id\":\"a\",\"text\":null}\n", ":1: text: expected a string"},
		{"empty id", "{\"id\":\"\",\"text\":\"x\"}\n", ":1: id: expected a non-empty string"},
		{"duplicate id", "{\"id\":\"a\",\"text\":\"x\"}\n{\"id\":\"a\",\"text\":\"y\"}\n", `:2: duplicate id "a" (first on line 1)`},
		{"array line", "[1]\n", ":1: invalid JSON"},
		{"null line", "null\n", ":1: invalid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "queue.jsonl", tt.content)
			_, err := LoadQueue(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// writeFile writes content to <tempdir>/name and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// tgt returns the Spans of a label of the implicit schema (one span, target) holding t (nil = null).
func tgt(t *Target) map[string]*Target { return map[string]*Target{"target": t} }

// labelQueue is a small queue for label loading tests.
var labelQueue = []Item{
	{ID: "a", Text: "cho thg Nam mượn 2 củ"},
	{ID: "b", Text: "ck 5tr qua tk tiết kiệm"},
	{ID: "c", Text: "Nam gửi tao 500k"},
}

func TestLoadLabelsValid(t *testing.T) {
	s := loadGidiSchema(t)
	path := writeFile(t, "labels.jsonl", strings.Join([]string{
		`{"id":"c","annotation_status":"uncertain","type":null,"target":null,"note":"ambiguous"}`,
		``,
		`{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11}}`,
	}, "\n"))
	got, err := LoadLabels(path, s, labelQueue)
	if err != nil {
		t.Fatalf("LoadLabels: %v", err)
	}
	want := map[string]Label{
		"a": {ID: "a", Status: "complete", Type: new("lend"), Spans: tgt(&Target{Text: "Nam", Start: 8, End: 11})},
		"c": {ID: "c", Status: "uncertain", Spans: tgt(nil), Note: "ambiguous"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadLabelsMissingFile(t *testing.T) {
	got, err := LoadLabels(filepath.Join(t.TempDir(), "none.jsonl"), loadGidiSchema(t), labelQueue)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

func TestLoadLabelsRejects(t *testing.T) {
	const ok = `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11}}`
	tests := []struct {
		name, content, want string
	}{
		{"duplicate id", ok + "\n" + ok + "\n", `:2: duplicate id "a" (first on line 1)`},
		{"unknown id", "\n" + `{"id":"zz","annotation_status":"skipped","type":null,"target":null}`, `:2: id "zz" is not in the queue`},
		{"unknown key", `{"id":"a","annotation_status":"skipped","type":null,"target":null,"extra":1}`, `:1: unknown field(s) ["extra"]`},
		{"unknown target key", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11,"x":0}}`, `:1: target: unknown field(s) ["x"]`},
		{"missing target key", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8}}`, `:1: target: missing field "end"`},
		{"missing type", `{"id":"a","annotation_status":"skipped","target":null}`, `:1: missing field "type"`},
		{"invalid json", `{"id":"a",`, ":1: invalid JSON"},
		{"byte offsets span", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"mượn","start":12,"end":20}}`, ":1: id \"a\": target:"},
		{"padded span", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":" Nam","start":7,"end":11}}`, "leading or trailing whitespace"},
		{"empty span", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"","start":8,"end":8}}`, "target.text: expected a non-empty string"},
		{"out of range span", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":30,"end":33}}`, "beyond the text length"},
		{"fractional offset", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8.5,"end":11}}`, "target.start: expected an integer"},
		{"null offset", `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":null}}`, "target.end: expected an integer"},
		{"target not object", `{"id":"a","annotation_status":"complete","type":"lend","target":"Nam"}`, "target: expected an object or null"},
		{"numeric type", `{"id":"a","annotation_status":"complete","type":3,"target":null}`, "type: expected a string or null"},
		{"null status", `{"id":"a","annotation_status":null,"type":null,"target":null}`, "annotation_status: expected a string"},
		{"note not string", `{"id":"a","annotation_status":"skipped","type":null,"target":null,"note":1}`, "note: expected a string"},
		{"unknown status", `{"id":"a","annotation_status":"approved","type":null,"target":null}`, `annotation_status: "approved"`},
		{"complete without type", `{"id":"a","annotation_status":"complete","type":null,"target":null}`, "type: required"},
		{"transfer with target", `{"id":"b","annotation_status":"complete","type":"transfer","target":{"text":"tk","start":11,"end":13}}`, `target: must be null for type "transfer"`},
	}
	s := loadGidiSchema(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "labels.jsonl", tt.content)
			_, err := LoadLabels(path, s, labelQueue)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("err %q does not cite the file", err)
			}
		})
	}
}

func TestWriteLabelsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "labels.jsonl")
	labels := map[string]Label{
		"c": {ID: "c", Status: "uncertain", Spans: tgt(nil), Note: "<ambiguous & odd>"},
		"a": {ID: "a", Status: "complete", Type: new("lend"), Spans: tgt(&Target{Text: "Nam", Start: 8, End: 11})},
		"b": {ID: "b", Status: "complete", Type: new("transfer"), Spans: tgt(nil)},
	}
	if err := WriteLabels(path, loadGidiSchema(t), labelQueue, labels); err != nil {
		t.Fatalf("WriteLabels: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11}}
{"id":"b","annotation_status":"complete","type":"transfer","target":null}
{"id":"c","annotation_status":"uncertain","type":null,"target":null,"note":"<ambiguous & odd>"}
`
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
	got, err := LoadLabels(path, loadGidiSchema(t), labelQueue)
	if err != nil {
		t.Fatalf("LoadLabels: %v", err)
	}
	if !reflect.DeepEqual(got, labels) {
		t.Errorf("round trip = %+v, want %+v", got, labels)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestWriteLabelsNonASCIIUnescaped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	queue := []Item{{ID: "x", Text: "ăn với Nam ở Pizza 4P 300k"}}
	labels := map[string]Label{"x": {ID: "x", Status: "complete", Type: new("expense"),
		Spans: tgt(&Target{Text: "Pizza 4P", Start: 13, End: 21}), Note: "với <Nam> & bạn"}}
	if err := WriteLabels(path, loadGidiSchema(t), queue, labels); err != nil {
		t.Fatalf("WriteLabels: %v", err)
	}
	data, _ := os.ReadFile(path)
	want := `{"id":"x","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21},"note":"với <Nam> & bạn"}` + "\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestWriteLabelsRejectsStrayIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	err := WriteLabels(path, loadGidiSchema(t), labelQueue, map[string]Label{"zz": {ID: "zz", Status: "skipped"}})
	if err == nil || !strings.Contains(err.Error(), `"zz"`) {
		t.Fatalf("err = %v, want stray id error", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file written despite error: %v", err)
	}
}
