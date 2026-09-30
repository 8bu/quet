package annotate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Filter selects which queue records filtered navigation (PrevMatch, NextMatch, Advance, SetFilter) visits.
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
// filter, cursor and the undo stack of this session's marks. It never writes the queue file.
type Session struct {
	schema    *Schema
	queuePath string
	outPath   string
	items     []Item
	labels    map[string]Label // saved labels by id
	drafts    map[int]Draft    // pending edits by queue index; only entries differing from the saved values
	undo      []undoEntry      // successful marks of this session, most recent last
	filter    Filter
	cursor    int
	marked    int
	now       func() time.Time
	start     time.Time
}

// undoEntry records the saved label a successful Mark replaced: prev is meaningful only when had is true.
type undoEntry struct {
	index int   // queue index of the marked record
	had   bool  // the record had a saved label before the mark
	prev  Label // that label (Target copied)
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
// atomically; on write failure the in-memory label is restored. Success: draft dropped, session mark count +1,
// the replaced label (or its absence) pushed on the undo stack. An existing label is overwritten in place, so the
// file keeps one line per id. Does NOT move the cursor.
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
		s.restoreLabel(item.ID, had, prev)
		return err
	}
	prev.Target = copyTarget(prev.Target)
	s.undo = append(s.undo, undoEntry{index: i, had: had, prev: prev})
	delete(s.drafts, i)
	s.marked++
	return nil
}

// restoreLabel sets the saved label of id to prev when had, else removes it (in memory only).
func (s *Session) restoreLabel(id string, had bool, prev Label) {
	if had {
		s.labels[id] = prev
	} else {
		delete(s.labels, id)
	}
}

// CanUndo reports whether Undo has a mark to revert.
func (s *Session) CanUndo() bool { return len(s.undo) > 0 }

// Undo reverts the most recent successful Mark of this session not yet undone: restores the record's previous saved
// label (or removes it if it had none), rewrites the labels file atomically, drops the record's draft, moves the
// cursor to it and decrements the session mark count (not below 0). Returns the record's queue index and a short
// description ("restored <status>" or "removed label"); ok=false when there is nothing to undo. On write failure
// the in-memory state is left as before the Undo, the entry stays on the stack and err is returned.
func (s *Session) Undo() (index int, desc string, ok bool, err error) {
	if len(s.undo) == 0 {
		return 0, "", false, nil
	}
	e := s.undo[len(s.undo)-1]
	id := s.items[e.index].ID
	cur, curHad := s.labels[id]
	s.restoreLabel(id, e.had, e.prev)
	if err := WriteLabels(s.outPath, s.items, s.labels); err != nil {
		s.restoreLabel(id, curHad, cur)
		return e.index, "", false, err
	}
	s.undo = s.undo[:len(s.undo)-1]
	delete(s.drafts, e.index)
	s.cursor = e.index
	s.marked = max(0, s.marked-1)
	desc = "removed label"
	if e.had {
		desc = "restored " + e.prev.Status
	}
	return e.index, desc, true, nil
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

// Find resolves a jump query (trimmed) to a queue index: a 1-based queue position, else an exact record id.
// Error when neither matches.
func (s *Session) Find(query string) (int, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return 0, errors.New("enter a queue position or record id")
	}
	n, numErr := strconv.Atoi(q)
	if numErr == nil && n >= 1 && n <= len(s.items) {
		return n - 1, nil
	}
	for i, it := range s.items {
		if it.ID == q {
			return i, nil
		}
	}
	if numErr == nil {
		return 0, fmt.Errorf("no record %d: the queue has %d", n, len(s.items))
	}
	return 0, fmt.Errorf("no record with id %q", q)
}

// Next moves to the next record in queue order, ignoring the filter; false at the end (no wrap, cursor unchanged).
func (s *Session) Next() bool {
	if s.cursor+1 >= len(s.items) {
		return false
	}
	s.cursor++
	return true
}

// Prev moves to the previous record in queue order, ignoring the filter; false at the start (no wrap, cursor
// unchanged).
func (s *Session) Prev() bool {
	if s.cursor <= 0 {
		return false
	}
	s.cursor--
	return true
}

// First moves to the first queue record, ignoring the filter; false only when the queue is empty.
func (s *Session) First() bool {
	if len(s.items) == 0 {
		return false
	}
	s.cursor = 0
	return true
}

// Last moves to the last queue record, ignoring the filter; false only when the queue is empty.
func (s *Session) Last() bool {
	if len(s.items) == 0 {
		return false
	}
	s.cursor = len(s.items) - 1
	return true
}

// NextMatch moves to the next record matching the filter after the cursor; false (cursor unchanged) when none
// (no wrap).
func (s *Session) NextMatch() bool {
	for j := s.cursor + 1; j < len(s.items); j++ {
		if s.Matches(j, s.filter) {
			s.cursor = j
			return true
		}
	}
	return false
}

// PrevMatch moves to the previous record matching the filter before the cursor; false (cursor unchanged) when none
// (no wrap).
func (s *Session) PrevMatch() bool {
	for j := s.cursor - 1; j >= 0; j-- {
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
