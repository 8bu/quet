# Annotating

`quet annotate` labels a queue of text records one at a time against a schema you define: each
record gets a **type**, an optional **target** (a span of its text), and an **annotation status**.
It is separate from reviewing: it never reads or writes the `.quet.db` review sidecar, and its only
output is a labels JSONL file that is both the saved progress and the export.

```sh
quet annotate examples/annotation/queue.jsonl \
  --schema examples/annotation/schema.yaml --out labels.jsonl
```

The [example schema](../examples/annotation/schema.yaml) labels review sentiment (`positive`,
`negative`, `mixed`, `neutral`) with the reviewed aspect as the target; the
[example queue](../examples/annotation/queue.jsonl) mixes English, Vietnamese, German and Japanese.

## Usage

```sh
quet annotate <queue.jsonl> --schema <schema.yaml> --out <labels.jsonl>
```

| Flag | Meaning |
| --- | --- |
| `--schema <path>` | YAML schema: types, statuses and null-target types (required) |
| `--out <path>` | Labels JSONL, rewritten after every mark; reopening it resumes (required) |

The queue argument and flags may come in any order, and `--flag value` and `--flag=value` are both
accepted. A missing queue, a missing `--schema` or `--out`, an extra argument, or a flag that
belongs to another command prints the usage on stderr and exits with status 2. `--schema` and
`--out` are rejected by every other command.

The schema, queue and existing labels are loaded and validated before anything else; any problem is
reported as `quet: <error>` (citing the line number where there is one) with exit status 1. The
annotation screen then needs an interactive terminal: when standard input or output is not one,
`quet annotate` prints `quet: quet annotate needs an interactive terminal` and exits with status 1.
`--out` must not be the queue file, and its directory must already exist.

## Queue

JSONL, one object per line, each with a string `id` and a string `text`. Other fields are ignored
and blank lines are skipped. A missing or duplicate `id`, a missing `text`, or a line that is not
valid JSON is an error. The queue file is never written.

```json
{"id": "rv-002", "text": "Phở ở đây ngon tuyệt, nước dùng rất đậm đà"}
```

## Schema

```yaml
version: sentiment-v1          # optional

types:                         # at least one; order = picker order and the 1-9 keys
  positive: The writer is pleased with the aspect named by the target.
  negative: The writer is unhappy with the aspect named by the target.
  neutral: A factual statement with no sentiment.

null_target_types: [neutral]   # optional; these types must have a null target

statuses:                      # at least one
  complete: Type and target confidently determined.
  uncertain: The text does not settle the type or the target.
  skipped: Not a review, or not worth labelling.
```

- `types` and `statuses` are either a mapping of name → description (shown in the type picker and
  help) or a plain list of names (`types: [positive, negative, neutral]`). Order is kept. Each needs
  at least one entry, without duplicates.
- `null_target_types` is an optional list; every entry must be a declared type.
- `version` is an optional string. All other keys are ignored, so a schema shared with other tools
  can carry its own settings.

Three keys are bound to fixed status names: `enter` marks `complete`, `u` marks `uncertain` and `s`
marks `skipped`. The schema may declare other statuses too (they are counted but have no key); a key
whose status the schema does not declare shows an error instead of marking anything.

## Labels

One JSON object per annotated record, in queue order, keys in this order:

```json
{"id":"rv-002","annotation_status":"complete","type":"positive","target":{"text":"nước dùng","start":22,"end":31}}
{"id":"rv-005","annotation_status":"complete","type":"neutral","target":null}
{"id":"rv-009","annotation_status":"skipped","type":null,"target":null}
```

- `type` and `target` are always present and may be `null`.
- `note` appears only when non-empty. Quet has no way to edit notes, but a note written by another
  tool is kept when the record is re-marked.
- Records you have not marked are absent.
- Non-ASCII text is written as is, not `\u` escaped.

The file is rewritten atomically (a temporary file in the same directory, then renamed over it)
every time a record is marked, so an interrupted session never leaves a half-written file.

## Validation

Every label is checked against the schema when it is marked and when the labels file is loaded:

- `annotation_status` must be a declared status.
- `type` may be `null` unless the status is `complete`; otherwise it must be a declared type.
- `target` may be `null`. Otherwise `start` and `end` are [code-point offsets](#offsets) with
  `0 ≤ start < end ≤` the text length, `text` is exactly the text between them, it is non-empty, and
  it neither starts nor ends with whitespace.
- `target` must be `null` when the type is in `null_target_types`, whatever the status.

Loading an existing labels file also rejects invalid JSON, unknown keys (a label may only have `id`,
`annotation_status`, `type`, `target` and `note`; a target only `text`, `start` and `end`), ids that
are not in the queue, and duplicate ids. Errors cite the line number. A missing labels file simply
means nothing is labelled yet.

## Resuming

Reopening the same `--out` resumes where you left off: saved labels are loaded and the cursor starts
on the first unfinished record. Only marking saves; choosing a type or a target builds an unsaved
draft for the current record, which `esc` discards. Quitting with `q` while the current record has
an unsaved draft asks for a second `q`.

After a successful mark the cursor moves to the next record in the current filter (wrapping around
the queue); under the `unfinished` and `all` filters it skips records that already have a label. When
nothing is left, the status line says `all records in <filter> are done`.

## Filters

`f` opens the filter picker:

| Filter | Records |
| --- | --- |
| `unfinished` | not yet labelled (the default) |
| `complete` | marked `complete` |
| `uncertain` | marked `uncertain` |
| `skipped` | marked `skipped` |
| `all` | every record, including labels with another schema status |

Any saved label counts as finished: `uncertain` and `skipped` records leave `unfinished` and are
found under their own filters. Labels with another status declared in the schema appear only under
`all`.

Switching filters moves the cursor to the first matching record at or after it, and leaves it where
it is when nothing matches.

## Keys

### Main screen

| Key | Action |
| --- | --- |
| `enter` | mark `complete` |
| `u` | mark `uncertain` |
| `s` | mark `skipped` |
| `t` | open the type picker |
| `1`–`9` | set the Nth type (schema order) |
| `x` | select the target span ([span mode](#span-mode)) |
| `n` | null target |
| `esc` | discard the unsaved draft |
| `a`, `←`, `h` | previous record |
| `d`, `→`, `l` | next record |
| `g` / `G` | first / last record |
| `f` | filter picker |
| `?` | help |
| `q` | quit (press twice when the current record has an unsaved draft) |
| `ctrl+c` | quit |

Choosing a null-target type clears the draft's target. The source text is never editable.

### Type picker

Opened with `t`; the highlighted type's schema description is shown.

| Key | Action |
| --- | --- |
| typing | fuzzy-filter the types (case-insensitive subsequence) |
| `backspace` | edit the query |
| `↑` / `↓`, `ctrl+p` / `ctrl+n` | move |
| `k` / `j` | move, while the query is empty |
| `1`–`9` | pick the type with that number |
| `enter` | select |
| `esc` | cancel |

### Span mode

Opened with `x`; refused with a message when the draft type is a null-target type. The selection
runs from an anchor to a head (inclusive, always at least one character) and starts on the existing
target, else on the first word. A word is a run of letters, digits and combining marks.

| Key | Action |
| --- | --- |
| `h` / `l`, `←` / `→` | move one character (collapses the selection) |
| `w` / `b` | select the next / previous whole word |
| `H` / `L`, `shift+←` / `shift+→` | move the head one character left / right, keeping the anchor (grows or shrinks the selection) |
| `W` / `B` | extend to the end of the next word / the start of the previous word |
| `0`, `home` / `$`, `end` | first / last character |
| `enter` | accept the selection as the target |
| `n` | null target, and leave span mode |
| `esc` | cancel |
| `?` | help |

A selection that is not a valid target (for example one that starts or ends with a space) is refused
on `enter` with the reason, and span mode stays open.

## Offsets

`start` and `end` count Unicode code points (Go runes), not bytes and not UTF-16 units: `start` is
inclusive, `end` exclusive, so the target is `text[start:end]` over the code points. In

```
Phở ở đây ngon tuyệt, nước dùng rất đậm đà
```

`Phở` is `start: 0, end: 3` (5 bytes of UTF-8) and `nước dùng` is `start: 22, end: 31` (bytes 30 to
43). In Python, `text[22:31]` gives the target; in Go, `string([]rune(text)[22:31])`.

Offsets are taken on the text exactly as it appears in the queue. A precomposed `ở` is one code
point, but the same letter written as `o` plus combining marks is three, so normalise the queue
before annotating if your tools expect a particular form.
