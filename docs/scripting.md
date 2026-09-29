# Scripting and agents

Everything you can do in the review screen can also be done from the command line, one command at a
time, without the TUI. That makes Quet usable from shell scripts, CI jobs and AI agents: list the
records, read them, and set statuses, flags, suggestions and edits.

The commands read and write the same `<corpus>.quet.db` sidecar as the TUI. They share the review
state and the undo log, so a record approved by a script shows as approved the next time you open
the corpus in the TUI, and `quet undo` (or `z` in the TUI) undoes the most recent change whoever
made it. A review screen that is already open does not see changes made from the command line until
you quit and reopen the corpus.

```sh
quet list <corpus> [--filter f] [--limit N] [--json]
quet show <corpus> <id> [--json]
quet set <corpus> <id>... --status <unreviewed|approved|rejected|needs_review> [--json]
quet flag <corpus> <id>... [--add a,b] [--remove c] [--json]
quet suggest <corpus> <id>... [--add a,b] [--remove c] [--json]
quet edit <corpus> <id> (--text <s> | --text-file <path|-> | --revert) [--json]
quet undo <corpus> [--json]
quet stats <corpus> [--json]
quet export <corpus> -o -
```

`--config` and `--flags-file` work with every command that takes a corpus, exactly as for the review
screen (see [Usage](usage.md#review-flags)). `--no-skip-reviewed` only applies to the review screen.

## Record IDs

Commands address records by the `id` that `list` prints. A record with an `id` field in the corpus
gets `id:<value>`, so `{"id": "note-001", …}` is `id:note-001`. Records without one get a stable
ID derived from their text (`h:<hash>`); copy it from `list` rather than building it yourself.

Every ID on the command line is resolved before anything is written. If one is unknown, the command
prints `quet: unknown record id "x"`, exits with status 1 and changes nothing.

## Commands

### list

```sh
$ quet list examples/corpus.jsonl --filter unreviewed --limit 2
id:note-001	unreviewed	bắn thg Nam 2 củ tiền hôm nọ
id:note-002	unreviewed	CK Nam 2tr
```

One line per record in corpus order, tab-separated: ID, status, final text. The text is put on a
single line with runs of whitespace squeezed, and is never truncated. `--filter` takes the same
names as the review screen (`unreviewed`, `approved`, `edited`, `diagnostic[:name]`,
`manual[:name]`, `suggested[:name]`, `source:<x>`, `batch:<x>`, …; see
[Usage](usage.md#review-flags)). `--limit N` stops after `N` records. With `--json` each line is a
[record object](#record-object) (JSONL).

### show

```sh
$ quet show examples/corpus.jsonl id:note-002
id: id:note-002
status: unreviewed
edited: false
source: claude
batch: slang-loan-03
manual_flags: -
suggested_flags: -
diagnostic: duplicate: 2 records share this text
text: CK Nam 2tr
```

Prints one record as `key: value` lines in this order: `id`, `status`, `edited`, `source`,
`batch`, `manual_flags`, `suggested_flags` (joined with `, `; `-` when empty), one `diagnostic:
name: detail` line per diagnostic, `original_text` (only when edited) and `text` last. Continuation
lines of multi-line text are indented by two spaces. With `--json` it prints a single
[record object](#record-object) instead. It takes exactly one ID.

### set

```sh
$ quet set examples/corpus.jsonl id:note-001 id:note-002 --status approved
approved id:note-001
approved id:note-002
```

Sets the review status of one or more records: `unreviewed`, `approved`, `rejected` or
`needs_review`. There is no auto-advance; each named record is changed and nothing else.

### flag

```sh
$ quet flag examples/corpus.jsonl id:note-001 --add slang,typo
manual_flags=slang,typo id:note-001
$ quet flag examples/corpus.jsonl id:note-001 --remove typo
manual_flags=slang id:note-001
```

Adds or removes [manual flags](reviewing.md#flags) on one or more records and prints the resulting
manual flags of each (`manual_flags= id:…` when none are left). The names, for `--add` and
`--remove` alike, must exist in `flags.yaml`; `quet stats <corpus> --json` lists them under
`manual_flags`. An unknown name exits with status 1 and lists the valid ones:

```
quet: unknown manual flag "slangg" (valid: slang, typo, ambiguous, unnatural, synthetic-looking, unusual)
```

Without a flags file the command exits with status 1 and says so (`quet: no flags file: manual
flags come from flags.yaml (run quet init, or pass --flags-file)`).

### suggest

```sh
$ quet suggest examples/corpus.jsonl id:note-001 --add code-switching
suggested_flags=code-switching,slang id:note-001
$ quet suggest examples/corpus.jsonl id:note-001 --remove code-switching
suggested_flags=slang id:note-001
```

Suggested flags are free-form hints for external and AI reviewers: any name is accepted, nothing
needs to be defined in `flags.yaml`. They are stored in the sidecar, never in the corpus, and are
merged with the `suggested_flags` already in the corpus metadata. Everywhere Quet shows or uses
suggestions — the record details in the TUI, the `suggested[:name]` filter, `list`/`show` and
[export](export.md#review-metadata) — you get the union of both, and that is also what `suggest`
prints (above, `slang` comes from the corpus metadata of `note-001`).

Suggestions that come from the corpus metadata cannot be removed; `--remove` on one of them exits
with status 1:

```
quet: suggested flag "slang" on id:note-001 comes from the corpus metadata and cannot be removed
```

`--add` and `--remove` (for `flag` and `suggest`) take comma-separated names and can be repeated;
names are trimmed and empty entries ignored. At least one of them is required.

### edit

```sh
$ quet edit examples/corpus.jsonl id:note-001 --text "bắn thằng Nam 2 củ tiền hôm nọ"
edited id:note-001
$ fix-text id:note-001 | quet edit examples/corpus.jsonl id:note-001 --text-file -
edited id:note-001
$ quet edit examples/corpus.jsonl id:note-001 --revert
unedited id:note-001
```

Replaces the final text of one record, like `e` in the TUI. Give exactly one of `--text <s>`
(stored verbatim), `--text-file <path>` (`-` reads standard input; one trailing newline is
stripped, so `echo fixed | quet edit … --text-file -` stores `fixed`) or `--revert`, which discards
the edit and goes back to the original text. The original text is always kept. The command prints
`edited <id>`, or `unedited <id>` after `--revert` or when the new text equals the original.

### undo

```sh
$ quet undo examples/corpus.jsonl
undid id:note-002
```

Undoes the most recent status, manual flag, suggestion or edit change in the shared undo log —
including changes made in the TUI. Prints `nothing to undo` when the log is empty.

### stats

`quet stats <corpus>` prints the counts shown in [Usage](usage.md#stats). `--json` prints one
object instead (see [below](#stats-object)).

### export

```sh
quet export examples/corpus.jsonl --status all -o - | jq -c .quet
```

`-o -` streams the export (JSONL or `--format txt`) to standard output; the `Wrote N records` line
goes to standard error. See [Export](export.md).

## Output

Without `--json`, write commands print one line per affected record: `approved id:note-001` (`set`),
`manual_flags=a,b id:…` (`flag`), `suggested_flags=a,b id:…` (`suggest`), `edited id:…` or
`unedited id:…` (`edit`), and `undid id:…` or `nothing to undo` (`undo`). With `--json`, `set`,
`flag`, `suggest` and `edit` print one [record object](#record-object) per line for every affected
record, in the order the IDs were given, showing the record after the change.

### Record object

Used by `list --json` (one per line), `show --json`, and the write commands:

```json
{"id":"id:note-001","status":"approved","edited":true,"text":"bắn thằng Nam 2 củ tiền hôm nọ","original_text":"bắn thg Nam 2 củ tiền hôm nọ","manual_flags":["slang"],"suggested_flags":["code-switching","slang"],"diagnostics":[],"source":"claude","batch":"slang-loan-03"}
```

| Field | Meaning |
| --- | --- |
| `id` | Record ID, as accepted by the other commands |
| `status` | `unreviewed`, `approved`, `rejected` or `needs_review` |
| `edited` | Whether the text was edited |
| `text` | Final text (the edit if there is one, else the original) |
| `original_text` | Imported text; only present when `edited` is `true` |
| `manual_flags` | Manual flags |
| `suggested_flags` | Corpus-metadata suggestions and sidecar suggestions, merged |
| `diagnostics` | [Diagnostics](diagnostics.md) with `name` and a human-readable `detail`; informational only |
| `source`, `batch` | From the corpus metadata; omitted when empty |

Arrays are never `null`; they are `[]` when empty.

### Stats object

```json
{"corpus":"examples/corpus.jsonl","sidecar":"examples/corpus.jsonl.quet.db","total":21,"approved":0,"rejected":0,"needs_review":0,"unreviewed":21,"edited":0,"diagnostics":{"duplicate":2,"empty":1,"possible_template":8,"repeated_chars":1,"too_long":1,"weird_symbols":1},"manual_flags":[{"name":"slang","description":"Notable Vietnamese slang or informal wording.","count":0},{"name":"typo","description":"Natural typo, abbreviation, or shorthand.","count":0}]}
```

`diagnostics` maps each diagnostic name to the number of records it fired on. `manual_flags` is the
`flags.yaml` taxonomy in file order with how many records carry each flag, so an agent can learn
the valid names for `quet flag` (the example is shortened to two flags).

### Undo object

```json
{"undone":{"id":"id:note-002","status":"unreviewed","edited":false,"text":"CK Nam 2tr","manual_flags":[],"suggested_flags":[],"diagnostics":[{"name":"duplicate","detail":"2 records share this text"}],"source":"claude","batch":"slang-loan-03"}}
```

`undone` is the record after the undo, or `null` when there was nothing to undo.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Runtime failure: unknown record ID, unknown manual flag, no flags file, removing a metadata suggestion, unreadable corpus, … |
| `2` | Command-line mistake: unknown flag, missing argument, or nothing to do (`flag`/`suggest` without `--add`/`--remove`, `edit` without exactly one of `--text`/`--text-file`/`--revert`) |

Errors go to standard error.

## The review screen needs a terminal

`quet [corpus|dir]` opens the interactive review screen, which needs a terminal on both standard
input and standard output. When either is redirected or piped — as it is for most agents — Quet
exits with status 1 before opening anything:

```
quet: the review screen needs an interactive terminal; use quet list/show/set/flag/suggest/edit for scripted review (see quet help)
```

## Example: an agent loop

Fetch a batch of unreviewed records, let a model (or any script) judge each one, and write the
verdicts back. `judge` stands for your own program: it reads a record object and prints a status.

```sh
corpus=examples/corpus.jsonl

quet stats "$corpus" --json | jq -r '.manual_flags[].name'     # valid manual flag names
quet list "$corpus" --filter unreviewed --limit 50 --json > batch.jsonl

while IFS= read -r rec; do
  id=$(jq -r .id <<<"$rec")
  verdict=$(judge <<<"$rec")                                  # approved | rejected | needs_review
  quet set "$corpus" "$id" --status "$verdict"
  if [ "$verdict" = needs_review ]; then
    quet suggest "$corpus" "$id" --add model-unsure
  fi
done < batch.jsonl
```

Then review what the model was unsure about in the TUI with `quet "$corpus" --filter
suggested:model-unsure`, and `quet undo "$corpus"` whenever a change was wrong.
