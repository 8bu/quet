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
  quet annotate <queue.jsonl> --schema <schema.yaml> (--out|--labels) <labels.jsonl> [--proposals <proposals.jsonl>]
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

quet annotate labels a JSONL queue ({"id","text"} per line) one record at a
time against a YAML schema of types, statuses, null_target_types and
null_label_statuses: a type, an optional target span of the text, and a
status. The labels JSONL is rewritten each time a record is marked;
reopening the same --out resumes.
Review state is separate: annotating never touches the .quet.db sidecar.

Re-check a subset: --labels opens a queue that is a subset of an existing
canonical labels file instead of --out. Only the queue's records are shown and
editable; every other label is kept byte-for-byte.
  quet annotate recheck-01.jsonl --schema schema.yaml --labels labels.jsonl

AI proposals: --proposals opens an advisory JSONL of suggested labels ({"id",
"annotation_status"} plus optional "type", "target", "note", "confidence",
"reason") next to each record's current annotation. Press p to accept one, P
to load it into the draft and edit it, or ignore it. The file is never
written; nothing is saved without p, enter, u or s; confidence and reason are
never written to the labels file.
  quet annotate recheck.jsonl --schema annotation.yaml --labels labels.jsonl --proposals proposals.jsonl

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

Annotate flags:
  --schema <path>       YAML schema: types, statuses, null_target_types,
                        null_label_statuses (default [skipped]: marking one
                        saves a null type and target)
  --out <path>          labels JSONL, rewritten on every mark (resumes); every
                        label id must be in the queue
  --labels <path>       re-check: existing labels JSONL (must exist) that may
                        hold ids outside the queue; only queue ids are editable,
                        all others are preserved byte-for-byte; written
                        atomically, refused if another program changed the
                        file since quet last read or wrote it. Excludes --out.
  --proposals <path>    advisory suggestions JSONL (read-only, optional, works
                        with --out or --labels): one object per line with
                        id and annotation_status, optionally type, target
                        ({text,start,end} or null), note, confidence (0..1)
                        and reason; other keys are ignored. Ids outside the
                        queue are ignored and reported on exit; invalid lines
                        and duplicate ids are refused. Confidence and reason
                        are never written.

Keys:
  w approve   s reject   space needs review   z undo
  a/h previous   d/l next   g first   G last   arrows navigate
  e edit   f flags   c config   / search   tab change panel   1/2/3 panel
  ? help   esc back/close   enter select   : commands   q quit
  annotate: enter complete   u uncertain   s skip   t/1-9 type   x span
    n null target   esc discard   a/d prev/next (any record)   [/] in filter
    g/G first/last   : or # go to   z undo   f filter   ? help   q quit
    with --proposals: p accept proposal   P edit proposal

Data stays local: no telemetry; the network is only used by quet update or
when update.check is on.
`
}
