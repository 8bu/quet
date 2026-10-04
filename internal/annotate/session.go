package annotate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Filter selects which queue records filtered navigation (PrevMatch, NextMatch, Advance, SetFilter) visits.
type Filter int

// Filters in display order. The proposal filters exist only in a session with proposals loaded (Session.Filters).
const (
	FilterUnfinished        Filter = iota // records with no saved label (default)
	FilterComplete                        // saved status complete
	FilterUncertain                       // saved status uncertain
	FilterSkipped                         // saved status skipped
	FilterAll                             // every record
	FilterProposed                        // records with a proposal
	FilterUnproposed                      // records without a proposal
	FilterProposedUncertain               // records whose proposal status is uncertain
)

// filterNames are the Filter display names, indexed by Filter.
var filterNames = [...]string{"unfinished", "complete", "uncertain", "skipped", "all", "proposed", "unproposed", "proposed uncertain"}

// baseFilters is the number of filters every session has: those before FilterProposed.
const baseFilters = int(FilterProposed)

// Filters returns the filters of this session in display order: the five saved-label filters, followed by
// proposed, unproposed and proposed uncertain when proposals are loaded.
func (s *Session) Filters() []Filter {
	n := baseFilters
	if s.HasProposals() {
		n = len(filterNames)
	}
	filters := make([]Filter, n)
	for i := range filters {
		filters[i] = Filter(i)
	}
	return filters
}

// String returns the filter's display name.
func (f Filter) String() string {
	if f >= 0 && int(f) < len(filterNames) {
		return filterNames[f]
	}
	return fmt.Sprintf("Filter(%d)", int(f))
}

// ParseFilter parses the display name (case-insensitive) of one of this session's filters (Filters).
func (s *Session) ParseFilter(name string) (Filter, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	filters := s.Filters()
	names := make([]string, len(filters))
	for i, f := range filters {
		if f.String() == want {
			return f, nil
		}
		names[i] = f.String()
	}
	return 0, fmt.Errorf("unknown filter %q (want one of %s)", name, strings.Join(names, ", "))
}

// Draft is the editable type, spans and span statuses of one record: pending edits, else the saved label's values.
type Draft struct {
	Type       string             // "" = none
	Spans      map[string]*Target // missing or nil = null span
	SpanStatus map[string]string  // missing = unset (Mark applies the span's default)
}

// Counts summarises annotation progress. Other = labels with another schema status; Remaining = Total - labeled.
type Counts struct{ Total, Complete, Uncertain, Skipped, Other, Remaining int }

// Session is an annotation session over a queue: saved labels (persisted to the labels file), per-record drafts,
// filter, cursor and the undo stack of this session's marks. It never writes the queue file.
//
// A normal session (Open) owns its labels file: every label id is in the queue and each write rewrites the whole
// file in queue order. A re-check session (OpenRecheck) edits a subset queue against a larger canonical labels file:
// only queue records are shown and editable, every other line is preserved byte for byte, and writes refuse when
// the file changed on disk since it was last read or written.
type Session struct {
	schema     *Schema
	queuePath  string
	schemaPath string // "" in a session opened from an in-memory schema (OpenRecheckItems)
	outPath    string
	items      []Item
	byID       map[string]int   // queue index by record id
	labels     map[string]Label // saved labels of queue records by id
	drafts     map[int]Draft    // pending edits by queue index; only entries differing from the saved values
	undo       []undoEntry      // successful marks of this session, most recent last
	filter     Filter
	cursor     int
	marked     int
	now        func() time.Time
	start      time.Time
	recheck    *recheckState // nil in a normal session
	proposals  *proposalSet  // nil unless LoadProposals succeeded
}

// recheckState is the canonical labels file of a re-check session.
type recheckState struct {
	orig *labelFile // as opened: original line bytes for byte-identical restores; its ids are never dropped
	base *labelFile // as last read or written: must equal the file on disk before each write
}

// undoEntry records what one successful save (Mark, SaveLabel or SaveLabels) replaced: one item per saved record,
// in ascending queue-index order.
type undoEntry struct {
	items []undoItem
}

// undoItem is the saved label one record had before a save: prev is meaningful only when had is true.
type undoItem struct {
	index int   // queue index of the record
	had   bool  // the record had a saved label before the save
	prev  Label // that label (deep copied)
}

// Open loads schema, queue and existing labels (resume) for a normal session writing outPath (LoadLabels: a missing
// file means no labels yet; every label id must be in the queue). Errors: any load error; an empty queue; outPath
// resolving to the queue file; out's parent directory missing. Cursor starts on the first unfinished record (0 if
// none); filter = FilterUnfinished.
func Open(queuePath, schemaPath, outPath string) (*Session, error) {
	s, err := open(queuePath, schemaPath, outPath)
	if err != nil {
		return nil, err
	}
	if s.labels, err = LoadLabels(outPath, s.schema, s.items); err != nil {
		return nil, err
	}
	for i := range s.items {
		if s.unfinished(i) {
			s.cursor = i
			break
		}
	}
	return s, nil
}

// OpenRecheck opens a re-check session: the queue (typically a subset of the corpus) is labeled against the
// existing canonical labels file at labelsPath, which may hold ids outside the queue (loadLabelFile). Errors: those
// of Open, plus a missing labels file and any invalid line. Cursor starts at 0; filter = FilterAll.
func OpenRecheck(queuePath, schemaPath, labelsPath string) (*Session, error) {
	s, err := open(queuePath, schemaPath, labelsPath)
	if err != nil {
		return nil, err
	}
	if err := s.attachRecheck(); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenRecheckItems is OpenRecheck over an in-memory schema and queue (for instance a project pulled from quet-web):
// there is no queue or schema file, so QueuePath and SchemaPath are "". Errors: no items, an unusable labels path
// (checkOut), a missing labels file and any invalid line. Cursor starts at 0; filter = FilterAll.
func OpenRecheckItems(schema *Schema, items []Item, labelsPath string) (*Session, error) {
	if len(items) == 0 {
		return nil, errors.New("queue has no records")
	}
	if err := checkOut(labelsPath, ""); err != nil {
		return nil, err
	}
	s := &Session{schema: schema, outPath: labelsPath, items: items, byID: indexItems(items), drafts: map[int]Draft{}}
	s.SetClock(time.Now)
	if err := s.attachRecheck(); err != nil {
		return nil, err
	}
	return s, nil
}

// attachRecheck loads the canonical labels file at the session's out path and turns s into a re-check session.
func (s *Session) attachRecheck() error {
	f, err := loadLabelFile(s.outPath, s.schema, s.items)
	if err != nil {
		return err
	}
	s.labels = f.queueLabels(s.items)
	s.recheck = &recheckState{orig: f, base: f}
	s.filter = FilterAll
	return nil
}

// open loads schema and queue and checks the labels path (checkOut) for Open and OpenRecheck; labels are not loaded.
func open(queuePath, schemaPath, outPath string) (*Session, error) {
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
	s := &Session{
		schema:     schema,
		queuePath:  queuePath,
		schemaPath: schemaPath,
		outPath:    outPath,
		items:      items,
		byID:       indexItems(items),
		drafts:     map[int]Draft{},
	}
	s.SetClock(time.Now)
	return s, nil
}

// indexItems maps each record id to its queue index.
func indexItems(items []Item) map[string]int {
	byID := make(map[string]int, len(items))
	for i, it := range items {
		byID[it.ID] = i
	}
	return byID
}

// checkOut rejects an out path whose parent directory is missing and, unless queuePath is "" (no queue file), one
// that resolves to the queue file.
func checkOut(outPath, queuePath string) error {
	outAbs, err := filepath.Abs(outPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", outPath, err)
	}
	queueAbs := ""
	sameErr := fmt.Errorf("labels file %s is the queue file %s; refusing to write over it", outPath, queuePath)
	if queuePath != "" {
		if queueAbs, err = filepath.Abs(queuePath); err != nil {
			return fmt.Errorf("resolve %s: %w", queuePath, err)
		}
		if realPath(outAbs) == realPath(queueAbs) {
			return sameErr
		}
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
	if queuePath == "" {
		return nil
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

// QueuePath returns the queue file path as given to Open or OpenRecheck.
func (s *Session) QueuePath() string { return s.queuePath }

// SchemaPath returns the schema file path as given to Open or OpenRecheck ("" for OpenRecheckItems).
func (s *Session) SchemaPath() string { return s.schemaPath }

// OutPath returns the labels file path as given to Open or OpenRecheck.
func (s *Session) OutPath() string { return s.outPath }

// Recheck reports whether the session was opened with OpenRecheck.
func (s *Session) Recheck() bool { return s.recheck != nil }

// LabelsTotal returns the number of distinct ids in the labels file as currently saved: in a re-check session the
// queue labels plus every label outside the queue; in a normal session the saved labels.
func (s *Session) LabelsTotal() int {
	if s.recheck != nil {
		return len(s.recheck.base.lines)
	}
	return len(s.labels)
}

// LabelsInQueue returns the number of saved labels whose id is in the queue.
func (s *Session) LabelsInQueue() int { return len(s.labels) }

// Len returns the number of queue records.
func (s *Session) Len() int { return len(s.items) }

// Item returns queue record i.
func (s *Session) Item(i int) Item { return s.items[i] }

// IndexOf returns the queue index of the record with the given id.
func (s *Session) IndexOf(id string) (int, bool) {
	i, ok := s.byID[id]
	return i, ok
}

// Label returns a deep copy of the saved label of record i, if any.
func (s *Session) Label(i int) (Label, bool) {
	l, ok := s.labels[s.items[i].ID]
	if ok {
		l = copyLabel(l)
	}
	return l, ok
}

// Draft returns a deep copy of the editable type, spans and span statuses of record i: pending edits, else the saved
// label's values. Its Spans map is never nil.
func (s *Session) Draft(i int) Draft {
	d, ok := s.drafts[i]
	if !ok {
		d = s.savedDraft(i)
	}
	d.Spans = copySpans(d.Spans)
	if d.Spans == nil {
		d.Spans = map[string]*Target{}
	}
	d.SpanStatus = copySpanStatus(d.SpanStatus)
	return d
}

// savedDraft returns the saved label's type, spans and span statuses of record i (zero Draft when unlabeled). Its
// maps are the label's own: copy before mutating.
func (s *Session) savedDraft(i int) Draft {
	l, ok := s.labels[s.items[i].ID]
	if !ok {
		return Draft{}
	}
	d := Draft{Spans: l.Spans, SpanStatus: l.SpanStatus}
	if l.Type != nil {
		d.Type = *l.Type
	}
	return d
}

// Dirty reports whether the draft of record i differs from the saved label (or from empty when unlabeled).
func (s *Session) Dirty(i int) bool {
	d, ok := s.drafts[i]
	return ok && !s.draftEqual(d, s.savedDraft(i))
}

// DiscardDraft drops pending edits of record i.
func (s *Session) DiscardDraft(i int) { delete(s.drafts, i) }

// setDraft stores d as the draft of record i, or drops it when it equals the saved values.
func (s *Session) setDraft(i int, d Draft) {
	if s.draftEqual(d, s.savedDraft(i)) {
		delete(s.drafts, i)
		return
	}
	s.drafts[i] = d
}

// SetType sets the draft type. Unknown type → error. Every span that is null for typ loses its draft span status;
// those that had a value lose it too, and cleared lists their names in schema order.
func (s *Session) SetType(i int, typ string) (cleared []string, err error) {
	if !s.schema.HasType(typ) {
		return nil, fmt.Errorf("type %q is not declared in the schema", typ)
	}
	d := s.Draft(i)
	d.Type = typ
	for _, sp := range s.schema.Spans {
		if !s.schema.NullSpan(sp.Name, typ) {
			continue
		}
		if d.Spans[sp.Name] != nil {
			d.Spans[sp.Name] = nil
			cleared = append(cleared, sp.Name)
		}
		delete(d.SpanStatus, sp.Name)
	}
	s.setDraft(i, d)
	return cleared, nil
}

// declaredSpan returns the span called name, or an error when the schema does not declare it.
func (s *Session) declaredSpan(name string) (SpanDef, error) {
	sp, ok := s.schema.Span(name)
	if !ok {
		return SpanDef{}, fmt.Errorf("span %q is not declared in the schema", name)
	}
	return sp, nil
}

// SetSpan sets the draft value of the span called name to runes [start,end). Errors: unknown span, draft type is
// null for the span, invalid span (SpanTarget).
func (s *Session) SetSpan(i int, name string, start, end int) error {
	if _, err := s.declaredSpan(name); err != nil {
		return err
	}
	d := s.Draft(i)
	if s.schema.NullSpan(name, d.Type) {
		return fmt.Errorf("type %q must have a null %s", d.Type, name)
	}
	t, err := SpanTarget(s.items[i].Text, start, end)
	if err != nil {
		return err
	}
	if d.Spans == nil {
		d.Spans = map[string]*Target{}
	}
	d.Spans[name] = &t
	s.setDraft(i, d)
	return nil
}

// ClearSpan sets the draft value of the span called name of record i to null (its span status is kept). Unknown span
// → error.
func (s *Session) ClearSpan(i int, name string) error {
	if _, err := s.declaredSpan(name); err != nil {
		return err
	}
	d := s.Draft(i)
	d.Spans[name] = nil
	s.setDraft(i, d)
	return nil
}

// statusSpan returns the span called name when its draft status can be edited under draft type typ. Errors: unknown
// span, a span without statuses, a span that is null for typ.
func (s *Session) statusSpan(name, typ string) (SpanDef, error) {
	sp, err := s.declaredSpan(name)
	if err != nil {
		return SpanDef{}, err
	}
	if len(sp.Statuses) == 0 {
		return SpanDef{}, fmt.Errorf("span %s declares no statuses", name)
	}
	if s.schema.NullSpan(name, typ) {
		return SpanDef{}, fmt.Errorf("type %q must have a null %s, which has no status", typ, name)
	}
	return sp, nil
}

// SetSpanStatus sets the draft status of the span called name. Errors: unknown span, a span that declares no
// statuses, a status not in the span's list, a span that is null for the draft type.
func (s *Session) SetSpanStatus(i int, name, status string) error {
	d := s.Draft(i)
	sp, err := s.statusSpan(name, d.Type)
	if err != nil {
		return err
	}
	if !slices.Contains(sp.Statuses, status) {
		return fmt.Errorf("status %q is not one of [%s] for span %s", status, strings.Join(sp.Statuses, ", "), name)
	}
	if d.SpanStatus == nil {
		d.SpanStatus = map[string]string{}
	}
	d.SpanStatus[name] = status
	s.setDraft(i, d)
	return nil
}

// CycleSpanStatus advances the draft status of the span called name to the next one in the span's list, wrapping
// (from the effective status: unset counts as the first listed status), and returns it. Errors as SetSpanStatus.
func (s *Session) CycleSpanStatus(i int, name string) (string, error) {
	d := s.Draft(i)
	sp, err := s.statusSpan(name, d.Type)
	if err != nil {
		return "", err
	}
	cur, _ := s.schema.effectiveSpanStatus(sp, d)
	next := sp.Statuses[(slices.Index(sp.Statuses, cur)+1)%len(sp.Statuses)]
	if d.SpanStatus == nil {
		d.SpanStatus = map[string]string{}
	}
	d.SpanStatus[name] = next
	s.setDraft(i, d)
	return next, nil
}

// SpanStatus returns the effective status of the span called name in the draft of record i and whether it is the
// default (the draft has none set, so Mark would use the span's first listed status). status is "" when the span is
// unknown, declares no statuses or is null for the draft type.
func (s *Session) SpanStatus(i int, name string) (status string, isDefault bool) {
	sp, ok := s.schema.Span(name)
	if !ok {
		return "", false
	}
	return s.schema.effectiveSpanStatus(sp, s.Draft(i))
}

// Mark saves the draft as a label with status: builds Label (existing Note preserved; type, spans and span statuses
// dropped when the schema lists status in null_label_statuses; otherwise spans null for the draft type forced null
// and every status-bearing span that is not null for the type given its draft status or the default), Validate,
// write file atomically (save); on write failure the in-memory label is restored. Success: draft dropped, session
// mark count +1, the replaced label (or its absence) pushed on the undo stack. An existing label is overwritten in
// place, so the file keeps one line per id. Does NOT move the cursor.
func (s *Session) Mark(i int, status string) error {
	return s.mark(i, status, s.Draft(i), "")
}

// mark is the save path shared by Mark and AcceptProposal: it builds the label of record i from status, the type,
// spans and span statuses of d and note (the existing saved note when note is empty), then validates, saves, drops
// the draft, counts the mark and pushes the undo entry. Nothing changes on error.
func (s *Session) mark(i int, status string, d Draft, note string) error {
	if note == "" {
		note = s.labels[s.items[i].ID].Note
	}
	l, err := s.composeLabel(s.items[i].ID, status, d, note)
	if err != nil {
		return err
	}
	if err := s.schema.Validate(l, s.items[i].Text); err != nil {
		return err
	}
	return s.commit(map[int]Label{i: l})
}

// composeLabel builds the label of record id from status, the type, spans and span statuses of d and note as
// given. A status the schema lists in null_label_statuses yields a null type and null spans; otherwise spans null for
// the type are forced null and every status-bearing span that is not null for the type gets its draft status or
// the default. Error: status not declared. The label is not validated against the record text.
func (s *Session) composeLabel(id, status string, d Draft, note string) (Label, error) {
	if !s.schema.HasStatus(status) {
		return Label{}, fmt.Errorf("status %q is not declared in the schema", status)
	}
	l := Label{ID: id, Status: status, Spans: make(map[string]*Target, len(s.schema.Spans)), Note: note}
	nullLabel := s.schema.NullLabel(status)
	if !nullLabel && d.Type != "" {
		l.Type = new(d.Type)
	}
	for _, sp := range s.schema.Spans {
		l.Spans[sp.Name] = nil
		if nullLabel || s.schema.NullSpan(sp.Name, d.Type) {
			continue
		}
		l.Spans[sp.Name] = copyTarget(d.Spans[sp.Name])
		if st, _ := s.schema.effectiveSpanStatus(sp, d); st != "" {
			if l.SpanStatus == nil {
				l.SpanStatus = map[string]string{}
			}
			l.SpanStatus[sp.Name] = st
		}
	}
	return l, nil
}

// SaveLabel saves l as the label of record i through the same path as Mark: status must be declared; a status in
// null_label_statuses drops type, spans and span statuses; otherwise spans null for l's type are forced null and each
// status-bearing span gets l's span status or the default; then Validate against the record text, atomic write
// (save), draft dropped and one undo entry. l may come from any source (a collaborator's label): l.ID is ignored
// (the id of record i is used) and l.Note is kept as given ("" = no note, not the previous note). Nothing changes
// on error. Does NOT move the cursor.
func (s *Session) SaveLabel(i int, l Label) error {
	if i < 0 || i >= len(s.items) {
		return fmt.Errorf("record index %d out of range", i)
	}
	built, err := s.fromLabel(i, l)
	if err != nil {
		return err
	}
	return s.commit(map[int]Label{i: built})
}

// SaveLabels saves many records, keyed by queue index, like SaveLabel but with ONE file write and ONE undo entry
// (Undo reverts the whole batch). Nothing is changed when any label fails (the error names the record id; records
// are checked in queue order) or the write fails. An empty map does nothing.
func (s *Session) SaveLabels(labels map[int]Label) error {
	if len(labels) == 0 {
		return nil
	}
	built := make(map[int]Label, len(labels))
	for _, i := range sortedIndexes(labels) {
		if i < 0 || i >= len(s.items) {
			return fmt.Errorf("record index %d out of range", i)
		}
		b, err := s.fromLabel(i, labels[i])
		if err != nil {
			return fmt.Errorf("record %s: %w", s.items[i].ID, err)
		}
		built[i] = b
	}
	return s.commit(built)
}

// fromLabel builds and validates the label to save for record i from the foreign label l (see SaveLabel).
func (s *Session) fromLabel(i int, l Label) (Label, error) {
	item := s.items[i]
	d := Draft{Spans: l.Spans, SpanStatus: l.SpanStatus}
	if l.Type != nil {
		d.Type = *l.Type
	}
	built, err := s.composeLabel(item.ID, l.Status, d, l.Note)
	if err != nil {
		return Label{}, err
	}
	if !s.schema.NullLabel(l.Status) {
		// compose only keeps statuses of declared spans; keep the others so Validate rejects them.
		for name, st := range l.SpanStatus {
			if _, declared := s.schema.Span(name); !declared {
				if built.SpanStatus == nil {
					built.SpanStatus = map[string]string{}
				}
				built.SpanStatus[name] = st
			}
		}
	}
	if err := s.schema.Validate(built, item.Text); err != nil {
		return Label{}, err
	}
	return built, nil
}

// sortedIndexes returns the keys of m in ascending order.
func sortedIndexes(m map[int]Label) []int {
	idx := make([]int, 0, len(m))
	for i := range m {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	return idx
}

// commit stores the already validated labels (by queue index) as the saved labels of their records and writes the
// labels file once (save). On success the drafts of those records are dropped, the mark count grows by one per
// record and one undo entry holding every replaced label is pushed. On a write failure the in-memory labels are
// restored and nothing else changes.
func (s *Session) commit(built map[int]Label) error {
	idx := sortedIndexes(built)
	entry := undoEntry{items: make([]undoItem, 0, len(idx))}
	for _, i := range idx {
		prev, had := s.labels[s.items[i].ID]
		entry.items = append(entry.items, undoItem{index: i, had: had, prev: copyLabel(prev)})
	}
	for _, i := range idx {
		s.labels[s.items[i].ID] = built[i]
	}
	if err := s.save(); err != nil {
		for _, it := range entry.items {
			s.restoreLabel(s.items[it.index].ID, it.had, it.prev)
		}
		return err
	}
	s.undo = append(s.undo, entry)
	for _, i := range idx {
		delete(s.drafts, i)
	}
	s.marked += len(idx)
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

// save persists the saved labels: WriteLabels in a normal session; in a re-check session writeMergedLabels, whose
// written file becomes the new baseline.
func (s *Session) save() error {
	if s.recheck == nil {
		return WriteLabels(s.outPath, s.schema, s.items, s.labels)
	}
	base, err := writeMergedLabels(s.outPath, s.schema, s.recheck.orig, s.recheck.base, s.items, s.labels)
	if err != nil {
		return err
	}
	s.recheck.base = base
	return nil
}

// CanUndo reports whether Undo has a save to revert.
func (s *Session) CanUndo() bool { return len(s.undo) > 0 }

// Undo reverts the most recent successful save of this session not yet undone (Mark, SaveLabel or SaveLabels):
// restores each record's previous saved label (or removes it if it had none), rewrites the labels file atomically
// (save) once, drops the records' drafts, moves the cursor to the first of them and decrements the session mark
// count by the number of records (not below 0). Returns that queue index and a short description: for one record
// "restored <status>" or "removed label", for a batch "reverted <n> labels". ok=false when there is nothing to undo.
// On write failure the in-memory state is left as before the Undo, the entry stays on the stack and err is returned.
func (s *Session) Undo() (index int, desc string, ok bool, err error) {
	if len(s.undo) == 0 {
		return 0, "", false, nil
	}
	e := s.undo[len(s.undo)-1]
	type current struct {
		label Label
		had   bool
	}
	cur := make([]current, len(e.items))
	for k, it := range e.items {
		id := s.items[it.index].ID
		cur[k].label, cur[k].had = s.labels[id]
		s.restoreLabel(id, it.had, it.prev)
	}
	first := e.items[0].index
	if err := s.save(); err != nil {
		for k, it := range e.items {
			s.restoreLabel(s.items[it.index].ID, cur[k].had, cur[k].label)
		}
		return first, "", false, err
	}
	s.undo = s.undo[:len(s.undo)-1]
	for _, it := range e.items {
		delete(s.drafts, it.index)
	}
	s.cursor = first
	s.marked = max(0, s.marked-len(e.items))
	if len(e.items) > 1 {
		return first, fmt.Sprintf("reverted %d labels", len(e.items)), true, nil
	}
	if e.items[0].had {
		return first, "restored " + e.items[0].prev.Status, true, nil
	}
	return first, "removed label", true, nil
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
// complete/uncertain/skipped match only FilterAll. The proposal filters match only in a session with proposals
// loaded (matchesProposal).
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
	case FilterProposed, FilterUnproposed, FilterProposedUncertain:
		return s.matchesProposal(i, f)
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
// (cursor unchanged). In a re-check session, where labeled records are the point, it is NextMatch: the next record
// matching the filter, labeled or not, without wrapping.
func (s *Session) Advance() bool {
	if s.recheck != nil {
		return s.NextMatch()
	}
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

// draftEqual reports whether a and b have the same type, the same spans (a missing span equals a null one) and the
// same effective span status for every span (an unset status equals the span's default).
func (s *Session) draftEqual(a, b Draft) bool {
	if a.Type != b.Type || !spansEqual(a.Spans, b.Spans) {
		return false
	}
	for _, sp := range s.schema.Spans {
		as, _ := s.schema.effectiveSpanStatus(sp, a)
		bs, _ := s.schema.effectiveSpanStatus(sp, b)
		if as != bs {
			return false
		}
	}
	return true
}

// effectiveSpanStatus returns the status of span sp in draft d: the draft's entry, else the span's first listed status
// (isDefault). It is "" when sp declares no statuses or is null for d's type.
func (s *Schema) effectiveSpanStatus(sp SpanDef, d Draft) (status string, isDefault bool) {
	if len(sp.Statuses) == 0 || s.NullSpan(sp.Name, d.Type) {
		return "", false
	}
	if st, ok := d.SpanStatus[sp.Name]; ok {
		return st, false
	}
	return sp.Statuses[0], true
}
