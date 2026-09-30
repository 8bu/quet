package annotate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Filter selects which queue records navigation visits.
type Filter int

// Filters in display order.
const (
	FilterUnfinished Filter = iota // records with no saved label (default)
	FilterComplete                 // saved status complete
	FilterUncertain                // saved status uncertain
	FilterSkipped                  // saved status skipped
	FilterAll                      // every record
)

// filterNames are the Filter display names, indexed by Filter.
var filterNames = [...]string{"unfinished", "complete", "uncertain", "skipped", "all"}

// Filters returns every filter in display order.
func Filters() []Filter {
	return []Filter{FilterUnfinished, FilterComplete, FilterUncertain, FilterSkipped, FilterAll}
}

// String returns the filter's display name.
func (f Filter) String() string {
	if f >= 0 && int(f) < len(filterNames) {
		return filterNames[f]
	}
	return fmt.Sprintf("Filter(%d)", int(f))
}

// ParseFilter parses a filter display name (case-insensitive).
func ParseFilter(s string) (Filter, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	for _, f := range Filters() {
		if f.String() == name {
			return f, nil
		}
	}
	return 0, fmt.Errorf("unknown filter %q (want one of %s)", s, strings.Join(filterNames[:], ", "))
}

// Draft is the editable type/target of one record: pending edits, else the saved label's values.
type Draft struct {
	Type   string  // "" = none
	Target *Target // nil = null target
}

// Counts summarises annotation progress. Other = labels with another schema status; Remaining = Total - labeled.
type Counts struct{ Total, Complete, Uncertain, Skipped, Other, Remaining int }

// Session is an annotation session over a queue: saved labels (persisted to the out file), per-record drafts,
// filter and cursor. It never writes the queue file.
type Session struct {
	schema    *Schema
	queuePath string
	outPath   string
	items     []Item
	labels    map[string]Label // saved labels by id
	drafts    map[int]Draft    // pending edits by queue index; only entries differing from the saved values
	filter    Filter
	cursor    int
	marked    int
	now       func() time.Time
	start     time.Time
}

// Open loads schema, queue and existing labels (resume). Errors: any load error; an empty queue; outPath resolving
// to the queue file; out's parent directory missing. Cursor starts on the first unfinished record (0 if none);
// filter = FilterUnfinished.
func Open(queuePath, schemaPath, outPath string) (*Session, error) {
	schema, err := LoadSchema(schemaPath)
	if err != nil {
		return nil, err
	}
	items, err := LoadQueue(queuePath)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("queue %s has no records", queuePath)
	}
	if err := checkOut(outPath, queuePath); err != nil {
		return nil, err
	}
	labels, err := LoadLabels(outPath, schema, items)
	if err != nil {
		return nil, err
	}
	s := &Session{
		schema:    schema,
		queuePath: queuePath,
		outPath:   outPath,
		items:     items,
		labels:    labels,
		drafts:    map[int]Draft{},
	}
	s.SetClock(time.Now)
	for i := range items {
		if s.unfinished(i) {
			s.cursor = i
			break
		}
	}
	return s, nil
}

// checkOut rejects an out path that resolves to the queue file or whose parent directory is missing.
func checkOut(outPath, queuePath string) error {
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", outPath, err)
	}
	queueAbs, err := filepath.Abs(queuePath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", queuePath, err)
	}
	sameErr := fmt.Errorf("labels file %s is the queue file %s; refusing to write over it", outPath, queuePath)
	if realPath(outAbs) == realPath(queueAbs) {
		return sameErr
	}
	dir := filepath.Dir(outAbs)
	fi, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("output directory %s does not exist", dir)
	}
	if err != nil {
		return fmt.Errorf("check output directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("output directory %s is not a directory", dir)
	}
	if oi, err := os.Stat(outAbs); err == nil {
		if qi, err := os.Stat(queueAbs); err == nil && os.SameFile(oi, qi) {
			return sameErr
		}
	}
	return nil
}

// realPath resolves symlinks in abs where possible: the whole path if it exists, else its parent directory.
func realPath(abs string) string {
	if p, err := filepath.EvalSymlinks(abs); err == nil {
		return p
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// Schema returns the loaded schema.
func (s *Session) Schema() *Schema { return s.schema }

// QueuePath returns the queue file path as given to Open.
func (s *Session) QueuePath() string { return s.queuePath }

// OutPath returns the labels file path as given to Open.
func (s *Session) OutPath() string { return s.outPath }

// Len returns the number of queue records.
func (s *Session) Len() int { return len(s.items) }

// Item returns queue record i.
func (s *Session) Item(i int) Item { return s.items[i] }

// Label returns the saved label of record i, if any.
func (s *Session) Label(i int) (Label, bool) {
	l, ok := s.labels[s.items[i].ID]
	if ok {
		l.Target = copyTarget(l.Target)
	}
	return l, ok
}

// Draft returns the editable type/target of record i: pending edits, else the saved label's values.
func (s *Session) Draft(i int) Draft {
	d, ok := s.drafts[i]
	if !ok {
		d = s.savedDraft(i)
	}
	d.Target = copyTarget(d.Target)
	return d
}

// savedDraft returns the saved label's type/target of record i (zero Draft when unlabeled).
func (s *Session) savedDraft(i int) Draft {
	l, ok := s.labels[s.items[i].ID]
	if !ok {
		return Draft{}
	}
	d := Draft{Target: l.Target}
	if l.Type != nil {
		d.Type = *l.Type
	}
	return d
}

// Dirty reports whether the draft of record i differs from the saved label (or from empty when unlabeled).
func (s *Session) Dirty(i int) bool {
	d, ok := s.drafts[i]
	return ok && !draftEqual(d, s.savedDraft(i))
}

// DiscardDraft drops pending edits of record i.
func (s *Session) DiscardDraft(i int) { delete(s.drafts, i) }

// setDraft stores d as the draft of record i, or drops it when it equals the saved values.
func (s *Session) setDraft(i int, d Draft) {
	if draftEqual(d, s.savedDraft(i)) {
		delete(s.drafts, i)
		return
	}
	s.drafts[i] = d
}

// SetType sets the draft type. Unknown type → error. If typ is a null-target type and the draft has a target,
// the target is cleared and cleared=true.
func (s *Session) SetType(i int, typ string) (cleared bool, err error) {
	if !s.schema.HasType(typ) {
		return false, fmt.Errorf("type %q is not declared in the schema", typ)
	}
	d := s.Draft(i)
	d.Type = typ
	if s.schema.NullTarget(typ) && d.Target != nil {
		d.Target = nil
		cleared = true
	}
	s.setDraft(i, d)
	return cleared, nil
}

// SetTarget sets the draft target to runes [start,end). Errors: invalid span (SpanTarget), draft type is a
// null-target type.
func (s *Session) SetTarget(i int, start, end int) error {
	d := s.Draft(i)
	if d.Type != "" && s.schema.NullTarget(d.Type) {
		return fmt.Errorf("type %q must have a null target", d.Type)
	}
	t, err := SpanTarget(s.items[i].Text, start, end)
	if err != nil {
		return err
	}
	d.Target = &t
	s.setDraft(i, d)
	return nil
}

// ClearTarget sets the draft target of record i to null.
func (s *Session) ClearTarget(i int) {
	d := s.Draft(i)
	d.Target = nil
	s.setDraft(i, d)
}

// Mark saves the draft as a label with status: builds Label (existing Note preserved), Validate, write file
// atomically; on write failure the in-memory label is restored. Success: draft dropped, session mark count +1.
// Does NOT move the cursor.
func (s *Session) Mark(i int, status string) error {
	if !s.schema.HasStatus(status) {
		return fmt.Errorf("status %q is not declared in the schema", status)
	}
	item := s.items[i]
	d := s.Draft(i)
	prev, had := s.labels[item.ID]
	l := Label{ID: item.ID, Status: status, Target: d.Target, Note: prev.Note}
	if d.Type != "" {
		l.Type = new(d.Type)
	}
	if err := s.schema.Validate(l, item.Text); err != nil {
		return err
	}
	s.labels[item.ID] = l
	if err := WriteLabels(s.outPath, s.items, s.labels); err != nil {
		if had {
			s.labels[item.ID] = prev
		} else {
			delete(s.labels, item.ID)
		}
		return err
	}
	delete(s.drafts, i)
	s.marked++
	return nil
}

// Counts summarises the saved labels.
func (s *Session) Counts() Counts {
	c := Counts{Total: len(s.items), Remaining: len(s.items) - len(s.labels)}
	for _, l := range s.labels {
		switch l.Status {
		case StatusComplete:
			c.Complete++
		case StatusUncertain:
			c.Uncertain++
		case StatusSkipped:
			c.Skipped++
		default:
			c.Other++
		}
	}
	return c
}

// unfinished reports whether record i has no saved label.
func (s *Session) unfinished(i int) bool {
	_, ok := s.labels[s.items[i].ID]
	return !ok
}

// Matches reports whether record i matches filter f. Unfinished = no saved label; labels with a status other than
// complete/uncertain/skipped match only FilterAll.
func (s *Session) Matches(i int, f Filter) bool {
	l, ok := s.labels[s.items[i].ID]
	switch f {
	case FilterUnfinished:
		return !ok
	case FilterComplete:
		return ok && l.Status == StatusComplete
	case FilterUncertain:
		return ok && l.Status == StatusUncertain
	case FilterSkipped:
		return ok && l.Status == StatusSkipped
	case FilterAll:
		return true
	}
	return false
}

// Filter returns the active filter.
func (s *Session) Filter() Filter { return s.filter }

// SetFilter switches filter; cursor moves to the first matching record at/after the cursor (wrapping); unchanged if
// none match.
func (s *Session) SetFilter(f Filter) {
	s.filter = f
	n := len(s.items)
	for k := range n {
		if j := (s.cursor + k) % n; s.Matches(j, f) {
			s.cursor = j
			return
		}
	}
}

// Cursor returns the current queue index (0..Len()-1).
func (s *Session) Cursor() int { return s.cursor }

// SetCursor moves the cursor to queue index i, clamped to the queue bounds.
func (s *Session) SetCursor(i int) {
	s.cursor = max(0, min(i, len(s.items)-1))
}

// Next moves to the next record matching the filter after the cursor; false at the end (no wrap).
func (s *Session) Next() bool {
	for j := s.cursor + 1; j < len(s.items); j++ {
		if s.Matches(j, s.filter) {
			s.cursor = j
			return true
		}
	}
	return false
}

// Prev moves to the previous record matching the filter before the cursor; false at the start (no wrap).
func (s *Session) Prev() bool {
	for j := s.cursor - 1; j >= 0; j-- {
		if s.Matches(j, s.filter) {
			s.cursor = j
			return true
		}
	}
	return false
}

// First moves to the first record matching the filter; false (cursor unchanged) if none match.
func (s *Session) First() bool {
	for j := range s.items {
		if s.Matches(j, s.filter) {
			s.cursor = j
			return true
		}
	}
	return false
}

// Last moves to the last record matching the filter; false (cursor unchanged) if none match.
func (s *Session) Last() bool {
	for j := len(s.items) - 1; j >= 0; j-- {
		if s.Matches(j, s.filter) {
			s.cursor = j
			return true
		}
	}
	return false
}

// Advance is called after a successful Mark: moves to the next record after the cursor (wrapping, excluding cursor)
// that matches the filter AND, when the filter is FilterUnfinished or FilterAll, is unfinished. false = none
// (cursor unchanged).
func (s *Session) Advance() bool {
	n := len(s.items)
	needUnfinished := s.filter == FilterUnfinished || s.filter == FilterAll
	for k := 1; k < n; k++ {
		j := (s.cursor + k) % n
		if s.Matches(j, s.filter) && (!needUnfinished || s.unfinished(j)) {
			s.cursor = j
			return true
		}
	}
	return false
}

// FilterPosition returns the 1-based position of the cursor among records matching the filter, and how many match.
// pos = 0 when the cursor record does not match.
func (s *Session) FilterPosition() (pos, n int) {
	for j := range s.items {
		if !s.Matches(j, s.filter) {
			continue
		}
		n++
		if j == s.cursor {
			pos = n
		}
	}
	return pos, n
}

// Speed returns the labels marked since Open and the rate per minute (0 before any mark or when elapsed < 1s).
func (s *Session) Speed() (marked int, perMinute float64) {
	elapsed := s.now().Sub(s.start)
	if s.marked == 0 || elapsed < time.Second {
		return s.marked, 0
	}
	return s.marked, float64(s.marked) / elapsed.Minutes()
}

// SetClock replaces time.Now (tests) and restarts the speed clock from now().
func (s *Session) SetClock(now func() time.Time) {
	s.now = now
	s.start = now()
}

// draftEqual reports whether a and b have the same type and target.
func draftEqual(a, b Draft) bool {
	if a.Type != b.Type {
		return false
	}
	if a.Target == nil || b.Target == nil {
		return a.Target == nil && b.Target == nil
	}
	return *a.Target == *b.Target
}

// copyTarget returns a copy of t so callers cannot mutate session state through it.
func copyTarget(t *Target) *Target {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
