# Quet

**Quick Utility for Evaluating Text**

A fast, keyboard-first TUI for reviewing and curating text corpora.

```
generate → quet → review → export
```

Open a corpus, approve or reject each record with a single key, and export the records you kept.
Every review action is also a plain command with JSON output, so scripts and AI agents can review
without the TUI. Everything is local: one static binary and one SQLite sidecar file next to your
corpus. No telemetry, no network access unless you run `quet update` or opt in to `update.check`,
and the source file is never modified.

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

See [Annotating](docs/annotating.md).

## Documentation

| | |
| --- | --- |
| [Usage](docs/usage.md) | Command line, file browser, `init`, `stats`, `update` |
| [Annotating](docs/annotating.md) | `quet annotate`: schemas, labels output, validation, span keys, offsets |
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
