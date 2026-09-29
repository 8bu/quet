# Diagnostics

Diagnostics are deterministic, explainable hints that Quet computes locally when a corpus loads, and
again after you edit a record or reload the config. They are informational only: they are not
flags, not part of the review state, cannot be dismissed, never change a status, and are never
exported. Thresholds live under `checks:` in the [config file](configuration.md).

| Diagnostic | Fires when |
| --- | --- |
| `duplicate` | The text equals another record's after Unicode NFC, trim, lowercase and whitespace collapsing (`CK Nam 2tr` = `ck   nam 2tr`). Duplicates are grouped and you can jump between matches. |
| `too_long` | Text is longer than `max_chars` runes (default 160). |
| `empty` | Text is blank. |
| `repeated_chars` | One character repeats at least `repeated_char_threshold` times (default 5), e.g. `ckkkkkkkkk Nam 2tr`, `aaaaaaa`, `????????`. |
| `weird_symbols` | An unusually high share of symbols/punctuation (threshold `weird_symbol_ratio`, default 0.35); emoji-heavy text trips it, while money notation such as `+20tr`, `-500k`, `$20`, `1.2tr` is excluded. |
| `possible_template` | The same normalized pattern occurs at least `template_min_occurrences` times (default 8). Amounts become `<AMOUNT>` and obvious variable tokens become `<TOKEN>`, e.g. `cho Nam vay 2tr` and `cho Linh vay 3tr` share `cho <TOKEN> vay <AMOUNT>`. |

In the TUI the details panel shows a compact `diagnostics:` line listing a record's diagnostics,
only when it has any. To review by diagnostic, start with `--filter diagnostic` (any diagnostic) or
`--filter diagnostic:<name>`, or pick a `diagnostic:<name>` row in the corpus panel; rows appear
only for diagnostics present in the corpus.

`examples/corpus.jsonl` trips every diagnostic at least once, so it is a quick way to see each one.
