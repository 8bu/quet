# Usage

```sh
quet [corpus.jsonl|corpus.json|corpus.txt|dir] [flags]  # review in the TUI (default)
quet stats <corpus> [flags]                            # print review counts
quet export <corpus> [flags]                           # write a reviewed corpus
quet init [--global] [-f|--force]                      # write starter quet.yaml and flags.yaml
quet help                                              # help (also: quet --help, -h)
```

The corpus argument and flags may come in any order, and `--flag value` and `--flag=value` are
both accepted:

```sh
quet examples/corpus.jsonl --filter unreviewed
quet --filter unreviewed examples/corpus.jsonl
quet --filter=unreviewed examples/corpus.jsonl
```

Unknown flags and missing arguments print the usage on stderr and exit with status 2.

## Review flags

| Flag | Meaning |
| --- | --- |
| `--filter <name>` | Start with a filter: `all`, `unreviewed`, `approved`, `rejected`, `needs_review`, `edited`, `auto[:name]`, `manual[:name]`, `suggested[:name]`, `source:<x>`, `batch:<x>` |
| `--no-skip-reviewed` | Stay on already reviewed records instead of skipping them |
| `--flags-file <path>` | Use this manual flag definitions file instead of the default search |
| `--config <path>` | Use this config file instead of `./quet.yaml` |
| `-h`, `--help` | Show help |
| `--version` | Print the version |

Export has its own flags; see [Export](export.md).

## Opening a file from the browser

Run `quet` with no corpus (or with a directory) to start on a file browser. It lists folders and
`.jsonl`, `.json` and `.txt` files only; files that already have review progress are marked
`● in review`.

| Key | Action |
| --- | --- |
| `w` / `s`, `↑` / `↓`, `k` / `j` | move |
| `enter`, `d`, `→`, `l` | open folder or file |
| `a`, `backspace`, `←`, `h` | go up a folder |
| `g` / `G` | first / last entry |
| `.` | show or hide hidden files |
| `q`, `esc` | quit |

Opening a file runs a gate first: the file must parse, contain at least one record, and at least one
record must have text in one of the recognised text fields. A file that fails stays unopened, the
reason is shown under the listing, and no sidecar is created for it. A file that passes opens
straight into the review screen. Review flags such as `--filter` apply to the file you open.

## Init

`quet init` writes a starter `./quet.yaml` (the built-in defaults, commented) and `./flags.yaml`
(the manual flags `typo`, `ambiguous`, `unnatural`, `synthetic-looking`, `unusual`).

| Flag | Meaning |
| --- | --- |
| `--global` | Write `~/.config/quet/config.yaml` and `~/.config/quet/flags.yaml` instead (`$XDG_CONFIG_HOME` is respected) |
| `-f`, `--force` | Overwrite existing files |

Without `--force` an existing file is kept and a message is printed on stderr. See
[Configuration](configuration.md#creating-config-files).

## Stats

```sh
$ quet stats examples/corpus.jsonl
Total:            21
Approved:          0
Rejected:          0
Needs review:      0
Unreviewed:       21
Edited:            0
Auto flags: duplicate 2, too_long 1, empty 1, repeated_chars 1, weird_symbols 1, missing_amount 1, possible_template 8
Corpus: examples/corpus.jsonl
Sidecar: examples/corpus.jsonl.quet.db
```

Counts come from your corpus and your review progress. The `Auto flags:` line lists only the
[checks](checks.md) that fired at least once, and is omitted when none did.
