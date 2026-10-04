# Web sync

`quet web` connects Quet to a [quet-web](https://github.com/8bu/quet-web) server that you deploy:
you publish a queue, its schema and optional
proposals as a *project*, collaborators label it in the browser, and you pull their labels back into
ordinary Quet labels files. The annotation screen has the same features behind the `w` key,
including a compare mode for choosing each record's final label among all collaborators.

Nothing here changes Quet for people who never run `quet web` or press `w`: there is no network
access and no extra file.

```sh
quet web remote add origin https://quet.example.com
quet web login
quet web push queue.jsonl --schema schema.yaml --project expenses
quet web list
quet web pull --project expenses --all --out-dir pulled/
```

## Setup

The admin API of quet-web sits behind Cloudflare Access. `quet web login` logs you in through the
browser, the same way you log in to the admin dashboard. You copy no token by hand.

1. The Access admin turns on **Managed OAuth** for the Access application that protects the
   quet-web admin API, and allows **loopback clients**: apps that listen on `127.0.0.1` for the
   login callback. This is done once, in the Cloudflare Zero Trust dashboard.
2. Register the server and log in:

   ```sh
   quet web remote add origin https://quet.example.com
   quet web login
   ```

   `login` opens your browser and prints the same URL, in case the browser does not open. You log in
   with Cloudflare Access. Quet waits up to five minutes for you to finish, then saves the login and
   prints `logged in to origin as <identity>`. Use `quet web login --no-browser` to print the URL
   only, and open it yourself.

Quet keeps the login in `remotes.yaml` and refreshes it without asking you, for as long as the
Access grant lasts. When the grant ends, a command stops with the error
"session expired: run quet web login". Run `quet web login` again.

`quet web logout [<name>]` removes the stored login (and any stored service token) of a remote and
prints `logged out of <name>`.

### Service tokens

A **service token** is for CI and other places without a browser. Use the environment variables
below, or store a token with `--service-token`:

1. In the Cloudflare Zero Trust dashboard create a service token (Access → Service Auth → Service
   Tokens → Create Service Token) and copy its **Client ID** (ends in `.access`) and **Client
   Secret**. The secret is shown once.
2. Make sure the Access policy that protects the quet-web admin application has a **Service Auth**
   rule that includes this token.
3. Store the token:

   ```sh
   quet web login --service-token   # prompts: Client ID (echoed), Client Secret (not echoed)
   ```

   `login --service-token` saves the token, then asks the server who it sees and prints it. When
   stdin is not a terminal, it reads the two values as two lines (client id, then secret), so it can
   be scripted:

   ```sh
   printf '%s\n%s\n' "$CLIENT_ID" "$CLIENT_SECRET" | quet web login --service-token
   ```

A remote has one login at a time. Storing a browser login removes its service token, and storing a
service token removes its browser login. For one request, Quet uses the first of these that exists:
the service token from the environment variables, the stored browser login, the stored service
token.

### Remotes

Remotes work like git remotes and are stored in `~/.config/quet/remotes.yaml` (under
`$XDG_CONFIG_HOME/quet/` when that is set), written atomically with mode `0600` because the file
holds secrets. A remote has either a browser login (`oauth`) or a service token:

```yaml
default: origin
remotes:
  origin:
    url: https://quet.example.com
    oauth:
      client_id: <registered by login>
      token_endpoint: https://quet.example.com/token
      access_token: <short lived>
      refresh_token: <rotated on each refresh>
      expires_at: 2026-10-04T10:00:00Z
  ci:
    url: https://quet.example.com
    client_id: xxxx.access
    client_secret: yyyy
```

| Command | Effect |
| --- | --- |
| `quet web remote add <name> <url>` | Add a remote; the first one becomes the default. The name is letters, digits, `.`, `_`, `-` (at most 32 characters). The URL is `http(s)://host[:port]` with no path or query. |
| `quet web remote list` | One row per remote: `*` marks the default, then name, URL and the stored login: `oauth`, `service token` or `no`. Secrets are never printed, here or anywhere else. |
| `quet web remote remove <name>` | Remove a remote; if it was the default there is no default afterwards. |
| `quet web remote default <name>` | Make a remote the default. |
| `quet web login [<name>]` | Log in to `<name>` (default: the default remote) through the browser and check it. `--no-browser` prints the URL only. `--service-token` stores a service token instead. |
| `quet web logout [<name>]` | Remove the stored login and service token of `<name>`. |

Every other command takes `--remote <name>`; without it the default remote is used. With no remote
configured, Quet uses `QUET_WEB_URL`, and fails with a hint when that is unset too.

### Environment variables

These override the picked remote's service token, whatever it is, and are what you want in CI:

| Variable | Overrides |
| --- | --- |
| `QUET_WEB_URL` | The server URL; also used on its own when no remote exists |
| `QUET_ACCESS_CLIENT_ID` | The Client ID, sent as `CF-Access-Client-Id` |
| `QUET_ACCESS_CLIENT_SECRET` | The Client Secret, sent as `CF-Access-Client-Secret` |

A server on `localhost` started with the development admin bypass needs no credentials; a remote
without them is allowed, and `login` says so. When Cloudflare Access rejects the login (HTTP 302,
401 or 403) the error says so and suggests `quet web login`.

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
| Remote & project… | `r` | Pick an existing remote or add one, saved to `remotes.yaml`; then pick a project from the server or `new project: <slug>` (prefilled from the queue file name). The choice is saved in the sidecar. The add form asks for the name and URL, then how to log in. **Browser** is the default: `enter` opens your browser, the status shows the login URL, `esc` cancels, and the remote is saved when you finish. **Service token** shows the Client ID and Client Secret (masked) fields. Use `space` or `←`/`→` on the *Log in with* row to switch. |
| Publish | `p` | A confirm screen (project, counts), then the same push as `quet web push` with the session's queue, schema and proposals. `enter` or `y` confirms, `esc` cancels. The status line shows the result. Not available in a re-check session opened without a queue file. |
| Compare collaborators | `c` | Compare mode, below. |
| Log in again | `l` | Run the browser login again for the linked remote, for example after `session expired`. The status shows the login URL and `esc` cancels. |

Move with `j`/`k` (or the arrows) and `enter`, or press the item's key; `esc` closes the menu.
Publish and Compare without a link open the Remote & project form first. When a call fails because
of the login, the status says to press `w`, then choose Log in again.

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
in any output. If `login --service-token` saves a token the server then rejects, it says so and exits
1, and the token stays stored: run `login --service-token` again with the right one.
