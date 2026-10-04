# Quet

**Quick Utility for Evaluating Text**

A fast, keyboard-first TUI for reviewing and curating text corpora.

```
generate → quet → review → export
```

Quet opens a corpus and shows one record at a time. You approve or reject each record with one key.
Then you export the records that you kept.

- Each review action is also a plain command with JSON output. Scripts and AI agents can review
  without the TUI.
- Quet is local. It is one static binary plus one SQLite sidecar file next to the corpus.
- Quet never changes the source file.
- Quet has no telemetry. It uses the network only for `quet update`, the `update.check` option, and
  `quet web`.

## Install

macOS and Linux, arm64 and x86_64:

```sh
curl -fsSL https://raw.githubusercontent.com/8bu/quet/main/install.sh | sh
```

The script downloads the latest release and checks it against `SHA256SUMS`. It installs to
`/usr/local/bin`, or to `~/.local/bin` when it cannot write there.

- To upgrade, run `quet update`.
- To pin a version, set `QUET_VERSION=0.1.1`.
- To choose the install folder, set `QUET_INSTALL_DIR`.
- To build from source (Go 1.26+), run `make install` in a checkout.

## Quickstart

```sh
quet examples/corpus.jsonl    # open a corpus
quet                          # or pick a file in the file browser
```

`w` approve · `s` reject · `space` needs review · `z` undo · `?` all keys · `q` quit

Quet saves progress after each key. You can quit at any time and continue later. To export:

```sh
quet export examples/corpus.jsonl    # writes examples/corpus.approved.jsonl
```

`quet init` writes a starter `quet.yaml` and `flags.yaml`. See [Configuration](docs/configuration.md).

Commands share the review state and the undo log with the TUI:

```sh
quet list examples/corpus.jsonl --filter unreviewed --json
quet set examples/corpus.jsonl id:note-001 --status approved
```

See [Scripting and agents](docs/scripting.md).

## Annotating

`quet annotate` labels a queue of `{"id", "text"}` records against a YAML schema. Each label has a
type, one or more spans of the text, and a status. Quet writes the labels to a JSONL file after each
mark, so you can stop and continue later.

```sh
quet annotate examples/annotation/queue.jsonl \
  --schema examples/annotation/schema.yaml --out labels.jsonl
```

`enter` complete · `u` uncertain · `s` skip · `t` type · `x` select span · `n` null span

- **Re-check:** use `--labels` instead of `--out` to edit a subset of a labels file. Quet keeps all
  other labels.
- **Multiple span fields:** a schema can declare `spans:`, for example a counterparty and an amount.
  `tab` changes the active field, and `c` changes its span status. See
  [`schema-multispan.yaml`](examples/annotation/schema-multispan.yaml).
- **Proposals:** `--proposals <file>` shows AI suggestions next to each record. `p` accepts a
  proposal. `P` loads it for edit. Quet never saves a proposal without a key press.

A label line looks like this:

```json
{"id":"ms-001","annotation_status":"complete","type":"expense","target":{"text":"Vinamilk","start":10,"end":18},"value":{"text":"500k","start":23,"end":27},"span_status":{"value":"complete"}}
```

Schemas without `spans:` work as before. See [Annotating](docs/annotating.md).

## Web companion: quet-web

[quet-web](https://github.com/8bu/quet-web) lets outside collaborators label in a browser. It runs
at [quet.8bu.dev](https://quet.8bu.dev).

- The owner sends a queue, a schema and proposals with `quet web push`.
- The owner makes collaborator accounts in the admin dashboard at
  [quet.8bu.dev/admin](https://quet.8bu.dev/admin). Cloudflare Access protects the dashboard.
- Each collaborator logs in with a username and a password and labels the assigned projects.
- The owner gets the labels back with `quet web pull`, and sees progress with `quet web list`.
- In `quet annotate`, `w` opens the web menu. Compare mode shows the label of each collaborator,
  and you pick the final label.

```sh
quet web remote add origin https://quet.8bu.dev   # one time, then: quet web login
quet web push queue.jsonl --schema schema.yaml --project expenses
quet web list
quet web pull --project expenses --all --out-dir pulled/
```

Labels from the web use the same format as Quet labels, with `note` and `span_status`. See
[Web sync](docs/web.md).

## Documentation

| | |
| --- | --- |
| [Usage](docs/usage.md) | Command line, file browser, `init`, `stats`, `update` |
| [Annotating](docs/annotating.md) | `quet annotate`: schemas, span fields, labels, validation, re-check, proposals, keys, offsets |
| [Web sync](docs/web.md) | `quet web`: remotes, login, push, list, pull, the sidecar, the web menu and compare mode |
| [Reviewing](docs/reviewing.md) | Statuses, auto-advance, undo, keys, flags, config edits |
| [Diagnostics](docs/diagnostics.md) | The six hints that Quet calculates and their limits |
| [Input formats](docs/formats.md) | `.jsonl`, `.json`, `.txt`, and how Quet keeps metadata |
| [Configuration](docs/configuration.md) | `quet.yaml`, `flags.yaml` and `quet init` |
| [Scripting and agents](docs/scripting.md) | `list`, `show`, `set`, `flag`, `suggest`, `edit`, `undo`, JSON output, exit codes |
| [Export](docs/export.md) | Presets, output files, review metadata |
| [Development](docs/development.md) | Build, test, layout, release |

See [CHANGELOG.md](CHANGELOG.md) for release notes.

## License

MIT. See [LICENSE](LICENSE).
