# Quet

**Quick Utility for Evaluating Text**

A fast, keyboard-first TUI for reviewing and curating text corpora.

```
generate → quet → review → export
```

Open a corpus, approve or reject each record with a single key, and export the records you kept.
Everything is local: one static binary and one SQLite sidecar file next to your corpus. No
telemetry, no network access unless you run `quet update` or opt in to `update.check`, and the
source file is never modified.

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

## Documentation

| | |
| --- | --- |
| [Usage](docs/usage.md) | Command line, file browser, `init`, `stats`, `update` |
| [Reviewing](docs/reviewing.md) | Statuses, auto-advance, undo, keybindings, flags, editing config |
| [Diagnostics](docs/diagnostics.md) | The six hints Quet computes locally and their thresholds |
| [Input formats](docs/formats.md) | `.jsonl`, `.json`, `.txt` and how metadata is preserved |
| [Configuration](docs/configuration.md) | `quet.yaml`, `flags.yaml` and `quet init` |
| [Export](docs/export.md) | Presets, output files, review metadata |
| [Development](docs/development.md) | Building, testing, layout, releasing |

See [CHANGELOG.md](CHANGELOG.md) for release notes.

## License

MIT. See [LICENSE](LICENSE).
