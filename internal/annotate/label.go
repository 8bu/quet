package annotate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// Label is one persisted annotation. Type serializes as null when nil, Spans holds one entry per declared span (nil
// value = null, serialized in schema order), SpanStatus only the spans that declare statuses (serialized as
// span_status when non-empty) and Note only when non-empty. The JSON form is produced and read by the schema-aware
// encodeLabel and decodeLabel.
type Label struct {
	ID, Status string
	Type       *string
	Spans      map[string]*Target
	SpanStatus map[string]string
	Note       string
}

// targetKeys are the only JSON members accepted in a span value when loading labels.
var targetKeys = map[string]bool{"text": true, "start": true, "end": true}

// labelKeys returns the only JSON members accepted when loading labels under s: id, annotation_status, type, note,
// every span name and, when some span declares statuses, span_status.
func (s *Schema) labelKeys() map[string]bool {
	keys := map[string]bool{"id": true, "annotation_status": true, "type": true, "note": true}
	for _, sp := range s.Spans {
		keys[sp.Name] = true
	}
	if s.HasSpanStatuses() {
		keys["span_status"] = true
	}
	return keys
}

// requiredLabelKeys returns the JSON members every loaded label must have: id, annotation_status, type and every
// span name (in that order).
func (s *Schema) requiredLabelKeys() []string {
	return append([]string{"id", "annotation_status", "type"}, s.spanNames()...)
}

// copyTarget returns a copy of t so callers cannot mutate session state through it.
func copyTarget(t *Target) *Target {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// copySpans returns a deep copy of spans (nil stays nil).
func copySpans(spans map[string]*Target) map[string]*Target {
	if spans == nil {
		return nil
	}
	c := make(map[string]*Target, len(spans))
	for name, t := range spans {
		c[name] = copyTarget(t)
	}
	return c
}

// copySpanStatus returns a copy of m (nil stays nil).
func copySpanStatus(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for name, st := range m {
		c[name] = st
	}
	return c
}

// copyLabel returns a deep copy of l (Type, Spans and SpanStatus copied).
func copyLabel(l Label) Label {
	if l.Type != nil {
		l.Type = new(*l.Type)
	}
	l.Spans = copySpans(l.Spans)
	l.SpanStatus = copySpanStatus(l.SpanStatus)
	return l
}

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
// ("; "-joined) in this order: undeclared status, missing type on a complete label, undeclared type, then per span
// in schema order an invalid span and a span on a type that must leave it null, then the span_status members
// (undeclared status, span without statuses, undeclared span, status on a span that is null for the type).
// Spans or span statuses missing from the label are not problems.
func (s *Schema) Validate(l Label, text string) error {
	return s.validate(l, func(name string, t Target) string { return spanProblem(name, t, text) })
}

// validateDetached checks l like Validate for a label whose record text is unknown (an id outside the queue in
// re-check mode): each span is only checked for internal consistency (detachedSpanProblem).
func (s *Schema) validateDetached(l Label) error {
	return s.validate(l, detachedSpanProblem)
}

// validate implements Validate with spanCheck describing why the non-null span called name is invalid ("" when valid).
func (s *Schema) validate(l Label, spanCheck func(name string, t Target) string) error {
	var problems []string
	if !s.HasStatus(l.Status) {
		problems = append(problems, fmt.Sprintf("annotation_status: %q is not one of [%s]", l.Status, strings.Join(s.statusNames(), ", ")))
	}
	typ := ""
	if l.Type == nil {
		if l.Status == StatusComplete {
			problems = append(problems, fmt.Sprintf("type: required when annotation_status is %q", StatusComplete))
		}
	} else {
		typ = *l.Type
		if !s.HasType(typ) {
			problems = append(problems, fmt.Sprintf("type: %q is not one of [%s]", typ, strings.Join(s.typeNames(), ", ")))
		}
	}
	for _, sp := range s.Spans {
		t := l.Spans[sp.Name]
		if t == nil {
			continue
		}
		if p := spanCheck(sp.Name, *t); p != "" {
			problems = append(problems, p)
		}
		if l.Type != nil && s.NullSpan(sp.Name, typ) {
			problems = append(problems, fmt.Sprintf("%s: must be null for type %q", sp.Name, typ))
		}
	}
	problems = append(problems, s.spanStatusProblems(l)...)
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

// spanStatusProblems describes every invalid span_status member of l: declared spans in schema order, then
// undeclared names sorted.
func (s *Schema) spanStatusProblems(l Label) []string {
	if len(l.SpanStatus) == 0 {
		return nil
	}
	typ := ""
	if l.Type != nil {
		typ = *l.Type
	}
	var problems []string
	for _, sp := range s.Spans {
		st, ok := l.SpanStatus[sp.Name]
		if !ok {
			continue
		}
		switch {
		case len(sp.Statuses) == 0:
			problems = append(problems, fmt.Sprintf("span_status.%s: span declares no statuses", sp.Name))
		case !slices.Contains(sp.Statuses, st):
			problems = append(problems, fmt.Sprintf("span_status.%s: %q is not one of [%s]", sp.Name, st, strings.Join(sp.Statuses, ", ")))
		}
		if s.NullSpan(sp.Name, typ) {
			problems = append(problems, fmt.Sprintf("span_status.%s: must be absent for type %q", sp.Name, typ))
		}
	}
	var unknown []string
	for name := range l.SpanStatus {
		if _, ok := s.Span(name); !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		problems = append(problems, fmt.Sprintf("span_status.%s: not a declared span", name))
	}
	return problems
}

// spanProblem describes why t is not a valid value of the span called name in text, or returns "".
func spanProblem(name string, t Target, text string) string {
	if t.Text == "" {
		return name + ".text: expected a non-empty string"
	}
	if padded([]rune(t.Text)) {
		return name + ".text: has leading or trailing whitespace"
	}
	runes := []rune(text)
	if err := checkSpan(len(runes), t.Start, t.End); err != nil {
		return name + ": " + err.Error()
	}
	if got := string(runes[t.Start:t.End]); got != t.Text {
		return fmt.Sprintf("%s: text[%d:%d] is %q, not %q", name, t.Start, t.End, got, t.Text)
	}
	return ""
}

// detachedSpanProblem describes why t is not a structurally valid value of the span called name without its record
// text: offsets must form a non-empty range [start,end) with start >= 0 whose length in code points equals that of
// t.Text. Returns "" when valid.
func detachedSpanProblem(name string, t Target) string {
	if t.Start < 0 {
		return fmt.Sprintf("%s: span start %d is negative", name, t.Start)
	}
	if t.End <= t.Start {
		return fmt.Sprintf("%s: span [%d,%d) is empty", name, t.Start, t.End)
	}
	if n := utf8.RuneCountInString(t.Text); n != t.End-t.Start {
		return fmt.Sprintf("%s: text %q has %d code points, but the span [%d,%d) has %d", name, t.Text, n, t.Start, t.End, t.End-t.Start)
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
	f, err := parseLabelFile(data, schema, func(l Label) error {
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

// queueLabels returns the file's labels whose id is in queue, keyed by id (deep copies).
func (f *labelFile) queueLabels(queue []Item) map[string]Label {
	labels := map[string]Label{}
	for _, it := range queue {
		if ln, ok := f.line(it.ID); ok {
			labels[it.ID] = copyLabel(ln.label)
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

// parseLabelFile strictly decodes every non-blank line of data under schema (decodeLabel), rejects duplicate ids and
// calls check (when non-nil) on each decoded label. Errors are prefixed with the line number (as forEachLine).
func parseLabelFile(data []byte, schema *Schema, check func(Label) error) (*labelFile, error) {
	raws := bytes.Split(data, []byte("\n"))
	f := &labelFile{data: data, index: map[string]int{}}
	lineNos := map[string]int{}
	err := forEachLine(data, func(lineNo int, line []byte) error {
		l, err := decodeLabel(schema, line)
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
	f, err := parseLabelFile(data, schema, func(l Label) error {
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

// decodeLabel strictly decodes one label line under schema: exactly the known keys (labelKeys), id,
// annotation_status, type and every span required. Each span is null or an object (decodeTarget named after the
// span); span_status, allowed only when a span declares statuses, is an object of strings.
func decodeLabel(schema *Schema, line []byte) (Label, error) {
	fields, err := decodeObject(line)
	if err != nil {
		return Label{}, err
	}
	if err := checkKeys(fields, schema.labelKeys(), schema.requiredLabelKeys()...); err != nil {
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
	l.Spans = make(map[string]*Target, len(schema.Spans))
	for _, sp := range schema.Spans {
		l.Spans[sp.Name] = nil
		if raw := fields[sp.Name]; !isJSONNull(raw) {
			t, err := decodeTarget(sp.Name, raw)
			if err != nil {
				return Label{}, err
			}
			l.Spans[sp.Name] = &t
		}
	}
	if raw, ok := fields["span_status"]; ok {
		if l.SpanStatus, err = decodeSpanStatus(raw); err != nil {
			return Label{}, err
		}
	}
	if _, ok := fields["note"]; ok {
		if l.Note, err = requiredString(fields, "note"); err != nil {
			return Label{}, err
		}
	}
	return l, nil
}

// ParseLabel decodes one label JSON object strictly under schema, with the same rules as a labels-file line: exactly
// the known keys, id/annotation_status/type and every span present, spans null or well-formed objects. It does not
// validate against the schema or a record text (Schema.Validate does).
func ParseLabel(schema *Schema, data []byte) (Label, error) {
	return decodeLabel(schema, data)
}

// LabelsEqual reports whether a and b have the same status, type, spans (a missing span equals a null one), span
// statuses (nil equals empty) and note. Ids are ignored.
func LabelsEqual(a, b Label) bool {
	a.ID, b.ID = "", ""
	return labelEqual(a, b)
}

// decodeTarget strictly decodes the value of the span called name: an object with exactly text (string), start and
// end (integers). Errors are prefixed with name ("target" for a schema without spans).
func decodeTarget(name string, raw json.RawMessage) (Target, error) {
	fields, err := decodeObject(raw)
	if err != nil {
		return Target{}, fmt.Errorf("%s: expected an object or null", name)
	}
	if err := checkKeys(fields, targetKeys, "text", "start", "end"); err != nil {
		return Target{}, fmt.Errorf("%s: %w", name, err)
	}
	var t Target
	if t.Text, err = requiredString(fields, "text"); err != nil {
		return Target{}, fmt.Errorf("%s.%w", name, err)
	}
	for _, f := range []struct {
		key string
		dst *int
	}{{"start", &t.Start}, {"end", &t.End}} {
		v := fields[f.key]
		if isJSONNull(v) || json.Unmarshal(v, f.dst) != nil {
			return Target{}, fmt.Errorf("%s.%s: expected an integer", name, f.key)
		}
	}
	return t, nil
}

// decodeSpanStatus strictly decodes a span_status value: an object whose members are all strings. An empty object
// decodes to nil.
func decodeSpanStatus(raw json.RawMessage) (map[string]string, error) {
	fields, err := decodeObject(raw)
	if err != nil {
		return nil, errors.New("span_status: expected an object")
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	var status map[string]string
	for _, name := range names {
		st, err := decodeString(fields[name])
		if err != nil {
			return nil, fmt.Errorf("span_status.%s: %w", name, err)
		}
		if status == nil {
			status = make(map[string]string, len(fields))
		}
		status[name] = st
	}
	return status, nil
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
func WriteLabels(path string, schema *Schema, queue []Item, labels map[string]Label) error {
	if _, err := checkStray(queue, labels); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, it := range queue {
		l, ok := labels[it.ID]
		if !ok {
			continue
		}
		if err := encodeLabel(&buf, schema, it.ID, l); err != nil {
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

// encodeLabel appends l with its ID set to id as one line (newline-terminated) to buf, with the members in this
// order: id, annotation_status, type, each span of schema in schema order (null when absent), span_status (only when
// it holds a status of a declared span; members in schema span order) and note (only when non-empty). For a schema
// without spans: the bytes of the pre-multi-span struct encoding.
func encodeLabel(buf *bytes.Buffer, schema *Schema, id string, l Label) error {
	enc := newLabelEncoder(buf)
	member := func(key string, v any) error {
		if err := enc.Encode(key); err != nil {
			return err
		}
		buf.Truncate(buf.Len() - 1) // the encoder's newline
		buf.WriteByte(':')
		if err := enc.Encode(v); err != nil {
			return err
		}
		buf.Truncate(buf.Len() - 1)
		return nil
	}
	buf.WriteByte('{')
	err := member("id", id)
	if err == nil {
		buf.WriteByte(',')
		err = member("annotation_status", l.Status)
	}
	if err == nil {
		buf.WriteByte(',')
		err = member("type", l.Type)
	}
	for _, sp := range schema.Spans {
		if err != nil {
			break
		}
		buf.WriteByte(',')
		err = member(sp.Name, l.Spans[sp.Name])
	}
	if err == nil {
		if status := orderedSpanStatus(schema, l.SpanStatus); status != nil {
			buf.WriteByte(',')
			err = member("span_status", status)
		}
	}
	if err == nil && l.Note != "" {
		buf.WriteByte(',')
		err = member("note", l.Note)
	}
	if err != nil {
		return fmt.Errorf("encode label %q: %w", id, err)
	}
	buf.WriteString("}\n")
	return nil
}

// orderedSpanStatus is m restricted to the declared spans as a JSON object marshaling its members in schema span
// order, or nil when it would be empty.
func orderedSpanStatus(schema *Schema, m map[string]string) *orderedStatus {
	var o orderedStatus
	for _, sp := range schema.Spans {
		if st, ok := m[sp.Name]; ok {
			o.names = append(o.names, sp.Name)
			o.statuses = append(o.statuses, st)
		}
	}
	if len(o.names) == 0 {
		return nil
	}
	return &o
}

// orderedStatus is a span_status object with its members in a fixed order.
type orderedStatus struct{ names, statuses []string }

// MarshalJSON encodes the members in order, with non-ASCII and HTML characters unescaped.
func (o *orderedStatus) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := newLabelEncoder(&buf)
	buf.WriteByte('{')
	for i, name := range o.names {
		if i > 0 {
			buf.WriteByte(',')
		}
		for k, v := range []string{name, o.statuses[i]} {
			if k == 1 {
				buf.WriteByte(':')
			}
			if err := enc.Encode(v); err != nil {
				return nil, err
			}
			buf.Truncate(buf.Len() - 1)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// writeMergedLabels atomically rewrites the canonical labels file of a re-check session with the queue labels
// replaced by labels, and returns the written file as the new baseline. orig is the file as opened, base as last
// read or written. It refuses (nothing written) when the file on disk no longer equals base.data (changed by
// someone else), when mergeLabels refuses, or when the merged output does not hold exactly one line per id of
// orig ∪ labels.
func writeMergedLabels(path string, schema *Schema, orig, base *labelFile, queue []Item, labels map[string]Label) (*labelFile, error) {
	cur, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read labels: %w", err)
	}
	if !bytes.Equal(cur, base.data) {
		return nil, fmt.Errorf("labels file %s was changed by another program since quet last read or wrote it; refusing to overwrite it (reopen to pick up the changes)", path)
	}
	data, err := mergeLabels(schema, orig, base, queue, labels)
	if err != nil {
		return nil, err
	}
	next, err := parseLabelFile(data, schema, nil)
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
func mergeLabels(schema *Schema, orig, base *labelFile, queue []Item, labels map[string]Label) ([]byte, error) {
	inQueue, err := checkStray(queue, labels)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
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
			} else if err := encodeLabel(&buf, schema, id, l); err != nil {
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
		if err := encodeLabel(&buf, schema, it.ID, l); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// labelEqual reports whether a and b have the same id, status, type, spans (a missing span equals a null one),
// span statuses (nil equals empty) and note.
func labelEqual(a, b Label) bool {
	if a.ID != b.ID || a.Status != b.Status || a.Note != b.Note {
		return false
	}
	if (a.Type == nil) != (b.Type == nil) || a.Type != nil && *a.Type != *b.Type {
		return false
	}
	return spansEqual(a.Spans, b.Spans) && spanStatusEqual(a.SpanStatus, b.SpanStatus)
}

// spansEqual reports whether a and b hold the same span values; a missing entry equals a nil one.
func spansEqual(a, b map[string]*Target) bool {
	for name, t := range a {
		if !targetEqual(t, b[name]) {
			return false
		}
	}
	for name, t := range b {
		if !targetEqual(a[name], t) {
			return false
		}
	}
	return true
}

// targetEqual reports whether a and b are both nil or point to equal targets.
func targetEqual(a, b *Target) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// spanStatusEqual reports whether a and b hold the same statuses; nil equals empty.
func spanStatusEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, st := range a {
		if other, ok := b[name]; !ok || other != st {
			return false
		}
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
