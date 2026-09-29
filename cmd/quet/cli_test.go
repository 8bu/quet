package main

import (
	"errors"
	"strings"
	"testing"
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
			name: "no corpus opens the browser, keeping review flags",
			args: []string{"--filter", "unreviewed"},
			want: command{kind: "review", status: "approved", filter: "unreviewed", hasFilter: true},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if err != nil {
				t.Fatalf("parseArgs(%q): %v", tt.args, err)
			}
			if got != tt.want {
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
		{name: "status outside export", args: []string{"corpus.jsonl", "--status", "approved"}, want: "--status is only valid with `quet export`"},
		{name: "bool flag with value", args: []string{"corpus.jsonl", "--force=yes"}, want: "does not take a value"},
		{name: "init with argument", args: []string{"init", "extra"}, want: `unexpected argument "extra"`},
		{name: "filter with init", args: []string{"init", "--filter", "x"}, want: "--filter is not valid with `quet init`"},
		{name: "global outside init", args: []string{"corpus.jsonl", "--global"}, want: "--global is only valid with `quet init`"},
		{name: "force when reviewing", args: []string{"corpus.jsonl", "--force"}, want: "--force is only valid with `quet export` or `quet init`"},
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
