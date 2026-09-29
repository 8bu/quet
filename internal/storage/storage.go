// Package storage persists review state in a SQLite sidecar (<corpus>.quet.db). Never touches the source corpus.
// It does not import review (review imports storage); statuses are plain strings here.
package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// RecordState is the persisted per-record state. Rows exist only for records that were ever touched.
type RecordState struct {
	Status      string  // "unreviewed" | "approved" | "rejected" | "needs_review"
	EditedText  *string // nil = not edited
	ManualFlags []string
	Annotations json.RawMessage // reserved (future structured annotations), stored as JSON TEXT, nullable
}

// Event is one persisted mutation (audit log + undo stack).
type Event struct {
	Seq      int64
	At       time.Time
	RecordID string
	Kind     string // "status" | "edit" | "flags" | "undo"
	Before   RecordState
	After    RecordState
	Undone   bool
}

type Store struct {
	db *sql.DB
}

// SidecarPath returns corpusPath + ".quet.db".
func SidecarPath(corpusPath string) string { return corpusPath + ".quet.db" }

const schemaVersion = "1"

const (
	createMetaTable = `CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL)`

	createRecordsTable = `CREATE TABLE IF NOT EXISTS records (
		id           TEXT PRIMARY KEY,
		status       TEXT NOT NULL,
		edited_text  TEXT,
		original_text TEXT,
		manual_flags TEXT NOT NULL DEFAULT '[]',
		auto_flags   TEXT NOT NULL DEFAULT '[]',
		annotations  TEXT,
		updated_at   TEXT NOT NULL)`

	createEventsTable = `CREATE TABLE IF NOT EXISTS events (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT,
		at           TEXT NOT NULL,
		record_id    TEXT NOT NULL,
		kind         TEXT NOT NULL,
		before_state TEXT NOT NULL,
		after_state  TEXT NOT NULL,
		undone       INTEGER NOT NULL DEFAULT 0)`

	upsertRecord = `INSERT INTO records
		(id, status, edited_text, original_text, manual_flags, auto_flags, annotations, updated_at)
		VALUES (?, ?, ?, ?, ?, '[]', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status       = excluded.status,
			edited_text  = excluded.edited_text,
			original_text = excluded.original_text,
			manual_flags = excluded.manual_flags,
			annotations  = excluded.annotations,
			updated_at   = excluded.updated_at`

	// restoreRecord leaves original_text and auto_flags untouched: the before-state blob
	// does not carry them.
	restoreRecord = `INSERT INTO records
		(id, status, edited_text, manual_flags, auto_flags, annotations, updated_at)
		VALUES (?, ?, ?, ?, '[]', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status       = excluded.status,
			edited_text  = excluded.edited_text,
			manual_flags = excluded.manual_flags,
			annotations  = excluded.annotations,
			updated_at   = excluded.updated_at`

	insertEvent = `INSERT INTO events (at, record_id, kind, before_state, after_state)
		VALUES (?, ?, ?, ?, ?)`
)

// Open opens/creates the DB (WAL, synchronous=FULL, busy_timeout), migrating schema (meta.schema_version).
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("storage: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage: create directory %s: %w", dir, err)
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(FULL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	// modernc.org/sqlite is safe for concurrent use but a single writer connection keeps the
	// sidecar's locking simple and predictable.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	for _, stmt := range []string{createMetaTable, createRecordsTable, createEventsTable} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("storage: migrate: %w", err)
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?) ON CONFLICT(key) DO NOTHING`,
		schemaVersion,
	); err != nil {
		return fmt.Errorf("storage: set schema_version: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// stateBlob is the JSON form of a RecordState as stored in the events table, so Undo can
// restore a record exactly.
type stateBlob struct {
	Status      string          `json:"status"`
	EditedText  *string         `json:"edited_text"`
	ManualFlags []string        `json:"manual_flags"`
	Annotations json.RawMessage `json:"annotations"`
}

func encodeState(st RecordState) (string, error) {
	blob := stateBlob{
		Status:      st.Status,
		EditedText:  st.EditedText,
		ManualFlags: st.ManualFlags,
	}
	if blob.ManualFlags == nil {
		blob.ManualFlags = []string{}
	}
	if len(st.Annotations) > 0 {
		blob.Annotations = st.Annotations
	}
	out, err := json.Marshal(blob)
	if err != nil {
		return "", fmt.Errorf("storage: encode state: %w", err)
	}
	return string(out), nil
}

func decodeState(raw string) (RecordState, error) {
	var blob stateBlob
	if err := json.Unmarshal([]byte(raw), &blob); err != nil {
		return RecordState{}, fmt.Errorf("storage: decode state: %w", err)
	}
	st := RecordState{
		Status:      blob.Status,
		EditedText:  blob.EditedText,
		ManualFlags: blob.ManualFlags,
		Annotations: blob.Annotations,
	}
	if len(st.ManualFlags) == 0 {
		st.ManualFlags = nil
	}
	if len(st.Annotations) == 0 || string(st.Annotations) == "null" {
		st.Annotations = nil
	}
	return st, nil
}

// manualFlagsJSON renders manual flags for the records table (never NULL).
func manualFlagsJSON(flags []string) (string, error) {
	if flags == nil {
		return "[]", nil
	}
	out, err := json.Marshal(flags)
	if err != nil {
		return "", fmt.Errorf("storage: encode manual flags: %w", err)
	}
	return string(out), nil
}

// annotationsArg renders annotations as a nullable TEXT value.
func annotationsArg(annotations json.RawMessage) any {
	if len(annotations) == 0 || string(annotations) == "null" {
		return nil
	}
	return string(annotations)
}

// LoadAll returns all persisted states keyed by record ID.
func (s *Store) LoadAll() (map[string]RecordState, error) {
	rows, err := s.db.Query(
		`SELECT id, status, edited_text, manual_flags, annotations FROM records`)
	if err != nil {
		return nil, fmt.Errorf("storage: load records: %w", err)
	}
	defer rows.Close()

	out := make(map[string]RecordState)
	for rows.Next() {
		var (
			id          string
			status      string
			edited      sql.NullString
			manualRaw   string
			annotations sql.NullString
		)
		if err := rows.Scan(&id, &status, &edited, &manualRaw, &annotations); err != nil {
			return nil, fmt.Errorf("storage: scan record: %w", err)
		}
		st := RecordState{Status: status}
		if edited.Valid {
			text := edited.String
			st.EditedText = &text
		}
		if err := json.Unmarshal([]byte(manualRaw), &st.ManualFlags); err != nil {
			return nil, fmt.Errorf("storage: record %s: manual flags: %w", id, err)
		}
		if len(st.ManualFlags) == 0 {
			st.ManualFlags = nil
		}
		if annotations.Valid && annotations.String != "" && annotations.String != "null" {
			st.Annotations = json.RawMessage(annotations.String)
		}
		out[id] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: load records: %w", err)
	}
	return out, nil
}

// Apply atomically (one tx) upserts the record row to `after` (also storing originalText) and appends an event.
func (s *Store) Apply(recordID, originalText, kind string, before, after RecordState) error {
	if recordID == "" {
		return errors.New("storage: empty record id")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	beforeJSON, err := encodeState(before)
	if err != nil {
		return err
	}
	afterJSON, err := encodeState(after)
	if err != nil {
		return err
	}
	manualJSON, err := manualFlagsJSON(after.ManualFlags)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("storage: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(upsertRecord,
		recordID, after.Status, after.EditedText, originalText, manualJSON,
		annotationsArg(after.Annotations), now,
	); err != nil {
		return fmt.Errorf("storage: save record %s: %w", recordID, err)
	}
	if _, err := tx.Exec(insertEvent, now, recordID, kind, beforeJSON, afterJSON); err != nil {
		return fmt.Errorf("storage: append event for %s: %w", recordID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit: %w", err)
	}
	return nil
}

// Undo atomically marks the latest not-undone event undone and restores its Before state. ok=false when nothing to undo.
func (s *Store) Undo() (recordID string, restored RecordState, ok bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", RecordState{}, false, fmt.Errorf("storage: begin: %w", err)
	}
	defer tx.Rollback()

	var (
		seq        int64
		beforeJSON string
	)
	err = tx.QueryRow(
		`SELECT seq, record_id, before_state FROM events WHERE undone = 0 ORDER BY seq DESC LIMIT 1`,
	).Scan(&seq, &recordID, &beforeJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", RecordState{}, false, nil
	}
	if err != nil {
		return "", RecordState{}, false, fmt.Errorf("storage: find undoable event: %w", err)
	}

	restored, err = decodeState(beforeJSON)
	if err != nil {
		return "", RecordState{}, false, err
	}
	manualJSON, err := manualFlagsJSON(restored.ManualFlags)
	if err != nil {
		return "", RecordState{}, false, err
	}

	if _, err := tx.Exec(`UPDATE events SET undone = 1 WHERE seq = ?`, seq); err != nil {
		return "", RecordState{}, false, fmt.Errorf("storage: mark event undone: %w", err)
	}
	if _, err := tx.Exec(restoreRecord,
		recordID, restored.Status, restored.EditedText, manualJSON,
		annotationsArg(restored.Annotations), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return "", RecordState{}, false, fmt.Errorf("storage: restore record %s: %w", recordID, err)
	}
	if err := tx.Commit(); err != nil {
		return "", RecordState{}, false, fmt.Errorf("storage: commit: %w", err)
	}
	return recordID, restored, true, nil
}

func scanEvent(scan func(dest ...any) error) (Event, error) {
	var (
		ev        Event
		at        string
		beforeRaw string
		afterRaw  string
		undone    int
	)
	if err := scan(&ev.Seq, &at, &ev.RecordID, &ev.Kind, &beforeRaw, &afterRaw, &undone); err != nil {
		return Event{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Event{}, fmt.Errorf("storage: parse event time %q: %w", at, err)
	}
	ev.At = t
	if ev.Before, err = decodeState(beforeRaw); err != nil {
		return Event{}, err
	}
	if ev.After, err = decodeState(afterRaw); err != nil {
		return Event{}, err
	}
	ev.Undone = undone != 0
	return ev, nil
}

// Events returns events with Seq > afterSeq in order (for stats/rate/debug).
func (s *Store) Events(afterSeq int64) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT seq, at, record_id, kind, before_state, after_state, undone
		 FROM events WHERE seq > ? ORDER BY seq`, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("storage: load events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		ev, err := scanEvent(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: scan event: %w", err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: load events: %w", err)
	}
	return out, nil
}

// CountEvents returns the total number of persisted events (undone ones included).
func (s *Store) CountEvents() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count events: %w", err)
	}
	return n, nil
}

// UndoneEvents returns the number of events marked undone.
func (s *Store) UndoneEvents() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE undone <> 0`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count undone events: %w", err)
	}
	return n, nil
}

func (s *Store) GetMeta(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("storage: get meta %s: %w", key, err)
	}
	return value, true, nil
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("storage: set meta %s: %w", key, err)
	}
	return nil
}
