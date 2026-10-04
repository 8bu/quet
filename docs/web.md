# Web sync

`quet web` connects Quet to a **quet-web** server: you publish a queue, its schema and optional
proposals as a *project*, collaborators label it in the browser, and you pull their labels back into
ordinary Quet labels files. The annotation screen has the same features behind the `w` key,
including a compare mode for choosing each record's final label among all collaborators.

Nothing here changes Quet for people who never run `quet web` or press `w`: there is no network
access and no extra file.

```sh
quet web remote add origin https://quet.8bu.dev
quet web login
quet web push queue.jsonl --schema schema.yaml --project expenses
quet web list
quet web pull --project expenses --all --out-dir pulled/
```

## Setup

The admin API of quet-web sits behind Cloudflare Access, which lets Quet in with a **service token**.

1. In the Cloudflare Zero Trust dashboard create a service token (Access → Service Auth → Service
   Tokens → Create Service Token) and copy its **Client ID** (ends in `.access`) and **Client
   Secret**. The secret is shown once.
2. Make sure the Access policy that protects the quet-web admin application has a **Service Auth**
   rule that includes this token.
3. Register the server and store the token:

   ```sh
   quet web remote add origin https://quet.8bu.dev
   quet web login            # prompts: Client ID (echoed), Client Secret (not echoed)
   ```

   `login` saves the token, then asks the server who it sees and prints it
   (`logged in to origin as <identity>`). When stdin is not a terminal, `login` reads the two values
   as two lines (client id, then secret), so it can be scripted:

   ```sh
   printf '%s\n%s\n' "$CLIENT_ID" "$CLIENT_SECRET" | quet web login
   ```

### Remotes

Remotes work like git remotes and are stored in `~/.config/quet/remotes.yaml` (under
`$XDG_CONFIG_HOME/quet/` when that is set), written atomically with mode `0600` because the file
holds secrets:

```yaml
default: origin
remotes:
  origin:
    url: https://quet.8bu.dev
    client_id: xxxx.access
    client_secret: yyyy
```

| Command | Effect |
| --- | --- |
| `quet web remote add <name> <url>` | Add a remote; the first one becomes the default. The name is letters, digits, `.`, `_`, `-` (at most 32 characters). The URL is `http(s)://host[:port]` with no path or query. |
| `quet web remote list` | One row per remote: `*` marks the default, then name, URL and whether credentials are stored (`yes`/`no`). Secrets are never printed, here or anywhere else. |
| `quet web remote remove <name>` | Remove a remote; if it was the default there is no default afterwards. |
| `quet web remote default <name>` | Make a remote the default. |
| `quet web login [<name>]` | Store the service token of `<name>` (default: the default remote) and check it. |

Every other command takes `--remote <name>`; without it the default remote is used. With no remote
configured at all, Quet talks to `https://quet.8bu.dev`.

### Environment variables

These override the picked remote, whatever it is, and are what you want in CI:

| Variable | Overrides |
| --- | --- |
| `QUET_WEB_URL` | The server URL (default `https://quet.8bu.dev` when no remote exists) |
| `QUET_ACCESS_CLIENT_ID` | The Client ID, sent as `CF-Access-Client-Id` |
| `QUET_ACCESS_CLIENT_SECRET` | The Client Secret, sent as `CF-Access-Client-Secret` |

A server on `localhost` started with the development admin bypass needs no credentials; a remote
without them is allowed. When Cloudflare Access rejects the token (HTTP 302, 401 or 403) the error
says so and suggests `quet web login`.

## Push

```sh
quet web push <queue.jsonl> --schema <schema.yaml> --project <slug> \
    [--proposals <proposals.jsonl>] [--name <name>] [--remote <name>]
```

The queue, schema and proposals are validated locally first (the same loaders as `quet annotate`),
so a bad file never reaches the server. The slug is lowercase letters, digits and `-`, at most 63
characters. The summary is one line:

```
pushed project expenses: created, 120 items (120 new), 40 proposals
```

Pushing again **updates** the project; it never starts over:

- **Upsert.** The project, its schema and its records are created or updated. Records are matched by
  `id`; their position is their line in the queue. Records missing from a later push are kept on the
  server, never deleted. The project name is the slug on creation; `--name` sets it, and an
  existing project keeps its name when `--name` is left out.
- **Text changes are refused on labelled items.** Changing the `text` of a record that any
  collaborator has labelled is an error and nothing from that batch is written; records without
  labels may change text freely.
- **Schema changes are checked against existing labels.** The server re-validates every stored
  label against the new schema and each record's text. If any would become invalid the push fails
  and lists the first of them (`collaborator id: error`), and nothing changes.
- **Proposals are replaced.** With `--proposals`, all proposals on the server are deleted and the
  file's are uploaded, so the server mirrors the file. Without `--proposals` the server's proposals
  are left alone. Proposal ids that are not records of the project are ignored and reported on
  stderr, like `quet annotate --proposals` does for ids outside the queue.

## List

```sh
quet web list [--remote <name>] [--json]
```

Prints each project (slug, name, record and proposal counts) with one line per collaborator:
labelled, complete, uncertain and skipped counts, the last label time (UTC) and `[disabled]` for a
disabled account. `--json` prints one JSON array of projects instead, each with its
`collaborators` and without the schema, which is empty (`[]`) when the server has no projects.

## Pull

```sh
quet web pull --project <slug> (--user <name> | --all) [--remote <name>] \
    (--out <labels.jsonl> [--force] | --labels <labels.jsonl> | --out-dir <dir> [--force])
```

Pull fetches the project's schema and records and the labels of one collaborator or of everyone.
The project's schema is parsed with Quet's own schema parser and **every pulled label is validated**
against it and against the record text before anything is written.

| Mode | Result |
| --- | --- |
| `--user <u> --out <file>` | A fresh labels file with that collaborator's labels, in the project's record order. An existing file is refused unless `--force`. |
| `--user <u> --labels <file>` | Merge into an existing labels file (it must exist), the way [re-check](annotating.md#re-check-subset) does: a pulled label replaces the line with the same id, a new id is appended, and **every other line stays byte for byte**, including labels of ids that are not in the project. Labels equal to what the file already has are left alone. The file is refused if another program changed it while Quet was working. `--force` does not apply. |
| `--all --out-dir <dir>` | One `<dir>/<username>.jsonl` per collaborator with at least one label (the directory is created). If any target file exists the whole pull is refused unless `--force`. |

Each action prints one summary line, such as
`pulled 12 labels of alice from expenses into alice.jsonl` or
`merged 12 labels of alice from expenses into labels.jsonl (3 added, 2 replaced, 7 unchanged)`.

`--user` names a collaborator of the project; an unknown name with no labels is an error that lists
the collaborators.

**Invalid labels write nothing.** If any pulled label is invalid (not valid against the schema or
the text, undecodable, or for a record that is not in the project), Quet prints up to 50
`collaborator id: error` lines on stderr, then how many there were, and exits with status 1 without
writing or changing any file.

`--all` always writes files named after collaborators; a username that is not a safe file name
(contains `/` or `\`, or is `.` or `..`) is refused.

## The sidecar

`quet web pull --labels <labels.jsonl>` may leave out `--project` and `--remote` when the labels
file has a sidecar, `<labels.jsonl>.quet-web.yaml`:

```yaml
remote: origin
project: expenses-2026
```

The sidecar remembers which remote and project a labels file belongs to. It holds **no secrets**
and is written with mode `0644`, so it is safe to commit next to the labels. Flags win over the
sidecar. The annotation screen writes it when you link a project from the web menu; the CLI only
reads it: `push` and `pull` never create or change it. Without a sidecar and without `--project`,
`pull --labels` stops with an error naming the sidecar path it looked for.

## In the annotation screen

In `quet annotate`, `w` opens the **Web** menu (in span mode `w` still moves by word). The menu
shows the current link (`remote/project`, or `not linked`) and offers:

| Item | Key | What it does |
| --- | --- | --- |
| Remote & project… | `r` | Pick an existing remote or add one (name, URL, Client ID, Client Secret, masked), saved to `remotes.yaml`; then pick a project from the server or `new project: <slug>` (prefilled from the queue file name). The choice is saved in the sidecar. |
| Publish | `p` | A confirm screen (project, counts), then the same push as `quet web push` with the session's queue, schema and proposals. `enter` or `y` confirms, `esc` cancels. The status line shows the result. Not available in a re-check session opened without a queue file. |
| Compare collaborators | `c` | Compare mode, below. |

Move with `j`/`k` (or the arrows) and `enter`, or press the item's key; `esc` closes the menu.
Publish and Compare without a link open the Remote & project form first.

### Compare mode

Compare pulls **all** collaborators' labels in the background (`pulling…` in the status line) and
shows each record with its candidates:

- `Local` is your saved label (or none), then every collaborator, numbered `1`-`9` in username
  order. Candidates with equal labels share a row (`alice, bob`).
- Spans are highlighted like in the record view. A remote label that is invalid against the schema
  is shown with `⚠` and cannot be picked.
- Remote labels for ids that are not in your local queue are ignored, and the count is reported.

| Key | Action |
| --- | --- |
| `1`-`9` | Pick that candidate as the record's final label (saved like a mark; `z` undoes it) |
| `[` / `]` | Previous / next **disagreement** |
| `a` / `d` | Previous / next record that has any remote label (`h`/`l` and the arrow keys work too) |
| `A` | Bulk-accept all unanimous records: shows a preview (count and the first rows: id, type, spans) |
| `enter` | In the preview: apply (one write, one undo entry, so `z` reverts the whole batch) |
| `esc` | In the preview: cancel. Otherwise: back to the annotation screen |
| `z` | Undo |
| `?` | Help |

A record is a **disagreement** when it has at least one remote label and either the remote labels
differ among themselves, or your local label is set and differs. It is **unanimous** when it has at
least one remote label, all remote labels are equal and your local label is unset; records whose
local label already equals the remote one are *agreed* and need nothing.

Compare mode never writes anything on its own: only a pick or an applied `A` saves, through the
same path as marking a record (null label statuses, default span statuses and validation apply).

## Exit codes and errors

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Failure: network or server error, local files that do not validate, invalid pulled labels, an existing output file without `--force` |
| `2` | Command line error: unknown subcommand, a missing or contradictory flag. The usage goes to stderr |

Errors are printed as `quet: <message>`; server errors include the HTTP status. Secrets never appear
in any output. If `login` saves a token the server then rejects, it says so and exits 1, and the
token stays stored: run `login` again with the right one.
