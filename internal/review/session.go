package review

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/8bu/quet/internal/checks"
	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/corpus"
	"github.com/8bu/quet/internal/storage"
)

type FilterKind string

const (
	FilterAll       FilterKind = "all"
	FilterStatus    FilterKind = "status" // Value: a ReviewStatus
	FilterEdited    FilterKind = "edited"
	FilterAuto      FilterKind = "auto"      // Value: auto flag name, "" = any
	FilterManual    FilterKind = "manual"    // Value: manual flag name, "" = any
	FilterSuggested FilterKind = "suggested" // Value: suggested flag name, "" = any
	FilterSource    FilterKind = "source"
	FilterBatch     FilterKind = "batch"
)

type Filter struct {
	Kind  FilterKind
	Value string
}

// ParseFilter accepts: all | unreviewed | approved | rejected | needs_review | edited | auto[:name] | manual[:name] | suggested[:name] | source:x | batch:x.
func ParseFilter(s string) (Filter, error) {
	name, value, hasValue := strings.Cut(strings.TrimSpace(s), ":")
	value = strings.TrimSpace(value)

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "all", "":
		return Filter{Kind: FilterAll}, nil
	case "edited":
		return Filter{Kind: FilterEdited}, nil
	case "auto":
		if !hasValue {
			return Filter{Kind: FilterAuto}, nil
		}
		return Filter{Kind: FilterAuto, Value: value}, nil
	case "manual":
		if !hasValue {
			return Filter{Kind: FilterManual}, nil
		}
		return Filter{Kind: FilterManual, Value: value}, nil
	case "suggested":
		if !hasValue {
			return Filter{Kind: FilterSuggested}, nil
		}
		return Filter{Kind: FilterSuggested, Value: value}, nil
	case "source":
		return Filter{Kind: FilterSource, Value: value}, nil
	case "batch":
		return Filter{Kind: FilterBatch, Value: value}, nil
	}

	// Bare status names ("needs review" is accepted as written in the UI/palette).
	if st, err := ParseStatus(strings.ToLower(strings.TrimSpace(s))); err == nil {
		return Filter{Kind: FilterStatus, Value: string(st)}, nil
	}
	if st, err := ParseStatus(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "_"))); err == nil {
		return Filter{Kind: FilterStatus, Value: string(st)}, nil
	}
	return Filter{}, fmt.Errorf("unknown filter %q (want all, unreviewed, approved, rejected, needs_review, edited, auto[:name], manual[:name], suggested[:name], source:x, batch:x)", s)
}

// String is the inverse of ParseFilter (e.g. "unreviewed", "auto:duplicate", "all").
func (f Filter) String() string {
	switch f.Kind {
	case FilterEdited:
		return "edited"
	case FilterStatus:
		st, err := ParseStatus(f.Value)
		if err != nil {
			return string(f.Value)
		}
		return string(st)
	case FilterAuto:
		return withValue("auto", f.Value)
	case FilterManual:
		return withValue("manual", f.Value)
	case FilterSuggested:
		return withValue("suggested", f.Value)
	case FilterSource:
		return "source:" + f.Value
	case FilterBatch:
		return "batch:" + f.Value
	default:
		return "all"
	}
}

func withValue(prefix, value string) string {
	if value == "" {
		return prefix
	}
	return prefix + ":" + value
}

type Counts struct {
	Total, Unreviewed, Approved, Rejected, NeedsReview, Edited int
}

// track updates the counts for a state transition (old -> new).
func (c *Counts) track(old, next State) {
	if before, after := old.EffectiveStatus(), next.EffectiveStatus(); before != after {
		c.status(before, -1)
		c.status(after, +1)
	}
	if old.Edited() != next.Edited() {
		if next.Edited() {
			c.Edited++
		} else {
			c.Edited--
		}
	}
}

func (c *Counts) status(st ReviewStatus, delta int) {
	switch st {
	case Approved:
		c.Approved += delta
	case Rejected:
		c.Rejected += delta
	case NeedsReview:
		c.NeedsReview += delta
	default:
		c.Unreviewed += delta
	}
}

// Session is the single source of truth for a review run. Not safe for concurrent use.
type Session struct {
	Corpus       *corpus.Corpus
	Analysis     *checks.Analysis
	Store        *storage.Store
	Config       config.Config
	FlagDefs     []config.FlagDef
	SkipReviewed bool

	states    []State         // per corpus index; states[i].Status == "" means Unreviewed
	byID      map[string]int  // record ID -> corpus index
	loadFlags [][]checks.Flag // corpus-wide analysis flags at load, per corpus index
	view      []int           // filtered snapshot of corpus indices (never rebuilt implicitly)
	pos       int             // position in view; meaningless when view is empty
	filter    Filter          // filter that built view
	counts    Counts          // incremental counters (never scanned per render)
	started   time.Time       // session start (Rate base)
	actions   []time.Time     // timestamps of this session's persisted mutations
}

var errNoCurrent = errors.New("review: no current record")

// Open loads the corpus, opens/creates the sidecar DB (storage.SidecarPath), restores states (by record ID), runs checks.Analyze
// on final texts, applies Filter{FilterAll} and positions on the first unresolved record (or 0).
func Open(corpusPath string, cfg config.Config, flags []config.FlagDef) (*Session, error) {
	c, err := corpus.Load(corpusPath)
	if err != nil {
		return nil, err
	}
	store, err := storage.Open(storage.SidecarPath(corpusPath))
	if err != nil {
		return nil, err
	}
	persisted, err := store.LoadAll()
	if err != nil {
		store.Close()
		return nil, err
	}
	return newSession(c, store, persisted, cfg, flags), nil
}

// newSession wires a session from an already loaded corpus and sidecar store. c must not be nil.
func newSession(c *corpus.Corpus, store *storage.Store, persisted map[string]storage.RecordState, cfg config.Config, flags []config.FlagDef) *Session {
	s := &Session{
		Corpus:       c,
		Store:        store,
		Config:       cfg,
		FlagDefs:     flags,
		SkipReviewed: cfg.Review.SkipReviewed,
		started:      time.Now(),
	}
	n := len(c.Records)
	s.states = make([]State, n)
	s.byID = make(map[string]int, n)
	texts := make([]string, n)
	for i := range c.Records {
		rec := &c.Records[i]
		s.byID[rec.ID] = i
		if rs, ok := persisted[rec.ID]; ok {
			s.states[i] = stateFromStorage(rs)
		}
		texts[i] = s.FinalText(i)
	}
	s.counts = Counts{Total: n, Unreviewed: n}
	for i := range s.states {
		s.counts.track(State{}, s.states[i])
	}
	s.Analysis = checks.Analyze(texts, cfg.Checks)
	s.loadFlags = s.Analysis.Flags
	s.SetFilter(Filter{Kind: FilterAll})
	return s
}

func (s *Session) Close() error {
	if s.Store == nil {
		return nil
	}
	return s.Store.Close()
}

func (s *Session) Len() int { return len(s.Corpus.Records) }

func (s *Session) Record(i int) *corpus.Record {
	if i < 0 || i >= len(s.Corpus.Records) {
		return nil
	}
	return &s.Corpus.Records[i]
}

// State returns a copy of record i's review state; callers must not mutate session state.
func (s *Session) State(i int) State {
	if i < 0 || i >= len(s.states) {
		return State{}
	}
	return s.states[i].Clone()
}

// FinalText is the text that would be exported: the edit when there is one, else the imported text.
func (s *Session) FinalText(i int) string {
	if i < 0 || i >= len(s.Corpus.Records) {
		return ""
	}
	if st := s.states[i]; st.EditedText != nil {
		return *st.EditedText
	}
	return s.Corpus.Records[i].Text
}

// AutoFlags returns the auto flags of record i: the corpus-wide analysis from load, or — for an
// edited record — freshly computed per-record flags merged with the corpus-wide flags
// (duplicate, possible_template) the record had at load.
func (s *Session) AutoFlags(i int) []checks.Flag {
	if i < 0 || i >= len(s.states) {
		return nil
	}
	loaded := s.loadedFlags(i)
	if !s.states[i].Edited() {
		return loaded
	}
	merged := checks.Single(s.FinalText(i), s.Config.Checks)
	for _, f := range loaded {
		if f.Name == checks.Duplicate || f.Name == checks.PossibleTemplate {
			if !hasFlagName(merged, f.Name) {
				merged = append(merged, f)
			}
		}
	}
	return merged
}

// loadedFlags copies the load-time analysis flags of record i.
func (s *Session) loadedFlags(i int) []checks.Flag {
	if i < 0 || i >= len(s.loadFlags) {
		return nil
	}
	return append([]checks.Flag(nil), s.loadFlags[i]...)
}

func hasFlagName(flags []checks.Flag, name string) bool {
	for _, f := range flags {
		if f.Name == name {
			return true
		}
	}
	return false
}

// Current returns the current record index (-1 if view empty).
func (s *Session) Current() int {
	if len(s.view) == 0 {
		return -1
	}
	return s.view[s.pos]
}

// Position returns (0-based position in view, view length).
func (s *Session) Position() (int, int) { return s.pos, len(s.view) }

func (s *Session) Filter() Filter { return s.filter }

// SetFilter rebuilds the view snapshot (statuses changing later do NOT remove items from it); keeps current if still in view,
// else moves to first unresolved in view (or first). Returns view length.
func (s *Session) SetFilter(f Filter) int {
	if f.Kind == FilterStatus {
		if st, err := ParseStatus(f.Value); err == nil {
			f.Value = string(st)
		}
	}
	current := s.Current()
	view := make([]int, 0, len(s.Corpus.Records))
	for i := range s.Corpus.Records {
		if s.matches(i, f) {
			view = append(view, i)
		}
	}
	s.filter = f
	s.view = view

	if current >= 0 {
		if p := viewIndex(view, current); p >= 0 {
			s.pos = p
			return len(view)
		}
	}
	if p := s.firstUnresolvedPos(); p >= 0 {
		s.pos = p
	} else {
		s.pos = 0
	}
	return len(view)
}

// View returns filtered record indices (do not mutate).
func (s *Session) View() []int { return s.view }

// Goto jumps to record index i; if i is not in the view, the filter is reset to all first.
func (s *Session) Goto(i int) {
	if i < 0 || i >= len(s.Corpus.Records) {
		return
	}
	if p := viewIndex(s.view, i); p >= 0 {
		s.pos = p
		return
	}
	s.SetFilter(Filter{Kind: FilterAll})
	if p := viewIndex(s.view, i); p >= 0 {
		s.pos = p
	}
}

// Next moves one record forward in the view; false when already at the end.
func (s *Session) Next() bool {
	if s.pos+1 >= len(s.view) {
		return false
	}
	s.pos++
	return true
}

// Prev moves one record back in the view; false when already at the start.
func (s *Session) Prev() bool {
	if len(s.view) == 0 || s.pos <= 0 {
		return false
	}
	s.pos--
	return true
}

func (s *Session) First() {
	if len(s.view) > 0 {
		s.pos = 0
	}
}

func (s *Session) Last() {
	if len(s.view) > 0 {
		s.pos = len(s.view) - 1
	}
}

// NextUnresolved advances to next Unreviewed record in view after current (when SkipReviewed), else plain Next. Returns false if none (stays).
func (s *Session) NextUnresolved() bool {
	if !s.SkipReviewed {
		return s.Next()
	}
	for p := s.pos + 1; p < len(s.view); p++ {
		if s.states[s.view[p]].EffectiveStatus() == Unreviewed {
			s.pos = p
			return true
		}
	}
	return false
}

// SetStatus persists status for current record then (for Approved/Rejected/NeedsReview) auto-advances via NextUnresolved.
// Returns whether it advanced.
func (s *Session) SetStatus(st ReviewStatus) (bool, error) {
	i := s.Current()
	if i < 0 {
		return false, errNoCurrent
	}
	switch st {
	case Unreviewed, Approved, Rejected, NeedsReview:
	default:
		return false, fmt.Errorf("review: unknown status %q", st)
	}
	before := s.states[i]
	if before.EffectiveStatus() == st {
		return false, nil
	}
	after := before.Clone()
	after.Status = st
	if err := s.persist(i, "status", before, after); err != nil {
		return false, err
	}
	if st == Unreviewed {
		return false, nil
	}
	return s.NextUnresolved(), nil
}

// SaveEdit persists edited text for current record (text equal to original => clears edit). Recomputes its Single() flags.
func (s *Session) SaveEdit(text string) error {
	i := s.Current()
	if i < 0 {
		return errNoCurrent
	}
	before := s.states[i]
	after := before.Clone()
	if text == s.Corpus.Records[i].Text {
		after.EditedText = nil
	} else {
		after.EditedText = new(text)
	}
	if s.FinalText(i) == finalTextOf(after, s.Corpus.Records[i].Text) {
		return nil // nothing changed
	}
	return s.persist(i, "edit", before, after)
}

// RevertEdit clears the edit for current record.
func (s *Session) RevertEdit() error {
	i := s.Current()
	if i < 0 {
		return errNoCurrent
	}
	before := s.states[i]
	if !before.Edited() {
		return nil
	}
	after := before.Clone()
	after.EditedText = nil
	return s.persist(i, "edit", before, after)
}

// SetManualFlags persists manual flags (sorted, unique) for current record.
func (s *Session) SetManualFlags(flags []string) error {
	i := s.Current()
	if i < 0 {
		return errNoCurrent
	}
	cleaned := cleanFlags(flags)
	before := s.states[i]
	if equalStrings(before.ManualFlags, cleaned) {
		return nil
	}
	after := before.Clone()
	after.ManualFlags = cleaned
	return s.persist(i, "flags", before, after)
}

// Undo reverts the most recent persisted mutation (survives restarts), moves current to that record. ok=false if nothing to undo.
func (s *Session) Undo() (ok bool, recordIndex int, err error) {
	id, restored, ok, err := s.Store.Undo()
	if err != nil || !ok {
		return false, -1, err
	}
	i, found := s.byID[id]
	if !found {
		return true, -1, nil
	}
	s.setState(i, stateFromStorage(restored))
	s.Goto(i)
	return true, i, nil
}

// persist writes one mutation to the sidecar, applies it in memory and records it for Rate().
func (s *Session) persist(i int, kind string, before, after State) error {
	if err := s.Store.Apply(s.Corpus.Records[i].ID, s.Corpus.Records[i].Text, kind, toStorage(before), toStorage(after)); err != nil {
		return err
	}
	s.setState(i, after)
	s.actions = append(s.actions, time.Now())
	return nil
}

// setState replaces record i's state and keeps the counters in sync.
func (s *Session) setState(i int, st State) {
	old := s.states[i]
	s.states[i] = st
	s.counts.track(old, st)
}

func (s *Session) Counts() Counts { return s.counts }

// Rate is reviews (status changes) per minute during this session; 0 until >=2 actions.
func (s *Session) Rate(now time.Time) float64 {
	if len(s.actions) < 2 {
		return 0
	}
	span := now.Sub(s.started).Minutes()
	if span <= 0 {
		return 0
	}
	return float64(len(s.actions)-1) / span
}

// Search does case-insensitive (Normalize-d) substring match over final text, original text, and metadata field values; returns up to limit record indices.
func (s *Session) Search(q string, limit int) []int {
	needle := checks.Normalize(q)
	raw := strings.ToLower(strings.TrimSpace(q))
	if needle == "" && raw == "" {
		return nil
	}
	var out []int
	for i := range s.Corpus.Records {
		if s.matchesQuery(i, needle, raw) {
			out = append(out, i)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

func (s *Session) matchesQuery(i int, needle, raw string) bool {
	rec := &s.Corpus.Records[i]
	if needle != "" {
		if strings.Contains(checks.Normalize(rec.Text), needle) {
			return true
		}
		if text := s.FinalText(i); text != rec.Text && strings.Contains(checks.Normalize(text), needle) {
			return true
		}
	}
	for _, f := range rec.Fields {
		if needle != "" && strings.Contains(checks.Normalize(f.Value), needle) {
			return true
		}
		if raw != "" && strings.Contains(strings.ToLower(f.Value), raw) {
			return true
		}
	}
	return false
}

// Duplicates returns the other record indices in i's duplicate group.
func (s *Session) Duplicates(i int) []int {
	if s.Analysis == nil || i < 0 || i >= len(s.Analysis.DupGroup) {
		return nil
	}
	group := s.Analysis.DupGroup[i]
	if group < 0 || int(group) >= len(s.Analysis.Groups) {
		return nil
	}
	members := s.Analysis.Groups[group]
	others := make([]int, 0, len(members))
	for _, idx := range members {
		if idx != i {
			others = append(others, idx)
		}
	}
	return others
}

// Facets for filter pickers (sorted, with counts).
type Facet struct {
	Value string
	Count int
}

// Sources returns the corpus-wide "source" facet counts, sorted by value.
func (s *Session) Sources() []Facet {
	counts := make(map[string]int)
	for i := range s.Corpus.Records {
		if v := s.Corpus.Records[i].Source; v != "" {
			counts[v]++
		}
	}
	return sortedFacets(counts)
}

// Batches returns the corpus-wide "batch" facet counts, sorted by value.
func (s *Session) Batches() []Facet {
	counts := make(map[string]int)
	for i := range s.Corpus.Records {
		if v := s.Corpus.Records[i].Batch; v != "" {
			counts[v]++
		}
	}
	return sortedFacets(counts)
}

// AutoFlagFacets returns corpus-wide auto flag counts, sorted by flag name.
func (s *Session) AutoFlagFacets() []Facet {
	counts := make(map[string]int)
	for i := range s.Corpus.Records {
		for _, f := range s.AutoFlags(i) {
			counts[f.Name]++
		}
	}
	return sortedFacets(counts)
}

// SuggestedFacets returns corpus-wide suggested flag counts, sorted by flag name.
func (s *Session) SuggestedFacets() []Facet {
	counts := make(map[string]int)
	for i := range s.Corpus.Records {
		for _, name := range s.Corpus.Records[i].SuggestedFlags {
			counts[name]++
		}
	}
	return sortedFacets(counts)
}

// ManualFacets returns corpus-wide manual flag counts, sorted by flag name. Flags defined in
// flags.yaml are always listed, with count 0 when unused, so the picker can show them.
func (s *Session) ManualFacets() []Facet {
	counts := make(map[string]int)
	for i := range s.states {
		for _, name := range s.states[i].ManualFlags {
			counts[name]++
		}
	}
	for _, def := range s.FlagDefs {
		if _, ok := counts[def.Name]; !ok {
			counts[def.Name] = 0
		}
	}
	return sortedFacets(counts)
}

func sortedFacets(counts map[string]int) []Facet {
	out := make([]Facet, 0, len(counts))
	for value, count := range counts {
		out = append(out, Facet{Value: value, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

// matches reports whether record i satisfies f.
func (s *Session) matches(i int, f Filter) bool {
	rec := &s.Corpus.Records[i]
	st := s.states[i]
	switch f.Kind {
	case FilterAll:
		return true
	case FilterStatus:
		want, err := ParseStatus(f.Value)
		return err == nil && st.EffectiveStatus() == want
	case FilterEdited:
		return st.Edited()
	case FilterAuto:
		return flagMatches(f.Value, autoFlagNames(s.AutoFlags(i)))
	case FilterManual:
		return flagMatches(f.Value, st.ManualFlags)
	case FilterSuggested:
		return flagMatches(f.Value, rec.SuggestedFlags)
	case FilterSource:
		return rec.Source == f.Value
	case FilterBatch:
		return rec.Batch == f.Value
	default:
		return false
	}
}

// flagMatches: "" matches any non-empty set, else case-insensitive membership.
func flagMatches(want string, names []string) bool {
	if want == "" {
		return len(names) > 0
	}
	for _, name := range names {
		if strings.EqualFold(name, want) {
			return true
		}
	}
	return false
}

func autoFlagNames(flags []checks.Flag) []string {
	if len(flags) == 0 {
		return nil
	}
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		names = append(names, f.Name)
	}
	return names
}

func (s *Session) firstUnresolvedPos() int {
	for p, i := range s.view {
		if s.states[i].EffectiveStatus() == Unreviewed {
			return p
		}
	}
	return -1
}

func viewIndex(view []int, record int) int {
	for p, i := range view {
		if i == record {
			return p
		}
	}
	return -1
}

// stateFromStorage converts persisted state to in-memory state, canonicalizing statuses.
func stateFromStorage(rs storage.RecordState) State {
	st := State{
		EditedText:  rs.EditedText,
		ManualFlags: rs.ManualFlags,
		Annotations: rs.Annotations,
	}
	if status, err := ParseStatus(rs.Status); err == nil {
		st.Status = status
	}
	if len(st.ManualFlags) == 0 {
		st.ManualFlags = nil
	}
	return st
}

// toStorage converts in-memory state to persisted state; the status is always written explicitly.
func toStorage(st State) storage.RecordState {
	return storage.RecordState{
		Status:      string(st.EffectiveStatus()),
		EditedText:  st.EditedText,
		ManualFlags: st.ManualFlags,
		Annotations: st.Annotations,
	}
}

func finalTextOf(st State, original string) string {
	if st.EditedText != nil {
		return *st.EditedText
	}
	return original
}

// cleanFlags trims, drops empties, deduplicates and sorts flag names.
func cleanFlags(flags []string) []string {
	seen := make(map[string]bool, len(flags))
	out := make([]string, 0, len(flags))
	for _, name := range flags {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
