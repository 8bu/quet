package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/annotate"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want command
	}{
		{
			name: "corpus then flags",
			args: []string{"corpus.jsonl", "--filter", "unreviewed"},
			want: command{kind: "review", corpus: "corpus.jsonl", status: "approved", filter: "unreviewed", hasFilter: true},
		},
		{
			name: "flags then corpus",
			args: []string{"--filter", "unreviewed", "corpus.jsonl"},
			want: command{kind: "review", corpus: "corpus.jsonl", status: "approved", filter: "unreviewed", hasFilter: true},
		},
		{
			name: "equals form and no-skip-reviewed",
			args: []string{"--filter=approved", "--no-skip-reviewed", "corpus.json"},
			want: command{kind: "review", corpus: "corpus.json", status: "approved", filter: "approved", hasFilter: true, noSkipReviewed: true},
		},
		{
			name: "stats subcommand",
			args: []string{"stats", "corpus.txt"},
			want: command{kind: "stats", corpus: "corpus.txt", status: "approved"},
		},
		{
			name: "stats subcommand with trailing flags",
			args: []string{"stats", "corpus.txt", "--flags-file", "flags.yaml"},
			want: command{kind: "stats", corpus: "corpus.txt", status: "approved", flagsFile: "flags.yaml", hasFlagsFile: true},
		},
		{
			name: "export subcommand",
			args: []string{"export", "corpus.jsonl", "--status", "needs-review", "-o", "review.jsonl", "-f"},
			want: command{kind: "export", corpus: "corpus.jsonl", status: "needs-review", hasStatus: true, output: "review.jsonl", hasOutput: true, force: true},
		},
		{
			name: "export defaults",
			args: []string{"export", "corpus.jsonl"},
			want: command{kind: "export", corpus: "corpus.jsonl", status: "approved"},
		},
		{
			name: "corpus named like a subcommand file is not a subcommand",
			args: []string{"stats.jsonl"},
			want: command{kind: "review", corpus: "stats.jsonl", status: "approved"},
		},
		{
			name: "queue named like the annotate subcommand is reviewed",
			args: []string{"annotate.jsonl"},
			want: command{kind: "review", corpus: "annotate.jsonl", status: "approved"},
		},
		{
			name: "no corpus opens the browser, keeping review flags",
			args: []string{"--filter", "unreviewed"},
			want: command{kind: "review", status: "approved", filter: "unreviewed", hasFilter: true},
		},
		{
			name: "diagnostic filter",
			args: []string{"corpus.jsonl", "--filter", "diagnostic:duplicate"},
			want: command{kind: "review", corpus: "corpus.jsonl", status: "approved", filter: "diagnostic:duplicate", hasFilter: true},
		},
		{
			name: "help flag",
			args: []string{"-h"},
			want: command{kind: "review", status: "approved", help: true},
		},
		{
			name: "help subcommand",
			args: []string{"help"},
			want: command{kind: "review", status: "approved", help: true},
		},
		{
			name: "version",
			args: []string{"--version"},
			want: command{kind: "review", status: "approved", version: true},
		},
		{
			name: "double dash ends flags",
			args: []string{"--", "--odd-name.jsonl"},
			want: command{kind: "review", corpus: "--odd-name.jsonl", status: "approved"},
		},
		{
			name: "init subcommand",
			args: []string{"init"},
			want: command{kind: "init", status: "approved"},
		},
		{
			name: "init global force",
			args: []string{"init", "--global", "--force"},
			want: command{kind: "init", status: "approved", global: true, force: true},
		},
		{
			name: "update subcommand",
			args: []string{"update"},
			want: command{kind: "update", status: "approved"},
		},
		{
			name: "update check",
			args: []string{"update", "--check"},
			want: command{kind: "update", status: "approved", check: true},
		},
		{
			name: "list with filter, limit and json",
			args: []string{"list", "corpus.jsonl", "--filter", "unreviewed", "--limit", "5", "--json"},
			want: command{kind: "list", corpus: "corpus.jsonl", status: "approved", filter: "unreviewed", hasFilter: true, limit: 5, hasLimit: true, json: true},
		},
		{
			name: "list with zero limit",
			args: []string{"list", "corpus.jsonl", "--limit=0"},
			want: command{kind: "list", corpus: "corpus.jsonl", status: "approved", hasLimit: true},
		},
		{
			name: "show one record",
			args: []string{"show", "corpus.jsonl", "id:note-001", "--json"},
			want: command{kind: "show", corpus: "corpus.jsonl", ids: []string{"id:note-001"}, status: "approved", json: true},
		},
		{
			name: "set several records",
			args: []string{"set", "corpus.jsonl", "id:a", "id:b", "--status", "needs_review"},
			want: command{kind: "set", corpus: "corpus.jsonl", ids: []string{"id:a", "id:b"}, status: "needs_review", hasStatus: true},
		},
		{
			name: "flag accumulates comma-split and repeated add",
			args: []string{"flag", "corpus.jsonl", "id:a", "--add", "pii, typo,,", "--add=tone", "--remove", " spam "},
			want: command{kind: "flag", corpus: "corpus.jsonl", ids: []string{"id:a"}, status: "approved", add: []string{"pii", "typo", "tone"}, hasAdd: true, remove: []string{"spam"}, hasRemove: true},
		},
		{
			name: "suggest with remove only",
			args: []string{"suggest", "corpus.jsonl", "id:a", "id:b", "--remove", "dup", "--json", "--config", "q.yaml"},
			want: command{kind: "suggest", corpus: "corpus.jsonl", ids: []string{"id:a", "id:b"}, status: "approved", remove: []string{"dup"}, hasRemove: true, json: true, configPath: "q.yaml", hasConfig: true},
		},
		{
			name: "edit with text",
			args: []string{"edit", "corpus.jsonl", "id:a", "--text", "-new text"},
			want: command{kind: "edit", corpus: "corpus.jsonl", ids: []string{"id:a"}, status: "approved", text: "-new text", hasText: true},
		},
		{
			name: "edit with text from stdin",
			args: []string{"edit", "corpus.jsonl", "id:a", "--text-file", "-"},
			want: command{kind: "edit", corpus: "corpus.jsonl", ids: []string{"id:a"}, status: "approved", textFile: "-", hasTextFile: true},
		},
		{
			name: "edit revert",
			args: []string{"edit", "corpus.jsonl", "id:a", "--revert"},
			want: command{kind: "edit", corpus: "corpus.jsonl", ids: []string{"id:a"}, status: "approved", revert: true},
		},
		{
			name: "undo",
			args: []string{"undo", "corpus.jsonl", "--json"},
			want: command{kind: "undo", corpus: "corpus.jsonl", status: "approved", json: true},
		},
		{
			name: "stats json",
			args: []string{"stats", "corpus.jsonl", "--json"},
			want: command{kind: "stats", corpus: "corpus.jsonl", status: "approved", json: true},
		},
		{
			name: "export to stdout",
			args: []string{"export", "corpus.jsonl", "-o", "-"},
			want: command{kind: "export", corpus: "corpus.jsonl", status: "approved", output: "-", hasOutput: true},
		},
		{
			name: "annotate",
			args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl"},
			want: command{kind: "annotate", corpus: "q.jsonl", status: "approved", schemaPath: "s.yaml", hasSchema: true, outPath: "l.jsonl", hasOut: true},
		},
		{
			name: "annotate with equals flags before the subcommand",
			args: []string{"--out=l.jsonl", "--schema=s.yaml", "annotate", "q.jsonl"},
			want: command{kind: "annotate", corpus: "q.jsonl", status: "approved", schemaPath: "s.yaml", hasSchema: true, outPath: "l.jsonl", hasOut: true},
		},
		{
			name: "annotate with flags around the queue",
			args: []string{"annotate", "--schema", "s.yaml", "q.jsonl", "--out=l.jsonl"},
			want: command{kind: "annotate", corpus: "q.jsonl", status: "approved", schemaPath: "s.yaml", hasSchema: true, outPath: "l.jsonl", hasOut: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if err != nil {
				t.Fatalf("parseArgs(%q): %v", tt.args, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseArgs(%q)\n got %+v\nwant %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseArgsErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"corpus.jsonl", "--bogus"}, want: "unknown flag --bogus"},
		{name: "missing value", args: []string{"corpus.jsonl", "--filter"}, want: "needs a value"},
		{name: "stats without corpus", args: []string{"stats"}, want: "missing corpus file"},
		{name: "two corpora", args: []string{"corpus.jsonl", "other.jsonl"}, want: `unexpected argument "other.jsonl"`},
		{name: "unknown status", args: []string{"export", "corpus.jsonl", "--status", "bogus"}, want: "unknown status"},
		{name: "unknown format", args: []string{"export", "corpus.jsonl", "-o", "x.jsonl", "--format", "csv"}, want: "unknown format"},
		{name: "filter outside review", args: []string{"export", "corpus.jsonl", "--filter", "all"}, want: "--filter is only valid when reviewing"},
		{name: "unknown filter", args: []string{"corpus.jsonl", "--filter", "bogus"}, want: "unknown filter"},
		{name: "removed auto filter", args: []string{"corpus.jsonl", "--filter", "auto"}, want: "unknown filter"},
		{name: "removed auto filter with name", args: []string{"corpus.jsonl", "--filter", "auto:duplicate"}, want: "unknown filter"},
		{name: "status outside export", args: []string{"corpus.jsonl", "--status", "approved"}, want: "--status is only valid with `quet export`"},
		{name: "bool flag with value", args: []string{"corpus.jsonl", "--force=yes"}, want: "does not take a value"},
		{name: "init with argument", args: []string{"init", "extra"}, want: `unexpected argument "extra"`},
		{name: "filter with init", args: []string{"init", "--filter", "x"}, want: "--filter is not valid with `quet init`"},
		{name: "global outside init", args: []string{"corpus.jsonl", "--global"}, want: "--global is only valid with `quet init`"},
		{name: "force when reviewing", args: []string{"corpus.jsonl", "--force"}, want: "--force is only valid with `quet export` or `quet init`"},
		{name: "update with argument", args: []string{"update", "x"}, want: `unexpected argument "x"`},
		{name: "check when reviewing", args: []string{"corpus.jsonl", "--check"}, want: "--check is only valid with `quet update`"},
		{name: "check with init", args: []string{"init", "--check"}, want: "--check is only valid with `quet update`"},
		{name: "filter with update", args: []string{"update", "--filter", "x"}, want: "--filter is not valid with `quet update`"},
		{name: "force with update", args: []string{"update", "--force"}, want: "--force is not valid with `quet update`"},
		{name: "list without corpus", args: []string{"list"}, want: "missing corpus file"},
		{name: "list with id", args: []string{"list", "corpus.jsonl", "id:a"}, want: `unexpected argument "id:a"`},
		{name: "undo with id", args: []string{"undo", "corpus.jsonl", "id:a"}, want: `unexpected argument "id:a"`},
		{name: "show without id", args: []string{"show", "corpus.jsonl"}, want: "`quet show` needs a record id"},
		{name: "show with two ids", args: []string{"show", "corpus.jsonl", "id:a", "id:b"}, want: "takes exactly one record id"},
		{name: "edit with two ids", args: []string{"edit", "corpus.jsonl", "id:a", "id:b", "--revert"}, want: "takes exactly one record id"},
		{name: "set without id", args: []string{"set", "corpus.jsonl", "--status", "approved"}, want: "needs at least one record id"},
		{name: "flag without id", args: []string{"flag", "corpus.jsonl", "--add", "pii"}, want: "needs at least one record id"},
		{name: "suggest without id", args: []string{"suggest", "corpus.jsonl", "--add", "pii"}, want: "needs at least one record id"},
		{name: "set without status", args: []string{"set", "corpus.jsonl", "id:a"}, want: "`quet set` needs --status"},
		{name: "set with export preset", args: []string{"set", "corpus.jsonl", "id:a", "--status", "all"}, want: "unknown status"},
		{name: "flag with nothing to do", args: []string{"flag", "corpus.jsonl", "id:a"}, want: "`quet flag` needs --add or --remove"},
		{name: "suggest with only empty names", args: []string{"suggest", "corpus.jsonl", "id:a", "--add", " , "}, want: "`quet suggest` needs --add or --remove"},
		{name: "edit with nothing", args: []string{"edit", "corpus.jsonl", "id:a"}, want: "needs exactly one of --text, --text-file or --revert"},
		{name: "edit with text and revert", args: []string{"edit", "corpus.jsonl", "id:a", "--text", "x", "--revert"}, want: "needs exactly one of"},
		{name: "edit with text and text-file", args: []string{"edit", "corpus.jsonl", "id:a", "--text", "x", "--text-file", "f"}, want: "needs exactly one of"},
		{name: "negative limit", args: []string{"list", "corpus.jsonl", "--limit", "-1"}, want: "--limit needs a non-negative whole number"},
		{name: "non-numeric limit", args: []string{"list", "corpus.jsonl", "--limit", "ten"}, want: "--limit needs a non-negative whole number"},
		{name: "unknown list filter", args: []string{"list", "corpus.jsonl", "--filter", "bogus"}, want: "unknown filter"},
		{name: "limit with show", args: []string{"show", "corpus.jsonl", "id:a", "--limit", "1"}, want: "--limit is not valid with `quet show`"},
		{name: "add with set", args: []string{"set", "corpus.jsonl", "id:a", "--status", "approved", "--add", "x"}, want: "--add is not valid with `quet set`"},
		{name: "status with flag", args: []string{"flag", "corpus.jsonl", "id:a", "--add", "x", "--status", "approved"}, want: "--status is not valid with `quet flag`"},
		{name: "no-skip-reviewed with list", args: []string{"list", "corpus.jsonl", "--no-skip-reviewed"}, want: "--no-skip-reviewed is not valid with `quet list`"},
		{name: "json when reviewing", args: []string{"corpus.jsonl", "--json"}, want: "--json is not valid when reviewing"},
		{name: "json with init", args: []string{"init", "--json"}, want: "--json is not valid with `quet init`"},
		{name: "json with export", args: []string{"export", "corpus.jsonl", "--json"}, want: "--json is not valid with `quet export`"},
		{name: "revert with stats", args: []string{"stats", "corpus.jsonl", "--revert"}, want: "--revert is not valid with `quet stats`"},
		{name: "annotate without queue", args: []string{"annotate", "--schema", "s.yaml", "--out", "l.jsonl"}, want: "missing queue file"},
		{name: "annotate without schema", args: []string{"annotate", "q.jsonl", "--out", "l.jsonl"}, want: "`quet annotate` needs --schema"},
		{name: "annotate with empty schema", args: []string{"annotate", "q.jsonl", "--schema=", "--out", "l.jsonl"}, want: "`quet annotate` needs --schema"},
		{name: "annotate without out", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml"}, want: "`quet annotate` needs --out"},
		{name: "annotate without flags", args: []string{"annotate", "q.jsonl"}, want: "`quet annotate` needs --schema"},
		{name: "annotate schema missing value", args: []string{"annotate", "q.jsonl", "--out", "l.jsonl", "--schema"}, want: "flag --schema needs a value"},
		{name: "annotate with two queues", args: []string{"annotate", "q.jsonl", "r.jsonl", "--schema", "s.yaml", "--out", "l.jsonl"}, want: `unexpected argument "r.jsonl"`},
		{name: "filter with annotate", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--filter", "all"}, want: "--filter is not valid with `quet annotate`"},
		{name: "output with annotate", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "-o", "x.jsonl"}, want: "--output is not valid with `quet annotate`"},
		{name: "schema when reviewing", args: []string{"corpus.jsonl", "--schema", "s.yaml"}, want: "--schema is only valid with `quet annotate`"},
		{name: "out when reviewing", args: []string{"corpus.jsonl", "--out=l.jsonl"}, want: "--out is only valid with `quet annotate`"},
		{name: "out with stats", args: []string{"stats", "corpus.jsonl", "--out", "l.jsonl"}, want: "--out is only valid with `quet annotate`"},
		{name: "schema with list", args: []string{"list", "corpus.jsonl", "--schema", "s.yaml"}, want: "--schema is only valid with `quet annotate`"},
		{name: "schema with init", args: []string{"init", "--schema", "s.yaml"}, want: "--schema is only valid with `quet annotate`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseArgs(tt.args)
			if err == nil {
				t.Fatalf("parseArgs(%q): want error, got nil", tt.args)
			}
			var usage *usageError
			if !errors.As(err, &usage) {
				t.Fatalf("parseArgs(%q) error %v is not a usage error", tt.args, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseArgs(%q) error = %q, want it to contain %q", tt.args, err, tt.want)
			}
		})
	}
}

func TestHelpTextIdentity(t *testing.T) {
	want := "Quet — Quick Utility for Evaluating Text\n\n" +
		"A fast, keyboard-first TUI for reviewing and curating text corpora.\n"
	if got := helpText(); !strings.HasPrefix(got, want) {
		t.Errorf("help text starts with %q, want %q", got[:min(len(got), len(want))], want)
	}
}

func TestStatusPreset(t *testing.T) {
	names := []string{"approved", "rejected", "needs_review", "needs-review", "all"}
	for _, name := range names {
		preset, err := statusPreset(name)
		if err != nil {
			t.Errorf("statusPreset(%q): %v", name, err)
			continue
		}
		if name != "needs-review" && preset.Name != name {
			t.Errorf("statusPreset(%q) = %q", name, preset.Name)
		}
	}
	if _, err := statusPreset("bogus"); err == nil {
		t.Error("statusPreset(bogus): want error, got nil")
	}
}

func TestReviewNeedsInteractiveTerminal(t *testing.T) {
	saved := isInteractive
	isInteractive = func() bool { return false }
	t.Cleanup(func() { isInteractive = saved })

	var stdout, stderr bytes.Buffer
	code := runReview(command{kind: "review", corpus: "missing.jsonl"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	want := "quet: the review screen needs an interactive terminal; use quet list/show/set/flag/suggest/edit for scripted review (see quet help)\n"
	if stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout %q, want nothing", stdout.String())
	}
}

// writeAnnotateFixture writes a small queue and schema to a temporary directory and
// returns their paths and the labels path next to them.
func writeAnnotateFixture(t *testing.T) (queue, schema, out string) {
	t.Helper()
	dir := t.TempDir()
	queue = filepath.Join(dir, "queue.jsonl")
	schema = filepath.Join(dir, "schema.yaml")
	out = filepath.Join(dir, "labels.jsonl")
	files := map[string]string{
		queue:  "{\"id\":\"a\",\"text\":\"great service\"}\n{\"id\":\"b\",\"text\":\"phở ngon quá\"}\n",
		schema: "types:\n  positive: Praise.\n  negative: Complaint.\nstatuses: [complete, uncertain, skipped]\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return queue, schema, out
}

// stubAnnotateScreen replaces the terminal checks and the annotation screen for one test
// and returns a pointer to the number of times the screen was launched.
func stubAnnotateScreen(t *testing.T, interactive bool) *int {
	t.Helper()
	savedInteractive, savedLaunch := isInteractive, launchAnnotate
	t.Cleanup(func() { isInteractive, launchAnnotate = savedInteractive, savedLaunch })
	launched := 0
	isInteractive = func() bool { return interactive }
	launchAnnotate = func(*annotate.Session) error {
		launched++
		return nil
	}
	return &launched
}

func TestAnnotateNeedsInteractiveTerminal(t *testing.T) {
	launched := stubAnnotateScreen(t, false)
	queue, schema, out := writeAnnotateFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--out", out}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if want := "quet: quet annotate needs an interactive terminal\n"; stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout %q, want nothing", stdout.String())
	}
	if *launched != 0 {
		t.Errorf("annotation screen launched %d times, want 0", *launched)
	}
}

func TestAnnotateOpenErrorSkipsScreen(t *testing.T) {
	launched := stubAnnotateScreen(t, true)
	queue, _, out := writeAnnotateFixture(t)
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", missing, "--out", out}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "quet: ") || strings.Contains(got, "Usage:") {
		t.Errorf("stderr %q, want a one-line runtime error", got)
	}
	if *launched != 0 {
		t.Errorf("annotation screen launched %d times, want 0", *launched)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("labels file exists after a failed open (stat error %v)", err)
	}
}

func TestAnnotateLaunchesScreen(t *testing.T) {
	launched := stubAnnotateScreen(t, true)
	queue, schema, out := writeAnnotateFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema=" + schema, "--out=" + out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, stderr.String())
	}
	if *launched != 1 {
		t.Errorf("annotation screen launched %d times, want 1", *launched)
	}
}
