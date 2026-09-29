# Quet

**Quick Utility for Evaluating Text**

A fast, keyboard-first TUI for reviewing and curating text corpora.

```
generate → quet → review → export
```

Open a corpus, approve or reject each record with a single key, and export the records you kept.
Everything is local: one static binary and one SQLite sidecar file next to your corpus. No network
and no telemetry, and the source file is never modified.

## Install

Requires Go 1.26+. From a checkout of this repository:

```sh
make install          # puts quet in $GOPATH/bin
```

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

## Documentation

| | |
| --- | --- |
| [Usage](docs/usage.md) | Command line, file browser, `stats` |
| [Reviewing](docs/reviewing.md) | Statuses, auto-advance, undo, keybindings, flags |
| [Auto checks](docs/checks.md) | The seven warnings Quet computes and their thresholds |
| [Input formats](docs/formats.md) | `.jsonl`, `.json`, `.txt` and how metadata is preserved |
| [Configuration](docs/configuration.md) | `quet.yaml` and `flags.yaml` |
| [Export](docs/export.md) | Presets, output files, review metadata |
| [Development](docs/development.md) | Building, testing, layout, releasing |

See [CHANGELOG.md](CHANGELOG.md) for release notes.
