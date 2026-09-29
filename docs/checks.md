# Auto checks

All seven checks are deterministic and explainable, and they never change a status — they only warn.
Thresholds live in the [config file](configuration.md).

| Flag | Fires when |
| --- | --- |
| `duplicate` | The text equals another record's after Unicode NFC, trim, lowercase and whitespace collapsing (`CK Nam 2tr` = `ck   nam 2tr`). Duplicates are grouped and you can jump between matches. |
| `too_long` | Text is longer than `max_chars` runes (default 160). |
| `empty` | Text is blank. |
| `repeated_chars` | One character repeats at least `repeated_char_threshold` times (default 5), e.g. `ckkkkkkkkk Nam 2tr`, `aaaaaaa`, `????????`. |
| `weird_symbols` | An unusually high share of symbols/punctuation (threshold `weird_symbol_ratio`, default 0.35); emoji-heavy text trips it, while money notation such as `+20tr`, `-500k`, `$20`, `1.2tr` is excluded. |
| `missing_amount` | No recognizable money amount is present: `50k`, `2tr`, `1tr2`, `2 triệu`, `2 củ`, `5 lít`, `1.5tr`, `1.2m`, `500000`, `500.000`, `500,000`, `$20`, `20usd`, `200k vnd`. |
| `possible_template` | The same normalized pattern occurs at least `template_min_occurrences` times (default 8). Amounts become `<AMOUNT>` and obvious variable tokens become `<TOKEN>`, e.g. `cho Nam vay 2tr` and `cho Linh vay 3tr` share `cho <TOKEN> vay <AMOUNT>`. |

`examples/corpus.jsonl` trips every check at least once, so it is a quick way to see each warning.
