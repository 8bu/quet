package corpus

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func hashOf(text string) string {
	sum := sha1.Sum([]byte(text))
	return "h:" + hex.EncodeToString(sum[:])
}

func load(t *testing.T, name, content string) *Corpus {
	t.Helper()
	c, err := Load(writeFile(t, name, content))
	if err != nil {
		t.Fatalf("Load(%s): %v", name, err)
	}
	return c
}

func TestLoadJSONL(t *testing.T) {
	const src = `{"id": "n1", "text": "bắn thg Nam 2 củ", "source": "claude", "batch": "b-1", "created_at": "2026-01-02T01:00:00Z", "lang": "vi", "suggested_flags": ["slang"]}

{"text": "CK Nam 2tr", "suggested_flags": "duplicate, slang ,,", "nested": {"a": 1, "b": [1, 2]}, "count": 3}
`
	path := filepath.Join(t.TempDir(), "corpus.jsonl")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Path != path || c.Format != "jsonl" || c.TextField != "text" {
		t.Errorf("corpus header = %q/%q/%q, want %q/jsonl/text", c.Path, c.Format, c.TextField, path)
	}
	if len(c.Records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(c.Records))
	}

	want0 := Record{
		ID:    "id:n1",
		Index: 0,
		Line:  1,
		Text:  "bắn thg Nam 2 củ",
		Raw:   json.RawMessage(`{"id": "n1", "text": "bắn thg Nam 2 củ", "source": "claude", "batch": "b-1", "created_at": "2026-01-02T01:00:00Z", "lang": "vi", "suggested_flags": ["slang"]}`),
		Fields: []Field{
			{Key: "id", Value: "n1"},
			{Key: "source", Value: "claude"},
			{Key: "batch", Value: "b-1"},
			{Key: "created_at", Value: "2026-01-02T01:00:00Z"},
			{Key: "lang", Value: "vi"},
			{Key: "suggested_flags", Value: `["slang"]`},
		},
		Source:         "claude",
		Batch:          "b-1",
		SuggestedFlags: []string{"slang"},
	}
	if !reflect.DeepEqual(c.Records[0], want0) {
		t.Errorf("record 0 = %+v\nwant %+v", c.Records[0], want0)
	}

	want1 := Record{
		ID:    hashOf("CK Nam 2tr"),
		Index: 1,
		Line:  3,
		Text:  "CK Nam 2tr",
		Raw:   json.RawMessage(`{"text": "CK Nam 2tr", "suggested_flags": "duplicate, slang ,,", "nested": {"a": 1, "b": [1, 2]}, "count": 3}`),
		Fields: []Field{
			{Key: "suggested_flags", Value: "duplicate, slang ,,"},
			{Key: "nested", Value: `{"a":1,"b":[1,2]}`},
			{Key: "count", Value: "3"},
		},
		SuggestedFlags: []string{"duplicate", "slang"},
	}
	if !reflect.DeepEqual(c.Records[1], want1) {
		t.Errorf("record 1 = %+v\nwant %+v", c.Records[1], want1)
	}
}

func TestLoadJSONLMalformedLine(t *testing.T) {
	const src = "{\"text\":\"ok\"}\n\n{\"text\": }\n{\"text\":\"never\"}\n"
	path := writeFile(t, "corpus.jsonl", src)
	c, err := Load(path)
	if err == nil {
		t.Fatalf("Load: want error, got %d records", len(c.Records))
	}
	msg := err.Error()
	if !strings.Contains(msg, ":3:") {
		t.Errorf("error %q does not name line 3", msg)
	}
	if !strings.Contains(msg, `{"text": }`) {
		t.Errorf("error %q does not include the line snippet", msg)
	}
}

func TestLoadJSONLTrailingGarbage(t *testing.T) {
	_, err := Load(writeFile(t, "corpus.jsonl", "{\"text\":\"a\"} {}\n"))
	if err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Fatalf("Load: got %v, want trailing-data error naming line 1", err)
	}
}

func TestLoadJSONForms(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"array", `[{"text": "a", "n": 1}, {"id": 2, "body": "b"}]`},
		{"records", `{"corpus": "x", "records": [{"text": "a", "n": 1}, {"id": 2, "body": "b"}]}`},
		{"data", `{"data": [{"text": "a", "n": 1}, {"id": 2, "body": "b"}]}`},
		{"items", `{"items": [{"text": "a", "n": 1}, {"id": 2, "body": "b"}]}`},
		{"corpus", `{"corpus": [{"text": "a", "n": 1}, {"id": 2, "body": "b"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := load(t, "corpus.json", tt.src)
			if c.Format != "json" || c.TextField != "text" {
				t.Errorf("format/textfield = %q/%q, want json/text", c.Format, c.TextField)
			}
			if len(c.Records) != 2 {
				t.Fatalf("len(records) = %d, want 2", len(c.Records))
			}
			if got := c.Records[0]; got.ID != hashOf("a") || got.Line != 0 || got.Index != 0 || got.Text != "a" ||
				string(got.Raw) != `{"text":"a","n":1}` || !reflect.DeepEqual(got.Fields, []Field{{Key: "n", Value: "1"}}) {
				t.Errorf("record 0 = %+v", got)
			}
			if got := c.Records[1]; got.ID != "id:2" || got.Line != 0 || got.Index != 1 || got.Text != "b" ||
				string(got.Raw) != `{"id":2,"body":"b"}` || !reflect.DeepEqual(got.Fields, []Field{{Key: "id", Value: "2"}}) {
				t.Errorf("record 1 = %+v", got)
			}
		})
	}
}

func TestLoadJSONErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"no records array", `{"corpus": "x"}`, "no records array"},
		{"root string", `"hello"`, "top-level JSON"},
		{"root number", `42`, "top-level JSON"},
		{"malformed", `[{"text": }]`, "malformed JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, "corpus.json", tt.src))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load: got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadJSONEmptyArray(t *testing.T) {
	c := load(t, "corpus.json", "[]")
	if len(c.Records) != 0 || c.Format != "json" || c.TextField != "text" {
		t.Fatalf("corpus = %+v, want empty json corpus", c)
	}
}

func TestLoadTXT(t *testing.T) {
	const src = "bắn thg Nam 2 củ\r\n\n  CK Nam 2tr  \nnested \"quotes\" and \\slashes\n"
	c := load(t, "corpus.txt", src)
	if c.Format != "txt" || c.TextField != "text" {
		t.Errorf("format/textfield = %q/%q, want txt/text", c.Format, c.TextField)
	}
	want := []Record{
		{ID: hashOf("bắn thg Nam 2 củ"), Index: 0, Line: 1, Text: "bắn thg Nam 2 củ"},
		{ID: hashOf("  CK Nam 2tr  "), Index: 1, Line: 3, Text: "  CK Nam 2tr  "},
		{ID: hashOf(`nested "quotes" and \slashes`), Index: 2, Line: 4, Text: `nested "quotes" and \slashes`},
	}
	if !reflect.DeepEqual(c.Records, want) {
		t.Errorf("records = %+v\nwant %+v", c.Records, want)
	}
}

func TestLoadUnknownExtension(t *testing.T) {
	_, err := Load(writeFile(t, "corpus.csv", "a,b\n"))
	if err == nil || !strings.Contains(err.Error(), ".jsonl, .json, .txt") {
		t.Fatalf("Load: got %v, want unsupported-format error", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("Load: want error for missing file")
	}
}

func TestTextExtraction(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		wantText  string
		wantField string
		wantKeys  []string
	}{
		{"text", `{"text": "a", "k": 1}`, "a", "text", []string{"k"}},
		{"content", `{"content": "b"}`, "b", "content", nil},
		{"note", `{"note": "c"}`, "c", "note", nil},
		{"input", `{"input": "d"}`, "d", "input", nil},
		{"prompt", `{"prompt": "e"}`, "e", "prompt", nil},
		{"sentence", `{"sentence": "f"}`, "f", "sentence", nil},
		{"body", `{"body": "g"}`, "g", "body", nil},
		{"case-insensitive", `{"TEXT": "h"}`, "h", "TEXT", nil},
		{"priority", `{"body": "g", "content": "b"}`, "b", "content", []string{"body"}},
		{"missing", `{"id": "x", "source": "claude"}`, "", "text", []string{"id", "source"}},
		{"non-string text", `{"text": 42}`, "42", "text", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := load(t, "corpus.jsonl", tt.src+"\n")
			if len(c.Records) != 1 {
				t.Fatalf("len(records) = %d, want 1", len(c.Records))
			}
			if c.TextField != tt.wantField {
				t.Errorf("TextField = %q, want %q", c.TextField, tt.wantField)
			}
			rec := c.Records[0]
			if rec.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", rec.Text, tt.wantText)
			}
			var keys []string
			for _, f := range rec.Fields {
				keys = append(keys, f.Key)
			}
			if !reflect.DeepEqual(keys, tt.wantKeys) {
				t.Errorf("field keys = %v, want %v", keys, tt.wantKeys)
			}
		})
	}
}

func TestNonObjectRecords(t *testing.T) {
	c := load(t, "corpus.jsonl", "\"just text\"\n42\n1.5\n")
	want := []Record{
		{ID: hashOf("just text"), Index: 0, Line: 1, Text: "just text", Raw: json.RawMessage(`"just text"`)},
		{ID: hashOf("42"), Index: 1, Line: 2, Text: "42", Raw: json.RawMessage(`42`)},
		{ID: hashOf("1.5"), Index: 2, Line: 3, Text: "1.5", Raw: json.RawMessage(`1.5`)},
	}
	if !reflect.DeepEqual(c.Records, want) {
		t.Errorf("records = %+v\nwant %+v", c.Records, want)
	}
}

func TestStableIDs(t *testing.T) {
	const src = `{"text": "dup"}
{"text": "dup"}
{"id": "dup-id", "text": "dup"}
{"id": "dup-id", "text": "dup"}
{"id": 7, "text": "seven"}
{"id": "", "text": "empty id"}
{"text": ""}
{"text": ""}
`
	c := load(t, "corpus.jsonl", src)
	want := []string{
		hashOf("dup"),
		hashOf("dup") + "#2",
		"id:dup-id",
		"id:dup-id#2",
		"id:7",
		hashOf("empty id"),
		hashOf(""),
		hashOf("") + "#2",
	}
	var got []string
	seen := map[string]bool{}
	for _, r := range c.Records {
		got = append(got, r.ID)
		if seen[r.ID] {
			t.Errorf("duplicate id %q", r.ID)
		}
		seen[r.ID] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v\nwant %v", got, want)
	}

	// IDs must not depend on records being distinct runs of the file: reloading the same content is stable.
	again := load(t, "corpus.jsonl", src)
	for i := range c.Records {
		if c.Records[i].ID != again.Records[i].ID {
			t.Fatalf("record %d id changed between loads: %q != %q", i, c.Records[i].ID, again.Records[i].ID)
		}
	}
}

func TestSuggestedFlags(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"array", `{"text": "a", "suggested_flags": ["slang", "typo"]}`, []string{"slang", "typo"}},
		{"array with empties", `{"text": "a", "suggested_flags": [" slang ", "", 3]}`, []string{"slang"}},
		{"comma string", `{"text": "a", "suggested_flags": " slang , typo ,, "}`, []string{"slang", "typo"}},
		{"camelCase", `{"text": "a", "suggestedFlags": ["slang"]}`, []string{"slang"}},
		{"absent", `{"text": "a"}`, nil},
		{"wrong type", `{"text": "a", "suggested_flags": 3}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := load(t, "corpus.jsonl", tt.src+"\n")
			if got := c.Records[0].SuggestedFlags; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SuggestedFlags = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadExamplesUnmodified(t *testing.T) {
	tests := []struct {
		path       string
		format     string
		records    int
		wantSource string
	}{
		{"../../examples/corpus.jsonl", "jsonl", 21, "claude"},
		{"../../examples/corpus.json", "json", 5, "claude"},
		{"../../examples/corpus.txt", "txt", 14, ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			before, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := Load(tt.path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			after, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("Load modified the source corpus")
			}
			if c.Format != tt.format {
				t.Errorf("format = %q, want %q", c.Format, tt.format)
			}
			if len(c.Records) != tt.records {
				t.Fatalf("len(records) = %d, want %d", len(c.Records), tt.records)
			}
			seen := map[string]bool{}
			for i, r := range c.Records {
				if seen[r.ID] {
					t.Errorf("record %d has duplicate id %q", i, r.ID)
				}
				seen[r.ID] = true
				if r.Index != i {
					t.Errorf("record %d index = %d", i, r.Index)
				}
			}
			if tt.wantSource != "" && c.Records[0].Source != tt.wantSource {
				t.Errorf("source = %q, want %q", c.Records[0].Source, tt.wantSource)
			}
		})
	}
}
