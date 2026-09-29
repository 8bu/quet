// Package corpus loads text corpora (.jsonl, .json, .txt) without ever modifying the source.
package corpus

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Field is one imported metadata key/value (value rendered as compact JSON or plain string for display/search).
type Field struct {
	Key   string
	Value string
}

// Record is one imported corpus entry. Immutable after Load.
type Record struct {
	ID             string          // stable: explicit "id" field value ("id:<v>") if present, else "h:<sha1 hex of text>" with "#<n>" suffix for the n-th (n>=2) identical text
	Index          int             // 0-based position in source
	Line           int             // 1-based source line (jsonl/txt); 0 for json
	Text           string          // original imported text ("" if text field missing)
	Raw            json.RawMessage // original JSON object/value bytes exactly as imported; nil for .txt
	Fields         []Field         // metadata excluding the text field, in source key order
	Source         string          // meta "source" if string
	Batch          string          // meta "batch" if string
	SuggestedFlags []string        // meta "suggested_flags" (array of strings, or comma-separated string)
}

// Corpus is a loaded corpus.
type Corpus struct {
	Path      string
	Format    string // "jsonl" | "json" | "txt"
	TextField string // detected text key for object records ("text" default)
	Records   []Record
}

// textKeys are the recognized text field names, in detection priority order.
var textKeys = []string{"text", "content", "note", "input", "prompt", "sentence", "body"}

// recordsKeys are the keys a top-level JSON object may wrap its record array in.
var recordsKeys = []string{"records", "data", "items", "corpus"}

// Load reads path; format is chosen by extension. Malformed input returns an error with line/position (never silently drops data).
func Load(path string) (*Corpus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("corpus: read %s: %w", path, err)
	}
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".jsonl":
		return loadJSONL(path, data)
	case ".json":
		return loadJSON(path, data)
	case ".txt":
		return loadTXT(path, data)
	default:
		return nil, fmt.Errorf("corpus: %s: unsupported extension %q (supported formats: .jsonl, .json, .txt)", path, ext)
	}
}

// Supported reports whether path has an extension Load understands
// (.jsonl, .json or .txt, case-insensitive).
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl", ".json", ".txt":
		return true
	}
	return false
}

// Usable reports why a loaded corpus is not worth reviewing: it has no
// records, or no record carries any text. nil means it is usable.
func (c *Corpus) Usable() error {
	if len(c.Records) == 0 {
		return errors.New("no records found")
	}
	for i := range c.Records {
		if strings.TrimSpace(c.Records[i].Text) != "" {
			return nil
		}
	}
	if c.Format == "txt" {
		return errors.New("every line is blank")
	}
	return fmt.Errorf("no record has text (looked for a %s field)", strings.Join(textKeys, ", "))
}

// loader accumulates records while keeping IDs unique.
type loader struct {
	corpus    *Corpus
	used      map[string]int
	textField string // first text key detected in source order
}

func newLoader(path, format string) *loader {
	return &loader{
		corpus: &Corpus{Path: path, Format: format},
		used:   map[string]int{},
	}
}

// finish sets the corpus-wide text field and returns the corpus.
func (l *loader) finish() *Corpus {
	if l.textField != "" {
		l.corpus.TextField = l.textField
	} else {
		l.corpus.TextField = "text"
	}
	return l.corpus
}

func loadJSONL(path string, data []byte) (*Corpus, error) {
	l := newLoader(path, "jsonl")
	for i, rawLine := range bytes.Split(data, []byte("\n")) {
		lineNo := i + 1
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		v, err := decodeValue(line)
		if err != nil {
			return nil, fmt.Errorf("corpus: %s:%d: malformed JSON: %v (line: %s)", path, lineNo, err, snippet(line))
		}
		l.add(v, json.RawMessage(bytes.Clone(line)), lineNo)
	}
	return l.finish(), nil
}

func loadJSON(path string, data []byte) (*Corpus, error) {
	root, err := decodeValue(data)
	if err != nil {
		return nil, fmt.Errorf("corpus: %s: malformed JSON: %v", path, err)
	}
	var elems []*value
	switch root.kind {
	case kindArray:
		elems = root.arr
	case kindObject:
		elems = recordsArray(root)
		if elems == nil {
			return nil, fmt.Errorf("corpus: %s: JSON object has no records array (looked for %q, %q, %q, %q)", path, recordsKeys[0], recordsKeys[1], recordsKeys[2], recordsKeys[3])
		}
	default:
		return nil, fmt.Errorf("corpus: %s: top-level JSON must be an array or an object wrapping a records array", path)
	}
	l := newLoader(path, "json")
	for _, e := range elems {
		raw, err := e.raw()
		if err != nil {
			return nil, fmt.Errorf("corpus: %s: %w", path, err)
		}
		l.add(e, raw, 0)
	}
	return l.finish(), nil
}

func loadTXT(path string, data []byte) (*Corpus, error) {
	l := newLoader(path, "txt")
	for i, rawLine := range bytes.Split(data, []byte("\n")) {
		line := strings.TrimSuffix(string(rawLine), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		l.addText(line, i+1)
	}
	return l.finish(), nil
}

// recordsArray returns the record elements of a wrapper object, or nil when it has none.
func recordsArray(obj *value) []*value {
	for _, key := range recordsKeys {
		if m := obj.lookup(key); m != nil && m.kind == kindArray {
			return m.arr
		}
	}
	return nil
}

// add appends one record decoded from v. raw is the record's original JSON bytes; line is its source line (0 for .json).
func (l *loader) add(v *value, raw json.RawMessage, line int) {
	rec := Record{
		Index: len(l.corpus.Records),
		Line:  line,
		Raw:   raw,
	}
	if v.kind != kindObject {
		rec.Text = scalarText(v)
		l.append(rec)
		return
	}
	if i := v.keyIndex("id"); i >= 0 {
		if s, ok := stringOrNumber(v.obj[i].val); ok && s != "" {
			rec.ID = "id:" + s
		}
	}
	textIdx := -1
	for _, key := range textKeys {
		if i := v.keyIndex(key); i >= 0 {
			textIdx = i
			rec.Text = scalarText(v.obj[i].val)
			if l.textField == "" {
				l.textField = v.obj[i].key
			}
			break
		}
	}
	for i, m := range v.obj {
		if i == textIdx {
			continue
		}
		rec.Fields = append(rec.Fields, Field{Key: m.key, Value: fieldValue(m.val)})
	}
	rec.Source = metaString(v, "source")
	rec.Batch = metaString(v, "batch")
	rec.SuggestedFlags = metaFlags(v)
	l.append(rec)
}

// addText appends a plain-text record (no metadata, no raw JSON).
func (l *loader) addText(text string, line int) {
	l.append(Record{
		Index: len(l.corpus.Records),
		Line:  line,
		Text:  text,
	})
}

// append assigns the record's stable ID and stores it.
func (l *loader) append(rec Record) {
	base := rec.ID
	if base == "" {
		base = "h:" + hashText(rec.Text)
	}
	seen := l.used[base]
	l.used[base] = seen + 1
	if seen > 0 {
		rec.ID = fmt.Sprintf("%s#%d", base, seen+1)
	} else {
		rec.ID = base
	}
	l.corpus.Records = append(l.corpus.Records, rec)
}

// hashText is the lowercase hex sha1 of text.
func hashText(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])
}

// fieldValue renders one metadata value: plain text for JSON strings, compact JSON otherwise.
func fieldValue(v *value) string {
	if s, ok := v.scalar.(string); ok && v.kind == kindScalar {
		return s
	}
	s, err := v.compact()
	if err != nil {
		return ""
	}
	return s
}

// metaString returns the value of the first of keys present as a JSON string (case-insensitive), else "".
func metaString(v *value, key string) string {
	m := v.lookup(key)
	if m == nil {
		return ""
	}
	if s, ok := m.scalar.(string); ok {
		return s
	}
	return ""
}

// metaFlags reads suggested flags from meta "suggested_flags"/"suggestedFlags": an array of strings,
// or a comma-separated string (entries trimmed, empties dropped).
func metaFlags(v *value) []string {
	m := v.lookupAny("suggested_flags", "suggestedFlags")
	if m == nil {
		return nil
	}
	switch m.kind {
	case kindArray:
		var flags []string
		for _, e := range m.arr {
			if s, ok := e.scalar.(string); ok && strings.TrimSpace(s) != "" {
				flags = append(flags, strings.TrimSpace(s))
			}
		}
		return flags
	case kindScalar:
		s, ok := m.scalar.(string)
		if !ok {
			return nil
		}
		var flags []string
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				flags = append(flags, part)
			}
		}
		return flags
	}
	return nil
}

// snippet trims b to a short, rune-safe one-line preview for error messages.
func snippet(b []byte) string {
	const maxRunes = 80
	s := string(b)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// decodeValue decodes exactly one JSON value, preserving object key order and number literals.
func decodeValue(raw []byte) (*value, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("unexpected data after JSON value")
	}
	return v, nil
}
