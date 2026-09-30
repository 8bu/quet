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
	"unicode/utf8"
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
	return s.validate(l, func(t Target) string { return targetProblem(t, text) })
}

// validateDetached checks l like Validate for a label whose record text is unknown (an id outside the queue in
// re-check mode): the target is only checked for internal consistency (detachedTargetProblem).
func (s *Schema) validateDetached(l Label) error {
	return s.validate(l, detachedTargetProblem)
}

// validate implements Validate with targetCheck describing why a non-null target is invalid ("" when valid).
func (s *Schema) validate(l Label, targetCheck func(Target) string) error {
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
		if p := targetCheck(*l.Target); p != "" {
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

// detachedTargetProblem describes why t is not a structurally valid span without its record text: offsets must
// form a non-empty range [start,end) with start >= 0 whose length in code points equals that of t.Text. Returns ""
// when valid.
func detachedTargetProblem(t Target) string {
	if t.Start < 0 {
		return fmt.Sprintf("target: span start %d is negative", t.Start)
	}
	if t.End <= t.Start {
		return fmt.Sprintf("target: span [%d,%d) is empty", t.Start, t.End)
	}
	if n := utf8.RuneCountInString(t.Text); n != t.End-t.Start {
		return fmt.Sprintf("target: text %q has %d code points, but the span [%d,%d) has %d", t.Text, n, t.Start, t.End, t.End-t.Start)
	}
	return ""
}

// LoadLabels reads the labels JSONL at path for a normal (--out) session. A missing file means no labels yet.
// Invalid JSON, unknown or missing keys, ids not in queue, duplicate ids and any Validate failure (against the queue
// text) are errors citing the line. Re-check sessions load a canonical labels file with loadLabelFile instead.
func LoadLabels(path string, schema *Schema, queue []Item) (map[string]Label, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Label{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read labels: %w", err)
	}
	texts := queueTexts(queue)
	f, err := parseLabelFile(data, func(l Label) error {
		text, ok := texts[l.ID]
		if !ok {
			return fmt.Errorf("id %q is not in the queue", l.ID)
		}
		if err := schema.Validate(l, text); err != nil {
			return fmt.Errorf("id %q: %w", l.ID, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("labels %s:%w", path, err)
	}
	return f.queueLabels(queue), nil
}

// labelFile is a parsed labels file: its exact content and its label lines in file order.
type labelFile struct {
	data  []byte         // the file content as read or written
	lines []labelLine    // non-blank lines in file order
	index map[string]int // id → index in lines
}

// labelLine is one label line: the decoded label and the line's exact bytes (without the newline).
type labelLine struct {
	label Label
	raw   []byte
}

// line returns the line of id, if the file has one.
func (f *labelFile) line(id string) (labelLine, bool) {
	i, ok := f.index[id]
	if !ok {
		return labelLine{}, false
	}
	return f.lines[i], true
}

// queueLabels returns the file's labels whose id is in queue, keyed by id (targets copied).
func (f *labelFile) queueLabels(queue []Item) map[string]Label {
	labels := map[string]Label{}
	for _, it := range queue {
		if ln, ok := f.line(it.ID); ok {
			l := ln.label
			l.Target = copyTarget(l.Target)
			labels[it.ID] = l
		}
	}
	return labels
}

// queueTexts maps each queue id to its record text.
func queueTexts(queue []Item) map[string]string {
	texts := make(map[string]string, len(queue))
	for _, it := range queue {
		texts[it.ID] = it.Text
	}
	return texts
}

// parseLabelFile strictly decodes every non-blank line of data (decodeLabel), rejects duplicate ids and calls
// check (when non-nil) on each decoded label. Errors are prefixed with the line number (as forEachLine).
func parseLabelFile(data []byte, check func(Label) error) (*labelFile, error) {
	raws := bytes.Split(data, []byte("\n"))
	f := &labelFile{data: data, index: map[string]int{}}
	lineNos := map[string]int{}
	err := forEachLine(data, func(lineNo int, line []byte) error {
		l, err := decodeLabel(line)
		if err != nil {
			return err
		}
		if first, dup := lineNos[l.ID]; dup {
			return fmt.Errorf("duplicate id %q (first on line %d)", l.ID, first)
		}
		if check != nil {
			if err := check(l); err != nil {
				return err
			}
		}
		lineNos[l.ID] = lineNo
		f.index[l.ID] = len(f.lines)
		f.lines = append(f.lines, labelLine{label: l, raw: raws[lineNo-1]})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// loadLabelFile reads the canonical labels file of a re-check session. Unlike LoadLabels the file must exist and
// may hold ids outside queue. Every line is decoded strictly and duplicate ids are rejected; labels of queue ids
// get the full Validate against the queue text, others validateDetached. Errors cite the file and line.
func loadLabelFile(path string, schema *Schema, queue []Item) (*labelFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("labels file %s does not exist", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read labels: %w", err)
	}
	texts := queueTexts(queue)
	f, err := parseLabelFile(data, func(l Label) error {
		var err error
		if text, ok := texts[l.ID]; ok {
			err = schema.Validate(l, text)
		} else {
			err = schema.validateDetached(l)
		}
		if err != nil {
			return fmt.Errorf("id %q: %w", l.ID, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("labels %s:%w", path, err)
	}
	return f, nil
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
// and HTML characters unescaped: the whole file of a normal (--out) session. Labels whose id is not in queue are an
// error. The file is written to a temp file in the same directory and renamed over path. Re-check sessions write
// with writeMergedLabels instead.
func WriteLabels(path string, queue []Item, labels map[string]Label) error {
	if _, err := checkStray(queue, labels); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := newLabelEncoder(&buf)
	for _, it := range queue {
		l, ok := labels[it.ID]
		if !ok {
			continue
		}
		if err := encodeLabel(enc, it.ID, l); err != nil {
			return err
		}
	}
	return writeAtomic(path, buf.Bytes())
}

// checkStray returns the set of queue ids, or an error listing the ids of labels that are not in queue.
func checkStray(queue []Item, labels map[string]Label) (map[string]bool, error) {
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
		return nil, fmt.Errorf("write labels: id(s) not in the queue: %q", stray)
	}
	return inQueue, nil
}

// newLabelEncoder returns the label line encoder: one object per line, non-ASCII and HTML characters unescaped.
func newLabelEncoder(buf *bytes.Buffer) *json.Encoder {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	return enc
}

// encodeLabel encodes l with its ID set to id as one line.
func encodeLabel(enc *json.Encoder, id string, l Label) error {
	l.ID = id
	if err := enc.Encode(l); err != nil {
		return fmt.Errorf("encode label %q: %w", id, err)
	}
	return nil
}

// writeMergedLabels atomically rewrites the canonical labels file of a re-check session with the queue labels
// replaced by labels, and returns the written file as the new baseline. orig is the file as opened, base as last
// read or written. It refuses (nothing written) when the file on disk no longer equals base.data (changed by
// someone else), when mergeLabels refuses, or when the merged output does not hold exactly one line per id of
// orig ∪ labels.
func writeMergedLabels(path string, orig, base *labelFile, queue []Item, labels map[string]Label) (*labelFile, error) {
	cur, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read labels: %w", err)
	}
	if !bytes.Equal(cur, base.data) {
		return nil, fmt.Errorf("labels file %s was changed by another program since quet last read or wrote it; refusing to overwrite it (reopen to pick up the changes)", path)
	}
	data, err := mergeLabels(orig, base, queue, labels)
	if err != nil {
		return nil, err
	}
	next, err := parseLabelFile(data, nil)
	if err != nil {
		return nil, fmt.Errorf("write labels %s: merged output line %w", path, err)
	}
	want := len(orig.lines)
	for id := range labels {
		if _, ok := orig.index[id]; !ok {
			want++
		}
		if _, ok := next.index[id]; !ok {
			return nil, fmt.Errorf("write labels %s: merged output lost label %q", path, id)
		}
	}
	for _, ln := range orig.lines {
		if _, ok := next.index[ln.label.ID]; !ok {
			return nil, fmt.Errorf("write labels %s: merged output lost label %q", path, ln.label.ID)
		}
	}
	if len(next.lines) != want {
		return nil, fmt.Errorf("write labels %s: merged output has %d labels, want %d", path, len(next.lines), want)
	}
	if err := writeAtomic(path, data); err != nil {
		return nil, err
	}
	return next, nil
}

// mergeLabels builds the canonical labels file content: base's lines in order, each ending with a newline (blank
// lines dropped). Lines of ids outside queue, and of queue ids whose label equals base's, keep base's bytes; a
// changed queue label that equals orig's keeps orig's bytes, otherwise it is re-encoded; a queue id with no label
// is dropped, except that dropping an id of orig is refused. Labels of queue ids not in base are appended in queue
// order. Labels whose id is not in queue are an error.
func mergeLabels(orig, base *labelFile, queue []Item, labels map[string]Label) ([]byte, error) {
	inQueue, err := checkStray(queue, labels)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := newLabelEncoder(&buf)
	for _, ln := range base.lines {
		id := ln.label.ID
		l, ok := labels[id]
		switch {
		case !inQueue[id] || ok && labelEqual(l, ln.label):
			buf.Write(ln.raw)
			buf.WriteByte('\n')
		case ok:
			if o, had := orig.line(id); had && labelEqual(l, o.label) {
				buf.Write(o.raw)
				buf.WriteByte('\n')
			} else if err := encodeLabel(enc, id, l); err != nil {
				return nil, err
			}
		default:
			if _, had := orig.index[id]; had {
				return nil, fmt.Errorf("write labels: refusing to drop label %q, which was in the labels file when it was opened", id)
			}
		}
	}
	for _, it := range queue {
		l, ok := labels[it.ID]
		if _, inBase := base.index[it.ID]; !ok || inBase {
			continue
		}
		if err := encodeLabel(enc, it.ID, l); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// labelEqual reports whether a and b have the same id, status, type, target and note.
func labelEqual(a, b Label) bool {
	if a.ID != b.ID || a.Status != b.Status || a.Note != b.Note {
		return false
	}
	if (a.Type == nil) != (b.Type == nil) || a.Type != nil && *a.Type != *b.Type {
		return false
	}
	if (a.Target == nil) != (b.Target == nil) || a.Target != nil && *a.Target != *b.Target {
		return false
	}
	return true
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
