package corpus

import (
	"encoding/json"
	"testing"
)

func TestJSONWith(t *testing.T) {
	tests := []struct {
		name      string
		record    Record
		textField string
		text      string
		extra     []KV
		want      string
	}{
		{
			name:      "replace text and extras in place, preserve order and literal unicode",
			record:    Record{Raw: json.RawMessage(`{"id": "x", "text": "old", "source": "claude", "meta": {"a": 1, "b": [true, null]}}`)},
			textField: "text",
			text:      `mới "quoted" <b>&`,
			extra: []KV{
				{Key: "review_status", Value: "approved"},
				{Key: "source", Value: "human"},
				{Key: "edited", Value: true},
				{Key: "n", Value: 3},
				{Key: "tags", Value: []string{"a", "b"}},
			},
			want: `{"id":"x","text":"mới \"quoted\" <b>&","source":"human","meta":{"a":1,"b":[true,null]},"review_status":"approved","edited":true,"n":3,"tags":["a","b"]}`,
		},
		{
			name:      "absent text field is appended",
			record:    Record{Raw: json.RawMessage(`{"text":"a","n":1}`)},
			textField: "status",
			text:      "b",
			want:      `{"text":"a","n":1,"status":"b"}`,
		},
		{
			name:      "extra replaces member and text appended",
			record:    Record{Raw: json.RawMessage(`{"a":1}`)},
			textField: "text",
			text:      "hi",
			extra:     []KV{{Key: "a", Value: "z"}},
			want:      `{"a":"z","text":"hi"}`,
		},
		{
			name:      "txt record has no raw json",
			record:    Record{},
			textField: "text",
			text:      "xin chào",
			extra:     []KV{{Key: "status", Value: "approved"}},
			want:      `{"text":"xin chào","status":"approved"}`,
		},
		{
			name:      "non-object raw json",
			record:    Record{Raw: json.RawMessage(`"just a string"`)},
			textField: "text",
			text:      "now an object",
			want:      `{"text":"now an object"}`,
		},
		{
			name:      "array raw json",
			record:    Record{Raw: json.RawMessage(`[1,2]`)},
			textField: "text",
			text:      "x",
			want:      `{"text":"x"}`,
		},
		{
			name:      "number literals and empty objects survive",
			record:    Record{Raw: json.RawMessage(`{"id": 7, "text": "t", "obj": {}}`)},
			textField: "text",
			text:      "new",
			want:      `{"id":7,"text":"new","obj":{}}`,
		},
		{
			name:      "control characters are escaped onto one line",
			record:    Record{Raw: json.RawMessage(`{"text":"old"}`)},
			textField: "text",
			text:      "dòng\nmới\ttab",
			want:      `{"text":"dòng\nmới\ttab"}`,
		},
		{
			name:      "extra values of any json type",
			record:    Record{Raw: json.RawMessage(`{"text":"t"}`)},
			textField: "text",
			text:      "t2",
			extra:     []KV{{Key: "nil", Value: nil}, {Key: "obj", Value: map[string]int{"k": 1}}},
			want:      `{"text":"t2","nil":null,"obj":{"k":1}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.record.JSONWith(tt.textField, tt.text, tt.extra)
			if err != nil {
				t.Fatalf("JSONWith: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("JSONWith =\n%s\nwant\n%s", got, tt.want)
			}
			if !json.Valid(got) {
				t.Errorf("JSONWith produced invalid JSON: %s", got)
			}
		})
	}
}

func TestJSONWithOrderPreservedAcrossNestedObjects(t *testing.T) {
	rec := Record{Raw: json.RawMessage(`{"z": {"b": 1, "a": 2}, "text": "x", "y": [{"k": 1, "j": 2}]}`)}
	got, err := rec.JSONWith("text", "x2", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":{"b":1,"a":2},"text":"x2","y":[{"k":1,"j":2}]}`
	if string(got) != want {
		t.Errorf("JSONWith =\n%s\nwant\n%s", got, want)
	}
}

func TestJSONWithInvalidRaw(t *testing.T) {
	rec := Record{ID: "id:bad", Raw: json.RawMessage(`{"text": `)}
	if _, err := rec.JSONWith("text", "x", nil); err == nil {
		t.Fatal("JSONWith: want error for malformed raw JSON")
	}
}

func TestJSONWithRoundTripsThroughLoad(t *testing.T) {
	c := load(t, "corpus.jsonl", `{"id": "n1", "text": "bắn thg Nam 2 củ", "source": "claude"}`+"\n")
	rec := c.Records[0]
	got, err := rec.JSONWith(c.TextField, "bắn thẳng Nam 2 củ", []KV{{Key: "review_status", Value: "approved"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"n1","text":"bắn thẳng Nam 2 củ","source":"claude","review_status":"approved"}`
	if string(got) != want {
		t.Errorf("JSONWith =\n%s\nwant\n%s", got, want)
	}
}

func TestCompactFieldRendering(t *testing.T) {
	tests := []struct {
		src  string
		want string
	}{
		{`"plain"`, `"plain"`},
		{`42`, "42"},
		{`1.50`, "1.50"},
		{`{"b": 1, "a": 2}`, `{"b":1,"a":2}`},
		{`[1, "x"]`, `[1,"x"]`},
		{`null`, "null"},
		{`true`, "true"},
	}
	for _, tt := range tests {
		v, err := decodeValue([]byte(tt.src))
		if err != nil {
			t.Fatalf("decodeValue(%s): %v", tt.src, err)
		}
		got, err := v.compact()
		if err != nil {
			t.Fatalf("compact(%s): %v", tt.src, err)
		}
		if got != tt.want {
			t.Errorf("compact(%s) = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestDecodeValueRejectsTrailingData(t *testing.T) {
	for _, src := range []string{`{"a":1} {}`, `1 2`, `[1] x`} {
		if _, err := decodeValue([]byte(src)); err == nil {
			t.Errorf("decodeValue(%s): want error", src)
		}
	}
	for _, src := range []string{"{}\n", "[]", `{"a":1}` + "\n"} {
		if _, err := decodeValue([]byte(src)); err != nil {
			t.Errorf("decodeValue(%s): unexpected error %v", src, err)
		}
	}
}
