package export

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/review"
	"github.com/8bu/quet/internal/storage"
)

func TestPresets(t *testing.T) {
	presets := Presets()
	want := []Preset{
		{
			Name:    "approved",
			Label:   "Export approved",
			Options: Options{Statuses: []review.ReviewStatus{review.Approved}},
			Suffix:  ".approved.jsonl",
		},
		{
			Name:    "rejected",
			Label:   "Export rejected",
			Options: Options{Statuses: []review.ReviewStatus{review.Rejected}},
			Suffix:  ".rejected.jsonl",
		},
		{
			Name:    "needs_review",
			Label:   "Export needs review",
			Options: Options{Statuses: []review.ReviewStatus{review.NeedsReview}},
			Suffix:  ".needs_review.jsonl",
		},
		{
			Name:  "all",
			Label: "Export all with review metadata",
			Options: Options{
				Statuses:   []review.ReviewStatus{review.Unreviewed, review.Approved, review.Rejected, review.NeedsReview},
				WithReview: true,
			},
			Suffix: ".reviewed.jsonl",
		},
		{
			Name:    "clean",
			Label:   "Export clean approved corpus",
			Options: Options{Statuses: []review.ReviewStatus{review.Approved}},
			Suffix:  ".clean.jsonl",
		},
	}
	if len(presets) != len(want) {
		t.Fatalf("got %d presets, want %d", len(presets), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(presets[i], want[i]) {
			t.Errorf("preset %d = %+v, want %+v", i, presets[i], want[i])
		}
	}
}

func TestDefaultPath(t *testing.T) {
	dir := t.TempDir()
	corpus := filepath.Join(dir, "corpus.jsonl")

	if got, want := DefaultPath(corpus, ".approved.jsonl"), filepath.Join(dir, "corpus.approved.jsonl"); got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}

	writeFile(t, filepath.Join(dir, "corpus.approved.jsonl"))
	if got, want := DefaultPath(corpus, ".approved.jsonl"), filepath.Join(dir, "corpus.approved-1.jsonl"); got != want {
		t.Errorf("DefaultPath with taken path = %q, want %q", got, want)
	}

	writeFile(t, filepath.Join(dir, "corpus.approved-1.jsonl"))
	if got, want := DefaultPath(corpus, ".approved.jsonl"), filepath.Join(dir, "corpus.approved-2.jsonl"); got != want {
		t.Errorf("DefaultPath with two taken paths = %q, want %q", got, want)
	}

	plain := filepath.Join(dir, "notes")
	if got, want := DefaultPath(plain, ".clean.jsonl"), filepath.Join(dir, "notes.clean.jsonl"); got != want {
		t.Errorf("DefaultPath without extension = %q, want %q", got, want)
	}
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Write(nil, &buf, Options{Format: "csv"}); err == nil {
		t.Fatal("Write with an unknown format: want error, got nil")
	}
	if buf.Len() != 0 {
		t.Errorf("Write with an unknown format wrote %d bytes, want 0", buf.Len())
	}
}

const testCorpus = `{"id": "note-a", "text": "cho Nam vay 2tr", "source": "claude", "batch": "t1", "suggested_flags": ["synthetic-looking"]}
{"id": "note-b", "text": "CK Nam 2tr", "source": "qwen", "batch": "t1", "suggested_flags": []}
{"id": "note-c", "text": "ckkkkkkkkk Nam 2tr", "source": "qwen", "batch": "t1"}
`

func TestWriteAndToFile(t *testing.T) {
	dir := t.TempDir()
	corpusPath := filepath.Join(dir, "corpus.jsonl")
	if err := os.WriteFile(corpusPath, []byte(testCorpus), 0o600); err != nil {
		t.Fatal(err)
	}

	session, err := review.Open(corpusPath, config.Default(), nil)
	if err != nil {
		t.Fatalf("review.Open: %v", err)
	}
	defer session.Close()

	approveRecord(t, session, 0, review.Approved)
	approveRecord(t, session, 1, review.Rejected)
	session.Goto(2)
	if err := session.SaveEdit("ckkkkkkkkk Nam 2tr (đã sửa)"); err != nil {
		t.Fatalf("SaveEdit: %v", err)
	}
	if err := session.SetManualFlags([]string{"typo"}); err != nil {
		t.Fatalf("SetManualFlags: %v", err)
	}

	t.Run("jsonl with review metadata", func(t *testing.T) {
		var buf bytes.Buffer
		n, err := Write(session, &buf, Options{Statuses: []review.ReviewStatus{review.Approved}, WithReview: true})
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != 1 {
			t.Fatalf("Write wrote %d records, want 1", n)
		}
		line := strings.TrimSuffix(buf.String(), "\n")
		record := decodeObject(t, line)
		for key, want := range map[string]any{"id": "note-a", "text": "cho Nam vay 2tr", "source": "claude", "batch": "t1"} {
			if got := record[key]; got != want {
				t.Errorf("record[%q] = %v, want %v", key, got, want)
			}
		}
		meta, ok := record["quet"].(map[string]any)
		if !ok {
			t.Fatalf("record has no quet object: %s", line)
		}
		if meta["id"] != "id:note-a" || meta["status"] != "approved" || meta["edited"] != false {
			t.Errorf("quet metadata = %v", meta)
		}
		if _, ok := meta["original_text"]; ok {
			t.Errorf("unedited record carries original_text: %v", meta["original_text"])
		}
		if !reflect.DeepEqual(meta["manual_flags"], []any{}) {
			t.Errorf("manual_flags = %v, want []", meta["manual_flags"])
		}
		if !reflect.DeepEqual(meta["suggested_flags"], []any{"synthetic-looking"}) {
			t.Errorf("suggested_flags = %v", meta["suggested_flags"])
		}
		if _, ok := meta["auto_flags"]; !ok {
			t.Errorf("auto_flags missing from %v", meta)
		}
	})

	t.Run("clean shape has no review metadata", func(t *testing.T) {
		var buf bytes.Buffer
		preset, err := presetByName("clean")
		if err != nil {
			t.Fatal(err)
		}
		n, err := Write(session, &buf, preset.Options)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != 1 {
			t.Fatalf("Write wrote %d records, want 1", n)
		}
		record := decodeObject(t, strings.TrimSuffix(buf.String(), "\n"))
		if _, ok := record["quet"]; ok {
			t.Errorf("clean export carries review metadata: %s", buf.String())
		}
	})

	t.Run("all statuses keep corpus order and show edits", func(t *testing.T) {
		var buf bytes.Buffer
		n, err := Write(session, &buf, Options{WithReview: true})
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != 3 {
			t.Fatalf("Write wrote %d records, want 3", n)
		}
		lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("got %d lines, want 3", len(lines))
		}
		for i, id := range []string{"note-a", "note-b", "note-c"} {
			if got := decodeObject(t, lines[i])["id"]; got != id {
				t.Errorf("line %d id = %v, want %q", i, got, id)
			}
		}
		meta := decodeObject(t, lines[2])["quet"].(map[string]any)
		if meta["edited"] != true || meta["original_text"] != "ckkkkkkkkk Nam 2tr" {
			t.Errorf("edited metadata = %v", meta)
		}
		if !reflect.DeepEqual(meta["manual_flags"], []any{"typo"}) {
			t.Errorf("manual_flags = %v, want [typo]", meta["manual_flags"])
		}
	})

	t.Run("txt format", func(t *testing.T) {
		var buf bytes.Buffer
		n, err := Write(session, &buf, Options{Statuses: []review.ReviewStatus{review.Approved}, Format: "txt"})
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != 1 || buf.String() != "cho Nam vay 2tr\n" {
			t.Errorf("txt export = %q (%d records)", buf.String(), n)
		}
	})

	t.Run("to file", func(t *testing.T) {
		out := filepath.Join(dir, "out.jsonl")
		n, err := ToFile(session, out, Options{Statuses: []review.ReviewStatus{review.Approved}, WithReview: true}, false)
		if err != nil {
			t.Fatalf("ToFile: %v", err)
		}
		if n != 1 {
			t.Fatalf("ToFile wrote %d records, want 1", n)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeObject(t, strings.TrimSuffix(string(data), "\n"))["id"]; got != "note-a" {
			t.Errorf("exported id = %v, want note-a", got)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*")); len(leftovers) != 0 {
			t.Errorf("temp files left behind: %v", leftovers)
		}

		if _, err := ToFile(session, out, Options{}, false); err == nil {
			t.Error("ToFile over an existing file without force: want error, got nil")
		}
		if _, err := ToFile(session, out, Options{}, true); err != nil {
			t.Errorf("ToFile with force: %v", err)
		}
	})

	t.Run("refuses the corpus and its sidecar", func(t *testing.T) {
		if _, err := ToFile(session, corpusPath, Options{}, false); err == nil {
			t.Error("ToFile over the source corpus: want error, got nil")
		}
		if _, err := ToFile(session, storage.SidecarPath(corpusPath), Options{}, true); err == nil {
			t.Error("ToFile over the sidecar: want error, got nil")
		}
		data, err := os.ReadFile(corpusPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != testCorpus {
			t.Error("source corpus was modified")
		}
	})
}

func approveRecord(t *testing.T, session *review.Session, index int, status review.ReviewStatus) {
	t.Helper()
	session.Goto(index)
	if _, err := session.SetStatus(status); err != nil {
		t.Fatalf("SetStatus(%s) on record %d: %v", status, index, err)
	}
}

func presetByName(name string) (Preset, error) {
	for _, preset := range Presets() {
		if preset.Name == name {
			return preset, nil
		}
	}
	return Preset{}, os.ErrNotExist
}

func decodeObject(t *testing.T, line string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(line), &object); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return object
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}
