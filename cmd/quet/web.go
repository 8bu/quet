package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/web"
)

// maxListedInvalid is how many invalid pulled labels the stderr report names.
const maxListedInvalid = 50

// loginInput, loginIsTerminal, loginOpenBrowser and readSecret are how `quet web login` reads the Cloudflare
// Access service token and opens the browser. Tests replace them.
var (
	// loginInput is the stdin of a non-terminal login: the client id and the secret, one per line.
	loginInput io.Reader = os.Stdin
	// loginIsTerminal reports whether stdin is a terminal, so the secret can be read without echo.
	loginIsTerminal = func() bool { return isatty.IsTerminal(os.Stdin.Fd()) }
	// loginOpenBrowser opens the authorization URL of a browser login (nil: the platform default browser).
	loginOpenBrowser func(url string) error
	// readSecret reads a line from the terminal without echoing it.
	readSecret = func() (string, error) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		return string(b), err
	}
)

// revokeTimeout bounds the revocation request of `quet web logout`.
const revokeTimeout = 5 * time.Second

// runWeb runs one `quet web` subcommand and returns the process exit code: 0 success, 1 failure.
// Failures are reported on stderr; secrets are never printed.
func runWeb(cmd command, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch cmd.webAction {
	case "remote":
		err = webRemote(cmd, stdout)
	case "login":
		err = webLogin(ctx, cmd, stdout, stderr)
	case "logout":
		err = webLogout(ctx, cmd, stdout, stderr)
	case "push":
		err = webPush(ctx, cmd, stdout, stderr)
	case "list":
		err = webList(ctx, cmd, stdout)
	case "pull":
		err = webPull(ctx, cmd, stdout, stderr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// webRemote runs `quet web remote add|list|remove|default`, which only touch remotes.yaml.
func webRemote(cmd command, stdout io.Writer) error {
	remotes, err := web.LoadRemotes()
	if err != nil {
		return err
	}
	switch cmd.webSub {
	case "list":
		printRemotes(stdout, remotes)
		return nil
	case "add":
		name, url := cmd.webArgs[0], cmd.webArgs[1]
		if err := remotes.Add(web.Remote{Name: name, URL: url}); err != nil {
			return err
		}
		if err := remotes.Save(); err != nil {
			return err
		}
		note := ""
		if remotes.Default == name {
			note = " (default)"
		}
		fmt.Fprintf(stdout, "added remote %s%s\n", name, note)
	case "remove":
		if err := remotes.Remove(cmd.webArgs[0]); err != nil {
			return err
		}
		if err := remotes.Save(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed remote %s\n", cmd.webArgs[0])
	case "default":
		if _, ok := remotes.Get(cmd.webArgs[0]); !ok {
			return fmt.Errorf("unknown remote %q", cmd.webArgs[0])
		}
		remotes.Default = cmd.webArgs[0]
		if err := remotes.Save(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "default remote is %s\n", cmd.webArgs[0])
	}
	return nil
}

// printRemotes lists the remotes: a * for the default, the name, the URL and the kind of stored
// credentials (oauth, service token or no). No secret is ever printed.
func printRemotes(stdout io.Writer, remotes *web.Remotes) {
	if len(remotes.List) == 0 {
		fmt.Fprintln(stdout, "no remotes (add one with `quet web remote add <name> <url>`)")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tNAME\tURL\tCREDENTIALS")
	for _, r := range remotes.List {
		mark, creds := "", "no"
		if r.Name == remotes.Default {
			mark = "*"
		}
		switch {
		case r.OAuth != nil:
			creds = "oauth"
		case r.ClientID != "" && r.ClientSecret != "":
			creds = "service token"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, r.Name, r.URL, creds)
	}
	tw.Flush()
}

// pickRemote returns the stored remote named by the first argument of cmd, else the default one. verb
// completes the error when there is none ("log in to", "log out of"). The stored URL is used, ignoring
// QUET_WEB_URL.
func pickRemote(remotes *web.Remotes, cmd command, verb string) (web.Remote, error) {
	name := remotes.Default
	if len(cmd.webArgs) > 0 {
		name = cmd.webArgs[0]
	}
	if name == "" {
		return web.Remote{}, fmt.Errorf("no remote to %s: add one with `quet web remote add <name> <url>` or name it", verb)
	}
	rem, ok := remotes.Get(name)
	if !ok {
		return web.Remote{}, fmt.Errorf("unknown remote %q (add it with `quet web remote add %s <url>`)", name, name)
	}
	return rem, nil
}

// webLogin runs `quet web login [<name>]`. By default it logs in with the browser (Cloudflare Access
// Managed OAuth), stores the token in the remote's entry and prints who the server sees; --no-browser
// only prints the URL to open. With --service-token it reads the Client ID (echoed) and Client Secret
// (not echoed) of a Cloudflare Access service token instead (two lines when stdin is not a terminal).
// Either way the other kind of credential of the remote is removed. The remote is the one named, else
// the default; the stored URL is used, ignoring QUET_WEB_URL.
func webLogin(ctx context.Context, cmd command, stdout, stderr io.Writer) error {
	remotes, err := web.LoadRemotes()
	if err != nil {
		return err
	}
	rem, err := pickRemote(remotes, cmd, "log in to")
	if err != nil {
		return err
	}
	if cmd.serviceToken {
		return serviceTokenLogin(ctx, remotes, rem, stdout, stderr)
	}
	opt := web.LoginOptions{Notify: func(url string) {
		if cmd.noBrowser {
			fmt.Fprintf(stdout, "Open this URL in your browser to log in:\n%s\n", url)
		} else {
			fmt.Fprintf(stdout, "Opening the browser to log in…\n%s\n", url)
		}
	}}
	opt.OpenBrowser = loginOpenBrowser
	if cmd.noBrowser {
		opt.OpenBrowser = func(string) error { return nil }
	}
	tok, err := web.Login(ctx, rem, opt)
	if errors.Is(err, web.ErrNoAuthNeeded) {
		fmt.Fprintf(stdout, "%s needs no login: the server accepts requests without credentials\n", rem.Name)
		return nil
	}
	if err != nil {
		return err
	}
	if err := web.SaveOAuth(rem.Name, tok); err != nil {
		return err
	}
	rem.SetOAuth(tok)
	client, err := web.NewClient(rem, web.WithTokenSaver(web.TokenSaver(rem.Name)))
	if err != nil {
		return err
	}
	identity, err := client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("logged in, but the server did not accept the login: %w", err)
	}
	fmt.Fprintf(stdout, "logged in to %s as %s\n", rem.Name, identity)
	return nil
}

// serviceTokenLogin is `quet web login --service-token`: it reads a Cloudflare Access service token,
// stores it in the remote's entry (dropping any browser login), then asks the server who it sees.
func serviceTokenLogin(ctx context.Context, remotes *web.Remotes, rem web.Remote, stdout, stderr io.Writer) error {
	id, secret, err := readCredentials(stderr)
	if err != nil {
		return err
	}
	rem.SetServiceToken(id, secret)
	remotes.Set(rem)
	if err := remotes.Save(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "saved credentials for remote %s\n", rem.Name)
	client, err := web.NewClient(rem)
	if err != nil {
		return err
	}
	identity, err := client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("credentials saved, but the server did not accept them: %w", err)
	}
	fmt.Fprintf(stdout, "logged in to %s as %s\n", rem.Name, identity)
	return nil
}

// webLogout runs `quet web logout [<name>]`: it revokes the stored browser login at the authorization
// server (best effort: a failure is only a warning on stderr) and removes the stored login and service
// token of the remote (the remote itself stays).
func webLogout(ctx context.Context, cmd command, stdout, stderr io.Writer) error {
	remotes, err := web.LoadRemotes()
	if err != nil {
		return err
	}
	rem, err := pickRemote(remotes, cmd, "log out of")
	if err != nil {
		return err
	}
	if rem.OAuth != nil {
		rctx, cancel := context.WithTimeout(ctx, revokeTimeout)
		if err := rem.OAuth.Revoke(rctx); err != nil {
			fmt.Fprintf(stderr, "quet: warning: %v\n", err)
		}
		cancel()
	}
	rem.ClearCredentials()
	remotes.Set(rem)
	if err := remotes.Save(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "logged out of %s\n", rem.Name)
	return nil
}

// readCredentials reads the client id and client secret for `quet web login`. On a terminal it
// prompts on stderr and reads the secret without echo; otherwise it reads two lines from stdin.
func readCredentials(stderr io.Writer) (id, secret string, err error) {
	if loginIsTerminal() {
		in := bufio.NewReader(os.Stdin)
		fmt.Fprint(stderr, "Client ID: ")
		if id, err = in.ReadString('\n'); err != nil {
			return "", "", fmt.Errorf("read client id: %w", err)
		}
		fmt.Fprint(stderr, "Client Secret: ")
		secret, err = readSecret()
		fmt.Fprintln(stderr)
		if err != nil {
			return "", "", fmt.Errorf("read client secret: %w", err)
		}
	} else {
		in := bufio.NewReader(loginInput)
		id, err = in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || id == "") {
			return "", "", errors.New("expected the client id and the client secret on two lines of stdin")
		}
		secret, err = in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || secret == "") {
			return "", "", errors.New("expected the client id and the client secret on two lines of stdin")
		}
	}
	id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
	if id == "" || secret == "" {
		return "", "", errors.New("the client id and the client secret must not be empty")
	}
	return id, secret, nil
}

// webClient resolves the remote to use (see web.Remotes.Resolve: --remote, the default remote or the
// environment) and returns a client for it. A refreshed browser login is saved back to remotes.yaml.
func webClient(name string) (*web.Client, error) {
	remotes, err := web.LoadRemotes()
	if err != nil {
		return nil, err
	}
	rem, err := remotes.Resolve(name)
	if err != nil {
		return nil, err
	}
	var opts []web.ClientOption
	if rem.OAuth != nil {
		opts = append(opts, web.WithTokenSaver(web.TokenSaver(rem.Name)))
	}
	return web.NewClient(rem, opts...)
}

// webPush runs `quet web push`: it validates the queue, schema and proposals locally, then
// publishes the project, its records and (with --proposals) its proposals, printing one summary line.
func webPush(ctx context.Context, cmd command, stdout, stderr io.Writer) error {
	client, err := webClient(cmd.remote)
	if err != nil {
		return err
	}
	res, err := web.Push(ctx, client, web.PushInput{
		QueuePath:     cmd.webArgs[0],
		SchemaPath:    cmd.schemaPath,
		ProposalsPath: cmd.proposalsPath,
		Project:       cmd.project,
		Name:          cmd.projectName,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, pushSummary(cmd.project, cmd.hasProposals, res))
	if len(res.IgnoredProposals) > 0 {
		fmt.Fprintf(stderr, "quet: %s\n", ignoredProposalsMessage(res.IgnoredProposals))
	}
	return nil
}

// pushSummary is the one-line result of a push, such as
// "pushed project expenses: created, 120 items (120 new), 40 proposals". The item breakdown lists
// only the non-zero counts; proposals appear only when they were pushed.
func pushSummary(project string, withProposals bool, res web.PushResult) string {
	verb := "updated"
	if res.Created {
		verb = "created"
	}
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{res.Inserted, "new"}, {res.Updated, "updated"}, {res.Unchanged, "unchanged"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	total := res.Inserted + res.Updated + res.Unchanged
	s := fmt.Sprintf("pushed project %s: %s, %d items", project, verb, total)
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	if withProposals {
		s += fmt.Sprintf(", %d proposals", res.Proposals)
	}
	return s
}

// listedProject is one project of `quet web list --json`: the summary and the progress of each
// collaborator, without the schema.
type listedProject struct {
	Slug          string               `json:"slug"`
	Name          string               `json:"name"`
	Items         int                  `json:"items"`
	Proposals     int                  `json:"proposals"`
	CreatedAt     int64                `json:"created_at"`
	UpdatedAt     int64                `json:"updated_at"`
	Collaborators []web.CollabProgress `json:"collaborators"`
}

// webList runs `quet web list`: every project of the server with its collaborators' progress, as
// text or (--json) as one JSON array.
func webList(ctx context.Context, cmd command, stdout io.Writer) error {
	client, err := webClient(cmd.remote)
	if err != nil {
		return err
	}
	summaries, err := client.Projects(ctx)
	if err != nil {
		return err
	}
	listed := make([]listedProject, 0, len(summaries))
	for _, s := range summaries {
		p, err := client.Project(ctx, s.Slug)
		if err != nil {
			return err
		}
		collabs := p.Collaborators
		if collabs == nil {
			collabs = []web.CollabProgress{}
		}
		listed = append(listed, listedProject{
			Slug: s.Slug, Name: s.Name, Items: s.Items, Proposals: s.Proposals,
			CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Collaborators: collabs,
		})
	}
	if cmd.json {
		return json.NewEncoder(stdout).Encode(listed)
	}
	if len(listed) == 0 {
		fmt.Fprintln(stdout, "no projects")
		return nil
	}
	for _, p := range listed {
		fmt.Fprintf(stdout, "%s  %q  %d items, %d proposals, %d collaborators\n", p.Slug, p.Name, p.Items, p.Proposals, len(p.Collaborators))
		for _, c := range p.Collaborators {
			line := fmt.Sprintf("  %s: %d labelled (%d complete, %d uncertain, %d skipped)", c.Username, c.Labelled, c.Complete, c.Uncertain, c.Skipped)
			if c.LastLabelAt != nil {
				line += ", last " + time.UnixMilli(*c.LastLabelAt).UTC().Format("2006-01-02 15:04")
			}
			if c.Disabled {
				line += " [disabled]"
			}
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

// webPull runs `quet web pull`. The project and remote come from the flags; with --labels, a missing
// one is taken from the labels file's sidecar. Every pulled label is validated first: any invalid
// one is reported on stderr and nothing is written. Then --user --out writes a fresh labels file,
// --user --labels merges into an existing one, and --all --out-dir writes one file per collaborator.
func webPull(ctx context.Context, cmd command, stdout, stderr io.Writer) error {
	project, remote := cmd.project, cmd.remote
	if cmd.hasLabels && (!cmd.hasProject || !cmd.hasRemote) {
		link, ok, err := web.LoadLink(cmd.labelsPath)
		if err != nil {
			return err
		}
		if ok {
			if !cmd.hasProject {
				project = link.Project
			}
			if !cmd.hasRemote {
				remote = link.Remote
			}
		}
	}
	if project == "" {
		return fmt.Errorf("no project: pass --project <slug> (%s does not link %s to one)", web.SidecarPath(cmd.labelsPath), cmd.labelsPath)
	}
	if cmd.hasOut && !cmd.force {
		if err := refuseExisting(cmd.outPath); err != nil {
			return err
		}
	}
	client, err := webClient(remote)
	if err != nil {
		return err
	}
	pulled, err := web.Pull(ctx, client, project, cmd.user)
	if err != nil {
		return err
	}
	if len(pulled.Invalid) > 0 {
		return reportInvalid(stderr, project, pulled.Invalid)
	}
	switch {
	case cmd.all:
		return pullAll(cmd, project, pulled, stdout)
	case cmd.hasLabels:
		return pullMerge(cmd, project, pulled, stdout)
	default:
		return pullOut(cmd, project, pulled, stdout)
	}
}

// refuseExisting returns an error when a file or directory exists at path.
func refuseExisting(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists (use --force to overwrite it)", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// reportInvalid prints up to maxListedInvalid "collaborator id: error" lines to stderr and returns
// the error that ends the pull with nothing written.
func reportInvalid(stderr io.Writer, project string, invalid []web.Invalid) error {
	for i, inv := range invalid {
		if i == maxListedInvalid {
			fmt.Fprintf(stderr, "… and %d more\n", len(invalid)-maxListedInvalid)
			break
		}
		fmt.Fprintf(stderr, "%s %s: %s\n", inv.Collaborator, inv.ID, inv.Error)
	}
	return fmt.Errorf("%d invalid label(s) in project %s; nothing written", len(invalid), project)
}

// userLabels returns the pulled labels of cmd.user. A user with no labels who is not a collaborator
// of the project is an error: that is almost certainly a typo.
func userLabels(cmd command, project string, pulled *web.Pulled) (map[string]annotate.Label, error) {
	labels := pulled.Labels[cmd.user]
	if len(labels) > 0 {
		return labels, nil
	}
	var names []string
	for _, c := range pulled.Project.Collaborators {
		if c.Username == cmd.user {
			return labels, nil
		}
		names = append(names, c.Username)
	}
	msg := fmt.Sprintf("project %s has no collaborator %q", project, cmd.user)
	if len(names) > 0 {
		msg += " (collaborators: " + strings.Join(names, ", ") + ")"
	}
	return nil, errors.New(msg)
}

// pullOut writes the labels of cmd.user to a fresh labels file at cmd.outPath, in project position
// order. An existing file is refused unless --force.
func pullOut(cmd command, project string, pulled *web.Pulled, stdout io.Writer) error {
	labels, err := userLabels(cmd, project, pulled)
	if err != nil {
		return err
	}
	if err := annotate.WriteLabels(cmd.outPath, pulled.Schema, pulled.Items, labels); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "pulled %d labels of %s from %s into %s\n", len(labels), cmd.user, project, cmd.outPath)
	return nil
}

// pullMerge merges the labels of cmd.user into the existing labels file cmd.labelsPath the way a
// re-check session does: a pulled label replaces the line with its id or is appended, and every
// other line stays byte for byte. Labels equal to the saved one are left alone. The file is refused
// if another program changed it underneath.
func pullMerge(cmd command, project string, pulled *web.Pulled, stdout io.Writer) error {
	labels, err := userLabels(cmd, project, pulled)
	if err != nil {
		return err
	}
	session, err := annotate.OpenRecheckItems(pulled.Schema, pulled.Items, cmd.labelsPath)
	if err != nil {
		return err
	}
	changes := map[int]annotate.Label{}
	var added, replaced, same int
	for id, l := range labels {
		i, ok := session.IndexOf(id)
		if !ok {
			return fmt.Errorf("pulled label for %q has no record in project %s", id, project)
		}
		old, had := session.Label(i)
		switch {
		case !had:
			added++
		case annotate.LabelsEqual(old, l):
			same++
			continue
		default:
			replaced++
		}
		changes[i] = l
	}
	if err := session.SaveLabels(changes); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "merged %d labels of %s from %s into %s (%d added, %d replaced, %d unchanged)\n",
		len(labels), cmd.user, project, cmd.labelsPath, added, replaced, same)
	return nil
}

// pullAll writes one <dir>/<username>.jsonl per collaborator with at least one label. Existing
// files are refused (all of them, before anything is written) unless --force.
func pullAll(cmd command, project string, pulled *web.Pulled, stdout io.Writer) error {
	var users []string
	for user, labels := range pulled.Labels {
		if len(labels) > 0 {
			users = append(users, user)
		}
	}
	slices.Sort(users)
	if len(users) == 0 {
		fmt.Fprintf(stdout, "no labels in project %s; nothing written\n", project)
		return nil
	}
	paths := make([]string, len(users))
	for i, user := range users {
		if user == "." || user == ".." || strings.ContainsAny(user, "/\\\x00") {
			return fmt.Errorf("collaborator %q cannot be used as a file name", user)
		}
		paths[i] = filepath.Join(cmd.outDir, user+".jsonl")
		if !cmd.force {
			if err := refuseExisting(paths[i]); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(cmd.outDir, 0o755); err != nil {
		return err
	}
	for i, user := range users {
		labels := pulled.Labels[user]
		if err := annotate.WriteLabels(paths[i], pulled.Schema, pulled.Items, labels); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "pulled %d labels of %s from %s into %s\n", len(labels), user, project, paths[i])
	}
	return nil
}
