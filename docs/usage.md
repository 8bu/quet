# Usage

```sh
quet [corpus.jsonl|corpus.json|corpus.txt|dir] [flags]     # review in the TUI (default)
quet list <corpus> [--filter f] [--limit N] [--json]       # list records
quet show <corpus> <id> [--json]                           # print one record
quet set <corpus> <id>... --status <status> [--json]       # set review status
quet flag <corpus> <id>... [--add a,b] [--remove c]        # add/remove manual flags
quet suggest <corpus> <id>... [--add a,b] [--remove c]     # add/remove suggested flags
quet edit <corpus> <id> (--text s|--text-file f|--revert)  # replace or revert the text
quet undo <corpus> [--json]                                # undo the last change
quet stats <corpus> [--json]                               # print review counts
quet export <corpus> [flags]                               # write a reviewed corpus
quet annotate <queue> --schema <s> --out <l>               # label records against a schema
quet init [--global] [-f|--force]                          # write starter quet.yaml and flags.yaml
quet update [--check]                                      # update to the latest release
quet help                                                  # help (also: quet --help, -h)
```

`list`, `show`, `set`, `flag`, `suggest`, `edit` and `undo` review without the TUI, against the same
sidecar and undo log; they are covered in [Scripting and agents](scripting.md). The review screen
needs an interactive terminal: when standard input or output is not one, `quet [corpus|dir]` exits
with status 1 and points you to those commands.

`quet annotate` labels a queue against a schema in its own screen and writes a labels JSONL file;
it does not use the review sidecar. See [Annotating](annotating.md).

The corpus argument and flags may come in any order, and `--flag value` and `--flag=value` are
both accepted:

```sh
quet examples/corpus.jsonl --filter unreviewed
quet --filter unreviewed examples/corpus.jsonl
quet --filter=unreviewed examples/corpus.jsonl
```

Unknown flags and missing arguments print the usage on stderr and exit with status 2; runtime
failures exit with status 1.

## Review flags

| Flag | Meaning |
| --- | --- |
| `--filter <name>` | Start with a filter: `all`, `unreviewed`, `approved`, `rejected`, `needs_review`, `edited`, `diagnostic[:name]`, `manual[:name]`, `suggested[:name]`, `source:<x>`, `batch:<x>` |
| `--no-skip-reviewed` | Stay on already reviewed records instead of skipping them |
| `--flags-file <path>` | Use this manual flag definitions file instead of the default search |
| `--config <path>` | Use this config file instead of `./quet.yaml` |
| `-h`, `--help` | Show help |
| `--version` | Print the version |

Export has its own flags; see [Export](export.md). `--config` and `--flags-file` also apply to every
other command that takes a corpus, and `--filter` to `list`; `--no-skip-reviewed` applies only to
the review screen.

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
Diagnostics: duplicate 2, too_long 1, empty 1, repeated_chars 1, weird_symbols 1, possible_template 8
Corpus: examples/corpus.jsonl
Sidecar: examples/corpus.jsonl.quet.db
```

Counts come from your corpus and your review progress. The `Diagnostics:` line lists only the
[diagnostics](diagnostics.md) that fired at least once, and is omitted when none did.

`quet stats <corpus> --json` prints the same counts as one JSON object, plus the manual flags
defined in `flags.yaml` with their usage counts; see
[Scripting and agents](scripting.md#stats-object).

## Update

```sh
quet update           # replace the running binary with the latest release
quet update --check   # only report whether a newer version exists
```

`quet update` downloads the `quet_<os>_<arch>` asset of the latest
[GitHub release](https://github.com/8bu/quet/releases), verifies it against the release's
`SHA256SUMS`, and replaces the running binary in place. When `quet` is a symlink, the real file it
points to is updated. Nothing is replaced if the checksum doesn't match. `--check` makes no changes
and only reports whether a newer version exists.

Like `install.sh`, it honours `QUET_RELEASES_URL` to download from a mirror instead of
`https://github.com/8bu/quet/releases`. If the binary's directory isn't writable, re-run with
sufficient permissions (for example `sudo quet update`) or use the install script.

To be told about new releases automatically, set `update.check: true`; see
[Configuration](configuration.md).
