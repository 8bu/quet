# Quet

**Quick Utility for Evaluating Text**

A fast, keyboard-first TUI for reviewing and curating text corpora.

```
generate → quet → review → export
```

Generate or collect a corpus, open it in `quet`, review it with single keystrokes, export the
records you kept. No network, no telemetry, no database server: one binary and one SQLite sidecar
file next to the corpus.

## Quickstart

```sh
go build -o quet ./cmd/quet
./quet examples/corpus.jsonl
```

`w` to approve, `s` to reject, `space` for needs review. Progress is saved as you go, so `q` quits
and you can open the same corpus again later to pick up where you left off.

## Installation

Requires Go 1.26+.

```sh
go build -o quet ./cmd/quet
./quet --version
```

```
quet 0.1.0
```

There is no packaging step: `quet` is a single static binary with an embedded SQLite (pure Go).

## Usage

```sh
quet <corpus.jsonl|corpus.json|corpus.txt> [flags]   # review in the TUI (default)
quet stats <corpus> [flags]                          # print review counts
quet export <corpus> [flags]                         # write a reviewed corpus
quet help                                            # help (also: quet --help, -h)
```

The corpus argument and flags may come in any order, and `--flag value` and `--flag=value` are
both accepted:

```sh
./quet examples/corpus.jsonl --filter unreviewed
./quet --filter unreviewed examples/corpus.jsonl
./quet --filter=unreviewed examples/corpus.jsonl
```

Unknown flags and missing arguments print the usage on stderr and exit with status 2.

`--help` starts with the product identity:

```
$ ./quet --help
Quet — Quick Utility for Evaluating Text

A fast, keyboard-first TUI for reviewing and curating text corpora.

Usage:
  quet <corpus.jsonl|corpus.json|corpus.txt> [flags]
...
```

### Review flags

| Flag | Meaning |
| --- | --- |
| `--filter <name>` | Start with a filter: `all`, `unreviewed`, `approved`, `rejected`, `needs_review`, `edited`, `auto[:name]`, `manual[:name]`, `suggested[:name]`, `source:<x>`, `batch:<x>` |
| `--no-skip-reviewed` | Stay on already reviewed records instead of skipping them |
| `--flags-file <path>` | Use this manual flag definitions file instead of the default search |
| `--config <path>` | Use this config file instead of `./quet.yaml` |
| `-h`, `--help` | Show help |
| `--version` | Print `quet 0.1.0` |

## Input formats

The format is chosen by extension. Malformed input is reported with its line/position; records are
never silently dropped. The text field is the first of `text`, `content`, `note`, `input`, `prompt`,
`sentence`, `body` found in the file.

**`.jsonl`** — one JSON object per line. Records keep their stable id (`id:<value>`); records without
an id get a stable content hash, so review progress survives reordering and re-runs.

```json
{"id": "note-001", "text": "bắn thg Nam 2 củ tiền hôm nọ", "source": "claude", "batch": "slang-loan-03", "created_at": "2026-01-02T01:00:00Z", "suggested_flags": ["slang"], "lang": "vi"}
{"id": "note-002", "text": "CK Nam 2tr", "source": "claude", "batch": "slang-loan-03", "suggested_flags": [], "lang": "vi"}
```

**`.json`** — an array of records, or an object wrapping one under `records`, `data`, `items` or
`corpus`:

```json
{
  "corpus": "vi-finance-notes",
  "version": 1,
  "records": [
    {"text": "bắn thg Nam 2 củ tiền hôm nọ", "source": "claude", "batch": "slang-loan-03"}
  ]
}
```

**`.txt`** — one record per non-empty line, no metadata:

```
bắn thg Nam 2 củ tiền hôm nọ
CK Nam 2tr
```

Metadata preservation: every key other than the text field is kept in its original order and written
back out on export, so `source`, `batch`, `created_at`, `lang`, `suggested_flags` and any custom
fields survive the round trip. The source corpus file is never modified — it is only read.

## Reviewing

A record has exactly one status:

| Status | Set with |
| --- | --- |
| `unreviewed` | initial state |
| `approved` | `w` |
| `rejected` | `s` |
| `needs_review` | `space` |

After `w`, `s` or `space` the status is persisted immediately, a small confirmation shows, and the
TUI auto-advances to the next unresolved record (skipping reviewed records, `skip_reviewed: true` by
default; use `--no-skip-reviewed` to visit every record). A reviewed record can be reopened and
changed at any time.

Every change is written straight to the SQLite sidecar `<corpus>.quet.db` (created next to the
corpus), so `q` at any moment is safe and reopening the corpus restores the review session. `z`
undoes the most recent status change, manual flag change or edit — undo history persists across
restarts. Editing never destroys the original: the original text is kept and only the final text is
what you review and export.

## Keybindings

| Key | Action |
| --- | --- |
| `w` | Approve current record |
| `s` | Reject current record |
| `space` | Mark current record needs review |
| `z` | Undo last status, flag or edit change |
| `a`, `h`, `←` | Previous record |
| `d`, `l`, `→` | Next record |
| `j`, `k`, `↑`, `↓` | Move within lists and panels |
| `g` | First record |
| `G` | Last record |
| `e` | Edit the record text inline |
| `ctrl+s` | Save the edit (edit mode) |
| `esc` | Cancel the edit, close a picker, go back |
| `enter` | Select / apply |
| `f` | Manual flag picker (`j`/`k` move, `space` toggle, `enter` apply, `esc` close) |
| `/` | Search text, original text and metadata; results in a selectable panel |
| `tab` / `shift+tab` | Change panel |
| `1` `2` `3` | Jump to panel: corpus/status, record, details |
| `:` | Command palette (searchable: approve, export, filters, help, quit, …) |
| `?` | Help overlay |
| `q` | Quit |

Typing is only captured in edit mode; everywhere else a plain letter is a shortcut. `?` lists
everything grouped by Review, Navigation, Record and Application, so the keys are discoverable
without this README.

## Flags

Flags come from three separate sources and are never merged blindly:

- **auto flags** — computed by Quet's deterministic checks (below). Warnings only.
- **suggested flags** — taken from the imported metadata (`suggested_flags` in the source file) and
  shown separately. No model is called, ever.
- **manual flags** — your own labels, defined in `flags.yaml`. They are not hard-coded.

```yaml
manual:
  slang:
    description: Notable Vietnamese slang or informal wording.

  typo:
    description: Natural typo, abbreviation, or shorthand.

  ambiguous:
    description: Meaning is genuinely ambiguous.

  unnatural:
    description: Unlikely wording for a real user.

  synthetic-looking:
    description: Looks strongly generated or templated.

  unusual:
    description: Valid but uncommon example worth revisiting.
```

Press `f` to tick manual flags; the picker shows each flag's description from `flags.yaml`.

## Auto checks

All seven checks are deterministic and explainable, and they never change a status — they only warn.
Thresholds live in the config file.

| Flag | Fires when |
| --- | --- |
| `duplicate` | The text equals another record's after Unicode NFC, trim, lowercase and whitespace collapsing (`CK Nam 2tr` = `ck   nam 2tr`). Duplicates are grouped and you can jump between matches. |
| `too_long` | Text is longer than `max_chars` runes (default 160). |
| `empty` | Text is blank. |
| `repeated_chars` | One character repeats at least `repeated_char_threshold` times (default 5), e.g. `ckkkkkkkkk Nam 2tr`, `aaaaaaa`, `????????`. |
| `weird_symbols` | An unusually high share of symbols/punctuation (threshold `weird_symbol_ratio`, default 0.35); emoji-heavy text trips it, while money notation such as `+20tr`, `-500k`, `$20`, `1.2tr` is excluded. |
| `missing_amount` | No recognizable money amount is present: `50k`, `2tr`, `1tr2`, `2 triệu`, `2 củ`, `5 lít`, `1.5tr`, `1.2m`, `500000`, `500.000`, `500,000`, `$20`, `20usd`, `200k vnd`. |
| `possible_template` | The same normalized pattern occurs at least `template_min_occurrences` times (default 8). Amounts become `<AMOUNT>` and obvious variable tokens become `<TOKEN>`, e.g. `cho Nam vay 2tr` and `cho Linh vay 3tr` share `cho <TOKEN> vay <AMOUNT>`. |

## Configuration

Quet reads the first existing of `./quet.yaml` and `~/.config/quet/config.yaml`
(`$XDG_CONFIG_HOME` is respected). Missing keys keep their defaults; `--config <path>` uses an
explicit file instead.

```yaml
review:
  skip_reviewed: true

checks:
  max_chars: 160
  repeated_char_threshold: 5
  weird_symbol_ratio: 0.35
  template_min_occurrences: 8

flags_file: "./flags.yaml"
```

`flags_file` is resolved relative to the config file's directory. Without it, Quet looks for
`./flags.yaml`, then `flags.yaml` next to the corpus, then `~/.config/quet/flags.yaml`; `--flags-file`
overrides the whole search. This repository ships the manual flag taxonomy as `./flags.yaml`, so
`./quet examples/corpus.jsonl` starts with the picker populated; `configs/quet.yaml` is a copyable
starting-point config.

## Export

Five presets, all available from the TUI command palette (`:`) and from the CLI:

| Preset | Records | Review metadata | Default suffix |
| --- | --- | --- | --- |
| `approved` | approved | no | `.approved.jsonl` |
| `rejected` | rejected | no | `.rejected.jsonl` |
| `needs_review` | needs review | no | `.needs_review.jsonl` |
| `all` | every record | yes | `.reviewed.jsonl` |
| `clean` | approved | no | `.clean.jsonl` |

`approved`, `rejected`, `needs_review` and `clean` contain the final text plus the original imported
metadata — no reviewer internals. `clean` is the training-corpus preset and has the same shape as
`approved`; `all` additionally embeds a `quet` object per record.

```sh
./quet export examples/corpus.jsonl --status approved -o approved.jsonl
./quet export examples/corpus.jsonl --status needs_review -o review.jsonl
./quet export examples/corpus.jsonl --status rejected --format txt -o rejected.txt
./quet export examples/corpus.jsonl --status all -o reviewed.jsonl
./quet export examples/corpus.jsonl                  # writes examples/corpus.approved.jsonl
```

```
$ ./quet export examples/corpus.jsonl --status all -o reviewed.jsonl
Wrote 15 records to reviewed.jsonl
```

The printed count is the number of records actually written.

| Export flag | Meaning |
| --- | --- |
| `--status <name>` | `approved` (default), `rejected`, `needs_review` (also `needs-review`), `all` |
| `-o`, `--output <path>` | Output file; defaults to `<corpus-without-extension><preset suffix>` next to the corpus, with `-1`, `-2`, … appended if that name is taken |
| `--with-review` | Add the `quet` review-metadata object to any preset |
| `--format <name>` | `jsonl` (default) or `txt` (final text, one record per line) |
| `-f`, `--force` | Overwrite an existing output file |

Exports are atomic: Quet streams into a temporary file in the target directory, fsyncs, and renames
it over the target. It refuses to write over the source corpus or its sidecar database, and refuses
to overwrite an existing file unless you pass `-f`. Nothing ever writes into the source corpus.

With `--with-review` each JSONL line keeps its imported metadata and gains one `quet` key:

```json
{"id": "note-001", "text": "bắn thg Nam 2 củ tiền hôm nọ", "source": "claude", "batch": "slang-loan-03", "created_at": "2026-01-02T01:00:00Z", "suggested_flags": ["slang"], "lang": "vi", "quet": {"id": "id:note-001", "status": "approved", "edited": false, "manual_flags": [], "auto_flags": [], "suggested_flags": ["slang"]}}
```

`original_text` appears inside `quet` only for records you edited.

## Stats

```sh
$ ./quet stats examples/corpus.jsonl
Total:            15
Approved:          0
Rejected:          0
Needs review:      0
Unreviewed:       15
Edited:            0
Auto flags: duplicate 2, empty 1, repeated_chars 1, weird_symbols 1
Corpus: examples/corpus.jsonl
Sidecar: examples/corpus.jsonl.quet.db
```

Counts come from your corpus and your review progress. The `Auto flags:` line lists only the checks
that fired at least once, and is omitted when none did.

## Development

```sh
go build -o quet ./cmd/quet
go test ./...
go vet ./...
gofmt -l .
```

Layout:

```
cmd/quet/                command line entry point and argument parsing
internal/corpus/         .jsonl/.json/.txt loading, stable ids, JSON re-encoding
internal/config/         quet.yaml and flags.yaml
internal/checks/         the seven deterministic auto checks
internal/review/         review session: states, filters, undo, counts, search
internal/storage/        SQLite sidecar (WAL, immediate writes)
internal/export/         the five export presets, atomic file writing
internal/tui/            Bubble Tea interface
flags.yaml               manual flag taxonomy (the default search location)
configs/                 copyable starting-point quet.yaml
examples/                small example corpora in all three formats
```

Tests live next to the code they cover (`internal/<package>/*_test.go`, `cmd/quet/*_test.go`); they
are deterministic and never touch the network. `internal/export/export_test.go` covers preset shapes,
default output paths, and a full review → export round trip.
