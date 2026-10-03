# Annotating

`quet annotate` labels a queue of text records one at a time against a schema you define: each
record gets a **type**, an optional **target** (a span of its text), and an **annotation status**.
A schema may also declare [several named span fields](#multiple-span-fields), each with its own
null rules and optional status, for example a counterparty and an amount in one pass. It is
separate from reviewing: it never reads or writes the `.quet.db` review sidecar, and its only
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
quet annotate <queue.jsonl> --schema <schema.yaml> --labels <labels.jsonl>
quet annotate <queue.jsonl> --schema <schema.yaml> (--out|--labels) <labels.jsonl> --proposals <proposals.jsonl>
```

| Flag | Meaning |
| --- | --- |
| `--schema <path>` | YAML schema: types, statuses, null-target types or [spans](#multiple-span-fields) (required) |
| `--out <path>` | Labels JSONL for this queue, rewritten after every mark; reopening it resumes |
| `--labels <path>` | Existing canonical labels JSONL to [re-check a subset](#re-check-subset) of |
| `--proposals <path>` | Optional advisory [proposals](#proposals) JSONL; read-only, works with `--out` or `--labels` |

Exactly one of `--out` and `--labels` is required; giving both is an error. The queue argument and
flags may come in any order, and `--flag value` and `--flag=value` are both accepted. A missing
queue, a missing `--schema`, neither or both of `--out` and `--labels`, an extra argument, or a flag
that belongs to another command prints the usage on stderr and exits with status 2. `--schema`,
`--out`, `--labels` and `--proposals` are rejected by every other command.

The schema, queue, existing labels and proposals are loaded and validated before anything else; any problem is
reported as `quet: <error>` (citing the line number where there is one) with exit status 1. The
annotation screen then needs an interactive terminal: when standard input or output is not one,
`quet annotate` prints `quet: quet annotate needs an interactive terminal` and exits with status 1.
The labels file (`--out` or `--labels`) must not be the queue file, and its directory must already
exist; a `--labels` file must exist too.

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

null_label_statuses: [skipped] # optional; default [skipped]; these statuses save a null type and target
```

- `types` and `statuses` are either a mapping of name → description (shown in the type picker and
  help) or a plain list of names (`types: [positive, negative, neutral]`). Order is kept. Each needs
  at least one entry, without duplicates.
- `null_target_types` is an optional list; every entry must be a declared type.
- `null_label_statuses` is an optional list; every entry must be a declared status. Marking a record
  with one of these statuses saves `"type":null,"target":null`, dropping any type or target the
  record had, so `s` on a `complete` label turns it into a plain skip. Undo brings the old label back.
  Without the key it defaults to `[skipped]` when `skipped` is declared; `null_label_statuses: []`
  turns clearing off.
- `version` is an optional string. All other keys are ignored, so a schema shared with other tools
  can carry its own settings.

Three keys are bound to fixed status names: `enter` marks `complete`, `u` marks `uncertain` and `s`
marks `skipped`. The schema may declare other statuses too (they are counted but have no key); a key
whose status the schema does not declare shows an error instead of marking anything.

## Multiple span fields

A schema may declare several named span fields, so one pass labels the type and every span of a
record (for example a counterparty and an amount) instead of one `target`. Nothing changes without
`spans:`: a schema that has none, and its labels and proposals files, behave exactly as before, with
one implicit span called `target` governed by `null_target_types`, the same messages, bytes and
screen. The rest of this page describes that case; this section adds what `spans:` changes.

### Schema

```yaml
version: expense-v1
types: {expense: Money paid out, income: Money received, transfer: Between own accounts}
statuses: {complete: Confident, uncertain: Unsure, skipped: Not a money note}
null_label_statuses: [skipped]

spans:                              # optional; YAML order = tab order and label key order
  target:
    description: Counterparty. Minimal span as typed.
    null_for_types: [transfer]      # this span must be null for these types
  value:
    description: Monetary amount. Exact substring, no normalization.
    statuses: [complete, uncertain] # optional per-span status; the first is the default
```

A complete example is [`schema-multispan.yaml`](../examples/annotation/schema-multispan.yaml) with
[`queue-multispan.jsonl`](../examples/annotation/queue-multispan.jsonl).

- `spans` is either a mapping of span name → options, or a plain list of names
  (`spans: [target, value]`, no options). A mapping value may be a description string, `null`, or a
  mapping with the optional keys `description` (string), `null_for_types` (declared types, no
  duplicates) and `statuses` (declared statuses, no duplicates; `[]` means none). Order is kept.
- `null_for_types` replaces `null_target_types` for that field: the span must be `null` when the
  record's type is listed, whatever the status.
- `statuses` gives the span its own status, separate from the record's `annotation_status`, for
  example to say that an amount is uncertain while the record is `complete`. A span without
  `statuses` has no span status.
- Span names must match `^[A-Za-z_][A-Za-z0-9_]*$`, be unique, and not be one of the reserved label
  keys `id`, `annotation_status`, `type`, `note` and `span_status`.
- `spans: null` counts as absent (the implicit `target` applies).
- Schema errors cite the line number: `spans` together with `null_target_types` (`spans: cannot be
  combined with null_target_types; use null_for_types per span`), an empty `spans` mapping or list,
  a duplicate, badly formed or reserved span name, an unknown key inside a span mapping, a
  `null_for_types` entry that is not a declared type, and a `statuses` entry that is not a declared
  status.
- `null_label_statuses` nulls the type and every span, and drops all span statuses.

### Labels

Every declared span name is a top-level key, always present, either `{"text","start","end"}` (same
[offsets](#offsets) as `target`) or `null`. Keys are written in this order: `id`,
`annotation_status`, `type`, each span in schema order, `span_status` (only when it has members),
`note` (only when non-empty).

```json
{"id":"ms-001","annotation_status":"complete","type":"expense","target":{"text":"Vinamilk","start":10,"end":18},"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"complete"}}
{"id":"ms-002","annotation_status":"uncertain","type":"transfer","target":null,"value":{"text":"2tr","start":13,"end":16},"span_status":{"value":"uncertain"}}
{"id":"ms-005","annotation_status":"skipped","type":null,"target":null,"value":null}
```

- `span_status` is an object keyed by span name, with string values, and covers only spans that
  declare `statuses`. It is not an allowed key at all in a schema where no span declares statuses.
- Marking fills in the span status for every span that declares `statuses` and is not null for the
  record's type: the status you chose with `c`, otherwise the span's **first listed status** (the
  default). A span keeps its status even when the span itself is `null`: it says how sure you are
  that there is no value. Spans that are null for the type never get one.
- A status in `null_label_statuses` saves `"type":null`, every span `null` and no `span_status`.
- Choosing a type that is null-for-a-span clears that span (and its draft status) in the draft.
- Loading is strict as before: allowed keys are `id`, `annotation_status`, `type`, `note`, every span
  name and (only when a span declares statuses) `span_status`; `id`, `annotation_status`, `type` and
  every span name are required. Span errors are prefixed with the span name (`value.text: ...`,
  `value: expected an object or null`); for the implicit `target` they are exactly the old messages.

### Validation

The checks under [Validation](#validation) apply to every span, each in schema order, joined with
`; ` after the status and type problems:

- `<name>: ...` for a span that is outside the text, does not match the text, is empty or has
  leading or trailing whitespace (the same wording as for `target`, with the name substituted);
- `<name>: must be null for type "<type>"` for a span in its `null_for_types`;
- `span_status.<name>: "<s>" is not one of [<span statuses>]`;
- `span_status.<name>: span declares no statuses`, `span_status.<name>: not a declared span`, and
  `span_status.<name>: must be absent for type "<type>"`.

A missing `span_status` entry is not an error on load (the file is read leniently); the default
applies the next time the record is marked.

### Proposals

`--proposals` takes the same per-field keys: each declared span name is an optional top-level key
(`{"text","start","end"}` or `null`; absent means null) and an optional `span_status` object with
string values. Other members stay ignored, so reading proposals needs the schema. As with
`target`, values are checked against the schema and the record's text when displayed and accepted:
an invalid span, an undeclared span status or a span set for a null-for-type type is marked
`⚠ invalid`, and `p` refuses it. `p` and `P` carry type, every span and the span statuses; a null
span is dropped for a type that must have it null.

```json
{"id":"ms-001","annotation_status":"complete","type":"expense","target":{"text":"Vinamilk","start":10,"end":18},"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"uncertain"},"confidence":0.8}
```

### On screen and keys

With more than one span, the record panel shows each field as `<Label>: "text" [a,b)` or
`<Label>: null`, plus ` · <status>` for a field with statuses (`(default)` while you have not set
one). Every non-null span is highlighted in the text in its own colour, the active field on top
where they overlap, and the active field is marked in the list. The proposal block lists each field
the same way. A schema with a single span (every schema without `spans:`) is drawn exactly as
before: `Target: ...`, the same footer and help.

- `tab` / `shift+tab` cycle the active field (with one field they do nothing and are not listed).
- `x` selects a span for the active field and `n` nulls it; in span mode `enter` writes to the
  active field, `n` nulls it, `tab` / `shift+tab` switch the active field keeping the selection, and
  `esc` cancels.
- `c` cycles the active field's span status through its `statuses` list, wrapping; a field without
  `statuses` answers `span <name> declares no statuses`.
- The active field is kept when you move to another record.

## Labels

One JSON object per annotated record, in queue order (with `--out`; see
[Re-check subset](#re-check-subset) for `--labels`), keys in this order:

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

With [`spans:`](#multiple-span-fields), `target` is replaced by one field per declared span, each
checked the same way and against its own `null_for_types`, plus the `span_status` rules.

`null_label_statuses` is applied when marking, not checked on load: a labels file that already has,
say, a `skipped` label with a type still opens, shows that type, and is cleaned up to
`"type":null,"target":null` the next time the record is marked with that status.

Loading an existing labels file also rejects invalid JSON, unknown keys (a label may only have `id`,
`annotation_status`, `type`, `target` and `note`; a target only `text`, `start` and `end`), ids that
are not in the queue (with `--out` only), and duplicate ids. Errors cite the line number. A missing
`--out` file simply means nothing is labelled yet.

## Resuming

Reopening the same `--out` resumes where you left off: saved labels are loaded and the cursor starts
on the first unfinished record. Only marking saves; choosing a type or a target builds an unsaved
draft for the current record, which `esc` discards. Quitting with `q` while the current record has
an unsaved draft asks for a second `q`.

After a successful mark the cursor moves to the next record in the current filter (wrapping around
the queue); under the `unfinished` and `all` filters it skips records that already have a label. When
nothing is left, the status line says `all records in <filter> are done`.

## Revising labels

Every record stays reachable after it is labelled: `a` / `d` (and `h` / `l`, `←` / `→`) step
through the whole queue in order, whatever the filter, so going back with `a` right after a mark
lands on the record you just labelled. `:` or `#` jumps straight to a queue position or record id,
and the `complete`, `uncertain` and `skipped` filters list labelled records by status.

A labelled record shows its saved status in the record panel title (`Record 12 / 600 · ✓ complete`,
`? uncertain`, `– skipped`, other statuses by name) and a `Saved:` line under its type and target.
To revise it, change the type or target with `t`, `1`–`9`, `x` or `n`, then save again with
`enter`, `u` or `s`; saving under a new status without edits just changes the status. The label is
replaced in place, so the labels file never holds an id twice, and the status line reads
`revised <status> <id>`.

`z` undoes the most recent mark of the session: the record gets back the label it had before (or
loses its label if it had none), the labels file is rewritten, and the cursor moves to it. Pressing
`z` again undoes the mark before that, down to the start of the session; then the status line says
`nothing to undo`. Undo does not reach marks from earlier sessions.

## Re-check subset

To re-check some records of a larger, already-labelled dataset, put them in a subset queue and open
it against the canonical labels file with `--labels` instead of `--out`:

```sh
quet annotate recheck-01.jsonl --schema schema.yaml --labels labels.jsonl
```

- The labels file must already exist (`labels file <path> does not exist` otherwise), so a typo
  never starts a fresh file. It may hold labels for ids that are not in the queue.
- Only the queue's records are shown, navigated and editable; every other label is kept exactly as
  it was, byte for byte.
- Every label is validated when the file is opened. Queue ids are checked against their text as
  usual; other ids have no text to compare, so they get the schema checks (declared status and type,
  a type when `complete`, no target on a null-target type) and a structural target check (`start ≥
  0`, `end > start`, and `text` exactly `end - start` code points long); with
  [`spans:`](#multiple-span-fields) every span gets the same checks and the `span_status` rules. Invalid JSON, unknown keys
  and duplicate ids anywhere in the file are errors too.
- Marking a queue record replaces its line in place; a queue record that had no label gets a new
  line appended at the end. Undo restores the previous line, or removes the appended one. Unchanged
  lines keep their original bytes; blank lines are dropped on the first write.
- Writes are atomic, as with `--out`. Before each write Quet re-reads the file, and if it differs
  from what Quet last read or wrote (another program changed it), nothing is written and the mark
  fails with an error asking you to reopen.

The session opens under the `all` filter on the first queue record. After a mark the cursor moves to
the next record of the filter in queue order, whether or not it already has a label, and does not
wrap: marking the last one leaves the cursor there and the status line says
`end of re-check in <filter>`. `a` / `d`, `[` / `]`, `g` / `G` and go to work as usual, within the
subset queue.

The header marks the mode: the title reads `Quet — re-check · <queue> → <labels>`, the counts row
starts with `Re-check <position> / <queue length>`, and an extra row shows
`Labels: <n> total · <m> in current queue`: the distinct labels in the file as saved, and how many
of them belong to the queue.

`--out` is unchanged and stays strict: every label id must be in the queue, and the file is rewritten
in queue order.

## Proposals

`--proposals <proposals.jsonl>` loads advisory suggestions, for example from a model or a script,
and shows each next to the record's current annotation. You accept one, load it into the draft to
edit it, or ignore it. It works with `--out` and with `--labels`:

```sh
quet annotate recheck.jsonl --schema annotation.yaml --labels labels.jsonl --proposals proposals.jsonl
```

### Format

JSONL, one object per line; blank lines are skipped.

```json
{"id":"rv-002","annotation_status":"complete","type":"positive","target":{"text":"nước dùng","start":22,"end":31},"note":"praised broth","confidence":0.91,"reason":"explicit praise of the broth"}
{"id":"rv-003","annotation_status":"uncertain","type":null,"target":null,"confidence":0.35}
```

| Member | Meaning |
| --- | --- |
| `id` | required, non-empty string: the queue record it is about |
| `annotation_status` | required string |
| `type` | optional string or `null` |
| `target` | optional (the implicit span of a schema without `spans:`): `null` or `{"text","start","end"}`, decoded like a [label's target](#labels) |
| span names, `span_status` | with [`spans:`](#multiple-span-fields): one optional member per declared span (same shape as `target`), and an optional `span_status` object of strings |
| `note` | optional string |
| `confidence` | optional number from 0 to 1, shown to you and never written |
| `reason` | optional string, shown to you and never written |

Any other member is ignored, so another tool's extra fields do no harm.

### Loading and validation

The file is read when `quet annotate` starts, after the queue and labels, and before the terminal
check. A problem refuses to open and is reported as `quet: proposals <path>:<line>: <error>` with
exit status 1: invalid JSON, a missing or ill-typed `id` or `annotation_status`, a wrongly typed
optional member, a bad `target` (or other [span](#multiple-span-fields)) shape, a `confidence` outside 0 to 1, or a duplicate `id` (the error
names the first line). The file must exist and must not be the queue file or the labels file.

Proposals are **not** checked against the schema when loaded. Ids that are not in the queue are not
an error: they are ignored, the header and startup status count them
(`Proposals: 12 for queue · 3 ignored (not in queue)`), and after you quit Quet prints
`quet: ignored 3 proposal(s) for ids not in the queue: a, b, c` on stderr (ten ids at most, then
`, … (+K more)`).

### On screen

When the current record has a proposal, the record panel shows two sections: `Current`, the draft and
saved status as always, and a separately styled `Proposal — not accepted` block with its type,
target (`"text" [start,end)` or `null`), status, confidence (two decimals, or `-`), reason and note.
The block ends with `✓ matches current` when the proposal equals the saved label, or
`⚠ invalid: <reason>` when it would not pass the [validation](#validation) for this record's text
(an undeclared type or status, a target that is not in the text, and so on). The footer adds
`p accept proposal  P edit proposal`, and `?` has a Proposals section. Without `--proposals`
nothing on screen changes.

- `p` accepts the proposal: it saves exactly as marking the record with the proposal's status would
  from a draft of the proposal's type and target, so `null_label_statuses` clearing applies. The
  note is the proposal's when it has one, otherwise the saved note stays. The proposal is validated
  first; an invalid one is refused with the validation error and nothing is written. Accepting goes
  through the normal save path (`--out`, or the re-check merge writer), moves on like a mark, and
  can be undone with `z`. It may replace an existing label: that is your explicit action, and undo
  restores it. A record without a proposal answers `no proposal for <id>`.
- `P` loads the proposal's type and target into the draft and saves nothing; the status line says
  `proposal loaded into draft — edit, then enter/u/s to save`. Change the type or target as usual,
  then mark with `enter`, `u` or `s`. It fails, with nothing changed, if the type is not declared
  or the target is not a valid span of the text.
- Doing neither is fine: a proposal you ignore has no effect.

### Safety

- The proposals file is never written.
- Nothing is saved without `p`, `enter`, `u` or `s`. Loading proposals, moving between records and
  `P` change no label.
- An existing label is never overwritten automatically, only by your own `p` or mark.
- `confidence` and `reason` are advisory and are never written to the labels file. A high
  confidence is not ground truth: check the text.
- [Re-check](#re-check-subset) semantics are unchanged: only queue records are editable, every other
  label is preserved byte for byte, writes are atomic and refused if the file changed underneath.

## Filters

`f` opens the filter picker:

| Filter | Records |
| --- | --- |
| `unfinished` | not yet labelled (the default) |
| `complete` | marked `complete` |
| `uncertain` | marked `uncertain` |
| `skipped` | marked `skipped` |
| `all` | every record, including labels with another schema status |
| `proposed` | has a proposal (only with `--proposals`) |
| `unproposed` | has no proposal (only with `--proposals`) |
| `proposed uncertain` | its proposal's status is `uncertain` (only with `--proposals`) |

Any saved label counts as finished: `uncertain` and `skipped` records leave `unfinished` and are
found under their own filters. Labels with another status declared in the schema appear only under
`all`.

Switching filters moves the cursor to the first matching record at or after it, and leaves it where
it is when nothing matches. `[` and `]` move to the previous and next record in the filter; the
other navigation keys ignore it.

## Keys

### Main screen

| Key | Action |
| --- | --- |
| `enter` | mark `complete` |
| `u` | mark `uncertain` |
| `s` | mark `skipped` |
| `t` | open the type picker |
| `1`–`9` | set the Nth type (schema order) |
| `x` | select the target span ([span mode](#span-mode)); with `spans:`, the active field's |
| `n` | null target; with `spans:`, null the active field |
| `tab` / `shift+tab` | next / previous active span field (only with more than one `spans:` field) |
| `c` | cycle the active field's span status (only with `spans:` where a field declares `statuses`) |
| `esc` | discard the unsaved draft |
| `a`, `A`, `h`, `H`, `←` | previous record in queue order, labelled or not |
| `d`, `D`, `l`, `L`, `→` | next record in queue order, labelled or not |
| `[` / `]` | previous / next record in the current filter |
| `g` / `G` | first / last record in the queue |
| `:`, `#` | [go to](#go-to) a queue position or record id |
| `z`, `Z` | undo the last mark of this session |
| `f` | filter picker |
| `p` | accept the record's [proposal](#proposals) and save it (only with `--proposals`) |
| `P` | load the record's proposal into the draft, without saving (only with `--proposals`) |
| `?` | help |
| `q` | quit (press twice when the current record has an unsaved draft) |
| `ctrl+c` | quit |

Choosing a null-target type clears the draft's target. The source text is never editable. At either
end of the queue `a` and `d` stay put and say `first record` or `last record`.

### Go to

Opened with `:` or `#`. Type a 1-based queue position (`12`) or an exact record id (`rv-002`);
surrounding spaces are ignored and a number is read as a position first.

| Key | Action |
| --- | --- |
| typing | edit the query (every printable key is text, including `q` and `?`) |
| `backspace` | delete the last character |
| `enter` | jump; an unknown position or id shows an error and keeps the prompt open |
| `esc` | cancel |

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

Opened with `x`; refused with a message when the draft type is a null-target type (with
[`spans:`](#multiple-span-fields): null for the active field). The selection
runs from an anchor to a head (inclusive, always at least one character) and starts on the existing
target, else on the first word. A word is a run of letters, digits and combining marks.

| Key | Action |
| --- | --- |
| `h` / `l`, `←` / `→` | move one character (collapses the selection) |
| `w` / `b` | select the whole word under the cursor if it isn't already selected (so `0` then `w` picks the first word), else the next / previous whole word |
| `H` / `L`, `shift+←` / `shift+→` | move the head one character left / right, keeping the anchor (grows or shrinks the selection) |
| `W` / `B` | extend to the end of the next word / the start of the previous word; from a single space or punctuation character, select the next / previous word instead |
| `0`, `home` / `$`, `end` | first / last character |
| `enter` | accept the selection as the target (with [`spans:`](#multiple-span-fields): write it to the active field) |
| `n` | null target, and leave span mode (with `spans:`: null the active field) |
| `tab` / `shift+tab` | with `spans:` and more than one field: switch the active field, keeping the selection |
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
