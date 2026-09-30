package annotate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Target is a span of the record text. Offsets are Unicode code points ([]rune indices).
type Target struct {
	Text  string `json:"text"`
	Start int    `json:"start"` // code points, inclusive
	End   int    `json:"end"`   // code points, exclusive
}

// Label is one persisted annotation. Type and Target serialize as null when nil; Note only when non-empty.
type Label struct {
	ID     string  `json:"id"`
	Status string  `json:"annotation_status"`
	Type   *string `json:"type"`
	Target *Target `json:"target"`
	Note   string  `json:"note,omitempty"`
}

// labelKeys and targetKeys are the only JSON members accepted when loading labels.
var (
	labelKeys  = map[string]bool{"id": true, "annotation_status": true, "type": true, "target": true, "note": true}
	targetKeys = map[string]bool{"text": true, "start": true, "end": true}
)

// SpanTarget returns the target for runes [start,end) of text, validated (bounds, non-empty, not whitespace-padded).
func SpanTarget(text string, start, end int) (Target, error) {
	runes := []rune(text)
	if err := checkSpan(len(runes), start, end); err != nil {
		return Target{}, err
	}
	span := runes[start:end]
	if padded(span) {
		return Target{}, fmt.Errorf("span %q has leading or trailing whitespace", string(span))
	}
	return Target{Text: string(span), Start: start, End: end}, nil
}

// checkSpan validates rune offsets [start,end) against a text of n runes.
func checkSpan(n, start, end int) error {
	switch {
	case start < 0:
		return fmt.Errorf("span start %d is negative", start)
	case end <= start:
		return fmt.Errorf("span [%d,%d) is empty", start, end)
	case end > n:
		return fmt.Errorf("span end %d is beyond the text length %d", end, n)
	}
	return nil
}

// padded reports whether the non-empty span starts or ends with whitespace.
func padded(span []rune) bool {
	return unicode.IsSpace(span[0]) || unicode.IsSpace(span[len(span)-1])
}

// Validate checks l against the schema and the record text. It returns nil or one error listing every problem
// ("; "-joined): undeclared status, missing type on a complete label, undeclared type, invalid target span, and a
// target on a null-target type.
func (s *Schema) Validate(l Label, text string) error {
	var problems []string
	if !s.HasStatus(l.Status) {
		problems = append(problems, fmt.Sprintf("annotation_status: %q is not one of [%s]", l.Status, strings.Join(s.statusNames(), ", ")))
	}
	if l.Type == nil {
		if l.Status == StatusComplete {
			problems = append(problems, fmt.Sprintf("type: required when annotation_status is %q", StatusComplete))
		}
	} else if !s.HasType(*l.Type) {
		problems = append(problems, fmt.Sprintf("type: %q is not one of [%s]", *l.Type, strings.Join(s.typeNames(), ", ")))
	}
	if l.Target != nil {
		if p := targetProblem(*l.Target, text); p != "" {
			problems = append(problems, p)
		}
		if l.Type != nil && s.NullTarget(*l.Type) {
			problems = append(problems, fmt.Sprintf("target: must be null for type %q", *l.Type))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

// targetProblem describes why t is not a valid span of text, or returns "".
func targetProblem(t Target, text string) string {
	if t.Text == "" {
		return "target.text: expected a non-empty string"
	}
	if padded([]rune(t.Text)) {
		return "target.text: has leading or trailing whitespace"
	}
	runes := []rune(text)
	if err := checkSpan(len(runes), t.Start, t.End); err != nil {
		return "target: " + err.Error()
	}
	if got := string(runes[t.Start:t.End]); got != t.Text {
		return fmt.Sprintf("target: text[%d:%d] is %q, not %q", t.Start, t.End, got, t.Text)
	}
	return ""
}

// LoadLabels reads the labels JSONL at path. A missing file means no labels yet. Invalid JSON, unknown or missing
// keys, ids not in queue, duplicate ids and any Validate failure (against the queue text) are errors citing the line.
func LoadLabels(path string, schema *Schema, queue []Item) (map[string]Label, error) {
	labels := map[string]Label{}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return labels, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read labels: %w", err)
	}
	texts := make(map[string]string, len(queue))
	for _, it := range queue {
		texts[it.ID] = it.Text
	}
	lines := map[string]int{}
	err = forEachLine(data, func(lineNo int, line []byte) error {
		l, err := decodeLabel(line)
		if err != nil {
			return err
		}
		text, ok := texts[l.ID]
		if !ok {
			return fmt.Errorf("id %q is not in the queue", l.ID)
		}
		if first, dup := lines[l.ID]; dup {
			return fmt.Errorf("duplicate id %q (first on line %d)", l.ID, first)
		}
		if err := schema.Validate(l, text); err != nil {
			return fmt.Errorf("id %q: %w", l.ID, err)
		}
		lines[l.ID] = lineNo
		labels[l.ID] = l
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("labels %s:%w", path, err)
	}
	return labels, nil
}

// decodeLabel strictly decodes one label line: exactly the known keys, id/annotation_status/type/target required.
func decodeLabel(line []byte) (Label, error) {
	fields, err := decodeObject(line)
	if err != nil {
		return Label{}, err
	}
	if err := checkKeys(fields, labelKeys, "id", "annotation_status", "type", "target"); err != nil {
		return Label{}, err
	}
	var l Label
	if l.ID, err = requiredString(fields, "id"); err != nil {
		return Label{}, err
	}
	if l.ID == "" {
		return Label{}, errors.New("id: expected a non-empty string")
	}
	if l.Status, err = requiredString(fields, "annotation_status"); err != nil {
		return Label{}, err
	}
	if raw := fields["type"]; !isJSONNull(raw) {
		typ, err := decodeString(raw)
		if err != nil {
			return Label{}, fmt.Errorf("type: %w or null", err)
		}
		l.Type = &typ
	}
	if raw := fields["target"]; !isJSONNull(raw) {
		t, err := decodeTarget(raw)
		if err != nil {
			return Label{}, err
		}
		l.Target = &t
	}
	if _, ok := fields["note"]; ok {
		if l.Note, err = requiredString(fields, "note"); err != nil {
			return Label{}, err
		}
	}
	return l, nil
}

// decodeTarget strictly decodes a target object: exactly text (string), start and end (integers).
func decodeTarget(raw json.RawMessage) (Target, error) {
	fields, err := decodeObject(raw)
	if err != nil {
		return Target{}, errors.New("target: expected an object or null")
	}
	if err := checkKeys(fields, targetKeys, "text", "start", "end"); err != nil {
		return Target{}, fmt.Errorf("target: %w", err)
	}
	var t Target
	if t.Text, err = requiredString(fields, "text"); err != nil {
		return Target{}, fmt.Errorf("target.%w", err)
	}
	for _, f := range []struct {
		key string
		dst *int
	}{{"start", &t.Start}, {"end", &t.End}} {
		v := fields[f.key]
		if isJSONNull(v) || json.Unmarshal(v, f.dst) != nil {
			return Target{}, fmt.Errorf("target.%s: expected an integer", f.key)
		}
	}
	return t, nil
}

// checkKeys rejects members not in allowed and missing required members, listing them in sorted order.
func checkKeys(fields map[string]json.RawMessage, allowed map[string]bool, required ...string) error {
	var unknown []string
	for k := range fields {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown field(s) %q", unknown)
	}
	for _, k := range required {
		if _, ok := fields[k]; !ok {
			return fmt.Errorf("missing field %q", k)
		}
	}
	return nil
}

// WriteLabels atomically writes labels (keyed by id) to path in queue order, one object per line with non-ASCII
// and HTML characters unescaped. Labels whose id is not in queue are an error. The file is written to a temp file
// in the same directory and renamed over path.
func WriteLabels(path string, queue []Item, labels map[string]Label) error {
	inQueue := make(map[string]bool, len(queue))
	for _, it := range queue {
		inQueue[it.ID] = true
	}
	var stray []string
	for id := range labels {
		if !inQueue[id] {
			stray = append(stray, id)
		}
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		return fmt.Errorf("write labels: id(s) not in the queue: %q", stray)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, it := range queue {
		l, ok := labels[it.ID]
		if !ok {
			continue
		}
		l.ID = it.ID
		if err := enc.Encode(l); err != nil {
			return fmt.Errorf("encode label %q: %w", it.ID, err)
		}
	}
	return writeAtomic(path, buf.Bytes())
}

// writeAtomic replaces path with data via a synced temp file in the same directory and a rename.
// An existing file's permissions are kept; new files get 0644.
func writeAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()

	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	keep = true
	return nil
}
