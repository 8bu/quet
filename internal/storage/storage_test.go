package storage

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested", "corpus.jsonl.quet.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func mustState(t *testing.T, status string, edited *string, flags []string) RecordState {
	t.Helper()
	return RecordState{Status: status, EditedText: edited, ManualFlags: flags}
}

func TestSidecarPath(t *testing.T) {
	if got, want := SidecarPath("/tmp/corpus.jsonl"), "/tmp/corpus.jsonl.quet.db"; got != want {
		t.Fatalf("SidecarPath = %q, want %q", got, want)
	}
}

func TestOpenPragmas(t *testing.T) {
	s, _ := openTestStore(t)
	checks := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"synchronous", "2"},
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
	}
	for _, c := range checks {
		var got string
		if err := s.db.QueryRow("PRAGMA " + c.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", c.pragma, err)
		}
		if !strings.EqualFold(got, c.want) {
			t.Errorf("PRAGMA %s = %q, want %q", c.pragma, got, c.want)
		}
	}
}

func TestMetaRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)

	version, ok, err := s.GetMeta("schema_version")
	if err != nil {
		t.Fatalf("GetMeta(schema_version): %v", err)
	}
	if !ok || version != "1" {
		t.Fatalf("schema_version = (%q, %v), want (\"1\", true)", version, ok)
	}
	if _, ok, err := s.GetMeta("missing"); err != nil || ok {
		t.Fatalf("GetMeta(missing) = (_, %v, %v), want (_, false, nil)", ok, err)
	}
	if err := s.SetMeta("cursor", "note-007"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if err := s.SetMeta("cursor", "note-009"); err != nil {
		t.Fatalf("SetMeta overwrite: %v", err)
	}
	got, ok, err := s.GetMeta("cursor")
	if err != nil || !ok || got != "note-009" {
		t.Fatalf("GetMeta(cursor) = (%q, %v, %v), want (\"note-009\", true, nil)", got, ok, err)
	}
}

func TestApplyEventsAndReopen(t *testing.T) {
	s, path := openTestStore(t)

	unreviewed := RecordState{Status: "unreviewed"}
	approved := mustState(t, "approved", nil, nil)
	if err := s.Apply("note-001", "bắn thg Nam 2 củ", "status", unreviewed, approved); err != nil {
		t.Fatalf("Apply status: %v", err)
	}
	edited := mustState(t, "approved", new("bắn thằng Nam 2 củ tiền"), nil)
	if err := s.Apply("note-001", "bắn thg Nam 2 củ", "edit", approved, edited); err != nil {
		t.Fatalf("Apply edit: %v", err)
	}
	flagged := mustState(t, "approved", new("bắn thằng Nam 2 củ tiền"), []string{"slang", "typo"})
	if err := s.Apply("note-001", "bắn thg Nam 2 củ", "flags", edited, flagged); err != nil {
		t.Fatalf("Apply flags: %v", err)
	}

	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAll size = %d, want 1", len(all))
	}
	got := all["note-001"]
	if got.Status != "approved" {
		t.Errorf("Status = %q, want approved", got.Status)
	}
	if got.EditedText == nil || *got.EditedText != "bắn thằng Nam 2 củ tiền" {
		t.Errorf("EditedText = %v, want edited text", got.EditedText)
	}
	if strings.Join(got.ManualFlags, ",") != "slang,typo" {
		t.Errorf("ManualFlags = %v, want [slang typo]", got.ManualFlags)
	}
	if got.Annotations != nil {
		t.Errorf("Annotations = %s, want nil", got.Annotations)
	}

	if n, err := s.CountEvents(); err != nil || n != 3 {
		t.Fatalf("CountEvents = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := s.UndoneEvents(); err != nil || n != 0 {
		t.Fatalf("UndoneEvents = (%d, %v), want (0, nil)", n, err)
	}

	events, err := s.Events(0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("Events len = %d, want 3", len(events))
	}
	kinds := []string{events[0].Kind, events[1].Kind, events[2].Kind}
	if strings.Join(kinds, ",") != "status,edit,flags" {
		t.Errorf("event kinds = %v, want [status edit flags]", kinds)
	}
	if events[0].Seq >= events[1].Seq || events[1].Seq >= events[2].Seq {
		t.Errorf("event seqs not ascending: %d %d %d", events[0].Seq, events[1].Seq, events[2].Seq)
	}
	if events[1].Before.Status != "approved" || events[1].After.EditedText == nil {
		t.Errorf("edit event states = %+v -> %+v", events[1].Before, events[1].After)
	}
	if last, err := s.Events(events[1].Seq); err != nil || len(last) != 1 || last[0].Seq != events[2].Seq {
		t.Errorf("Events(afterSeq) = (%v, %v), want just seq %d", last, err, events[2].Seq)
	}

	// A fresh Store over the same file must see exactly the same state.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	reopened, err := s2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll after reopen: %v", err)
	}
	got2 := reopened["note-001"]
	if got2.Status != got.Status || got2.EditedText == nil || *got2.EditedText != *got.EditedText ||
		strings.Join(got2.ManualFlags, ",") != "slang,typo" {
		t.Fatalf("reopened state = %+v, want %+v", got2, got)
	}
}

func TestApplyKeepsUnreviewedRowAndAnnotations(t *testing.T) {
	s, _ := openTestStore(t)

	ann := json.RawMessage(`{"type":"loan_out","amount_text":"2 củ"}`)
	after := RecordState{Status: "unreviewed", Annotations: ann}
	if err := s.Apply("note-002", "CK Nam 2tr", "flags", RecordState{}, after); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got, ok := all["note-002"]
	if !ok {
		t.Fatalf("unreviewed row was not kept: %v", all)
	}
	if got.Status != "unreviewed" {
		t.Errorf("Status = %q, want unreviewed", got.Status)
	}
	if string(got.Annotations) != string(ann) {
		t.Errorf("Annotations = %s, want %s", got.Annotations, ann)
	}
	if got.ManualFlags != nil {
		t.Errorf("ManualFlags = %v, want nil", got.ManualFlags)
	}
}

func TestUndoRestoresLatestAndSurvivesReopen(t *testing.T) {
	s, path := openTestStore(t)

	unreviewed := RecordState{Status: "unreviewed"}
	approved := RecordState{Status: "approved"}
	if err := s.Apply("note-001", "one", "status", unreviewed, approved); err != nil {
		t.Fatalf("Apply note-001: %v", err)
	}
	edited := RecordState{Status: "unreviewed", EditedText: new("two")}
	if err := s.Apply("note-002", "two-original", "edit", unreviewed, edited); err != nil {
		t.Fatalf("Apply note-002: %v", err)
	}
	flagged := RecordState{Status: "unreviewed", ManualFlags: []string{"slang"}}
	if err := s.Apply("note-002", "two-original", "flags", edited, flagged); err != nil {
		t.Fatalf("Apply note-002 flags: %v", err)
	}

	id, restored, ok, err := s.Undo()
	if err != nil || !ok {
		t.Fatalf("Undo = (%q, %+v, %v, %v), want ok", id, restored, ok, err)
	}
	if id != "note-002" || len(restored.ManualFlags) != 0 || restored.EditedText == nil {
		t.Fatalf("Undo restored %q %+v, want note-002 back to the edited state", id, restored)
	}
	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := all["note-002"]; got.EditedText == nil || len(got.ManualFlags) != 0 {
		t.Errorf("after undo note-002 = %+v, want edited without flags", got)
	}
	if n, err := s.UndoneEvents(); err != nil || n != 1 {
		t.Fatalf("UndoneEvents after undo = (%d, %v), want (1, nil)", n, err)
	}
	events, err := s.Events(0)
	if err != nil || len(events) != 3 {
		t.Fatalf("Events = (%v, %v)", events, err)
	}
	if !events[2].Undone || events[0].Undone || events[1].Undone {
		t.Errorf("only the latest event must be undone: %v", []bool{events[0].Undone, events[1].Undone, events[2].Undone})
	}

	// Undo is persisted: a fresh Store (i.e. a restart) keeps going from there.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if id, restored, ok, err := s2.Undo(); err != nil || !ok || id != "note-002" || restored.EditedText != nil {
		t.Fatalf("Undo after reopen = (%q, %+v, %v, %v), want note-002 edited cleared", id, restored, ok, err)
	}
	if id, _, ok, err := s2.Undo(); err != nil || !ok || id != "note-001" {
		t.Fatalf("second Undo after reopen = (%q, _, %v, %v), want note-001", id, ok, err)
	}
	if _, restored, ok, err := s2.Undo(); err != nil || ok {
		t.Fatalf("Undo with empty stack = (_, %+v, %v, %v), want ok=false", restored, ok, err)
	}
	final, err := s2.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := final["note-001"]; got.Status != "unreviewed" {
		t.Errorf("note-001 after undo = %+v, want unreviewed", got)
	}
	if got := final["note-002"]; got.EditedText != nil {
		t.Errorf("note-002 after undo = %+v, want no edit", got)
	}
}
