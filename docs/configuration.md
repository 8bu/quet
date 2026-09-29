# Configuration

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

`review.skip_reviewed` controls [auto-advance](reviewing.md#statuses); the `checks` keys are the
thresholds of the [auto checks](checks.md).

## Manual flags file

`flags_file` is resolved relative to the config file's directory. Without it, Quet looks for
`./flags.yaml`, then `flags.yaml` next to the corpus, then `~/.config/quet/flags.yaml`; `--flags-file`
overrides the whole search. The file's format is shown in [Reviewing](reviewing.md#flags).

This repository ships the manual flag taxonomy as `./flags.yaml`, so `quet examples/corpus.jsonl`
starts with the picker populated; `configs/quet.yaml` is a copyable starting-point config.
