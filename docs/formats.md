# Input formats

The format is chosen by extension. Malformed input is reported with its line/position; records are
never silently dropped. The text field is the first of `text`, `content`, `note`, `input`, `prompt`,
`sentence`, `body` found in the file.

**`.jsonl`** — one JSON object per line. Records keep their stable id (`id:<value>`); records without
an id get a stable content hash, so review progress survives reordering and re-runs.

```json
{"id": "note-001", "text": "bắn thg Nam 2 củ tiền hôm nọ", "source": "claude", "batch": "slang-loan-03", "created_at": "2026-01-02T01:00:00Z", "suggested_flags": ["slang"], "lang": "vi"}
{"id": "note-002", "text": "CK Nam 2tr", "source": "claude", "batch": "slang-loan-03", "suggested_flags": [], "lang": "vi"}
```

**`.json`** — an array of records, or an object wrapping one under `records`, `data`, `items` or
`corpus`:

```json
{
  "corpus": "vi-finance-notes",
  "version": 1,
  "records": [
    {"text": "bắn thg Nam 2 củ tiền hôm nọ", "source": "claude", "batch": "slang-loan-03"}
  ]
}
```

**`.txt`** — one record per non-empty line, no metadata:

```
bắn thg Nam 2 củ tiền hôm nọ
CK Nam 2tr
```

## Metadata preservation

Every key other than the text field is kept in its original order and written back out on
[export](export.md), so `source`, `batch`, `created_at`, `lang`, `suggested_flags` and any custom
fields survive the round trip. The source corpus file is never modified — it is only read.
