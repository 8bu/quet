# Quet

**Quick Utility for Evaluating Text**

A fast, keyboard-first TUI for reviewing and curating text corpora.

```
generate → quet → review → export
```

Open a corpus, approve or reject each record with a single key, and export the records you kept.
Every review action is also a plain command with JSON output, so scripts and AI agents can review
without the TUI. Everything is local: one static binary and one SQLite sidecar file next to your
corpus. No telemetry, no network access unless you run `quet update`, opt in to `update.check`, or use
[`quet web`](docs/web.md) with your own quet-web server, and the source file is never modified.

## Install

macOS and Linux, arm64 and x86_64:

```sh
curl -fsSL https://raw.githubusercontent.com/8bu/quet/main/install.sh | sh
```

The script downloads the latest release binary, verifies it against the release's `SHA256SUMS`, and
installs it to `/usr/local/bin` (or `~/.local/bin` when that isn't writable). To upgrade, run
`quet update` (re-running the script still works); set `QUET_VERSION=0.1.1` to pin a version or
`QUET_INSTALL_DIR` to choose where it goes.

From source, with Go 1.26+: `make install` in a checkout puts `quet` in `$GOPATH/bin`.

## Quickstart

```sh
quet examples/corpus.jsonl    # open a corpus
quet                          # or pick one from a file browser
```

`w` approve · `s` reject · `space` needs review · `z` undo · `?` all keys · `q` quit

Progress is saved on every keystroke, so you can quit at any time and pick up where you left off.
When you're done:

```sh
quet export examples/corpus.jsonl    # writes examples/corpus.approved.jsonl
```

To customise diagnostic thresholds and manual flags, `quet init` writes a starter `quet.yaml` and
`flags.yaml` (`c` in the TUI edits the config and reloads it).

Everything can also be scripted, sharing the same review state and undo log as the TUI:

```sh
quet list examples/corpus.jsonl --filter unreviewed --json    # one JSON record per line
quet set examples/corpus.jsonl id:note-001 --status approved
```

See [Scripting and agents](docs/scripting.md).

## Annotating

`quet annotate` labels a queue of `{"id", "text"}` records one at a time against a YAML schema you
write: a type, an optional target span of the text, and a status (`enter` complete · `u` uncertain
· `s` skip). Labels go to a JSONL file that is rewritten after every mark, so reopening it resumes;
review state is not touched.

```sh
quet annotate examples/annotation/queue.jsonl \
  --schema examples/annotation/schema.yaml --out labels.jsonl
```

To re-check a subset of an existing labels file, open the subset queue with `--labels` instead of
`--out`: only the subset's records are shown and editable, and every other label is preserved.

```sh
quet annotate recheck-01.jsonl --schema schema.yaml --labels labels.jsonl
```

### Multiple span fields

A schema may declare several named span fields with `spans:`, so one pass labels the type and every
span (for example a counterparty and an amount) instead of a single `target`:

```yaml
spans:                              # optional; YAML order = tab order and label key order
  target:
    description: Counterparty. Minimal span as typed.
    null_for_types: [transfer]      # this span must be null for these types
  value:
    description: Monetary amount. Exact substring, no normalization.
    statuses: [complete, uncertain] # optional per-span status; the first is the default
```

Each label gets one top-level key per span (`{"text","start","end"}` or `null`) and, when a span
declares `statuses`, a `span_status` object:

```json
{"id":"ms-001","annotation_status":"complete","type":"expense","target":{"text":"Vinamilk","start":10,"end":18},"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"complete"}}
```

`tab` / `shift+tab` cycle the active field, `x` and `n` select or null it, and `c` cycles its span
status; `--proposals` takes the same per-field keys. Schemas without `spans:` (one implicit `target`,
`null_target_types`) and their labels and proposals files behave exactly as before. `spans:` cannot be
combined with `null_target_types`. See
[`schema-multispan.yaml`](examples/annotation/schema-multispan.yaml) and
[Multiple span fields](docs/annotating.md#multiple-span-fields).

### Proposals

`--proposals <proposals.jsonl>` adds advisory AI suggestions next to each record's current
annotation. It works with `--out` and with `--labels`:

```sh
quet annotate recheck.jsonl --schema annotation.yaml --labels labels.jsonl --proposals proposals.jsonl
```

Each line is an object with a required `id` and `annotation_status` and optional `type`, `target`
(`{"text","start","end"}` or `null`), `note`, `confidence` (0 to 1) and `reason`; other keys are
ignored. Invalid lines and duplicate ids refuse to open, citing `proposals <path>:<line>`; ids that
are not in the queue are ignored and reported on exit. The record panel shows the proposal in its
own `Proposal — not accepted` block beside `Current`, marked `✓ matches current` or
`⚠ invalid: …`. `p` accepts it (validated first, saved like a mark, undoable), `P` loads it into the
draft so you can edit it and then mark with `enter`/`u`/`s`, and ignoring it does nothing. Three
extra filters appear: `proposed`, `unproposed`, `proposed uncertain`.

The proposals file is never written, nothing is saved without `p`, `enter`, `u` or `s`, an existing
label is never overwritten automatically, `confidence` and `reason` are never written to the labels
file, and re-check merge semantics are unchanged.

See [Annotating](docs/annotating.md) and its [Re-check subset](docs/annotating.md#re-check-subset),
[Proposals](docs/annotating.md#proposals) and
[Multiple span fields](docs/annotating.md#multiple-span-fields) sections.

### Sharing with quet-web

`quet web` publishes a queue, schema and proposals to a [quet-web](docs/web.md) server and pulls
every collaborator's labels back, either into a labels file you pick or (in the `quet annotate`
TUI, key `w`) record by record in a compare mode.

```sh
quet web remote add origin https://quet.8bu.dev   # once; then: quet web login
quet web push queue.jsonl --schema schema.yaml --project expenses
quet web pull --project expenses --all --out-dir pulled/
```

Credentials are a Cloudflare Access service token kept in `~/.config/quet/remotes.yaml` (mode
0600) and never printed. See [Web sync](docs/web.md).

## Documentation

| | |
| --- | --- |
| [Usage](docs/usage.md) | Command line, file browser, `init`, `stats`, `update` |
| [Annotating](docs/annotating.md) | `quet annotate`: schemas, multiple span fields, labels output, validation, re-check subsets, proposals, span keys, offsets |
| [Web sync](docs/web.md) | `quet web`: remotes, login, push, list, pull, the sidecar, and the annotate web menu and compare mode |
| [Reviewing](docs/reviewing.md) | Statuses, auto-advance, undo, keybindings, flags, editing config |
| [Diagnostics](docs/diagnostics.md) | The six hints Quet computes locally and their thresholds |
| [Input formats](docs/formats.md) | `.jsonl`, `.json`, `.txt` and how metadata is preserved |
| [Configuration](docs/configuration.md) | `quet.yaml`, `flags.yaml` and `quet init` |
| [Scripting and agents](docs/scripting.md) | `list`, `show`, `set`, `flag`, `suggest`, `edit`, `undo`, JSON output, exit codes |
| [Export](docs/export.md) | Presets, output files, review metadata |
| [Development](docs/development.md) | Building, testing, layout, releasing |

See [CHANGELOG.md](CHANGELOG.md) for release notes.

## License

MIT. See [LICENSE](LICENSE).
