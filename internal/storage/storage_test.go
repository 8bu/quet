package storage

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strconv"
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
	if want := strconv.Itoa(schemaVersion); !ok || version != want {
		t.Fatalf("schema_version = (%q, %v), want (%q, true)", version, ok, want)
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

// v1Schema is the version 1 sidecar DDL, whose records table still carried auto_flags.
var v1Schema = []string{
	`CREATE TABLE meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL)`,
	`CREATE TABLE records (
		id           TEXT PRIMARY KEY,
		status       TEXT NOT NULL,
		edited_text  TEXT,
		original_text TEXT,
		manual_flags TEXT NOT NULL DEFAULT '[]',
		auto_flags   TEXT NOT NULL DEFAULT '[]',
		annotations  TEXT,
		updated_at   TEXT NOT NULL)`,
	`CREATE TABLE events (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT,
		at           TEXT NOT NULL,
		record_id    TEXT NOT NULL,
		kind         TEXT NOT NULL,
		before_state TEXT NOT NULL,
		after_state  TEXT NOT NULL,
		undone       INTEGER NOT NULL DEFAULT 0)`,
	`INSERT INTO meta (key, value) VALUES ('schema_version', '1')`,
	`INSERT INTO records (id, status, edited_text, original_text, manual_flags, auto_flags, annotations, updated_at) VALUES
		('note-001', 'approved', NULL, 'one', '[]', '["duplicate"]', NULL, '2026-01-01T00:00:00Z'),
		('note-002', 'needs_review', 'two fixed', 'two', '["slang","typo"]', '[]', '{"k":1}', '2026-01-01T00:00:01Z')`,
	`INSERT INTO events (at, record_id, kind, before_state, after_state) VALUES
		('2026-01-01T00:00:00Z', 'note-001', 'status',
			'{"status":"unreviewed","edited_text":null,"manual_flags":[],"annotations":null}',
			'{"status":"approved","edited_text":null,"manual_flags":[],"annotations":null}'),
		('2026-01-01T00:00:01Z', 'note-002', 'flags',
			'{"status":"needs_review","edited_text":"two fixed","manual_flags":["slang"],"annotations":{"k":1}}',
			'{"status":"needs_review","edited_text":"two fixed","manual_flags":["slang","typo"],"annotations":{"k":1}}')`,
}

func TestOpenMigratesV1Sidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl.quet.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, stmt := range v1Schema {
		if _, err := raw.Exec(stmt); err != nil {
			raw.Close()
			t.Fatalf("build v1 sidecar: %v\n%s", err, stmt)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v1 sidecar: %v", err)
	}
	defer s.Close()

	if version, ok, err := s.GetMeta("schema_version"); err != nil || !ok || version != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version = (%q, %v, %v), want %d", version, ok, err, schemaVersion)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name = 'auto_flags'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("auto_flags columns after migration = (%d, %v), want 0", n, err)
	}
	var original string
	if err := s.db.QueryRow(`SELECT original_text FROM records WHERE id = 'note-002'`).Scan(&original); err != nil || original != "two" {
		t.Fatalf("original_text = (%q, %v), want two", original, err)
	}

	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := all["note-001"]; got.Status != "approved" || got.EditedText != nil || got.ManualFlags != nil {
		t.Errorf("note-001 = %+v, want approved, unedited, no flags", got)
	}
	got := all["note-002"]
	if got.Status != "needs_review" || got.EditedText == nil || *got.EditedText != "two fixed" ||
		strings.Join(got.ManualFlags, ",") != "slang,typo" || string(got.Annotations) != `{"k":1}` {
		t.Errorf("note-002 = %+v, want needs_review, edit kept, flags slang,typo, annotations kept", got)
	}
	if n, err := s.CountEvents(); err != nil || n != 2 {
		t.Fatalf("CountEvents = (%d, %v), want (2, nil)", n, err)
	}

	// Writing and undo work on the migrated table.
	if err := s.Apply("note-003", "three", "status", RecordState{Status: "unreviewed"}, RecordState{Status: "rejected"}); err != nil {
		t.Fatalf("Apply on migrated sidecar: %v", err)
	}
	if id, restored, ok, err := s.Undo(); err != nil || !ok || id != "note-003" || restored.Status != "unreviewed" {
		t.Fatalf("Undo new write = (%q, %+v, %v, %v)", id, restored, ok, err)
	}
	if id, restored, ok, err := s.Undo(); err != nil || !ok || id != "note-002" || strings.Join(restored.ManualFlags, ",") != "slang" {
		t.Fatalf("Undo migrated event = (%q, %+v, %v, %v), want note-002 back to slang", id, restored, ok, err)
	}
	all, err = s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll after undo: %v", err)
	}
	if got := all["note-002"]; strings.Join(got.ManualFlags, ",") != "slang" || got.EditedText == nil || *got.EditedText != "two fixed" {
		t.Errorf("note-002 after undo = %+v, want edit kept with flag slang", got)
	}
	if got := all["note-003"]; got.Status != "unreviewed" {
		t.Errorf("note-003 after undo = %+v, want unreviewed", got)
	}

	// Reopening an already migrated sidecar is a no-op.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen migrated sidecar: %v", err)
	}
	defer again.Close()
	if all, err := again.LoadAll(); err != nil || len(all) != 3 {
		t.Fatalf("LoadAll after reopen = (%d records, %v), want 3", len(all), err)
	}
}

// v2Schema is the version 2 sidecar DDL, whose records table has no suggested_flags column
// and whose event blobs carry no suggested_flags key.
var v2Schema = []string{
	`CREATE TABLE meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL)`,
	`CREATE TABLE records (
		id           TEXT PRIMARY KEY,
		status       TEXT NOT NULL,
		edited_text  TEXT,
		original_text TEXT,
		manual_flags TEXT NOT NULL DEFAULT '[]',
		annotations  TEXT,
		updated_at   TEXT NOT NULL)`,
	`CREATE TABLE events (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT,
		at           TEXT NOT NULL,
		record_id    TEXT NOT NULL,
		kind         TEXT NOT NULL,
		before_state TEXT NOT NULL,
		after_state  TEXT NOT NULL,
		undone       INTEGER NOT NULL DEFAULT 0)`,
	`INSERT INTO meta (key, value) VALUES ('schema_version', '2')`,
	`INSERT INTO records (id, status, edited_text, original_text, manual_flags, annotations, updated_at) VALUES
		('note-001', 'approved', NULL, 'one', '[]', NULL, '2026-01-01T00:00:00Z'),
		('note-002', 'rejected', 'two fixed', 'two', '["slang","typo"]', '{"k":1}', '2026-01-01T00:00:01Z')`,
	`INSERT INTO events (at, record_id, kind, before_state, after_state) VALUES
		('2026-01-01T00:00:00Z', 'note-001', 'status',
			'{"status":"unreviewed","edited_text":null,"manual_flags":[],"annotations":null}',
			'{"status":"approved","edited_text":null,"manual_flags":[],"annotations":null}'),
		('2026-01-01T00:00:01Z', 'note-002', 'status',
			'{"status":"needs_review","edited_text":"two fixed","manual_flags":["slang","typo"],"annotations":{"k":1}}',
			'{"status":"rejected","edited_text":"two fixed","manual_flags":["slang","typo"],"annotations":{"k":1}}')`,
}

func TestOpenMigratesV2SidecarAddingSuggestedFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.jsonl.quet.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, stmt := range v2Schema {
		if _, err := raw.Exec(stmt); err != nil {
			raw.Close()
			t.Fatalf("build v2 sidecar: %v\n%s", err, stmt)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open v2 sidecar: %v", err)
	}
	defer s.Close()

	if version, ok, err := s.GetMeta("schema_version"); err != nil || !ok || version != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version = (%q, %v, %v), want %d", version, ok, err, schemaVersion)
	}
	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := all["note-001"]; got.Status != "approved" || got.SuggestedFlags != nil {
		t.Errorf("note-001 = %+v, want approved with no suggested flags", got)
	}
	got := all["note-002"]
	if got.Status != "rejected" || got.EditedText == nil || *got.EditedText != "two fixed" ||
		strings.Join(got.ManualFlags, ",") != "slang,typo" || got.SuggestedFlags != nil ||
		string(got.Annotations) != `{"k":1}` {
		t.Errorf("note-002 = %+v, want rejected, edit, flags and annotations kept, no suggestions", got)
	}

	// Suggested flags persist on the migrated table and are undone like other state.
	before := got
	after := got
	after.SuggestedFlags = []string{"offensive", "spam"}
	if err := s.Apply("note-002", "two", "suggest", before, after); err != nil {
		t.Fatalf("Apply suggest: %v", err)
	}
	if all, err = s.LoadAll(); err != nil || strings.Join(all["note-002"].SuggestedFlags, ",") != "offensive,spam" {
		t.Fatalf("suggested flags after Apply = (%+v, %v), want offensive,spam", all["note-002"], err)
	}
	if id, restored, ok, err := s.Undo(); err != nil || !ok || id != "note-002" || restored.SuggestedFlags != nil {
		t.Fatalf("Undo suggest = (%q, %+v, %v, %v), want note-002 without suggestions", id, restored, ok, err)
	}

	// Events written before the migration (no suggested_flags key) still undo.
	if id, restored, ok, err := s.Undo(); err != nil || !ok || id != "note-002" ||
		restored.Status != "needs_review" || restored.SuggestedFlags != nil {
		t.Fatalf("Undo v2 event = (%q, %+v, %v, %v), want note-002 back to needs_review", id, restored, ok, err)
	}
	if all, err = s.LoadAll(); err != nil {
		t.Fatalf("LoadAll after undo: %v", err)
	}
	if got := all["note-002"]; got.Status != "needs_review" || strings.Join(got.ManualFlags, ",") != "slang,typo" ||
		got.SuggestedFlags != nil || storedOriginal(t, s, "note-002") != "two" {
		t.Errorf("note-002 after undo = %+v, want needs_review, flags kept, no suggestions", got)
	}

	// Reopening an already migrated sidecar is a no-op.
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen migrated sidecar: %v", err)
	}
	defer again.Close()
	if all, err := again.LoadAll(); err != nil || len(all) != 2 {
		t.Fatalf("LoadAll after reopen = (%d records, %v), want 2", len(all), err)
	}
}

// storedOriginal returns the stored original_text of record id.
func storedOriginal(t *testing.T, s *Store, id string) string {
	t.Helper()
	var text string
	if err := s.db.QueryRow(`SELECT original_text FROM records WHERE id = ?`, id).Scan(&text); err != nil {
		t.Fatalf("original_text of %s: %v", id, err)
	}
	return text
}
