package annotate

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Proposal lines used across tests, for the Gidi fixture queue.
const (
	propLend      = `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":4,"end":7},"note":"nợ","confidence":0.75,"reason":"vay -> lend","model":"x","extra":{"a":1}}`
	propUncertain = `{"id":"case-uncertain","annotation_status":"uncertain","type":null,"target":null}`
)

// writeProposals writes lines to proposals.jsonl in dir and returns its path.
func writeProposals(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "proposals.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadProposalSession opens the Gidi fixture and loads a proposals file with lines.
func loadProposalSession(t *testing.T, f fixture, lines ...string) (*Session, string) {
	t.Helper()
	s := f.open(t)
	path := writeProposals(t, f.dir, lines...)
	if _, err := s.LoadProposals(path); err != nil {
		t.Fatalf("LoadProposals: %v", err)
	}
	return s, path
}

// mustRead returns the content of path.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadProposals(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	path := writeProposals(t, f.dir, propLend, "", "  ", propUncertain)
	ignored, err := s.LoadProposals(path)
	if err != nil || len(ignored) != 0 {
		t.Fatalf("LoadProposals = %v, %v", ignored, err)
	}
	if !s.HasProposals() || s.ProposalsSource() != path || s.ProposalCount() != 2 || len(s.IgnoredProposals()) != 0 {
		t.Errorf("HasProposals=%v path=%q count=%d ignored=%v", s.HasProposals(), s.ProposalsSource(), s.ProposalCount(), s.IgnoredProposals())
	}
	p, ok := s.Proposal(idxLend)
	if !ok {
		t.Fatal("no proposal for case-lend")
	}
	conf := 0.75
	want := Proposal{
		ID: "case-lend", Status: StatusComplete, Type: new("lend"), Spans: tgt(&Target{Text: "Nam", Start: 4, End: 7}),
		Note: "nợ", Confidence: &conf, Reason: "vay -> lend",
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Proposal = %+v, want %+v", p, want)
	}
	u, ok := s.Proposal(idxUncertain)
	if !ok || u.Status != StatusUncertain || u.Type != nil || u.Spans["target"] != nil || u.Confidence != nil || u.Note != "" || u.Reason != "" {
		t.Errorf("uncertain Proposal = %+v, %v", u, ok)
	}
	if _, ok := s.Proposal(idxExpense); ok {
		t.Error("Proposal for a record without one")
	}

	// Proposal returns a copy: mutating it changes nothing.
	*p.Type, p.Spans["target"].Text, *p.Confidence = "x", "x", 0
	if again, _ := s.Proposal(idxLend); !reflect.DeepEqual(again, want) {
		t.Errorf("Proposal after mutating a copy = %+v", again)
	}
}

func TestLoadProposalsIgnoresIDsOutsideQueue(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	path := writeProposals(t, f.dir,
		`{"id":"zzz","annotation_status":"skipped"}`, propLend, `{"id":"aaa","annotation_status":"complete","type":"nope"}`)
	ignored, err := s.LoadProposals(path)
	if err != nil {
		t.Fatalf("LoadProposals: %v", err)
	}
	if want := []string{"aaa", "zzz"}; !reflect.DeepEqual(ignored, want) || !reflect.DeepEqual(s.IgnoredProposals(), want) {
		t.Errorf("ignored = %v / %v, want %v", ignored, s.IgnoredProposals(), want)
	}
	if s.ProposalCount() != 1 {
		t.Errorf("ProposalCount = %d, want 1 (ignored ids excluded)", s.ProposalCount())
	}
	// The returned lists are copies.
	ignored[0] = "changed"
	if s.IgnoredProposals()[0] != "aaa" {
		t.Error("IgnoredProposals aliases internal state")
	}
}

func TestLoadProposalsErrors(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"malformed json", []string{propUncertain, `{"id":`}, ":2: invalid JSON object"},
		{"not an object", []string{`[1]`}, ":1: invalid JSON object"},
		{"missing id", []string{`{"annotation_status":"complete"}`}, `:1: missing field "id"`},
		{"empty id", []string{`{"id":"","annotation_status":"complete"}`}, ":1: id: expected a non-empty string"},
		{"id not a string", []string{`{"id":3,"annotation_status":"complete"}`}, ":1: id: expected a string"},
		{"missing status", []string{`{"id":"case-lend"}`}, `:1: missing field "annotation_status"`},
		{"status not a string", []string{`{"id":"case-lend","annotation_status":null}`}, ":1: annotation_status: expected a string"},
		{"type not a string", []string{`{"id":"case-lend","annotation_status":"complete","type":4}`}, ":1: type: expected a string or null"},
		{"target not an object", []string{`{"id":"case-lend","annotation_status":"complete","target":"Nam"}`}, ":1: target: expected an object or null"},
		{"target missing end", []string{`{"id":"case-lend","annotation_status":"complete","target":{"text":"Nam","start":4}}`}, `:1: target: missing field "end"`},
		{"target start not an integer", []string{`{"id":"case-lend","annotation_status":"complete","target":{"text":"Nam","start":"4","end":7}}`}, ":1: target.start: expected an integer"},
		{"note not a string", []string{`{"id":"case-lend","annotation_status":"complete","note":1}`}, ":1: note: expected a string"},
		{"reason not a string", []string{`{"id":"case-lend","annotation_status":"complete","reason":[]}`}, ":1: reason: expected a string"},
		{"confidence too high", []string{`{"id":"case-lend","annotation_status":"complete","confidence":1.5}`}, ":1: confidence: 1.5 is outside 0..1"},
		{"confidence negative", []string{`{"id":"case-lend","annotation_status":"complete","confidence":-0.1}`}, ":1: confidence: -0.1 is outside 0..1"},
		{"confidence not a number", []string{`{"id":"case-lend","annotation_status":"complete","confidence":"high"}`}, ":1: confidence: expected a number"},
		{"duplicate id", []string{propLend, "", propUncertain, `{"id":"case-lend","annotation_status":"skipped"}`}, `:4: duplicate id "case-lend" (first on line 1)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, "")
			s := f.open(t)
			path := writeProposals(t, f.dir, tt.lines...)
			if _, err := s.LoadProposals(path); err == nil || !strings.HasPrefix(err.Error(), "proposals "+path) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadProposals error = %v, want prefix %q containing %q", err, "proposals "+path, tt.want)
			}
			if s.HasProposals() || s.ProposalCount() != 0 || s.ProposalsSource() != "" {
				t.Error("a failed load left proposals in the session")
			}
		})
	}
}

func TestLoadProposalsPathChecks(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	missing := filepath.Join(f.dir, "nope.jsonl")
	if _, err := s.LoadProposals(missing); err == nil || err.Error() != "proposals file "+missing+" does not exist" {
		t.Errorf("missing file error = %v", err)
	}

	queueLink := filepath.Join(f.dir, "queue-link.jsonl")
	if err := os.Symlink(f.queue, queueLink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.queue, queueLink} {
		if _, err := s.LoadProposals(path); err == nil || !strings.Contains(err.Error(), "is the queue file") {
			t.Errorf("LoadProposals(%s) = %v, want queue file refusal", path, err)
		}
	}

	if err := os.WriteFile(f.out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	outLink := filepath.Join(f.dir, "out-link.jsonl")
	if err := os.Symlink(f.out, outLink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.out, outLink} {
		if _, err := s.LoadProposals(path); err == nil || !strings.Contains(err.Error(), "is the labels file") {
			t.Errorf("LoadProposals(%s) = %v, want labels file refusal", path, err)
		}
	}
	if s.HasProposals() {
		t.Error("a refused path loaded proposals")
	}

	path := writeProposals(t, f.dir, propLend)
	if _, err := s.LoadProposals(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadProposals(path); err == nil || !strings.Contains(err.Error(), "already loaded") {
		t.Errorf("second LoadProposals = %v, want already loaded", err)
	}
}

// TestAcceptProposal: the label is canonical (no confidence/reason/extra keys), undo removes it, the proposal
// file is never touched and the draft is dropped.
func TestAcceptProposal(t *testing.T) {
	f := newFixture(t, "")
	s, path := loadProposalSession(t, f, propLend, propUncertain)
	before := mustRead(t, path)

	mustSetType(t, s, idxLend, "borrow") // a pending draft is replaced by the accepted label
	if err := s.AcceptProposal(idxLend); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	wantLine := `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":4,"end":7},"note":"nợ"}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{wantLine}) {
		t.Errorf("labels file = %q, want %q", got, wantLine)
	}
	for _, banned := range []string{"confidence", "reason", "model", "extra"} {
		if strings.Contains(string(mustRead(t, f.out)), banned) {
			t.Errorf("labels file contains %q", banned)
		}
	}
	l, ok := s.Label(idxLend)
	if !ok || l.Status != StatusComplete || *l.Type != "lend" || l.Note != "nợ" || s.Dirty(idxLend) {
		t.Errorf("label = %+v, %v, dirty=%v", l, ok, s.Dirty(idxLend))
	}
	if d := s.Draft(idxLend); d.Type != "lend" || d.Spans["target"] == nil {
		t.Errorf("draft after accept = %+v", d)
	}

	if err := s.AcceptProposal(idxUncertain); err != nil {
		t.Fatalf("AcceptProposal(uncertain): %v", err)
	}
	wantUncertain := `{"id":"case-uncertain","annotation_status":"uncertain","type":null,"target":null}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{wantLine, wantUncertain}) {
		t.Errorf("labels file = %q", got)
	}
	if c := s.Counts(); c.Complete != 1 || c.Uncertain != 1 {
		t.Errorf("counts = %+v", c)
	}

	for range 2 {
		if _, _, ok, err := s.Undo(); !ok || err != nil {
			t.Fatalf("Undo = %v, %v", ok, err)
		}
	}
	if data := mustRead(t, f.out); len(data) != 0 {
		t.Errorf("labels file after undoing both accepts = %q, want empty", data)
	}
	if !bytes.Equal(mustRead(t, path), before) {
		t.Error("proposals file changed")
	}
}

// TestAcceptProposalNote: the proposal's note wins; without one the saved note is kept.
func TestAcceptProposalNote(t *testing.T) {
	f := newFixture(t, "")
	saved := `{"id":"case-lend","annotation_status":"uncertain","type":null,"target":null,"note":"giữ lại"}` + "\n"
	if err := os.WriteFile(f.out, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	noNote := `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":4,"end":7}}`
	s, _ := loadProposalSession(t, f, noNote, `{"id":"case-expense","annotation_status":"complete","type":"expense","note":"mới"}`)
	if err := s.AcceptProposal(idxLend); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Label(idxLend); l.Note != "giữ lại" {
		t.Errorf("note without a proposal note = %q, want the saved note", l.Note)
	}
	if err := s.AcceptProposal(idxExpense); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Label(idxExpense); l.Note != "mới" {
		t.Errorf("note = %q, want the proposal note", l.Note)
	}
	// Accepting over a canonical label is allowed and undoable: the saved uncertain label comes back.
	if _, desc, ok, err := s.Undo(); !ok || err != nil || desc != "removed label" {
		t.Fatalf("Undo = %q, %v, %v", desc, ok, err)
	}
	if _, desc, ok, err := s.Undo(); !ok || err != nil || desc != "restored uncertain" {
		t.Fatalf("Undo = %q, %v, %v", desc, ok, err)
	}
	if got := string(mustRead(t, f.out)); got != saved {
		t.Errorf("labels file after undo = %q, want %q", got, saved)
	}
}

// TestAcceptProposalNullLabelStatus: a skipped proposal that carries a type and target saves null/null, exactly
// as Mark(skipped) would.
func TestAcceptProposalNullLabelStatus(t *testing.T) {
	f := newFixture(t, "")
	s, _ := loadProposalSession(t, f, `{"id":"case-lend","annotation_status":"skipped","type":"lend","target":{"text":"Nam","start":4,"end":7}}`)
	if err := s.ProposalProblem(idxLend); err != nil {
		t.Fatalf("ProposalProblem: %v", err)
	}
	if err := s.AcceptProposal(idxLend); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	want := `{"id":"case-lend","annotation_status":"skipped","type":null,"target":null}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("labels file = %q, want %q", got, want)
	}
}

func TestAcceptProposalInvalid(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"undeclared type", `{"id":"case-lend","annotation_status":"complete","type":"loan"}`, `type: "loan" is not one of`},
		{"undeclared status", `{"id":"case-lend","annotation_status":"maybe"}`, `annotation_status: "maybe" is not one of`},
		{"complete without type", `{"id":"case-lend","annotation_status":"complete"}`, "type: required when annotation_status is"},
		{"span out of range", `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":40,"end":43}}`, "target:"},
		{"span text mismatch", `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Tam","start":4,"end":7}}`, `text[4:7] is "Nam", not "Tam"`},
		{"padded span", `{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":" Nam","start":3,"end":7}}`, "leading or trailing whitespace"},
		{"target on null-target type", `{"id":"case-lend","annotation_status":"complete","type":"transfer","target":{"text":"Nam","start":4,"end":7}}`, `target: must be null for type "transfer"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, "")
			s, _ := loadProposalSession(t, f, tt.line)
			if err := s.ProposalProblem(idxLend); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ProposalProblem = %v, want %q", err, tt.want)
			}
			if err := s.AcceptProposal(idxLend); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("AcceptProposal = %v, want %q", err, tt.want)
			}
			if _, err := os.Stat(f.out); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("labels file written for an invalid proposal: %v", err)
			}
			if _, ok := s.Label(idxLend); ok || s.CanUndo() {
				t.Error("session state changed for an invalid proposal")
			}
		})
	}
}

// TestApplyProposalInvalid: undeclared types and bad spans are refused and leave the draft alone; a proposal with
// a valid span but an invalid status still applies (the reviewer picks the status).
func TestApplyProposalInvalid(t *testing.T) {
	f := newFixture(t, "")
	s, _ := loadProposalSession(t, f,
		`{"id":"case-lend","annotation_status":"complete","type":"loan"}`,
		`{"id":"case-repayment-in","annotation_status":"complete","type":"repayment_in","target":{"text":"Tam","start":0,"end":3}}`,
		`{"id":"case-expense","annotation_status":"maybe","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21}}`)
	mustSetType(t, s, idxLend, "lend")
	for _, i := range []int{idxLend, idxRepaymentIn} {
		if err := s.ApplyProposal(i); err == nil {
			t.Errorf("ApplyProposal(%d) succeeded for an invalid proposal", i)
		}
	}
	if d := s.Draft(idxLend); d.Type != "lend" {
		t.Errorf("draft after a refused apply = %+v", d)
	}
	if s.Dirty(idxRepaymentIn) {
		t.Error("draft changed by a refused apply")
	}
	if err := s.ApplyProposal(idxExpense); err != nil {
		t.Errorf("ApplyProposal with an unusable status: %v", err)
	}
	if d := s.Draft(idxExpense); d.Type != "expense" || d.Spans["target"] == nil || d.Spans["target"].Text != "Pizza 4P" {
		t.Errorf("draft = %+v", d)
	}
}

// TestApplyProposal: the draft gets the proposal's type/target without saving; editing then marking saves the
// edited label and leaves the proposal untouched.
func TestApplyProposal(t *testing.T) {
	f := newFixture(t, "")
	s, path := loadProposalSession(t, f, propLend)
	before := mustRead(t, path)
	want, _ := s.Proposal(idxLend)

	if err := s.ApplyProposal(idxLend); err != nil {
		t.Fatalf("ApplyProposal: %v", err)
	}
	if d := s.Draft(idxLend); d.Type != "lend" || d.Spans["target"] == nil || *d.Spans["target"] != *want.Spans["target"] || !s.Dirty(idxLend) {
		t.Errorf("draft = %+v dirty=%v", d, s.Dirty(idxLend))
	}
	if _, err := os.Stat(f.out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ApplyProposal wrote the labels file: %v", err)
	}
	if _, ok := s.Label(idxLend); ok {
		t.Error("ApplyProposal saved a label")
	}

	mustSetType(t, s, idxLend, "borrow")
	mustSetTarget(t, s, idxLend, 0, 3)
	mustMark(t, s, idxLend, StatusUncertain)
	edited := `{"id":"case-lend","annotation_status":"uncertain","type":"borrow","target":{"text":"cho","start":0,"end":3}}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{edited}) {
		t.Errorf("labels file = %q, want %q", got, edited)
	}
	if got, _ := s.Proposal(idxLend); !reflect.DeepEqual(got, want) {
		t.Errorf("proposal changed: %+v", got)
	}
	if s.ProposalMatches(idxLend) {
		t.Error("ProposalMatches after editing away from the proposal")
	}
	if _, _, ok, err := s.Undo(); !ok || err != nil {
		t.Fatalf("Undo = %v, %v", ok, err)
	}
	if !bytes.Equal(mustRead(t, path), before) {
		t.Error("proposals file changed")
	}
}

// TestApplyProposalNullTargetType: a proposal that puts a target on a null-target type loads with a null target.
func TestApplyProposalNullTargetType(t *testing.T) {
	f := newFixture(t, "")
	s, _ := loadProposalSession(t, f, `{"id":"case-transfer","annotation_status":"complete","type":"transfer","target":{"text":"5tr","start":3,"end":6}}`)
	if err := s.ApplyProposal(idxTransfer); err != nil {
		t.Fatalf("ApplyProposal: %v", err)
	}
	if d := s.Draft(idxTransfer); d.Type != "transfer" || d.Spans["target"] != nil {
		t.Errorf("draft = %+v, want transfer with a null target", d)
	}
	mustMark(t, s, idxTransfer, StatusComplete)
	want := `{"id":"case-transfer","annotation_status":"complete","type":"transfer","target":null}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("labels file = %q, want %q", got, want)
	}
}

func TestProposalMatches(t *testing.T) {
	f := newFixture(t, "")
	s, _ := loadProposalSession(t, f, propLend, propUncertain)
	if s.ProposalMatches(idxLend) || s.ProposalMatches(idxExpense) {
		t.Error("ProposalMatches without a saved label")
	}
	if err := s.AcceptProposal(idxLend); err != nil {
		t.Fatal(err)
	}
	if !s.ProposalMatches(idxLend) {
		t.Error("ProposalMatches false right after accepting")
	}
	mustMark(t, s, idxUncertain, StatusSkipped)
	if s.ProposalMatches(idxUncertain) {
		t.Error("ProposalMatches true for a different status")
	}
	mustSetTarget(t, s, idxLend, 0, 3)
	mustMark(t, s, idxLend, StatusComplete)
	if s.ProposalMatches(idxLend) {
		t.Error("ProposalMatches true for a different target")
	}
	if s.ProposalMatches(idxExpense) {
		t.Error("ProposalMatches true for a record without a proposal")
	}
}

func TestProposalFilters(t *testing.T) {
	f := newFixture(t, "")
	s, _ := loadProposalSession(t, f, propLend, propUncertain, `{"id":"case-expense","annotation_status":"skipped"}`)
	var names []string
	for _, fl := range s.Filters() {
		names = append(names, fl.String())
		if got, err := s.ParseFilter(strings.ToUpper(fl.String())); err != nil || got != fl {
			t.Errorf("ParseFilter(%q) = %v, %v", fl.String(), got, err)
		}
	}
	want := []string{"unfinished", "complete", "uncertain", "skipped", "all", "proposed", "unproposed", "proposed uncertain"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Filters() = %v, want %v", names, want)
	}
	matches := func(fl Filter) []int {
		var got []int
		for i := range s.Len() {
			if s.Matches(i, fl) {
				got = append(got, i)
			}
		}
		return got
	}
	for fl, wantIdx := range map[Filter][]int{
		FilterProposed:          {idxLend, idxExpense, idxUncertain},
		FilterUnproposed:        {idxReal1, idxRepaymentIn, idxTransfer, idxReal2},
		FilterProposedUncertain: {idxUncertain},
	} {
		if got := matches(fl); !reflect.DeepEqual(got, wantIdx) {
			t.Errorf("%v matches %v, want %v", fl, got, wantIdx)
		}
	}
	// The proposal filters are independent of the saved label.
	mustMark(t, s, idxUncertain, StatusSkipped)
	if got := matches(FilterProposedUncertain); !reflect.DeepEqual(got, []int{idxUncertain}) {
		t.Errorf("proposed uncertain after marking = %v", got)
	}
	s.SetFilter(FilterProposed)
	s.SetCursor(0)
	if !s.NextMatch() || s.Cursor() != idxLend {
		t.Errorf("NextMatch under proposed = %d", s.Cursor())
	}
	if pos, n := s.FilterPosition(); pos != 1 || n != 3 {
		t.Errorf("FilterPosition = %d/%d, want 1/3", pos, n)
	}
}

// TestNoProposals: a session that never loaded proposals behaves exactly as before.
func TestNoProposals(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	if s.HasProposals() || s.ProposalsSource() != "" || s.ProposalCount() != 0 || s.IgnoredProposals() != nil {
		t.Error("proposal accessors report data without LoadProposals")
	}
	if _, ok := s.Proposal(idxLend); ok {
		t.Error("Proposal returned a proposal")
	}
	if err := s.ProposalProblem(idxLend); err != nil {
		t.Errorf("ProposalProblem = %v", err)
	}
	if s.ProposalMatches(idxLend) {
		t.Error("ProposalMatches = true")
	}
	if err := s.AcceptProposal(idxLend); err == nil {
		t.Error("AcceptProposal succeeded without proposals")
	}
	if err := s.ApplyProposal(idxLend); err == nil {
		t.Error("ApplyProposal succeeded without proposals")
	}
	if _, err := os.Stat(f.out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("labels file written: %v", err)
	}
	var names []string
	for _, fl := range s.Filters() {
		names = append(names, fl.String())
	}
	if want := []string{"unfinished", "complete", "uncertain", "skipped", "all"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Filters() = %v, want %v", names, want)
	}
	for _, fl := range []Filter{FilterProposed, FilterUnproposed, FilterProposedUncertain} {
		if s.Matches(idxLend, fl) {
			t.Errorf("%v matches without proposals", fl)
		}
	}
}

// TestRecheckProposals: accepting in a re-check session changes only that record's line and Undo restores the file
// byte for byte, also when the accept replaced an existing canonical label.
func TestRecheckProposals(t *testing.T) {
	f := newRecheckFixture(t, 27) // r027 is a queue record without a canonical label
	s := f.open(t)
	labeled, unlabeled := 9, 27 // r009 has a canonical label, r027 has none
	idx := func(rec int) int {
		t.Helper()
		i, err := s.Find(recheckID(rec))
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	path := filepath.Join(f.dir, "proposals.jsonl")
	lines := []string{
		`{"id":"r009","annotation_status":"complete","type":"alpha","target":{"text":"phở","start":7,"end":10},"confidence":0.5}`,
		`{"id":"r027","annotation_status":"later","type":"beta","note":"xem lại","reason":"r"}`,
		`{"id":"r001","annotation_status":"skipped"}`, // in the corpus, not in the subset queue
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	propBefore := mustRead(t, path)
	ignored, err := s.LoadProposals(path)
	if err != nil || !reflect.DeepEqual(ignored, []string{"r001"}) || s.ProposalCount() != 2 {
		t.Fatalf("LoadProposals = %v, %v (count %d)", ignored, err, s.ProposalCount())
	}
	original := f.read(t)
	origLines := fileLines(t, original)
	total := s.LabelsTotal()

	// Accept over the existing canonical label of r009.
	if err := s.AcceptProposal(idx(labeled)); err != nil {
		t.Fatalf("AcceptProposal(r009): %v", err)
	}
	got := fileLines(t, f.read(t))
	if len(got) != len(origLines) || s.LabelsTotal() != total {
		t.Fatalf("lines %d, LabelsTotal %d; want %d, %d", len(got), s.LabelsTotal(), len(origLines), total)
	}
	changed := 0
	for n := range got {
		if got[n] != origLines[n] {
			changed++
			wantLine := `{"id":"r009","annotation_status":"complete","type":"alpha","target":{"text":"phở","start":7,"end":10}}`
			if got[n] != wantLine {
				t.Errorf("changed line = %s, want %s", got[n], wantLine)
			}
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want 1", changed)
	}
	if strings.Contains(string(f.read(t)), "confidence") {
		t.Error("labels file holds the confidence")
	}

	// Accept on a record with no canonical label appends one line.
	if err := s.AcceptProposal(idx(unlabeled)); err != nil {
		t.Fatalf("AcceptProposal(r027): %v", err)
	}
	if now := fileLines(t, f.read(t)); len(now) != len(origLines)+1 || s.LabelsTotal() != total+1 {
		t.Errorf("after the second accept: %d lines, LabelsTotal %d", len(now), s.LabelsTotal())
	} else if last := now[len(now)-1]; last != `{"id":"r027","annotation_status":"later","type":"beta","target":null,"note":"xem lại"}` {
		t.Errorf("appended line = %s", last)
	}

	for range 2 {
		if _, _, ok, err := s.Undo(); !ok || err != nil {
			t.Fatalf("Undo = %v, %v", ok, err)
		}
	}
	if !bytes.Equal(f.read(t), original) {
		t.Error("labels file differs from the original after undoing both accepts")
	}
	if !bytes.Equal(mustRead(t, path), propBefore) {
		t.Error("proposals file changed")
	}
}
