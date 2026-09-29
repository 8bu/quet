package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/8bu/quet/internal/checks"
	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/storage"
)

// corpusLines is the shared fixture: n1..n4 with one duplicate pair (n2/n4) and one
// repeated-character record (n3).
var corpusLines = []string{
	`{"id":"n1","text":"cho Nam vay 2tr","source":"claude","batch":"b1","suggested_flags":["slang"]}`,
	`{"id":"n2","text":"CK Nam 2tr","source":"claude","batch":"b1","suggested_flags":[]}`,
	`{"id":"n3","text":"aaaaaaaaaa","source":"qwen","batch":"b2","suggested_flags":["typo"]}`,
	`{"id":"n4","text":"CK Nam 2tr","source":"qwen","batch":"b2"}`,
}

var testFlagDefs = []config.FlagDef{
	{Name: "slang", Description: "Notable slang."},
	{Name: "typo", Description: "Typo."},
	{Name: "unused", Description: "Never applied."},
}

func testConfig() config.Config {
	return config.Config{Checks: checks.Options{
		MaxChars:               40,
		RepeatedCharThreshold:  5,
		WeirdSymbolRatio:       0.35,
		TemplateMinOccurrences: 8,
	}}
}

// writeCorpus writes lines as <dir>/corpus.jsonl and returns its path.
func writeCorpus(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "corpus.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	return path
}

func openSession(t *testing.T, path string, cfg config.Config) *Session {
	t.Helper()
	s, err := Open(path, cfg, testFlagDefs)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func autoAdvanceConfig() config.Config {
	cfg := testConfig()
	cfg.Review.SkipReviewed = true
	return cfg
}

func flagNames(flags []checks.Flag) []string {
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		names = append(names, f.Name)
	}
	return names
}

func TestOpenStartsOnFirstUnreviewed(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())

	if _, err := os.Stat(storage.SidecarPath(path)); err != nil {
		t.Fatalf("sidecar %s not created: %v", storage.SidecarPath(path), err)
	}
	if got := s.Len(); got != 4 {
		t.Fatalf("Len = %d, want 4", got)
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("Current = %d, want 0 (first unreviewed)", got)
	}
	if got := s.Filter(); got.Kind != FilterAll {
		t.Fatalf("Filter = %+v, want all", got)
	}
	if got := s.Counts(); got != (Counts{Total: 4, Unreviewed: 4}) {
		t.Fatalf("Counts = %+v, want 4 unreviewed", got)
	}
	if pos, n := s.Position(); pos != 0 || n != 4 {
		t.Fatalf("Position = (%d, %d), want (0, 4)", pos, n)
	}
	if rec := s.Record(1); rec == nil || rec.ID != "id:n2" || rec.Text != "CK Nam 2tr" {
		t.Fatalf("Record(1) = %+v, want n2", rec)
	}
	if got := s.FinalText(1); got != "CK Nam 2tr" {
		t.Fatalf("FinalText(1) = %q", got)
	}
	if s.Record(-1) != nil || s.Record(4) != nil {
		t.Fatalf("Record out of range must be nil")
	}
}

func TestStatusesPersistAndAutoAdvance(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())

	advanced, err := s.SetStatus(Approved)
	if err != nil || !advanced {
		t.Fatalf("SetStatus(approved) = (%v, %v), want advanced", advanced, err)
	}
	if got := s.Current(); got != 1 {
		t.Fatalf("Current after approve = %d, want 1", got)
	}
	if got := s.Counts(); got != (Counts{Total: 4, Unreviewed: 3, Approved: 1}) {
		t.Fatalf("Counts = %+v", got)
	}

	if advanced, err = s.SetStatus(Rejected); err != nil || !advanced {
		t.Fatalf("SetStatus(rejected) = (%v, %v)", advanced, err)
	}
	if advanced, err = s.SetStatus(NeedsReview); err != nil || !advanced {
		t.Fatalf("SetStatus(needs_review) = (%v, %v)", advanced, err)
	}
	if got := s.Current(); got != 3 {
		t.Fatalf("Current = %d, want 3", got)
	}
	if advanced, err = s.SetStatus(Approved); err != nil || advanced {
		t.Fatalf("SetStatus(approved) at last unresolved = (%v, %v), want no advance", advanced, err)
	}
	if got := s.Counts(); got != (Counts{Total: 4, Approved: 2, Rejected: 1, NeedsReview: 1}) {
		t.Fatalf("Counts = %+v", got)
	}

	// Re-applying the same status is a no-op: no extra event.
	before, err := s.Store.CountEvents()
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if advanced, err = s.SetStatus(Approved); err != nil || advanced {
		t.Fatalf("repeat SetStatus(approved) = (%v, %v)", advanced, err)
	}
	after, err := s.Store.CountEvents()
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if after != before {
		t.Fatalf("events = %d -> %d, want no new event for a repeated status", before, after)
	}

	if _, err := s.SetStatus(ReviewStatus("bogus")); err == nil {
		t.Fatalf("SetStatus(bogus) must fail")
	}
}

func TestSkipReviewedControlsAdvance(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	cfg := autoAdvanceConfig()
	s := openSession(t, path, cfg)

	if _, err := s.SetStatus(Approved); err != nil { // n1 -> n2
		t.Fatalf("SetStatus: %v", err)
	}
	s.Goto(2)
	if _, err := s.SetStatus(NeedsReview); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := s.Current(); got != 3 {
		t.Fatalf("Current = %d, want 3 (skipping reviewed n2)", got)
	}
	// Nothing unreviewed ahead: wrap back to the skipped n1.
	if advanced, err := s.SetStatus(Approved); err != nil || !advanced {
		t.Fatalf("SetStatus at end = (%v, %v), want wrap", advanced, err)
	}
	if got := s.Current(); got != 1 {
		t.Fatalf("Current = %d, want 1 (wrapped to first unreviewed)", got)
	}
	// Everything reviewed: a revision pass steps record by record.
	if advanced, err := s.SetStatus(Rejected); err != nil || !advanced {
		t.Fatalf("SetStatus last unreviewed = (%v, %v), want advance", advanced, err)
	}
	if got := s.Current(); got != 2 {
		t.Fatalf("Current = %d, want 2 (plain next once all reviewed)", got)
	}
	// Re-applying the existing status confirms it: no event, but it moves on.
	events, err := s.Store.CountEvents()
	if err != nil {
		t.Fatal(err)
	}
	if advanced, err := s.SetStatus(NeedsReview); err != nil || !advanced {
		t.Fatalf("confirm existing status = (%v, %v), want advance", advanced, err)
	}
	if got := s.Current(); got != 3 {
		t.Fatalf("Current = %d, want 3", got)
	}
	if after, _ := s.Store.CountEvents(); after != events {
		t.Fatalf("confirming existing status wrote %d event(s)", after-events)
	}
	if advanced, err := s.SetStatus(Approved); err != nil || advanced {
		t.Fatalf("SetStatus on last record, all reviewed = (%v, %v), want stay", advanced, err)
	}

	// skip_reviewed=false advances one record at a time, reviewed or not.
	plainPath := writeCorpus(t, corpusLines)
	plain := openSession(t, plainPath, testConfig())
	if plain.SkipReviewed {
		t.Fatalf("SkipReviewed = true, want false")
	}
	if _, err := plain.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := plain.Current(); got != 1 {
		t.Fatalf("Current = %d, want 1", got)
	}
	if !plain.Next() || plain.Current() != 2 {
		t.Fatalf("Next must move to 2")
	}
	if !plain.Next() || plain.Current() != 3 {
		t.Fatalf("Next must move to 3")
	}
	if plain.Next() {
		t.Fatalf("Next at the end must return false")
	}
	if !plain.Prev() || plain.Current() != 2 {
		t.Fatalf("Prev must move back to 2")
	}
	if !plain.NextUnresolved() || plain.Current() != 3 {
		t.Fatalf("NextUnresolved without skip = plain next, want 3")
	}
}

func TestNextUnresolvedStaysAtEnd(t *testing.T) {
	path := writeCorpus(t, []string{corpusLines[0], corpusLines[1]})
	s := openSession(t, path, autoAdvanceConfig())
	s.Goto(0)
	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := s.Current(); got != 1 {
		t.Fatalf("Current = %d, want 1", got)
	}
	if s.NextUnresolved() {
		t.Fatalf("NextUnresolved with no unresolved left must return false")
	}
	if got := s.Current(); got != 1 {
		t.Fatalf("Current moved to %d, want to stay at 1", got)
	}
}

func TestReconfigureRerunsChecksKeepingReviewState(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())

	if _, err := s.SetStatus(Approved); err != nil { // n1 -> n2
		t.Fatalf("SetStatus: %v", err)
	}
	if hasFlagName(s.AutoFlags(0), checks.TooLong) {
		t.Fatalf("n1 flagged too_long before reconfigure: %v", flagNames(s.AutoFlags(0)))
	}
	counts := s.Counts()
	pos, n := s.Position()

	cfg := autoAdvanceConfig()
	cfg.Checks.MaxChars = 12 // n1 has 15 chars; n2, n3 and n4 have 10
	s.Reconfigure(config.Settings{Config: cfg, Flags: testFlagDefs})

	if !hasFlagName(s.AutoFlags(0), checks.TooLong) {
		t.Fatalf("n1 auto flags = %v, want too_long", flagNames(s.AutoFlags(0)))
	}
	if hasFlagName(s.AutoFlags(1), checks.TooLong) {
		t.Fatalf("n2 auto flags = %v, want no too_long", flagNames(s.AutoFlags(1)))
	}
	if got := facetValue(s.AutoFlagFacets(), checks.TooLong); got != 1 {
		t.Fatalf("too_long facet = %d, want 1", got)
	}
	if got := s.State(0).EffectiveStatus(); got != Approved {
		t.Fatalf("status(0) = %v, want approved", got)
	}
	if got := s.Counts(); got != counts {
		t.Fatalf("Counts = %+v, want %+v", got, counts)
	}
	if gotPos, gotN := s.Position(); gotPos != pos || gotN != n {
		t.Fatalf("Position = (%d, %d), want (%d, %d)", gotPos, gotN, pos, n)
	}
	if got := s.Current(); got != 1 {
		t.Fatalf("Current = %d, want 1", got)
	}
	if s.Config.Checks.MaxChars != 12 {
		t.Fatalf("Config.Checks.MaxChars = %d, want 12", s.Config.Checks.MaxChars)
	}
}

func TestReconfigureSkipReviewedFollowsOnlyConfigChanges(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())
	s.SkipReviewed = false // runtime toggle

	s.Reconfigure(config.Settings{Config: autoAdvanceConfig(), Flags: testFlagDefs})
	if s.SkipReviewed {
		t.Fatalf("unrelated reload overrode the runtime toggle")
	}

	s.Reconfigure(config.Settings{Config: testConfig(), Flags: testFlagDefs}) // skip_reviewed true -> false
	if s.SkipReviewed {
		t.Fatalf("SkipReviewed = true after config turned it off")
	}
	s.Reconfigure(config.Settings{Config: autoAdvanceConfig(), Flags: testFlagDefs}) // false -> true
	if !s.SkipReviewed {
		t.Fatalf("SkipReviewed = false after config turned it on")
	}
}

func TestReconfigureReplacesFlagTaxonomy(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())
	s.FlagsPath = "/old/flags.yaml"

	s.Goto(0)
	if err := s.SetManualFlags([]string{"slang"}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}

	defs := []config.FlagDef{{Name: "ambiguous", Description: "Ambiguous."}}
	s.Reconfigure(config.Settings{Config: testConfig(), FlagsPath: "/new/flags.yaml", Flags: defs})

	if len(s.FlagDefs) != 1 || s.FlagDefs[0].Name != "ambiguous" {
		t.Fatalf("FlagDefs = %+v, want [ambiguous]", s.FlagDefs)
	}
	if s.FlagsPath != "/new/flags.yaml" {
		t.Fatalf("FlagsPath = %q, want /new/flags.yaml", s.FlagsPath)
	}
	if got := strings.Join(s.State(0).ManualFlags, ","); got != "slang" {
		t.Fatalf("ManualFlags(0) = %q, want slang kept after taxonomy change", got)
	}
	if got, want := facetString(s.ManualFacets()), "ambiguous=0,slang=1"; got != want {
		t.Fatalf("ManualFacets = %q, want %q", got, want)
	}

	s.Reconfigure(config.Settings{Config: testConfig()})
	if s.FlagDefs != nil || s.FlagsPath != "" {
		t.Fatalf("FlagDefs/FlagsPath = %+v/%q, want cleared", s.FlagDefs, s.FlagsPath)
	}
}

func TestEditRevertAndAutoFlags(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())

	s.Goto(2) // n3: "aaaaaaaaaa" -> repeated_chars from the corpus analysis
	if !hasFlagName(s.AutoFlags(2), checks.RepeatedChars) {
		t.Fatalf("AutoFlags(2) = %v, want repeated_chars", flagNames(s.AutoFlags(2)))
	}
	if err := s.SaveEdit("aaa bbb"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if got := s.FinalText(2); got != "aaa bbb" {
		t.Fatalf("FinalText(2) = %q, want the edit", got)
	}
	if st := s.State(2); !st.Edited() || st.EditedText == nil || *st.EditedText != "aaa bbb" {
		t.Fatalf("State(2) = %+v, want edited", st)
	}
	if hasFlagName(s.AutoFlags(2), checks.RepeatedChars) {
		t.Fatalf("AutoFlags(2) = %v, want recomputed flags without repeated_chars", flagNames(s.AutoFlags(2)))
	}
	if got := s.Record(2).Text; got != "aaaaaaaaaa" {
		t.Fatalf("original text was destroyed: %q", got)
	}
	if got := s.Counts(); got != (Counts{Total: 4, Unreviewed: 4, Edited: 1}) {
		t.Fatalf("Counts = %+v", got)
	}

	// Editing keeps the corpus-wide duplicate flag of the record.
	s.Goto(3)
	if err := s.SaveEdit("CK Nam 2tr edited"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if !hasFlagName(s.AutoFlags(3), checks.Duplicate) {
		t.Fatalf("AutoFlags(3) = %v, want the corpus-wide duplicate flag kept", flagNames(s.AutoFlags(3)))
	}

	// Revert restores the original text and the load-time flags.
	if err := s.RevertEdit(); err != nil {
		t.Fatalf("RevertEdit: %v", err)
	}
	if got := s.FinalText(3); got != "CK Nam 2tr" {
		t.Fatalf("FinalText(3) = %q after revert", got)
	}
	if st := s.State(3); st.Edited() {
		t.Fatalf("State(3) = %+v, want not edited", st)
	}
	if err := s.RevertEdit(); err != nil {
		t.Fatalf("RevertEdit when not edited must be a no-op, got %v", err)
	}

	// Saving the original text clears the edit.
	s.Goto(2)
	if err := s.SaveEdit("aaaaaaaaaa"); err != nil {
		t.Fatalf("SaveEdit(original): %v", err)
	}
	if st := s.State(2); st.Edited() {
		t.Fatalf("State(2) = %+v, want edit cleared", st)
	}
	if got := s.Counts().Edited; got != 0 {
		t.Fatalf("Edited count = %d, want 0", got)
	}
}

func TestUndoRestoresRevertedEdit(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())

	s.Goto(2)
	if err := s.SaveEdit("aaa bbb"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if err := s.RevertEdit(); err != nil {
		t.Fatalf("RevertEdit: %v", err)
	}
	if got := s.Counts().Edited; got != 0 {
		t.Fatalf("Edited = %d after revert, want 0", got)
	}

	ok, idx, err := s.Undo() // undo the revert: the edit comes back
	if err != nil || !ok || idx != 2 {
		t.Fatalf("Undo of revert = (%v, %d, %v), want (true, 2, nil)", ok, idx, err)
	}
	if got := s.FinalText(2); got != "aaa bbb" {
		t.Fatalf("FinalText(2) = %q, want the edit restored", got)
	}
	if st := s.State(2); !st.Edited() || st.EditedText == nil || *st.EditedText != "aaa bbb" {
		t.Fatalf("State(2) = %+v, want edited", st)
	}
	if got := s.Counts().Edited; got != 1 {
		t.Fatalf("Edited = %d after undo, want 1", got)
	}
	if hasFlagName(s.AutoFlags(2), checks.RepeatedChars) {
		t.Fatalf("AutoFlags(2) = %v, want recomputed flags for the restored edit", flagNames(s.AutoFlags(2)))
	}
}

func TestManualFlagsAndFacets(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())

	s.Goto(0)
	if err := s.SetManualFlags([]string{" typo ", "slang", "typo", "", "  "}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}
	if got, want := strings.Join(s.State(0).ManualFlags, ","), "slang,typo"; got != want {
		t.Fatalf("ManualFlags = %q, want %q", got, want)
	}
	if err := s.SetManualFlags([]string{"typo", "slang"}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}
	if got := s.State(0).ManualFlags; strings.Join(got, ",") != "slang,typo" {
		t.Fatalf("ManualFlags = %v, want unchanged", got)
	}
	if err := s.SetManualFlags(nil); err != nil {
		t.Fatalf("SetManualFlags(nil): %v", err)
	}
	if got := s.State(0).ManualFlags; got != nil {
		t.Fatalf("ManualFlags = %v, want nil", got)
	}

	if err := s.SetManualFlags([]string{"slang"}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}
	manual := s.ManualFacets()
	if got, want := facetString(manual), "slang=1,typo=0,unused=0"; got != want {
		t.Fatalf("ManualFacets = %s, want %s", got, want)
	}
	if got, want := facetString(s.Sources()), "claude=2,qwen=2"; got != want {
		t.Fatalf("Sources = %s, want %s", got, want)
	}
	if got, want := facetString(s.Batches()), "b1=2,b2=2"; got != want {
		t.Fatalf("Batches = %s, want %s", got, want)
	}
	if got, want := facetString(s.SuggestedFacets()), "slang=1,typo=1"; got != want {
		t.Fatalf("SuggestedFacets = %s, want %s", got, want)
	}
	if got := facetValue(s.AutoFlagFacets(), checks.Duplicate); got != 2 {
		t.Fatalf("AutoFlagFacets[duplicate] = %d, want 2", got)
	}
}

func TestSearch(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())

	if got := s.Search("CK   nam 2tr", 0); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("Search(duplicate text) = %v, want [1 3]", got)
	}
	if got := s.Search("qwen", 0); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("Search(raw metadata) = %v, want [2 3]", got)
	}
	if got := s.Search("typo", 0); len(got) != 1 || got[0] != 2 {
		t.Fatalf("Search(suggested flag metadata) = %v, want [2]", got)
	}
	if got := s.Search("nam", 1); len(got) != 1 {
		t.Fatalf("Search limit = %v, want exactly 1 hit", got)
	}
	if got := s.Search("   ", 0); got != nil {
		t.Fatalf("Search(empty) = %v, want nil", got)
	}
	if got := s.Search("nothing-here", 0); got != nil {
		t.Fatalf("Search(miss) = %v, want nil", got)
	}

	s.Goto(0)
	if err := s.SaveEdit("hihi"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if got := s.Search("cho nam vay", 0); len(got) != 1 || got[0] != 0 {
		t.Fatalf("Search(original of an edited record) = %v, want the original text to still match", got)
	}
	if got := s.Search("hihi", 0); len(got) != 1 || got[0] != 0 {
		t.Fatalf("Search(edit) = %v, want [0]", got)
	}
}

func TestDuplicates(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())

	if got := s.Duplicates(1); len(got) != 1 || got[0] != 3 {
		t.Fatalf("Duplicates(1) = %v, want [3]", got)
	}
	if got := s.Duplicates(3); len(got) != 1 || got[0] != 1 {
		t.Fatalf("Duplicates(3) = %v, want [1]", got)
	}
	if got := s.Duplicates(0); len(got) != 0 {
		t.Fatalf("Duplicates(0) = %v, want none", got)
	}
}

func TestFilterViewSnapshotAndGoto(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())

	if got := s.SetFilter(Filter{Kind: FilterStatus, Value: "approved"}); got != 0 {
		t.Fatalf("SetFilter(approved) size = %d, want 0", got)
	}
	if got := s.Current(); got != -1 {
		t.Fatalf("Current with empty view = %d, want -1", got)
	}
	s.Goto(0) // not in view: resets the filter
	if got := s.Filter().Kind; got != FilterAll {
		t.Fatalf("Filter after Goto = %v, want all", got)
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("Current after Goto = %d, want 0", got)
	}

	if _, err := s.SetStatus(Approved); err != nil { // n1 approved, auto-advance to 1
		t.Fatalf("SetStatus: %v", err)
	}
	unreviewed, err := ParseFilter("unreviewed")
	if err != nil {
		t.Fatalf("ParseFilter: %v", err)
	}
	if got := s.SetFilter(unreviewed); got != 3 {
		t.Fatalf("SetFilter(\"unreviewed\") size = %d, want 3", got)
	}
	if got := s.View(); len(got) != 3 || got[0] != 1 {
		t.Fatalf("View = %v, want [1 2 3]", got)
	}
	if got := s.Current(); got != 1 {
		t.Fatalf("Current after filter = %d, want 1", got)
	}
	// Snapshot semantics: approving the current record does not shrink the view.
	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := s.View(); len(got) != 3 {
		t.Fatalf("View shrank to %v, want the snapshot to keep 3 entries", got)
	}
	s.SetFilter(Filter{Kind: FilterStatus, Value: "unreviewed"})
	if got := s.View(); len(got) != 2 || got[0] != 2 {
		t.Fatalf("fresh view = %v, want [2 3]", got)
	}

	if got := s.SetFilter(Filter{Kind: FilterManual, Value: ""}); got != 0 {
		t.Fatalf("SetFilter(manual) = %d, want 0", got)
	}
	s.Goto(0)
	if err := s.SetManualFlags([]string{"slang"}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}
	if got := s.SetFilter(Filter{Kind: FilterManual, Value: ""}); got != 1 {
		t.Fatalf("SetFilter(manual) = %d, want 1", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterManual, Value: "slang"}); got != 1 {
		t.Fatalf("SetFilter(manual:slang) = %d, want 1", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterManual, Value: "typo"}); got != 0 {
		t.Fatalf("SetFilter(manual:typo) = %d, want 0", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterEdited}); got != 0 {
		t.Fatalf("SetFilter(edited) = %d, want 0", got)
	}
	s.Goto(2)
	if err := s.SaveEdit("aaa bbb"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if got := s.SetFilter(Filter{Kind: FilterEdited}); got != 1 {
		t.Fatalf("SetFilter(edited) = %d, want 1", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterSource, Value: "qwen"}); got != 2 {
		t.Fatalf("SetFilter(source:qwen) = %d, want 2", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterBatch, Value: "b1"}); got != 2 {
		t.Fatalf("SetFilter(batch:b1) = %d, want 2", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterSuggested, Value: ""}); got != 2 {
		t.Fatalf("SetFilter(suggested) = %d, want 2", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterSuggested, Value: "typo"}); got != 1 {
		t.Fatalf("SetFilter(suggested:typo) = %d, want 1", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterAuto, Value: checks.Duplicate}); got != 2 {
		t.Fatalf("SetFilter(auto:duplicate) = %d, want 2", got)
	}
	// n2/n4 are duplicates, n3 has repeated chars.
	if got := s.SetFilter(Filter{Kind: FilterAuto, Value: ""}); got != 3 {
		t.Fatalf("SetFilter(auto) = %d, want 3", got)
	}

	// Navigating the view.
	s.SetFilter(Filter{Kind: FilterStatus, Value: "unreviewed"})
	s.First()
	if got := s.Current(); got != 2 {
		t.Fatalf("First = %d, want 2", got)
	}
	s.Last()
	if got := s.Current(); got != 3 {
		t.Fatalf("Last = %d, want 3", got)
	}
	if s.Next() {
		t.Fatalf("Next at the end of the view must return false")
	}
}

func TestFilterParseRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want Filter
	}{
		{"all", Filter{Kind: FilterAll}},
		{"", Filter{Kind: FilterAll}},
		{"unreviewed", Filter{Kind: FilterStatus, Value: "unreviewed"}},
		{"approved", Filter{Kind: FilterStatus, Value: "approved"}},
		{"rejected", Filter{Kind: FilterStatus, Value: "rejected"}},
		{"needs_review", Filter{Kind: FilterStatus, Value: "needs_review"}},
		{"needs-review", Filter{Kind: FilterStatus, Value: "needs_review"}},
		{"needs review", Filter{Kind: FilterStatus, Value: "needs_review"}},
		{"edited", Filter{Kind: FilterEdited}},
		{"auto", Filter{Kind: FilterAuto}},
		{"auto:duplicate", Filter{Kind: FilterAuto, Value: "duplicate"}},
		{"manual", Filter{Kind: FilterManual}},
		{"manual:slang", Filter{Kind: FilterManual, Value: "slang"}},
		{"suggested", Filter{Kind: FilterSuggested}},
		{"suggested:typo", Filter{Kind: FilterSuggested, Value: "typo"}},
		{"source:claude", Filter{Kind: FilterSource, Value: "claude"}},
		{"batch:slang-loan-03", Filter{Kind: FilterBatch, Value: "slang-loan-03"}},
		{"  Unreviewed  ", Filter{Kind: FilterStatus, Value: "unreviewed"}},
	}
	for _, c := range cases {
		got, err := ParseFilter(c.in)
		if err != nil {
			t.Errorf("ParseFilter(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseFilter(%q) = %+v, want %+v", c.in, got, c.want)
			continue
		}
		round, err := ParseFilter(got.String())
		if err != nil || round != c.want {
			t.Errorf("round trip %q: %+v (%v), want %+v", c.in, round, err, c.want)
		}
	}
	if got, err := ParseFilter("bogus"); err == nil {
		t.Errorf("ParseFilter(bogus) = %+v, want error", got)
	}
}

func TestUndoRestoresStatusEditAndFlags(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())

	if _, err := s.SetStatus(Approved); err != nil { // n1, advances to n2
		t.Fatalf("SetStatus: %v", err)
	}
	if _, err := s.SetStatus(Rejected); err != nil { // n2, advances to n3
		t.Fatalf("SetStatus: %v", err)
	}
	if err := s.SaveEdit("aaa bbb"); err != nil { // n3
		t.Fatalf("SaveEdit: %v", err)
	}
	s.Goto(3)
	if err := s.SetManualFlags([]string{"slang"}); err != nil { // n4
		t.Fatalf("SetManualFlags: %v", err)
	}

	ok, idx, err := s.Undo()
	if err != nil || !ok || idx != 3 {
		t.Fatalf("Undo flags = (%v, %d, %v), want (true, 3, nil)", ok, idx, err)
	}
	if got := s.State(3).ManualFlags; got != nil {
		t.Fatalf("n4 flags after undo = %v, want nil", got)
	}
	if got := s.Current(); got != 3 {
		t.Fatalf("Current after undo = %d, want 3", got)
	}

	ok, idx, err = s.Undo()
	if err != nil || !ok || idx != 2 {
		t.Fatalf("Undo edit = (%v, %d, %v), want (true, 2, nil)", ok, idx, err)
	}
	if got := s.State(2); got.Edited() {
		t.Fatalf("n3 after undo = %+v, want edit cleared", got)
	}
	if got := s.FinalText(2); got != "aaaaaaaaaa" {
		t.Fatalf("FinalText(2) = %q after undo", got)
	}
	if got := s.Counts().Edited; got != 0 {
		t.Fatalf("Edited = %d after undo, want 0", got)
	}
	if !hasFlagName(s.AutoFlags(2), checks.RepeatedChars) {
		t.Fatalf("AutoFlags(2) = %v after undo, want the load-time flags back", flagNames(s.AutoFlags(2)))
	}

	if ok, idx, err = s.Undo(); err != nil || !ok || idx != 1 {
		t.Fatalf("Undo status = (%v, %d, %v), want (true, 1, nil)", ok, idx, err)
	}
	if got := s.State(1).EffectiveStatus(); got != Unreviewed {
		t.Fatalf("n2 status after undo = %v, want unreviewed", got)
	}
	if ok, idx, err = s.Undo(); err != nil || !ok || idx != 0 {
		t.Fatalf("Undo status = (%v, %d, %v), want (true, 0, nil)", ok, idx, err)
	}
	if got := s.Counts(); got != (Counts{Total: 4, Unreviewed: 4}) {
		t.Fatalf("Counts after undoing everything = %+v, want all unreviewed", got)
	}
	if ok, idx, err = s.Undo(); err != nil || ok || idx != -1 {
		t.Fatalf("Undo with empty stack = (%v, %d, %v), want (false, -1, nil)", ok, idx, err)
	}
}

func TestUndoFromEarlierSession(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, autoAdvanceConfig())

	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	s.Goto(0)
	if err := s.SaveEdit("cho Nam vay 3tr"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openSession(t, path, autoAdvanceConfig())
	if got := again.State(0).EffectiveStatus(); got != Approved {
		t.Fatalf("status after reopen = %v, want approved", got)
	}
	if got := again.FinalText(0); got != "cho Nam vay 3tr" {
		t.Fatalf("FinalText after reopen = %q", got)
	}
	if got := again.Current(); got != 1 {
		t.Fatalf("Current after reopen = %d, want 1 (first unreviewed)", got)
	}
	if got := again.Counts(); got != (Counts{Total: 4, Unreviewed: 3, Approved: 1, Edited: 1}) {
		t.Fatalf("Counts after reopen = %+v", got)
	}

	ok, idx, err := again.Undo()
	if err != nil || !ok || idx != 0 {
		t.Fatalf("Undo after restart = (%v, %d, %v), want (true, 0, nil)", ok, idx, err)
	}
	if got := again.State(0).Edited(); got {
		t.Fatalf("edit survived undo after restart")
	}
	if ok, idx, err = again.Undo(); err != nil || !ok || idx != 0 {
		t.Fatalf("second Undo after restart = (%v, %d, %v)", ok, idx, err)
	}
	if got := again.State(0).EffectiveStatus(); got != Unreviewed {
		t.Fatalf("status = %v after undo, want unreviewed", got)
	}
	if got := again.Counts(); got != (Counts{Total: 4, Unreviewed: 4}) {
		t.Fatalf("Counts = %+v after undo", got)
	}
}

func TestReopenRestoresEverythingByRecordID(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())

	s.Goto(0)
	if err := s.SaveEdit("cho Nam vay 5tr"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if err := s.SetManualFlags([]string{"slang"}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}
	s.Goto(0)
	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	s.Goto(1)
	if _, err := s.SetStatus(Rejected); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	s.Goto(2)
	if _, err := s.SetStatus(NeedsReview); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	want := s.Counts()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openSession(t, path, testConfig())
	if got := again.Counts(); got != want {
		t.Fatalf("Counts after reopen = %+v, want %+v", got, want)
	}
	if got := again.FinalText(0); got != "cho Nam vay 5tr" {
		t.Fatalf("FinalText(0) = %q after reopen", got)
	}
	if got := again.State(0).ManualFlags; strings.Join(got, ",") != "slang" {
		t.Fatalf("ManualFlags(0) = %v after reopen", got)
	}
	if got := again.State(1).EffectiveStatus(); got != Rejected {
		t.Fatalf("status(1) = %v, want rejected", got)
	}
	if got := again.State(2).EffectiveStatus(); got != NeedsReview {
		t.Fatalf("status(2) = %v, want needs_review", got)
	}
	if got := again.State(3).EffectiveStatus(); got != Unreviewed {
		t.Fatalf("status(3) = %v, want unreviewed", got)
	}
	// The session starts on the first unreviewed record of the restored corpus.
	if got := again.Current(); got != 3 {
		t.Fatalf("Current after reopen = %d, want 3", got)
	}
}

func TestCountsAndRate(t *testing.T) {
	path := writeCorpus(t, []string{corpusLines[0], corpusLines[1], corpusLines[2]})
	s := openSession(t, path, autoAdvanceConfig())

	now := time.Now()
	if got := s.Rate(now); got != 0 {
		t.Fatalf("Rate with no actions = %v, want 0", got)
	}
	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := s.Rate(now); got != 0 {
		t.Fatalf("Rate with one action = %v, want 0", got)
	}
	if _, err := s.SetStatus(Rejected); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := s.Rate(time.Now()); got <= 0 {
		t.Fatalf("Rate with two actions = %v, want > 0", got)
	}
	if got := s.Rate(s.started.Add(-time.Minute)); got != 0 {
		t.Fatalf("Rate in the past = %v, want 0", got)
	}
	if got := s.Counts(); got != (Counts{Total: 3, Unreviewed: 1, Approved: 1, Rejected: 1}) {
		t.Fatalf("Counts = %+v", got)
	}
}

func TestStatusFilterAcceptsAlias(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())
	f, err := ParseFilter("needs-review")
	if err != nil {
		t.Fatalf("ParseFilter: %v", err)
	}
	s.Goto(0)
	if _, err := s.SetStatus(NeedsReview); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := s.SetFilter(f); got != 1 {
		t.Fatalf("SetFilter(needs-review) = %d, want 1", got)
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("Current = %d, want 0", got)
	}
	if got := s.Filter().String(); got != "needs_review" {
		t.Fatalf("Filter().String() = %q, want needs_review", got)
	}
}

func TestUndoWithoutRecordInCorpus(t *testing.T) {
	path := writeCorpus(t, corpusLines)
	s := openSession(t, path, testConfig())
	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	// Simulate a corpus that no longer contains the record (ID lookup miss).
	s.byID = map[string]int{}
	ok, idx, err := s.Undo()
	if err != nil || !ok || idx != -1 {
		t.Fatalf("Undo for an unknown record = (%v, %d, %v), want (true, -1, nil)", ok, idx, err)
	}
}

// TestLargeCorpusLoadsAndFilters proves the session stays usable at scale: loading,
// analysing, filtering and navigating 20k records must simply complete.
func TestLargeCorpusLoadsAndFilters(t *testing.T) {
	const n = 20000
	var b strings.Builder
	b.Grow(n * 96)
	for i := range n {
		if i%7 == 0 {
			fmt.Fprintf(&b, `{"id":"n-%05d","text":"CK Nam %dtr","source":"claude","batch":"b%d","suggested_flags":["slang"]}`+"\n", i, i%90, i%3)
			continue
		}
		fmt.Fprintf(&b, `{"id":"n-%05d","text":"note %d cho Nam vay %dtr","source":"qwen","batch":"b%d"}`+"\n", i, i, i%50, i%3)
	}
	path := filepath.Join(t.TempDir(), "corpus.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}

	s := openSession(t, path, autoAdvanceConfig())
	if got := s.Len(); got != n {
		t.Fatalf("Len = %d, want %d", got, n)
	}
	if got := s.Counts(); got != (Counts{Total: n, Unreviewed: n}) {
		t.Fatalf("Counts = %+v", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterSource, Value: "claude"}); got != n/7+(boolToInt(n%7 != 0)) {
		t.Fatalf("SetFilter(source:claude) = %d", got)
	}
	if got := s.SetFilter(Filter{Kind: FilterStatus, Value: "unreviewed"}); got != n {
		t.Fatalf("SetFilter(unreviewed) = %d, want %d", got, n)
	}
	if _, err := s.SetStatus(Approved); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if !s.Next() {
		t.Fatalf("Next on a 20k view must advance")
	}
	if got := len(s.Search("note 19998", 0)); got != 1 {
		t.Fatalf("Search = %d hits, want 1", got)
	}
	if got := len(s.AutoFlagFacets()); got == 0 {
		t.Fatalf("AutoFlagFacets is empty")
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func facetString(facets []Facet) string {
	parts := make([]string, 0, len(facets))
	for _, f := range facets {
		parts = append(parts, fmt.Sprintf("%s=%d", f.Value, f.Count))
	}
	return strings.Join(parts, ",")
}

func facetValue(facets []Facet, value string) int {
	for _, f := range facets {
		if f.Value == value {
			return f.Count
		}
	}
	return -1
}
