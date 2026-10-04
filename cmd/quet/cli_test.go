package main

import (
	"bytes"
	"errors"
	"fmt"
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
		{
			name: "annotate re-check with labels",
			args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--labels=l.jsonl"},
			want: command{kind: "annotate", corpus: "q.jsonl", status: "approved", schemaPath: "s.yaml", hasSchema: true, labelsPath: "l.jsonl", hasLabels: true},
		},
		{
			name: "annotate with proposals and out",
			args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--proposals", "p.jsonl"},
			want: command{kind: "annotate", corpus: "q.jsonl", status: "approved", schemaPath: "s.yaml", hasSchema: true, outPath: "l.jsonl", hasOut: true, proposalsPath: "p.jsonl", hasProposals: true},
		},
		{
			name: "annotate re-check with proposals equals flag",
			args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--labels=l.jsonl", "--proposals=p.jsonl"},
			want: command{kind: "annotate", corpus: "q.jsonl", status: "approved", schemaPath: "s.yaml", hasSchema: true, labelsPath: "l.jsonl", hasLabels: true, proposalsPath: "p.jsonl", hasProposals: true},
		},
		{
			name: "web remote add",
			args: []string{"web", "remote", "add", "origin", "https://quet.example.com"},
			want: command{kind: "web", status: "approved", webAction: "remote", webSub: "add", webArgs: []string{"origin", "https://quet.example.com"}},
		},
		{
			name: "web remote list",
			args: []string{"web", "remote", "list"},
			want: command{kind: "web", status: "approved", webAction: "remote", webSub: "list"},
		},
		{
			name: "web remote default",
			args: []string{"web", "remote", "default", "origin"},
			want: command{kind: "web", status: "approved", webAction: "remote", webSub: "default", webArgs: []string{"origin"}},
		},
		{
			name: "web login default remote",
			args: []string{"web", "login"},
			want: command{kind: "web", status: "approved", webAction: "login"},
		},
		{
			name: "web login named remote",
			args: []string{"web", "login", "origin"},
			want: command{kind: "web", status: "approved", webAction: "login", webArgs: []string{"origin"}},
		},
		{
			name: "web login with the browser flag",
			args: []string{"web", "login", "--no-browser", "origin"},
			want: command{kind: "web", status: "approved", webAction: "login", webArgs: []string{"origin"}, noBrowser: true},
		},
		{
			name: "web login with a service token",
			args: []string{"web", "login", "--service-token"},
			want: command{kind: "web", status: "approved", webAction: "login", serviceToken: true},
		},
		{
			name: "web logout named remote",
			args: []string{"web", "logout", "origin"},
			want: command{kind: "web", status: "approved", webAction: "logout", webArgs: []string{"origin"}},
		},
		{
			name: "web push with every flag, any order",
			args: []string{"web", "push", "--schema=s.yaml", "q.jsonl", "--project", "expenses", "--name", "Expenses 2026", "--proposals", "p.jsonl", "--remote", "origin"},
			want: command{
				kind: "web", status: "approved", webAction: "push", webArgs: []string{"q.jsonl"},
				schemaPath: "s.yaml", hasSchema: true, project: "expenses", hasProject: true,
				projectName: "Expenses 2026", hasName: true, proposalsPath: "p.jsonl", hasProposals: true,
				remote: "origin", hasRemote: true,
			},
		},
		{
			name: "web list json",
			args: []string{"--json", "web", "list", "--remote=origin"},
			want: command{kind: "web", status: "approved", webAction: "list", json: true, remote: "origin", hasRemote: true},
		},
		{
			name: "web pull user to a fresh file",
			args: []string{"web", "pull", "--project", "p", "--user", "alice", "--out", "l.jsonl", "--force"},
			want: command{
				kind: "web", status: "approved", webAction: "pull", project: "p", hasProject: true,
				user: "alice", hasUser: true, outPath: "l.jsonl", hasOut: true, force: true,
			},
		},
		{
			name: "web pull user into labels without project (sidecar)",
			args: []string{"web", "pull", "--user", "alice", "--labels", "l.jsonl"},
			want: command{
				kind: "web", status: "approved", webAction: "pull",
				user: "alice", hasUser: true, labelsPath: "l.jsonl", hasLabels: true,
			},
		},
		{
			name: "web pull all",
			args: []string{"web", "pull", "--project", "p", "--all", "--out-dir", "dir", "-f"},
			want: command{
				kind: "web", status: "approved", webAction: "pull", project: "p", hasProject: true,
				all: true, outDir: "dir", hasOutDir: true, force: true,
			},
		},
		{
			name: "file named web is reviewed",
			args: []string{"web.jsonl"},
			want: command{kind: "review", corpus: "web.jsonl", status: "approved"},
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
		{name: "annotate without out", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml"}, want: "`quet annotate` needs --out <labels.jsonl> or --labels <labels.jsonl>"},
		{name: "annotate with out and labels", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--labels", "l.jsonl"}, want: "--out and --labels are mutually exclusive"},
		{name: "annotate labels missing value", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--labels"}, want: "flag --labels needs a value"},
		{name: "annotate without flags", args: []string{"annotate", "q.jsonl"}, want: "`quet annotate` needs --schema"},
		{name: "annotate schema missing value", args: []string{"annotate", "q.jsonl", "--out", "l.jsonl", "--schema"}, want: "flag --schema needs a value"},
		{name: "annotate with two queues", args: []string{"annotate", "q.jsonl", "r.jsonl", "--schema", "s.yaml", "--out", "l.jsonl"}, want: `unexpected argument "r.jsonl"`},
		{name: "filter with annotate", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--filter", "all"}, want: "--filter is not valid with `quet annotate`"},
		{name: "output with annotate", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "-o", "x.jsonl"}, want: "--output is not valid with `quet annotate`"},
		{name: "schema when reviewing", args: []string{"corpus.jsonl", "--schema", "s.yaml"}, want: "--schema is only valid with `quet annotate`"},
		{name: "out when reviewing", args: []string{"corpus.jsonl", "--out=l.jsonl"}, want: "--out is only valid with `quet annotate`"},
		{name: "out with stats", args: []string{"stats", "corpus.jsonl", "--out", "l.jsonl"}, want: "--out is only valid with `quet annotate`"},
		{name: "labels when reviewing", args: []string{"corpus.jsonl", "--labels", "l.jsonl"}, want: "--labels is only valid with `quet annotate`"},
		{name: "labels with export", args: []string{"export", "corpus.jsonl", "--labels=l.jsonl"}, want: "--labels is only valid with `quet annotate`"},
		{name: "labels with list", args: []string{"list", "corpus.jsonl", "--labels", "l.jsonl"}, want: "--labels is only valid with `quet annotate`"},
		{name: "schema with list", args: []string{"list", "corpus.jsonl", "--schema", "s.yaml"}, want: "--schema is only valid with `quet annotate`"},
		{name: "schema with init", args: []string{"init", "--schema", "s.yaml"}, want: "--schema is only valid with `quet annotate`"},
		{name: "proposals when reviewing", args: []string{"corpus.jsonl", "--proposals", "p.jsonl"}, want: "--proposals is only valid with `quet annotate`"},
		{name: "proposals with stats", args: []string{"stats", "corpus.jsonl", "--proposals=p.jsonl"}, want: "--proposals is only valid with `quet annotate`"},
		{name: "proposals with export", args: []string{"export", "corpus.jsonl", "--proposals", "p.jsonl"}, want: "--proposals is only valid with `quet annotate`"},
		{name: "proposals with list", args: []string{"list", "corpus.jsonl", "--proposals", "p.jsonl"}, want: "--proposals is only valid with `quet annotate`"},
		{name: "proposals with init", args: []string{"init", "--proposals", "p.jsonl"}, want: "--proposals is only valid with `quet annotate`"},
		{name: "annotate proposals missing value", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--proposals"}, want: "flag --proposals needs a value"},
		{name: "annotate with empty proposals", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--proposals="}, want: "--proposals"},
		{name: "web without subcommand", args: []string{"web"}, want: "`quet web` needs a subcommand"},
		{name: "web unknown subcommand", args: []string{"web", "sync"}, want: "unknown subcommand `quet web sync`"},
		{name: "web remote without subcommand", args: []string{"web", "remote"}, want: "`quet web remote` needs a subcommand"},
		{name: "web remote unknown subcommand", args: []string{"web", "remote", "rename"}, want: "unknown subcommand `quet web remote rename`"},
		{name: "web remote add without url", args: []string{"web", "remote", "add", "origin"}, want: "needs a name and a URL"},
		{name: "web remote add extra argument", args: []string{"web", "remote", "add", "origin", "https://x.dev", "more"}, want: `unexpected argument "more"`},
		{name: "web remote remove without name", args: []string{"web", "remote", "remove"}, want: "needs a remote name"},
		{name: "web remote default without name", args: []string{"web", "remote", "default"}, want: "needs a remote name"},
		{name: "web remote list with argument", args: []string{"web", "remote", "list", "x"}, want: `unexpected argument "x"`},
		{name: "web remote with flag", args: []string{"web", "remote", "list", "--json"}, want: "--json is not valid with `quet web remote list`"},
		{name: "web login with two names", args: []string{"web", "login", "a", "b"}, want: `unexpected argument "b"`},
		{name: "web login with flag", args: []string{"web", "login", "--remote", "a"}, want: "--remote is not valid with `quet web login`"},
		{name: "web login with both modes", args: []string{"web", "login", "--no-browser", "--service-token"}, want: "--no-browser and --service-token are mutually exclusive"},
		{name: "web logout with two names", args: []string{"web", "logout", "a", "b"}, want: `unexpected argument "b"`},
		{name: "web logout with flag", args: []string{"web", "logout", "--no-browser"}, want: "--no-browser is not valid with `quet web logout`"},
		{name: "web list with login flag", args: []string{"web", "list", "--service-token"}, want: "--service-token is not valid with `quet web list`"},
		{name: "login flag outside web", args: []string{"stats", "c.jsonl", "--no-browser"}, want: "--no-browser is only valid with `quet web`"},
		{name: "web push without queue", args: []string{"web", "push", "--schema", "s.yaml", "--project", "p"}, want: "missing queue file"},
		{name: "web push two queues", args: []string{"web", "push", "q.jsonl", "r.jsonl", "--schema", "s.yaml", "--project", "p"}, want: `unexpected argument "r.jsonl"`},
		{name: "web push without schema", args: []string{"web", "push", "q.jsonl", "--project", "p"}, want: "`quet web push` needs --schema"},
		{name: "web push without project", args: []string{"web", "push", "q.jsonl", "--schema", "s.yaml"}, want: "`quet web push` needs --project"},
		{name: "web push with empty project", args: []string{"web", "push", "q.jsonl", "--schema", "s.yaml", "--project="}, want: "needs a value for --project"},
		{name: "web push with out", args: []string{"web", "push", "q.jsonl", "--schema", "s.yaml", "--project", "p", "--out", "l.jsonl"}, want: "--out is not valid with `quet web push`"},
		{name: "web push with json", args: []string{"web", "push", "q.jsonl", "--schema", "s.yaml", "--project", "p", "--json"}, want: "--json is not valid with `quet web push`"},
		{name: "web list with argument", args: []string{"web", "list", "x"}, want: `unexpected argument "x"`},
		{name: "web list with project", args: []string{"web", "list", "--project", "p"}, want: "--project is not valid with `quet web list`"},
		{name: "web pull without user or all", args: []string{"web", "pull", "--project", "p", "--out", "l.jsonl"}, want: "needs --user <name> or --all"},
		{name: "web pull user and all", args: []string{"web", "pull", "--project", "p", "--user", "a", "--all", "--out-dir", "d"}, want: "--user and --all are mutually exclusive"},
		{name: "web pull user without output", args: []string{"web", "pull", "--project", "p", "--user", "a"}, want: "needs --out <labels.jsonl> or --labels <labels.jsonl>"},
		{name: "web pull out and labels", args: []string{"web", "pull", "--project", "p", "--user", "a", "--out", "l.jsonl", "--labels", "l.jsonl"}, want: "--out and --labels are mutually exclusive"},
		{name: "web pull user with out-dir", args: []string{"web", "pull", "--project", "p", "--user", "a", "--out-dir", "d"}, want: "--out-dir is only valid with --all"},
		{name: "web pull all without out-dir", args: []string{"web", "pull", "--project", "p", "--all"}, want: "`quet web pull --all` needs --out-dir"},
		{name: "web pull all with out", args: []string{"web", "pull", "--project", "p", "--all", "--out", "l.jsonl"}, want: "--all writes one file per collaborator"},
		{name: "web pull all with labels", args: []string{"web", "pull", "--project", "p", "--all", "--labels", "l.jsonl", "--out-dir", "d"}, want: "--all writes one file per collaborator"},
		{name: "web pull labels with force", args: []string{"web", "pull", "--user", "a", "--labels", "l.jsonl", "--force"}, want: "--force is not valid with --labels"},
		{name: "web pull out without project", args: []string{"web", "pull", "--user", "a", "--out", "l.jsonl"}, want: "`quet web pull` needs --project"},
		{name: "web pull with empty user", args: []string{"web", "pull", "--project", "p", "--user=", "--out", "l.jsonl"}, want: "needs a value for --user"},
		{name: "web pull with schema", args: []string{"web", "pull", "--project", "p", "--user", "a", "--out", "l.jsonl", "--schema", "s.yaml"}, want: "--schema is not valid with `quet web pull`"},
		{name: "web pull with extra argument", args: []string{"web", "pull", "x", "--project", "p", "--all", "--out-dir", "d"}, want: `unexpected argument "x"`},
		{name: "project outside web", args: []string{"corpus.jsonl", "--project", "p"}, want: "--project is only valid with `quet web`"},
		{name: "user with stats", args: []string{"stats", "corpus.jsonl", "--user", "a"}, want: "--user is only valid with `quet web`"},
		{name: "all with list", args: []string{"list", "corpus.jsonl", "--all"}, want: "--all is only valid with `quet web`"},
		{name: "out-dir with annotate", args: []string{"annotate", "q.jsonl", "--schema", "s.yaml", "--out", "l.jsonl", "--out-dir", "d"}, want: "--out-dir is only valid with `quet web`"},
		{name: "remote flag with init", args: []string{"init", "--remote", "x"}, want: "--remote is only valid with `quet web`"},
		{name: "name flag when reviewing", args: []string{"corpus.jsonl", "--name", "x"}, want: "--name is only valid with `quet web`"},
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

func TestAnnotateRecheckMissingLabelsSkipsScreen(t *testing.T) {
	launched := stubAnnotateScreen(t, true)
	queue, schema, labels := writeAnnotateFixture(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--labels", labels}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "quet: ") || !strings.Contains(got, labels) || strings.Contains(got, "Usage:") {
		t.Errorf("stderr %q, want a one-line runtime error naming %s", got, labels)
	}
	if *launched != 0 {
		t.Errorf("annotation screen launched %d times, want 0", *launched)
	}
	if _, err := os.Stat(labels); !os.IsNotExist(err) {
		t.Errorf("labels file created by a failed re-check open (stat error %v)", err)
	}
}

func TestAnnotateRecheckLaunchesScreen(t *testing.T) {
	launched := stubAnnotateScreen(t, true)
	queue, schema, labels := writeAnnotateFixture(t)
	body := "{\"id\":\"elsewhere\",\"annotation_status\":\"complete\",\"type\":\"positive\",\"target\":null}\n"
	if err := os.WriteFile(labels, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--labels", labels}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, stderr.String())
	}
	if *launched != 1 {
		t.Errorf("annotation screen launched %d times, want 1", *launched)
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

// writeProposals writes body to a proposals file in a temporary directory and returns its path.
func writeProposals(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proposals.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnnotateProposalsMissingFileSkipsScreen(t *testing.T) {
	launched := stubAnnotateScreen(t, true)
	queue, schema, out := writeAnnotateFixture(t)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--out", out, "--proposals", missing}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := stderr.String(); !strings.HasPrefix(got, "quet: ") || !strings.Contains(got, missing) || strings.Contains(got, "Usage:") {
		t.Errorf("stderr %q, want a one-line runtime error naming %s", got, missing)
	}
	if *launched != 0 {
		t.Errorf("annotation screen launched %d times, want 0", *launched)
	}
}

func TestAnnotateProposalsErrorBeforeTerminalCheck(t *testing.T) {
	launched := stubAnnotateScreen(t, false)
	queue, schema, out := writeAnnotateFixture(t)
	proposals := writeProposals(t, "not json\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--out", out, "--proposals", proposals}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := stderr.String(); !strings.Contains(got, proposals+":1") || strings.Contains(got, "interactive terminal") {
		t.Errorf("stderr %q, want the proposals load error citing %s:1 and no terminal complaint", got, proposals)
	}
	if *launched != 0 {
		t.Errorf("annotation screen launched %d times, want 0", *launched)
	}
}

func TestAnnotateProposalsLaunchesScreenAndReportsIgnored(t *testing.T) {
	var got *annotate.Session
	launched := stubAnnotateScreen(t, true)
	launchAnnotate = func(s *annotate.Session) error {
		got = s
		*launched++
		return nil
	}
	queue, schema, out := writeAnnotateFixture(t)
	proposals := writeProposals(t, "{\"id\":\"a\",\"annotation_status\":\"complete\",\"type\":\"positive\"}\n"+
		"{\"id\":\"zzz\",\"annotation_status\":\"complete\"}\n{\"id\":\"yyy\",\"annotation_status\":\"complete\"}\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--out", out, "--proposals", proposals}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, stderr.String())
	}
	if *launched != 1 || got == nil || !got.HasProposals() || got.ProposalCount() != 1 {
		t.Fatalf("launched %d times, session %v: want one launch with one queue proposal", *launched, got)
	}
	want := "quet: ignored 2 proposal(s) for ids not in the queue: yyy, zzz\n"
	if stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr.String(), want)
	}
}

func TestAnnotateProposalsRecheckCombines(t *testing.T) {
	launched := stubAnnotateScreen(t, true)
	queue, schema, labels := writeAnnotateFixture(t)
	body := "{\"id\":\"elsewhere\",\"annotation_status\":\"complete\",\"type\":\"positive\",\"target\":null}\n"
	if err := os.WriteFile(labels, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	proposals := writeProposals(t, "{\"id\":\"b\",\"annotation_status\":\"uncertain\"}\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"annotate", queue, "--schema", schema, "--labels", labels, "--proposals", proposals}, &stdout, &stderr)
	if code != 0 || *launched != 1 || stderr.Len() != 0 {
		t.Errorf("exit %d, launched %d, stderr %q; want 0, 1, empty", code, *launched, stderr.String())
	}
}

func TestIgnoredProposalsMessageCapsList(t *testing.T) {
	var ids []string
	for i := range 13 {
		ids = append(ids, fmt.Sprintf("id%02d", i))
	}
	want := "ignored 13 proposal(s) for ids not in the queue: id00, id01, id02, id03, id04, id05, id06, id07, id08, id09, … (+3 more)"
	if got := ignoredProposalsMessage(ids); got != want {
		t.Errorf("message %q, want %q", got, want)
	}
	if got, want := ignoredProposalsMessage(ids[:10]), "ignored 10 proposal(s) for ids not in the queue: id00, id01, id02, id03, id04, id05, id06, id07, id08, id09"; got != want {
		t.Errorf("message %q, want %q", got, want)
	}
}
