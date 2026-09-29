# Reviewing

## Statuses

A record has exactly one status:

| Status | Set with |
| --- | --- |
| `unreviewed` | initial state |
| `approved` | `w` |
| `rejected` | `s` |
| `needs_review` | `space` |

After `w`, `s` or `space` the status is persisted immediately, a small confirmation shows, and the
TUI auto-advances to the next unreviewed record (`skip_reviewed: true` by default; use
`--no-skip-reviewed` to visit every record). When nothing unreviewed is left ahead it wraps back to
the first unreviewed record you skipped past; once everything in view is reviewed it simply steps
to the next record, so a second pass keeps moving. The status line says `wrapped to first
unreviewed` or `all reviewed` when that happens. Pressing the status a record already has (say `w`
on an approved record) confirms it and moves on without writing an undo entry.

## Saving and undo

Every change is written straight to the SQLite sidecar `<corpus>.quet.db` (created next to the
corpus), so `q` at any moment is safe and reopening the corpus restores the review session. `z`
undoes the most recent status change, manual flag change or edit — undo history persists across
restarts. Editing never destroys the original: the original text is kept and only the final text is
what you review and export.

## Keybindings

| Key | Action |
| --- | --- |
| `w` | Approve current record |
| `s` | Reject current record |
| `space` | Mark current record needs review |
| `z` | Undo last status, flag or edit change |
| `a`, `h`, `←` | Previous record |
| `d`, `l`, `→` | Next record |
| `j`, `k`, `↑`, `↓` | Move within lists and panels |
| `g` | First record |
| `G` | Last record |
| `e` | Edit the record text inline |
| `ctrl+s` | Save the edit (edit mode) |
| `esc` | Cancel the edit, close a picker, go back |
| `enter` | Select / apply |
| `f` | Manual flag picker (`j`/`k` move, `space` toggle, `enter` apply, `esc` close) |
| `c` | Edit the config file in `$VISUAL`/`$EDITOR` and reload it |
| `/` | Search text, original text and metadata; results in a selectable panel |
| `tab` / `shift+tab` | Change panel |
| `1` `2` `3` | Jump to panel: corpus/status, record, details |
| `:` | Command palette (searchable: approve, export, filters, help, quit, …) |
| `?` | Help overlay |
| `q` | Quit |

Typing is only captured in edit mode; everywhere else a plain letter is a shortcut. `?` lists
everything grouped by Review, Navigation, Record and Application, so you never need this page to
find a key.

## Flags

A record's review data is its status, its manual flags and its suggested flags (plus any edit).
The two kinds of flags are kept separate and never merged blindly:

- **manual flags** — your own labels, defined in `flags.yaml`. They are not hard-coded, and you
  toggle them with `f`.
- **suggested flags** — taken from the imported metadata (`suggested_flags` in the source file) and
  shown separately; they are reserved for external reviewers. No model is called, ever.

[Diagnostics](diagnostics.md) such as `duplicate` or `too_long` are not flags: Quet computes them
locally as hints, they are not part of the review data, and they are never exported.

```yaml
manual:
  slang:
    description: Notable Vietnamese slang or informal wording.

  typo:
    description: Natural typo, abbreviation, or shorthand.

  ambiguous:
    description: Meaning is genuinely ambiguous.

  unnatural:
    description: Unlikely wording for a real user.

  synthetic-looking:
    description: Looks strongly generated or templated.

  unusual:
    description: Valid but uncommon example worth revisiting.
```

Press `f` to tick manual flags; the picker shows each flag's description from `flags.yaml`. Where
Quet looks for that file is described in [Configuration](configuration.md). When no flags file is
found, the status line says so and `f` offers to create a starter one — at `flags_file` if the
config sets it, else `./flags.yaml`.

## Editing config

| Key / palette command | Action |
| --- | --- |
| `c`, `Edit config file` | Open the config file in the editor; creates `./quet.yaml` from the starter when no config was loaded |
| `Edit flags file` | Same for the flags file |
| `Reload config` | Reload config and flags without opening the editor |

The editor is `$VISUAL`, else `$EDITOR`, else `vi`; arguments are allowed (`EDITOR="code -w"`).
When it exits, the settings are reloaded into the open session: new manual flags show up in the
picker, diagnostic thresholds apply and diagnostics are recomputed, and `skip_reviewed` changes
only if its value in the file changed (so `--no-skip-reviewed` survives an unrelated edit). If the
config has an error, the previous settings stay in effect and the error is shown in the status
line.
