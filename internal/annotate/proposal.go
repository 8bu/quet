package annotate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Proposal is one advisory suggested label for a queue record, loaded from a proposals file. It is never
// persisted: Confidence and Reason are shown to the reviewer only, and only Status, Type, Target and Note can
// reach the labels file, through AcceptProposal.
type Proposal struct {
	ID         string
	Status     string
	Type       *string  // nil = no type
	Target     *Target  // nil = null target
	Note       string   // "" = none
	Confidence *float64 // nil = not given; otherwise within [0,1]
	Reason     string   // "" = none
}

// proposalSet is the proposals of a session: those for queue records by queue index, and the ids of the rest.
type proposalSet struct {
	path    string
	byIndex map[int]Proposal
	ignored []string // sorted ids not in the queue
}

// LoadProposals loads the advisory proposals JSONL at path into the session; call it once after Open or
// OpenRecheck. Each non-blank line is an object with a non-empty string id and a string annotation_status, and
// optionally type (string or null), target (null or an object decoded like a label's target), note (string),
// confidence (number in [0,1]) and reason (string); null optional members count as absent and other members are
// ignored. Proposals are not schema-validated here (ProposalProblem does that per record). Errors, all refusing to
// load: a missing file, a path that is the queue or the labels file, invalid JSON, a missing or ill-typed member,
// a bad target shape, a confidence out of range and a duplicate id, each citing "proposals <path>:<line>". ids not
// in the queue are not an error: they are dropped and returned sorted (also IgnoredProposals). The file is never
// written.
func (s *Session) LoadProposals(path string) (ignored []string, err error) {
	if s.proposals != nil {
		return nil, fmt.Errorf("proposals already loaded from %s", s.proposals.path)
	}
	if err := s.checkProposalsPath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proposals: %w", err)
	}
	index := make(map[string]int, len(s.items))
	for i, it := range s.items {
		index[it.ID] = i
	}
	set := &proposalSet{path: path, byIndex: map[int]Proposal{}}
	firstLine := map[string]int{}
	err = forEachLine(data, func(lineNo int, line []byte) error {
		p, err := decodeProposal(line)
		if err != nil {
			return err
		}
		if first, dup := firstLine[p.ID]; dup {
			return fmt.Errorf("duplicate id %q (first on line %d)", p.ID, first)
		}
		firstLine[p.ID] = lineNo
		if i, ok := index[p.ID]; ok {
			set.byIndex[i] = p
		} else {
			set.ignored = append(set.ignored, p.ID)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("proposals %s:%w", path, err)
	}
	sort.Strings(set.ignored)
	s.proposals = set
	return append([]string(nil), set.ignored...), nil
}

// checkProposalsPath requires the proposals file to exist and to be neither the queue file nor the labels file.
func (s *Session) checkProposalsPath(path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("proposals file %s does not exist", path)
	} else if err != nil {
		return fmt.Errorf("check proposals file: %w", err)
	}
	for _, other := range []struct{ path, what string }{{s.queuePath, "queue"}, {s.outPath, "labels"}} {
		same, err := sameFile(path, other.path)
		if err != nil {
			return err
		}
		if same {
			return fmt.Errorf("proposals file %s is the %s file %s", path, other.what, other.path)
		}
	}
	return nil
}

// sameFile reports whether a and b resolve to the same file: equal paths after resolving symlinks, or the same
// file on disk (hard links). b need not exist.
func sameFile(a, b string) (bool, error) {
	aAbs, err := filepath.Abs(a)
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", a, err)
	}
	bAbs, err := filepath.Abs(b)
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", b, err)
	}
	if realPath(aAbs) == realPath(bAbs) {
		return true, nil
	}
	ai, err := os.Stat(aAbs)
	if err != nil {
		return false, nil
	}
	bi, err := os.Stat(bAbs)
	if err != nil {
		return false, nil
	}
	return os.SameFile(ai, bi), nil
}

// decodeProposal decodes one proposal line as described at LoadProposals.
func decodeProposal(line []byte) (Proposal, error) {
	fields, err := decodeObject(line)
	if err != nil {
		return Proposal{}, err
	}
	var p Proposal
	if p.ID, err = requiredString(fields, "id"); err != nil {
		return Proposal{}, err
	}
	if p.ID == "" {
		return Proposal{}, errors.New("id: expected a non-empty string")
	}
	if p.Status, err = requiredString(fields, "annotation_status"); err != nil {
		return Proposal{}, err
	}
	if raw, ok := fields["type"]; ok && !isJSONNull(raw) {
		typ, err := decodeString(raw)
		if err != nil {
			return Proposal{}, fmt.Errorf("type: %w or null", err)
		}
		p.Type = &typ
	}
	if raw, ok := fields["target"]; ok && !isJSONNull(raw) {
		t, err := decodeTarget(raw)
		if err != nil {
			return Proposal{}, err
		}
		p.Target = &t
	}
	if p.Note, err = optionalString(fields, "note"); err != nil {
		return Proposal{}, err
	}
	if p.Reason, err = optionalString(fields, "reason"); err != nil {
		return Proposal{}, err
	}
	if raw, ok := fields["confidence"]; ok && !isJSONNull(raw) {
		var c float64
		if err := json.Unmarshal(raw, &c); err != nil {
			return Proposal{}, errors.New("confidence: expected a number")
		}
		if c < 0 || c > 1 {
			return Proposal{}, fmt.Errorf("confidence: %v is outside 0..1", c)
		}
		p.Confidence = &c
	}
	return p, nil
}

// optionalString returns the string member key of fields, "" when it is absent or null, and an error when it is
// any other kind.
func optionalString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok || isJSONNull(raw) {
		return "", nil
	}
	return requiredString(fields, key)
}

// HasProposals reports whether LoadProposals succeeded.
func (s *Session) HasProposals() bool { return s.proposals != nil }

// ProposalsPath returns the proposals file path as given to LoadProposals ("" when none is loaded).
func (s *Session) ProposalsPath() string {
	if s.proposals == nil {
		return ""
	}
	return s.proposals.path
}

// ProposalCount returns the number of proposals for queue records.
func (s *Session) ProposalCount() int {
	if s.proposals == nil {
		return 0
	}
	return len(s.proposals.byIndex)
}

// IgnoredProposals returns the sorted ids of loaded proposals that are not in the queue.
func (s *Session) IgnoredProposals() []string {
	if s.proposals == nil {
		return nil
	}
	return append([]string(nil), s.proposals.ignored...)
}

// Proposal returns a copy of the proposal for queue record i, if any.
func (s *Session) Proposal(i int) (Proposal, bool) {
	if s.proposals == nil {
		return Proposal{}, false
	}
	p, ok := s.proposals.byIndex[i]
	if !ok {
		return Proposal{}, false
	}
	if p.Type != nil {
		p.Type = new(*p.Type)
	}
	p.Target = copyTarget(p.Target)
	if p.Confidence != nil {
		p.Confidence = new(*p.Confidence)
	}
	return p, true
}

// proposalLabel returns the label a proposal states for record i: its own status, type and target, with no note
// and no null_label_statuses clearing. Callers must own p (Proposal returns a copy).
func (s *Session) proposalLabel(i int, p Proposal) Label {
	return Label{ID: s.items[i].ID, Status: p.Status, Type: p.Type, Target: p.Target}
}

// ProposalProblem returns why the proposal for record i is not a valid label under the schema and the record
// text (Validate on the proposal's own status, type and target), or nil when it is valid or there is no proposal.
func (s *Session) ProposalProblem(i int) error {
	p, ok := s.Proposal(i)
	if !ok {
		return nil
	}
	return s.schema.Validate(s.proposalLabel(i, p), s.items[i].Text)
}

// ProposalMatches reports whether record i has both a proposal and a saved label and the proposal's status, type
// and target equal the label's.
func (s *Session) ProposalMatches(i int) bool {
	p, ok := s.Proposal(i)
	if !ok {
		return false
	}
	l, ok := s.labels[s.items[i].ID]
	if !ok || p.Status != l.Status {
		return false
	}
	return (p.Type == nil) == (l.Type == nil) && draftEqual(proposalDraft(p), s.savedDraft(i))
}

// proposalDraft returns the type and target of p as a Draft.
func proposalDraft(p Proposal) Draft {
	d := Draft{Target: p.Target}
	if p.Type != nil {
		d.Type = *p.Type
	}
	return d
}

// AcceptProposal saves the proposal for record i as its label, exactly as Mark(i, status) would from a draft of the
// proposal's type and target (so null_label_statuses clearing applies): the note is the proposal's when non-empty,
// else the existing saved note. The proposal's own label is validated first (ProposalProblem) and refused with that
// error when invalid, without touching the labels file. It replaces an existing saved label (Undo restores it),
// drops the record's draft, and never writes the proposal's confidence or reason. Errors: no proposal for i, an
// invalid proposal, and those of Mark.
func (s *Session) AcceptProposal(i int) error {
	p, ok := s.Proposal(i)
	if !ok {
		return fmt.Errorf("no proposal for record %s", s.items[i].ID)
	}
	if err := s.schema.Validate(s.proposalLabel(i, p), s.items[i].Text); err != nil {
		return err
	}
	return s.mark(i, p.Status, proposalDraft(p), p.Note)
}

// ApplyProposal loads the proposal's type and target into the draft of record i without saving: the type must be
// declared, a target must be a valid span of the record text, and a null-target type gets a null target. The
// status is not applied: the reviewer marks as usual. Errors: no proposal for i, an undeclared type, an invalid
// target; the draft is unchanged on error.
func (s *Session) ApplyProposal(i int) error {
	p, ok := s.Proposal(i)
	if !ok {
		return fmt.Errorf("no proposal for record %s", s.items[i].ID)
	}
	d := proposalDraft(p)
	if p.Type != nil && !s.schema.HasType(*p.Type) {
		return fmt.Errorf("proposal type %q is not declared in the schema", *p.Type)
	}
	if d.Type != "" && s.schema.NullTarget(d.Type) {
		d.Target = nil
	}
	if d.Target != nil {
		if problem := targetProblem(*d.Target, s.items[i].Text); problem != "" {
			return fmt.Errorf("proposal %s", problem)
		}
	}
	s.setDraft(i, d)
	return nil
}

// matchesProposal reports whether record i matches the proposal filter f; false when no proposals are loaded.
func (s *Session) matchesProposal(i int, f Filter) bool {
	if s.proposals == nil {
		return false
	}
	p, ok := s.proposals.byIndex[i]
	switch f {
	case FilterProposed:
		return ok
	case FilterUnproposed:
		return !ok
	case FilterProposedUncertain:
		return ok && p.Status == StatusUncertain
	}
	return false
}
