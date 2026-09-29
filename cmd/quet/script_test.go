package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scriptCorpus makes a fresh working directory holding corpus.jsonl (three records, the
// first with a metadata suggestion) and taxonomy.yaml (manual flags typo and offensive),
// isolated from the user's configuration. It returns the corpus path.
func scriptCorpus(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	files := map[string]string{
		"corpus.jsonl": `{"id":"n1","text":"CK Nam 2tr","source":"claude","suggested_flags":["slang"]}` + "\n" +
			`{"id":"n2","text":"bắn thg Nam 2 củ"}` + "\n" +
			`{"id":"n3","text":"hello   world\nagain"}` + "\n",
		"taxonomy.yaml": "manual:\n  typo: Spelling mistake\n  offensive:\n    description: Offensive content\n",
	}
	for name, body := range files {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return "corpus.jsonl"
}

// mustRun runs a command line that must succeed and returns its stdout.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := runCLI(args...)
	if code != 0 {
		t.Fatalf("quet %s: exit %d, stderr %q", strings.Join(args, " "), code, stderr)
	}
	return stdout
}

// decodeRecords parses JSONL record output.
func decodeRecords(t *testing.T, stdout string) []recordOut {
	t.Helper()
	var records []recordOut
	for line := range strings.Lines(stdout) {
		var rec recordOut
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

// showRecord returns the current state of one record via `quet show --json`.
func showRecord(t *testing.T, corpus, id string) recordOut {
	t.Helper()
	records := decodeRecords(t, mustRun(t, "show", corpus, id, "--json"))
	if len(records) != 1 {
		t.Fatalf("show %s printed %d records, want 1", id, len(records))
	}
	return records[0]
}

// recordIDs returns the IDs of records, in order.
func recordIDs(records []recordOut) []string {
	ids := make([]string, len(records))
	for i, rec := range records {
		ids[i] = rec.ID
	}
	return ids
}

func TestListJSONHonoursFilterAndLimit(t *testing.T) {
	corpus := scriptCorpus(t)

	all := decodeRecords(t, mustRun(t, "list", corpus, "--json"))
	if got, want := recordIDs(all), []string{"id:n1", "id:n2", "id:n3"}; !slices.Equal(got, want) {
		t.Fatalf("list ids = %v, want %v", got, want)
	}
	first := all[0]
	if first.Status != "unreviewed" || first.Text != "CK Nam 2tr" || first.Source != "claude" {
		t.Errorf("first record = %+v", first)
	}
	if first.ManualFlags == nil || first.Diagnostics == nil || !slices.Equal(first.SuggestedFlags, []string{"slang"}) {
		t.Errorf("first record arrays = manual %v, suggested %v, diagnostics %v", first.ManualFlags, first.SuggestedFlags, first.Diagnostics)
	}

	limited := decodeRecords(t, mustRun(t, "list", corpus, "--limit", "2", "--json"))
	if got := recordIDs(limited); !slices.Equal(got, []string{"id:n1", "id:n2"}) {
		t.Errorf("list --limit 2 = %v", got)
	}

	mustRun(t, "set", corpus, "id:n2", "--status", "approved")
	approved := decodeRecords(t, mustRun(t, "list", corpus, "--filter", "approved", "--json"))
	if got := recordIDs(approved); !slices.Equal(got, []string{"id:n2"}) {
		t.Errorf("list --filter approved = %v", got)
	}

	human := mustRun(t, "list", corpus, "--filter", "unreviewed")
	if want := "id:n1\tunreviewed\tCK Nam 2tr\nid:n3\tunreviewed\thello world again\n"; human != want {
		t.Errorf("list human output = %q, want %q", human, want)
	}
}

func TestShowUnknownIDFails(t *testing.T) {
	corpus := scriptCorpus(t)

	code, stdout, stderr := runCLI("show", corpus, "id:nope")
	if code != 1 || stdout != "" || !strings.Contains(stderr, `unknown record id "id:nope"`) {
		t.Fatalf("show unknown id: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestSetThenStatsJSON(t *testing.T) {
	corpus := scriptCorpus(t)

	set := decodeRecords(t, mustRun(t, "set", corpus, "id:n3", "id:n1", "--status", "rejected", "--json"))
	if got := recordIDs(set); !slices.Equal(got, []string{"id:n3", "id:n1"}) {
		t.Fatalf("set output ids = %v, want argument order", got)
	}
	for _, rec := range set {
		if rec.Status != "rejected" {
			t.Errorf("%s status = %q, want rejected", rec.ID, rec.Status)
		}
	}

	var stats statsOut
	if err := json.Unmarshal([]byte(mustRun(t, "stats", corpus, "--json")), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Rejected != 2 || stats.Unreviewed != 1 || stats.Approved != 0 {
		t.Errorf("stats = %+v, want total 3, rejected 2, unreviewed 1", stats)
	}
	if stats.Corpus != corpus || stats.Sidecar == "" || stats.Diagnostics == nil || stats.ManualFlags == nil {
		t.Errorf("stats = %+v", stats)
	}
}

func TestFlagAddRemoveValidatesNames(t *testing.T) {
	corpus := scriptCorpus(t)
	flags := []string{"--flags-file", "taxonomy.yaml"}

	added := decodeRecords(t, mustRun(t, append([]string{"flag", corpus, "id:n1", "id:n2", "--add", "typo,offensive", "--json"}, flags...)...))
	for _, rec := range added {
		if !slices.Equal(rec.ManualFlags, []string{"offensive", "typo"}) {
			t.Errorf("%s manual flags = %v, want [offensive typo]", rec.ID, rec.ManualFlags)
		}
	}
	mustRun(t, append([]string{"flag", corpus, "id:n1", "--remove", "typo"}, flags...)...)
	if got := showRecord(t, corpus, "id:n1").ManualFlags; !slices.Equal(got, []string{"offensive"}) {
		t.Errorf("after --remove typo: manual flags = %v, want [offensive]", got)
	}

	code, _, stderr := runCLI(append([]string{"flag", corpus, "id:n3", "--add", "typo,bogus"}, flags...)...)
	if code != 1 || !strings.Contains(stderr, `"bogus"`) || !strings.Contains(stderr, "typo, offensive") {
		t.Errorf("unknown flag: exit %d, stderr %q", code, stderr)
	}
	code, _, stderr = runCLI(append([]string{"flag", corpus, "id:n3", "id:nope", "--add", "typo"}, flags...)...)
	if code != 1 || !strings.Contains(stderr, `unknown record id "id:nope"`) {
		t.Errorf("unknown id: exit %d, stderr %q", code, stderr)
	}
	if got := showRecord(t, corpus, "id:n3").ManualFlags; len(got) != 0 {
		t.Errorf("failed flag commands wrote %v to id:n3", got)
	}

	var stats statsOut
	if err := json.Unmarshal([]byte(mustRun(t, append([]string{"stats", corpus, "--json"}, flags...)...)), &stats); err != nil {
		t.Fatal(err)
	}
	want := []flagCountOut{{"typo", "Spelling mistake", 1}, {"offensive", "Offensive content", 2}}
	if !slices.Equal(stats.ManualFlags, want) {
		t.Errorf("stats manual_flags = %+v, want %+v", stats.ManualFlags, want)
	}
}

func TestSuggestMergesWithMetadata(t *testing.T) {
	corpus := scriptCorpus(t)

	added := decodeRecords(t, mustRun(t, "suggest", corpus, "id:n1", "--add", "review_me,slang", "--json"))
	if len(added) != 1 || !slices.Equal(added[0].SuggestedFlags, []string{"review_me", "slang"}) {
		t.Fatalf("suggest --add output = %+v", added)
	}
	filtered := decodeRecords(t, mustRun(t, "list", corpus, "--filter", "suggested:review_me", "--json"))
	if got := recordIDs(filtered); !slices.Equal(got, []string{"id:n1"}) {
		t.Errorf("list --filter suggested:review_me = %v", got)
	}

	code, _, stderr := runCLI("suggest", corpus, "id:n1", "--remove", "slang")
	if code != 1 || !strings.Contains(stderr, "corpus metadata") {
		t.Errorf("removing a metadata suggestion: exit %d, stderr %q", code, stderr)
	}

	removed := decodeRecords(t, mustRun(t, "suggest", corpus, "id:n1", "--remove", "review_me", "--json"))
	if len(removed) != 1 || !slices.Equal(removed[0].SuggestedFlags, []string{"slang"}) {
		t.Errorf("suggest --remove output = %+v", removed)
	}
}

func TestEditTextStdinAndRevert(t *testing.T) {
	corpus := scriptCorpus(t)
	const original = "bắn thg Nam 2 củ"

	edited := decodeRecords(t, mustRun(t, "edit", corpus, "id:n2", "--text", "fixed text", "--json"))
	if len(edited) != 1 || !edited[0].Edited || edited[0].Text != "fixed text" {
		t.Fatalf("edit --text output = %+v", edited)
	}
	shown := showRecord(t, corpus, "id:n2")
	if shown.Text != "fixed text" || shown.OriginalText == nil || *shown.OriginalText != original {
		t.Errorf("after edit: show = %+v", shown)
	}

	saved := stdin
	t.Cleanup(func() { stdin = saved })
	stdin = strings.NewReader("from stdin\n")
	mustRun(t, "edit", corpus, "id:n2", "--text-file", "-")
	if got := showRecord(t, corpus, "id:n2").Text; got != "from stdin" {
		t.Errorf("after --text-file -: text = %q, want %q", got, "from stdin")
	}

	if out := mustRun(t, "edit", corpus, "id:n2", "--revert"); out != "unedited id:n2\n" {
		t.Errorf("edit --revert output = %q", out)
	}
	shown = showRecord(t, corpus, "id:n2")
	if shown.Edited || shown.OriginalText != nil || shown.Text != original {
		t.Errorf("after revert: show = %+v", shown)
	}
}

func TestUndoRevertsLastWrite(t *testing.T) {
	corpus := scriptCorpus(t)

	if out := mustRun(t, "undo", corpus, "--json"); out != "{\"undone\":null}\n" {
		t.Errorf("undo with empty log = %q", out)
	}
	if out := mustRun(t, "undo", corpus); out != "nothing to undo\n" {
		t.Errorf("undo with empty log (human) = %q", out)
	}

	mustRun(t, "set", corpus, "id:n1", "--status", "approved")
	mustRun(t, "set", corpus, "id:n3", "--status", "rejected")
	var undo struct {
		Undone *recordOut `json:"undone"`
	}
	if err := json.Unmarshal([]byte(mustRun(t, "undo", corpus, "--json")), &undo); err != nil {
		t.Fatal(err)
	}
	if undo.Undone == nil || undo.Undone.ID != "id:n3" || undo.Undone.Status != "unreviewed" {
		t.Fatalf("undo = %+v, want id:n3 back to unreviewed", undo.Undone)
	}
	if got := showRecord(t, corpus, "id:n1").Status; got != "approved" {
		t.Errorf("earlier write undone too: id:n1 status = %q", got)
	}
	if out := mustRun(t, "undo", corpus); out != "undid id:n1\n" {
		t.Errorf("second undo = %q", out)
	}
}

func TestExportToStdout(t *testing.T) {
	corpus := scriptCorpus(t)
	mustRun(t, "set", corpus, "id:n1", "--status", "approved")

	code, stdout, stderr := runCLI("export", corpus, "-o", "-")
	if code != 0 {
		t.Fatalf("export -o -: exit %d, stderr %q", code, stderr)
	}
	lines := slices.Collect(strings.Lines(stdout))
	if len(lines) != 1 {
		t.Fatalf("export stdout = %q, want one JSONL line", stdout)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil || rec["text"] != "CK Nam 2tr" {
		t.Errorf("export line = %q (%v)", lines[0], err)
	}
	if !strings.Contains(stderr, "Wrote 1 records to stdout") {
		t.Errorf("export stderr = %q", stderr)
	}
	if _, err := os.Stat("-"); err == nil {
		t.Error(`export created a file named "-"`)
	}
	if matches, _ := filepath.Glob("corpus.approved*"); len(matches) > 0 {
		t.Errorf("export -o - wrote files %v", matches)
	}
}
