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
  quet help

Without a corpus, or given a directory, quet opens a file browser: w/s move,
enter open, a up a folder, . hidden files, q quit.

quet init writes a starter quet.yaml and flags.yaml to the current directory
(--global: ~/.config/quet/config.yaml and flags.yaml). Existing files are
never overwritten without --force.

Flags (any position; --flag value and --flag=value are both accepted):
  --config <path>       config file to use instead of ./quet.yaml
  --flags-file <path>   manual flag definitions instead of ./flags.yaml
  --filter <name>       start filtered: all, unreviewed, approved, rejected,
                        needs_review, edited, auto[:name], manual[:name],
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

Data stays local: no telemetry, no network.
`
}
