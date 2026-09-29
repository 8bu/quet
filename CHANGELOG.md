# Changelog

All notable changes to Quet are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Running `quet` without a corpus, or with a directory, opens a file browser that lists folders and
  `.jsonl`/`.json`/`.txt` files and marks files that already have review progress. Opening a file
  runs a gate (it must parse and hold at least one record with text) before any sidecar is
  created; rejected files show the reason and stay unopened.

### Changed

- The record panel now shrinks to fit the record's text, so the details panel sits directly
  below it instead of at the bottom of the screen. It still grows up to 70% of the column for
  long records, and edit mode keeps the full height.

### Fixed

- Review actions no longer silently stay on the same record when no unreviewed record is left
  ahead. Quet now wraps to the first unreviewed record you skipped past, or, once everything is
  reviewed, steps to the next record. Re-pressing a record's existing status also moves on.

## [0.1.0] - 2026-09-29

First release.

### Added

- Corpus loading for `.jsonl`, `.json` (array, or an object wrapping one) and `.txt`, with the
  text field detected from `text`, `content`, `note`, `input`, `prompt`, `sentence` or `body`.
- Full metadata preservation: every imported key survives a review round trip and is written back
  in its original order on export. The source corpus is only ever read.
- Stable record IDs (an explicit `id` field, otherwise a content hash, with a `#2`, `#3`… suffix
  for duplicates) so review progress survives reordering and re-runs.
- Interactive TUI: three focusable panels, contextual footer hints, a grouped `?` help overlay, a
  searchable `:` command palette, and `1`/`2`/`3` panel jumps.
- Review statuses (`unreviewed`, `approved`, `rejected`, `needs_review`) persisted immediately on
  change, with automatic advance to the next unresolved record.
- Three separate flag sources: `auto_flags`, `suggested_flags` (read from imported metadata) and
  `manual_flags` (defined in an external `flags.yaml`, never hard-coded).
- Seven deterministic, explainable auto checks: `duplicate`, `too_long`, `empty`,
  `repeated_chars`, `weird_symbols`, `missing_amount` and `possible_template`. Warnings only —
  checks never change a status.
- Inline editing that preserves the original text alongside the edited text.
- Search across the final text, the original text and imported metadata, with a selectable result
  panel.
- Eleven filter kinds: all, unreviewed, approved, rejected, needs review, edited, auto flag,
  manual flag, suggested flag, source and batch.
- Undo (`z`) for status changes, manual flag changes and edits, persisted so it survives a restart.
- SQLite sidecar storage (`<corpus>.quet.db`) in WAL mode with full synchronous writes, keyed by
  stable record ID, with an `annotations` column reserved for future structured annotations.
- Five export presets (`approved`, `rejected`, `needs_review`, `all`, `clean`), available from the
  CLI and the command palette, written atomically via a temporary file and rename. Exports refuse
  to overwrite the source corpus, its sidecar, or an existing file without `--force`.
- Configuration from `./quet.yaml` or `~/.config/quet/config.yaml`, merged over sensible defaults.
- CLI commands: interactive review, `stats`, `export`, and `--version`.
- Verified performance on 100,000 short records: under a second to open, ~16 ms per review action.

[Unreleased]: https://github.com/8bu/quet/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/8bu/quet/releases/tag/v0.1.0
