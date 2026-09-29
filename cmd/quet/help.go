package main

// helpText is the full help, also used as the usage text after a command line error.
// The first two paragraphs are the product identity from the spec and must stay exactly as written.
func helpText() string {
	return `Quet — Quick Utility for Evaluating Text

A fast, keyboard-first TUI for reviewing and curating text corpora.

Usage:
  quet [corpus.jsonl|corpus.json|corpus.txt|dir] [flags]
  quet stats <corpus> [flags]
  quet export <corpus> [flags]
  quet init [--global] [--force]
  quet update [--check]
  quet help

Without a corpus, or given a directory, quet opens a file browser: w/s move,
enter open, a up a folder, . hidden files, q quit.

quet init writes a starter quet.yaml and flags.yaml to the current directory
(--global: ~/.config/quet/config.yaml and flags.yaml). Existing files are
never overwritten without --force.

quet update replaces this binary with the latest release after verifying it
against the release's SHA256SUMS; --check only reports whether one exists.

Scripting (no TUI): read and change the same review state and undo log as the
review screen. Record ids are those printed by quet list.
  quet list <corpus> [--filter f] [--limit N] [--json]
  quet show <corpus> <id> [--json]
  quet set <corpus> <id>... --status <unreviewed|approved|rejected|needs_review>
      [--json]
  quet flag <corpus> <id>... [--add a,b] [--remove c] [--json]
  quet suggest <corpus> <id>... [--add a,b] [--remove c] [--json]
  quet edit <corpus> <id> (--text <s> | --text-file <path|-> | --revert)
      [--json]
  quet undo <corpus> [--json]
  quet stats <corpus> [--json]
  quet export <corpus> -o -
--json prints JSON (list: one record per line). flag sets manual flags named
in flags.yaml; suggest sets free-form suggested flags; --add and --remove take
comma-separated names and may repeat. --text-file - reads stdin. export -o -
writes the records to stdout and its summary to stderr.

Flags (any position; --flag value and --flag=value are both accepted):
  --config <path>       config file to use instead of ./quet.yaml
  --flags-file <path>   manual flag definitions instead of ./flags.yaml
  --filter <name>       start filtered: all, unreviewed, approved, rejected,
                        needs_review, edited, diagnostic[:name], manual[:name],
                        suggested[:name], source:<x>, batch:<x>
  --no-skip-reviewed    stay on records that were already reviewed
  -h, --help            show this help
  --version             print the version and exit

Export flags:
  --status <name>       approved (default), rejected, needs_review, all
  -o, --output <path>   output file (default: <corpus>.<preset>.jsonl next to the corpus)
  --with-review         add a "quet" review-metadata object to each exported record
  --format <name>       jsonl (default) or txt
  -f, --force           overwrite an existing output file

Init flags:
  --global              write to ~/.config/quet/ instead of the current dir
  -f, --force           overwrite existing config files

Keys:
  w approve   s reject   space needs review   z undo
  a/h previous   d/l next   g first   G last   arrows navigate
  e edit   f flags   c config   / search   tab change panel   1/2/3 panel
  ? help   esc back/close   enter select   : commands   q quit

Data stays local: no telemetry; the network is only used by quet update or
when update.check is on.
`
}
