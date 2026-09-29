# Configuration

Quet reads the first existing of `./quet.yaml` and `~/.config/quet/config.yaml`
(`$XDG_CONFIG_HOME` is respected). Missing keys keep their defaults; `--config <path>` uses an
explicit file instead. Only the `.yaml` names (`quet.yaml`, `flags.yaml`, `config.yaml`) are
looked for; `.yml` files are not picked up.

```yaml
review:
  skip_reviewed: true

checks:
  max_chars: 160
  repeated_char_threshold: 5
  weird_symbol_ratio: 0.35
  template_min_occurrences: 8

update:
  check: false

flags_file: "./flags.yaml"
```

`review.skip_reviewed` controls [auto-advance](reviewing.md#statuses); the `checks` keys are the
thresholds of the [diagnostics](diagnostics.md).

`update.check` is opt-in. When `true`, Quet checks for a newer release on every run: the TUI shows
`quet <version> available — run: quet update`, and `stats`, `export` and `init` print the same
notice on stderr. The check never blocks for more than a moment and is silently skipped on network
errors. With it off (the default) Quet makes no network requests. See [Usage](usage.md#update).

## Creating config files

```sh
quet init             # writes ./quet.yaml and ./flags.yaml
quet init --global    # writes ~/.config/quet/config.yaml and ~/.config/quet/flags.yaml
```

The starter `quet.yaml` holds the built-in defaults with comments; the starter `flags.yaml` defines
`typo`, `ambiguous`, `unnatural`, `synthetic-looking` and `unusual`. Existing files are kept (a
message goes to stderr) unless you pass `-f`/`--force`. See [Usage](usage.md#init).

Inside the TUI, `c` opens the config file in your editor (creating `./quet.yaml` from the starter
when none was loaded) and the settings are reloaded when the editor exits; with no flags file, `f`
offers to create one. See [Reviewing](reviewing.md#editing-config).

## Manual flags file

`flags_file` is resolved relative to the config file's directory. Without it, Quet looks for
`./flags.yaml`, then `flags.yaml` next to the corpus, then `~/.config/quet/flags.yaml`; `--flags-file`
overrides the whole search. The file's format is shown in [Reviewing](reviewing.md#flags).

This repository ships the manual flag taxonomy as `./flags.yaml`, so `quet examples/corpus.jsonl`
starts with the picker populated; `configs/quet.yaml` is a copyable starting-point config, and
`quet init` writes one for you.
