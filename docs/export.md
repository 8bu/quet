# Export

Five presets, all available from the TUI command palette (`:`) and from the CLI:

| Preset | Records | Review metadata | Default suffix |
| --- | --- | --- | --- |
| `approved` | approved | no | `.approved.jsonl` |
| `rejected` | rejected | no | `.rejected.jsonl` |
| `needs_review` | needs review | no | `.needs_review.jsonl` |
| `all` | every record | yes | `.reviewed.jsonl` |
| `clean` | approved | no | `.clean.jsonl` |

`approved`, `rejected`, `needs_review` and `clean` contain the final text plus the original imported
metadata — no reviewer internals. `clean` is the training-corpus preset and has the same shape as
`approved`; `all` additionally embeds a `quet` object per record.

```sh
quet export examples/corpus.jsonl --status approved -o approved.jsonl
quet export examples/corpus.jsonl --status needs_review -o review.jsonl
quet export examples/corpus.jsonl --status rejected --format txt -o rejected.txt
quet export examples/corpus.jsonl --status all -o reviewed.jsonl
quet export examples/corpus.jsonl                  # writes examples/corpus.approved.jsonl
```

```
$ quet export examples/corpus.jsonl --status all -o reviewed.jsonl
Wrote 21 records to reviewed.jsonl
```

The printed count is the number of records actually written.

## Flags

| Export flag | Meaning |
| --- | --- |
| `--status <name>` | `approved` (default), `rejected`, `needs_review` (also `needs-review`), `all` |
| `-o`, `--output <path>` | Output file; defaults to `<corpus-without-extension><preset suffix>` next to the corpus, with `-1`, `-2`, … appended if that name is taken |
| `--with-review` | Add the `quet` review-metadata object to any preset |
| `--format <name>` | `jsonl` (default) or `txt` (final text, one record per line) |
| `-f`, `--force` | Overwrite an existing output file |

## Safety

Exports are atomic: Quet streams into a temporary file in the target directory, fsyncs, and renames
it over the target. It refuses to write over the source corpus or its sidecar database, and refuses
to overwrite an existing file unless you pass `-f`. Nothing ever writes into the source corpus.

## Review metadata

With `--with-review` each JSONL line keeps its imported metadata and gains one `quet` key:

```json
{"id": "note-001", "text": "bắn thg Nam 2 củ tiền hôm nọ", "source": "claude", "batch": "slang-loan-03", "created_at": "2026-01-02T01:00:00Z", "suggested_flags": ["slang"], "lang": "vi", "quet": {"id": "id:note-001", "status": "approved", "edited": false, "manual_flags": [], "suggested_flags": ["slang"]}}
```

`original_text` appears inside `quet` only for records you edited. [Diagnostics](diagnostics.md)
are informational only and are never exported.
