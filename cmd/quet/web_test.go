package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/8bu/quet/internal/web"
)

// webSchemaYAML is the schema of every web test project: the implicit target, b has none.
const webSchemaYAML = "types: [a, b]\nstatuses:\n  complete: Confident\n  uncertain: Unsure\n  skipped:\nnull_target_types: [b]\n"

// secretValue is the Client Secret used by the tests; it must never show up in any output.
const secretValue = "s3cr3t-token-value"

// fakeItem and fakeLabel are the records and collaborator labels of a fake project.
type fakeItem struct {
	id, text string
	position int
}

type fakeLabel struct {
	collaborator string
	updatedAt    int64
	raw          string
}

// fakeProject is the state of one project of the fake quet-web admin API.
type fakeProject struct {
	name, schemaYAML string
	schema           json.RawMessage
	items            map[string]fakeItem
	proposals        map[string]json.RawMessage
	labels           []fakeLabel
	collaborators    []string
}

// fakeWeb is a minimal in-memory quet-web admin API that records the credentials it saw.
type fakeWeb struct {
	*httptest.Server
	mu       sync.Mutex
	projects map[string]*fakeProject
	ids      []string // CF-Access-Client-Id of every request
	secrets  []string // CF-Access-Client-Secret of every request
	status   int      // when non-zero, every request answers this status with a non-JSON body
}

// newFakeWeb starts a fake server that is closed when the test ends.
func newFakeWeb(t *testing.T) *fakeWeb {
	t.Helper()
	f := &fakeWeb{projects: map[string]*fakeProject{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// seed stores a project with items (ids a1.. in position order) and the given labels.
func (f *fakeWeb) seed(slug string, texts []string, collaborators []string, labels ...fakeLabel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := &fakeProject{
		name: slug, schemaYAML: webSchemaYAML, schema: json.RawMessage(`{}`),
		items: map[string]fakeItem{}, proposals: map[string]json.RawMessage{}, labels: labels, collaborators: collaborators,
	}
	for i, text := range texts {
		id := "a" + strconv.Itoa(i+1)
		p.items[id] = fakeItem{id, text, i}
	}
	f.projects[slug] = p
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// serve answers one admin API request.
func (f *fakeWeb) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, r.Header.Get("CF-Access-Client-Id"))
	f.secrets = append(f.secrets, r.Header.Get("CF-Access-Client-Secret"))
	body, _ := io.ReadAll(r.Body)
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte("<html>Access denied</html>"))
		return
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/api/admin/")
	parts := strings.Split(rest, "/")
	if rest == "whoami" {
		jsonReply(w, 200, map[string]string{"identity": "svc@example.com"})
		return
	}
	if rest == "projects" {
		slugs := make([]string, 0, len(f.projects))
		for s := range f.projects {
			slugs = append(slugs, s)
		}
		sort.Strings(slugs)
		list := []map[string]any{}
		for _, s := range slugs {
			p := f.projects[s]
			list = append(list, map[string]any{"slug": s, "name": p.name, "items": len(p.items), "proposals": len(p.proposals),
				"collaborators": len(p.collaborators), "created_at": 1, "updated_at": 2})
		}
		jsonReply(w, 200, map[string]any{"projects": list})
		return
	}
	if len(parts) < 2 || parts[0] != "projects" {
		jsonReply(w, 404, map[string]string{"error": "not found"})
		return
	}
	slug, sub := parts[1], parts[2:]
	p := f.projects[slug]
	if len(sub) == 0 && r.Method == "PUT" {
		var in struct {
			Schema     json.RawMessage `json:"schema"`
			SchemaYAML string          `json:"schema_yaml"`
			Name       *string         `json:"name"`
		}
		_ = json.Unmarshal(body, &in)
		created := p == nil
		if created {
			p = &fakeProject{name: slug, items: map[string]fakeItem{}, proposals: map[string]json.RawMessage{}}
			f.projects[slug] = p
		}
		if in.Name != nil {
			p.name = *in.Name
		}
		p.schema, p.schemaYAML = in.Schema, in.SchemaYAML
		jsonReply(w, 200, map[string]any{"project": map[string]any{"slug": slug}, "created": created})
		return
	}
	if p == nil {
		jsonReply(w, 404, map[string]string{"error": "project not found"})
		return
	}
	switch {
	case len(sub) == 0 && r.Method == "GET":
		collabs := []map[string]any{}
		for _, c := range p.collaborators {
			n := 0
			for _, l := range p.labels {
				if l.collaborator == c {
					n++
				}
			}
			collabs = append(collabs, map[string]any{"username": c, "disabled": false, "labelled": n, "complete": n, "uncertain": 0, "skipped": 0, "last_label_at": 1759000000000})
		}
		jsonReply(w, 200, map[string]any{
			"project": map[string]any{"slug": slug, "name": p.name, "schema": p.schema, "schema_yaml": p.schemaYAML,
				"items": len(p.items), "proposals": len(p.proposals), "created_at": 1, "updated_at": 2},
			"collaborators": collabs,
		})
	case sub[0] == "items" && r.Method == "POST":
		var in struct {
			Items []struct {
				ID       string `json:"id"`
				Text     string `json:"text"`
				Position int    `json:"position"`
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &in)
		var ins, upd, same int
		for _, it := range in.Items {
			old, ok := p.items[it.ID]
			switch {
			case !ok:
				ins++
			case old.position == it.Position && old.text == it.Text:
				same++
			default:
				upd++
			}
			p.items[it.ID] = fakeItem{it.ID, it.Text, it.Position}
		}
		jsonReply(w, 200, map[string]int{"inserted": ins, "updated": upd, "unchanged": same})
	case sub[0] == "items" && r.Method == "GET":
		all := make([]fakeItem, 0, len(p.items))
		for _, it := range p.items {
			all = append(all, it)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].position < all[j].position })
		page := []map[string]any{}
		for _, it := range all {
			page = append(page, map[string]any{"id": it.id, "position": it.position, "text": it.text})
		}
		jsonReply(w, 200, map[string]any{"items": page, "total": len(all)})
	case sub[0] == "proposals" && r.Method == "DELETE":
		p.proposals = map[string]json.RawMessage{}
		jsonReply(w, 200, map[string]any{"deleted": true})
	case sub[0] == "proposals" && r.Method == "POST":
		var in struct {
			Proposals []json.RawMessage `json:"proposals"`
		}
		_ = json.Unmarshal(body, &in)
		upserted, ignored := 0, []string{}
		for _, raw := range in.Proposals {
			var head struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &head)
			if _, ok := p.items[head.ID]; !ok {
				ignored = append(ignored, head.ID)
				continue
			}
			p.proposals[head.ID] = raw
			upserted++
		}
		jsonReply(w, 200, map[string]any{"upserted": upserted, "ignored": ignored})
	case sub[0] == "labels" && r.Method == "GET":
		page := []map[string]any{}
		for _, l := range p.labels {
			if who := r.URL.Query().Get("collaborator"); who == "" || who == l.collaborator {
				page = append(page, map[string]any{"collaborator": l.collaborator, "updated_at": l.updatedAt, "label": json.RawMessage(l.raw)})
			}
		}
		jsonReply(w, 200, map[string]any{"labels": page, "next_cursor": nil})
	default:
		jsonReply(w, 404, map[string]string{"error": "not found"})
	}
}

// webEnv isolates the config directory and the web environment overrides for a test, and returns a
// working directory for its files.
func webEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	for _, k := range []string{web.EnvURL, web.EnvClientID, web.EnvClientSecret} {
		t.Setenv(k, "")
	}
	return t.TempDir()
}

// runWebCmd runs quet with args and returns its exit code, stdout and stderr.
func runWebCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// addRemote registers the fake server as the default remote "origin".
func addRemote(t *testing.T, f *fakeWeb) {
	t.Helper()
	mustRun(t, "web", "remote", "add", "origin", f.URL)
}

// labelJSON is a valid label line of the web test schema.
func labelJSON(id, status, typ string, text string, start, end int) string {
	if typ == "" {
		return `{"id":"` + id + `","annotation_status":"` + status + `","type":null,"target":null}`
	}
	if text == "" {
		return `{"id":"` + id + `","annotation_status":"` + status + `","type":"` + typ + `","target":null}`
	}
	return `{"id":"` + id + `","annotation_status":"` + status + `","type":"` + typ + `","target":{"text":"` + text + `","start":` + strconv.Itoa(start) + `,"end":` + strconv.Itoa(end) + `}}`
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubLogin makes `quet web login` read in as its two lines on a non-terminal stdin until the test ends.
func stubLogin(t *testing.T, in string) {
	t.Helper()
	oldInput, oldTerm := loginInput, loginIsTerminal
	loginInput, loginIsTerminal = strings.NewReader(in), func() bool { return false }
	t.Cleanup(func() { loginInput, loginIsTerminal = oldInput, oldTerm })
}

func TestWebRemoteAddListRemoveDefault(t *testing.T) {
	webEnv(t)
	out := mustRun(t, "web", "remote", "list")
	if !strings.Contains(out, "no remotes") {
		t.Errorf("empty list = %q", out)
	}
	if out := mustRun(t, "web", "remote", "add", "origin", "https://quet.example.com"); out != "added remote origin (default)\n" {
		t.Errorf("add = %q", out)
	}
	if out := mustRun(t, "web", "remote", "add", "dev", "http://localhost:8787"); out != "added remote dev\n" {
		t.Errorf("add second = %q", out)
	}
	out = mustRun(t, "web", "remote", "list")
	for _, want := range []string{"* ", "origin", "https://quet.example.com", "dev", "http://localhost:8787", "CREDENTIALS", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}
	if code, _, stderr := runWebCmd(t, "web", "remote", "add", "origin", "https://other.dev"); code != 1 || !strings.Contains(stderr, "already exists") {
		t.Errorf("duplicate add: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runWebCmd(t, "web", "remote", "add", "bad", "ftp://x"); code != 1 || !strings.Contains(stderr, "invalid remote URL") {
		t.Errorf("bad URL: exit %d, stderr %q", code, stderr)
	}
	if out := mustRun(t, "web", "remote", "default", "dev"); out != "default remote is dev\n" {
		t.Errorf("default = %q", out)
	}
	if code, _, stderr := runWebCmd(t, "web", "remote", "default", "nope"); code != 1 || !strings.Contains(stderr, `unknown remote "nope"`) {
		t.Errorf("default unknown: exit %d, stderr %q", code, stderr)
	}
	if out := mustRun(t, "web", "remote", "list"); !strings.Contains(out, "* dev") && !strings.Contains(out, "*  dev") {
		t.Errorf("default marker not on dev:\n%s", out)
	}
	if out := mustRun(t, "web", "remote", "remove", "dev"); out != "removed remote dev\n" {
		t.Errorf("remove = %q", out)
	}
	if code, _, stderr := runWebCmd(t, "web", "remote", "remove", "dev"); code != 1 || !strings.Contains(stderr, `unknown remote "dev"`) {
		t.Errorf("remove twice: exit %d, stderr %q", code, stderr)
	}
}

func TestWebUsageErrorExitsTwo(t *testing.T) {
	webEnv(t)
	code, _, stderr := runWebCmd(t, "web", "pull", "--project", "p")
	if code != 2 || !strings.Contains(stderr, "needs --user <name> or --all") || !strings.Contains(stderr, "Usage:") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestWebLoginStoresCredentialsAndNeverEchoesTheSecret(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	stubLogin(t, "abc123.access\n"+secretValue+"\n")

	code, stdout, stderr := runWebCmd(t, "web", "login", "--service-token")
	if code != 0 {
		t.Fatalf("login: exit %d, stderr %q", code, stderr)
	}
	if want := "saved credentials for remote origin\nlogged in to origin as svc@example.com\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if strings.Contains(stdout+stderr, secretValue) {
		t.Errorf("login printed the secret:\n%s\n%s", stdout, stderr)
	}
	if f.ids[0] != "abc123.access" || f.secrets[0] != secretValue {
		t.Errorf("whoami sent id %q secret %q", f.ids[0], f.secrets[0])
	}

	path := web.RemotesPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("remotes.yaml mode = %v, want 0600", info.Mode().Perm())
	}
	if !strings.Contains(readFile(t, path), secretValue) {
		t.Error("the secret was not stored in remotes.yaml")
	}

	out := mustRun(t, "web", "remote", "list")
	if strings.Contains(out, secretValue) || strings.Contains(out, "abc123") {
		t.Errorf("remote list shows credentials:\n%s", out)
	}
	if !strings.Contains(out, "service token") {
		t.Errorf("remote list does not say credentials are set:\n%s", out)
	}
}

func TestWebLoginInputErrors(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	stubLogin(t, "")

	for name, in := range map[string]string{
		"one line":     "only-an-id\n",
		"empty":        "",
		"empty secret": "id\n\n",
	} {
		stubLogin(t, in)
		if code, _, stderr := runWebCmd(t, "web", "login", "--service-token"); code != 1 || stderr == "" {
			t.Errorf("%s: exit %d, stderr %q", name, code, stderr)
		}
	}
	stubLogin(t, "id\nsecret\n")
	if code, _, stderr := runWebCmd(t, "web", "login", "ghost", "--service-token"); code != 1 || !strings.Contains(stderr, `unknown remote "ghost"`) {
		t.Errorf("unknown remote: exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(readFile(t, web.RemotesPath()), "secret") {
		t.Error("a failed login stored credentials")
	}
}

func TestWebLoginSavedButRejected(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.status = http.StatusForbidden
	addRemote(t, f)
	stubLogin(t, "id\n"+secretValue+"\n")

	code, stdout, stderr := runWebCmd(t, "web", "login", "--service-token")
	if code != 1 || !strings.Contains(stderr, "run `quet web login`") || !strings.Contains(stderr, "credentials saved") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout+stderr, secretValue) {
		t.Errorf("output shows the secret:\n%s\n%s", stdout, stderr)
	}
}

func TestWebEnvOverridesCredentials(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	t.Setenv(web.EnvClientID, "env-id")
	t.Setenv(web.EnvClientSecret, "env-secret")
	mustRun(t, "web", "list")
	if f.ids[0] != "env-id" || f.secrets[0] != "env-secret" {
		t.Errorf("sent id %q secret %q", f.ids[0], f.secrets[0])
	}
}

func TestWebRejectedCredentialsAreReported(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.status = http.StatusUnauthorized
	addRemote(t, f)
	code, _, stderr := runWebCmd(t, "web", "list")
	if code != 1 || !strings.Contains(stderr, "run `quet web login`") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

// pushFiles writes the queue, schema and proposals of a push and returns their paths.
func pushFiles(t *testing.T, dir string) (queue, schema, proposals string) {
	t.Helper()
	queue = filepath.Join(dir, "queue.jsonl")
	schema = filepath.Join(dir, "schema.yaml")
	proposals = filepath.Join(dir, "proposals.jsonl")
	writeFile(t, queue, `{"id":"a1","text":"foo bar"}`+"\n"+`{"id":"a2","text":"baz"}`+"\n"+`{"id":"a3","text":"qux"}`+"\n")
	writeFile(t, schema, webSchemaYAML)
	writeFile(t, proposals, `{"id":"a1","annotation_status":"complete","type":"a","target":{"text":"foo","start":0,"end":3}}`+"\n"+
		`{"id":"zz","annotation_status":"skipped"}`+"\n")
	return queue, schema, proposals
}

func TestWebPushSummary(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	queue, schema, proposals := pushFiles(t, dir)

	code, stdout, stderr := runWebCmd(t, "web", "push", queue, "--schema", schema, "--project", "expenses", "--proposals", proposals)
	if code != 0 {
		t.Fatalf("push: exit %d, stderr %q", code, stderr)
	}
	if want := "pushed project expenses: created, 3 items (3 new), 1 proposals\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if want := "quet: ignored 1 proposal(s) for ids not in the queue: zz\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if p := f.projects["expenses"]; p == nil || p.name != "expenses" || len(p.items) != 3 || p.schemaYAML != webSchemaYAML {
		t.Fatalf("server state = %+v", p)
	}

	// Second push without --proposals: nothing changes, proposals are left alone and not reported.
	stdout = mustRun(t, "web", "push", queue, "--schema", schema, "--project", "expenses", "--name", "Expenses")
	if want := "pushed project expenses: updated, 3 items (3 unchanged)\n"; stdout != want {
		t.Errorf("second push = %q, want %q", stdout, want)
	}
	if got := f.projects["expenses"]; got.name != "Expenses" || len(got.proposals) != 1 {
		t.Errorf("name %q proposals %d", got.name, len(got.proposals))
	}
	if code, _, stderr := runWebCmd(t, "web", "push", filepath.Join(dir, "missing.jsonl"), "--schema", schema, "--project", "expenses"); code != 1 || stderr == "" {
		t.Errorf("missing queue: exit %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runWebCmd(t, "web", "push", queue, "--schema", schema, "--project", "Bad Slug"); code != 1 || !strings.Contains(stderr, "invalid project slug") {
		t.Errorf("bad slug: exit %d, stderr %q", code, stderr)
	}
}

func TestWebList(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	f.seed("expenses", []string{"foo bar", "baz"}, []string{"alice", "bob"}, fakeLabel{"alice", 1, labelJSON("a1", "complete", "a", "foo", 0, 3)})

	out := mustRun(t, "web", "list")
	for _, want := range []string{"expenses", "2 items", "2 collaborators", "alice: 1 labelled", "bob: 0 labelled"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	out = mustRun(t, "web", "list", "--json")
	var got []struct {
		Slug          string `json:"slug"`
		Items         int    `json:"items"`
		Collaborators []struct {
			Username string `json:"username"`
			Labelled int    `json:"labelled"`
		} `json:"collaborators"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json output %q: %v", out, err)
	}
	if len(got) != 1 || got[0].Slug != "expenses" || got[0].Items != 2 || len(got[0].Collaborators) != 2 || got[0].Collaborators[0].Username != "alice" || got[0].Collaborators[0].Labelled != 1 {
		t.Errorf("--json = %+v", got)
	}
	if strings.Contains(out, "schema") {
		t.Errorf("--json holds the schema: %s", out)
	}

	f.projects = map[string]*fakeProject{}
	if out := mustRun(t, "web", "list", "--json"); out != "[]\n" {
		t.Errorf("empty --json = %q", out)
	}
	if out := mustRun(t, "web", "list"); out != "no projects\n" {
		t.Errorf("empty list = %q", out)
	}
}

// seedPullProject seeds the project "expenses" with three records and labels by alice and bob.
func seedPullProject(f *fakeWeb) {
	f.seed("expenses", []string{"foo bar", "baz", "qux"}, []string{"alice", "bob", "carol"},
		fakeLabel{"alice", 3, labelJSON("a3", "skipped", "", "", 0, 0)},
		fakeLabel{"alice", 1, labelJSON("a1", "complete", "a", "foo", 0, 3)},
		fakeLabel{"alice", 2, labelJSON("a2", "complete", "b", "", 0, 0)},
		fakeLabel{"bob", 4, labelJSON("a1", "uncertain", "b", "", 0, 0)},
	)
}

func TestWebPullUserOutWritesExactBytes(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	seedPullProject(f)
	out := filepath.Join(dir, "alice.jsonl")

	stdout := mustRun(t, "web", "pull", "--project", "expenses", "--user", "alice", "--out", out)
	if want := "pulled 3 labels of alice from expenses into " + out + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	want := labelJSON("a1", "complete", "a", "foo", 0, 3) + "\n" +
		labelJSON("a2", "complete", "b", "", 0, 0) + "\n" +
		labelJSON("a3", "skipped", "", "", 0, 0) + "\n"
	if got := readFile(t, out); got != want {
		t.Errorf("labels file =\n%s\nwant\n%s", got, want)
	}

	// An existing file is refused untouched, and --force overwrites it.
	writeFile(t, out, "keep me\n")
	code, _, stderr := runWebCmd(t, "web", "pull", "--project", "expenses", "--user", "alice", "--out", out)
	if code != 1 || !strings.Contains(stderr, "already exists") || !strings.Contains(stderr, "--force") {
		t.Errorf("without --force: exit %d, stderr %q", code, stderr)
	}
	if got := readFile(t, out); got != "keep me\n" {
		t.Errorf("file was changed: %q", got)
	}
	mustRun(t, "web", "pull", "--project", "expenses", "--user", "bob", "--out", out, "--force")
	if got, want := readFile(t, out), labelJSON("a1", "uncertain", "b", "", 0, 0)+"\n"; got != want {
		t.Errorf("forced file = %q, want %q", got, want)
	}
}

func TestWebPullUnknownUser(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	seedPullProject(f)
	out := filepath.Join(dir, "x.jsonl")

	code, _, stderr := runWebCmd(t, "web", "pull", "--project", "expenses", "--user", "alcie", "--out", out)
	if code != 1 || !strings.Contains(stderr, `no collaborator "alcie"`) || !strings.Contains(stderr, "alice, bob, carol") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a file was written for an unknown user")
	}
	// A known collaborator without labels is not an error.
	mustRun(t, "web", "pull", "--project", "expenses", "--user", "carol", "--out", out)
	if got := readFile(t, out); got != "" {
		t.Errorf("carol's file = %q, want empty", got)
	}
	if code, _, stderr := runWebCmd(t, "web", "pull", "--project", "ghost", "--user", "alice", "--out", filepath.Join(dir, "y.jsonl")); code != 1 || !strings.Contains(stderr, "not found") {
		t.Errorf("unknown project: exit %d, stderr %q", code, stderr)
	}
}

func TestWebPullLabelsMergeKeepsOtherLinesByteForByte(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	seedPullProject(f)
	labels := filepath.Join(dir, "labels.jsonl")
	kept := `{"id":"outside",  "annotation_status":"skipped", "type":null,"target":null}` // not in the project, odd spacing
	oldA1 := labelJSON("a1", "uncertain", "b", "", 0, 0)
	spaced := `{"id":"a3", "annotation_status":"skipped","type":null,"target":null}`
	writeFile(t, labels, kept+"\n"+oldA1+"\n"+spaced+"\n")

	stdout := mustRun(t, "web", "pull", "--project", "expenses", "--user", "alice", "--labels", labels)
	if want := "merged 3 labels of alice from expenses into " + labels + " (1 added, 1 replaced, 1 unchanged)\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	lines := strings.Split(strings.TrimSuffix(readFile(t, labels), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("labels file has %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if lines[0] != kept {
		t.Errorf("line outside the project changed: %q", lines[0])
	}
	if want := labelJSON("a1", "complete", "a", "foo", 0, 3); lines[1] != want {
		t.Errorf("a1 not replaced in place: %q, want %q", lines[1], want)
	}
	if lines[2] != spaced {
		t.Errorf("unchanged label was rewritten: %q", lines[2])
	}
	if want := labelJSON("a2", "complete", "b", "", 0, 0); lines[3] != want {
		t.Errorf("a2 not appended: %q, want %q", lines[3], want)
	}

	// Pulling again changes nothing.
	before := readFile(t, labels)
	stdout = mustRun(t, "web", "pull", "--project", "expenses", "--user", "alice", "--labels", labels)
	if !strings.Contains(stdout, "(0 added, 0 replaced, 3 unchanged)") || readFile(t, labels) != before {
		t.Errorf("second merge: %q", stdout)
	}

	// The labels file must exist.
	if code, _, stderr := runWebCmd(t, "web", "pull", "--project", "expenses", "--user", "alice", "--labels", filepath.Join(dir, "nope.jsonl")); code != 1 || stderr == "" {
		t.Errorf("missing labels file: exit %d, stderr %q", code, stderr)
	}
}

func TestWebPullLabelsUsesSidecarAndLeavesItAlone(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	seedPullProject(f)
	dev := newFakeWeb(t) // the default remote does not know the project; the sidecar's remote does
	mustRun(t, "web", "remote", "add", "origin", dev.URL)
	mustRun(t, "web", "remote", "add", "staging", f.URL)
	labels := filepath.Join(dir, "labels.jsonl")
	writeFile(t, labels, "")
	sidecar := web.SidecarPath(labels)
	writeFile(t, sidecar, "remote: staging\nproject: expenses\n")

	mustRun(t, "web", "pull", "--user", "bob", "--labels", labels)
	if got, want := readFile(t, labels), labelJSON("a1", "uncertain", "b", "", 0, 0)+"\n"; got != want {
		t.Errorf("labels = %q, want %q", got, want)
	}
	if got := readFile(t, sidecar); got != "remote: staging\nproject: expenses\n" {
		t.Errorf("sidecar was rewritten: %q", got)
	}

	// Flags win over the sidecar.
	code, _, stderr := runWebCmd(t, "web", "pull", "--user", "bob", "--labels", labels, "--project", "ghost")
	if code != 1 || !strings.Contains(stderr, "not found") {
		t.Errorf("--project override: exit %d, stderr %q", code, stderr)
	}

	// No sidecar and no --project: a runtime error naming the sidecar.
	bare := filepath.Join(dir, "bare.jsonl")
	writeFile(t, bare, "")
	code, _, stderr = runWebCmd(t, "web", "pull", "--user", "bob", "--labels", bare)
	if code != 1 || !strings.Contains(stderr, "no project") || !strings.Contains(stderr, ".quet-web.yaml") {
		t.Errorf("no sidecar: exit %d, stderr %q", code, stderr)
	}
}

func TestWebPullAllWritesOneFilePerCollaborator(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	seedPullProject(f)
	outDir := filepath.Join(dir, "pulled", "nested")

	stdout := mustRun(t, "web", "pull", "--project", "expenses", "--all", "--out-dir", outDir)
	wantOut := "pulled 3 labels of alice from expenses into " + filepath.Join(outDir, "alice.jsonl") + "\n" +
		"pulled 1 labels of bob from expenses into " + filepath.Join(outDir, "bob.jsonl") + "\n"
	if stdout != wantOut {
		t.Errorf("stdout = %q, want %q", stdout, wantOut)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "alice.jsonl,bob.jsonl" { // carol has no labels: no file
		t.Errorf("files = %v", names)
	}
	if got, want := readFile(t, filepath.Join(outDir, "bob.jsonl")), labelJSON("a1", "uncertain", "b", "", 0, 0)+"\n"; got != want {
		t.Errorf("bob.jsonl = %q, want %q", got, want)
	}

	// An existing file refuses the whole pull before anything is written; --force overwrites.
	if err := os.Remove(filepath.Join(outDir, "alice.jsonl")); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runWebCmd(t, "web", "pull", "--project", "expenses", "--all", "--out-dir", outDir)
	if code != 1 || !strings.Contains(stderr, "bob.jsonl already exists") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(outDir, "alice.jsonl")); err == nil {
		t.Error("alice.jsonl was written although bob.jsonl blocked the pull")
	}
	mustRun(t, "web", "pull", "--project", "expenses", "--all", "--out-dir", outDir, "--force")
	if _, err := os.Stat(filepath.Join(outDir, "alice.jsonl")); err != nil {
		t.Error("--force did not write alice.jsonl")
	}
}

func TestWebPullAllRefusesUnsafeUsernames(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	f.seed("expenses", []string{"foo bar"}, []string{"../evil"}, fakeLabel{"../evil", 1, labelJSON("a1", "skipped", "", "", 0, 0)})
	outDir := filepath.Join(dir, "out")

	code, _, stderr := runWebCmd(t, "web", "pull", "--project", "expenses", "--all", "--out-dir", outDir)
	if code != 1 || !strings.Contains(stderr, "cannot be used as a file name") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.jsonl")); err == nil {
		t.Error("a file escaped the output directory")
	}
}

func TestWebPullInvalidLabelsWriteNothing(t *testing.T) {
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	f.seed("expenses", []string{"foo bar", "baz"}, []string{"alice", "bob"},
		fakeLabel{"alice", 1, labelJSON("a1", "complete", "a", "foo", 0, 3)},
		fakeLabel{"alice", 2, labelJSON("a2", "complete", "a", "nope", 0, 4)}, // text does not match
		fakeLabel{"bob", 3, `{"id":"a1","annotation_status":"bogus","type":null,"target":null}`},
		fakeLabel{"bob", 4, labelJSON("zz", "skipped", "", "", 0, 0)}, // not a record of the project
	)
	existing := filepath.Join(dir, "labels.jsonl")
	writeFile(t, existing, labelJSON("a1", "skipped", "", "", 0, 0)+"\n")
	fresh := filepath.Join(dir, "fresh.jsonl")
	outDir := filepath.Join(dir, "all")

	for name, args := range map[string][]string{
		"--out":     {"--user", "alice", "--out", fresh},
		"--labels":  {"--user", "alice", "--labels", existing},
		"--out-dir": {"--all", "--out-dir", outDir},
	} {
		code, stdout, stderr := runWebCmd(t, append([]string{"web", "pull", "--project", "expenses"}, args...)...)
		if code != 1 || stdout != "" {
			t.Errorf("%s: exit %d, stdout %q", name, code, stdout)
		}
		if !strings.Contains(stderr, "alice a2:") || !strings.Contains(stderr, "invalid label(s)") || !strings.Contains(stderr, "nothing written") {
			t.Errorf("%s: stderr = %q", name, stderr)
		}
		if name == "--out-dir" && (!strings.Contains(stderr, "bob a1:") || !strings.Contains(stderr, `bob zz:`)) {
			t.Errorf("%s: stderr misses bob's invalid labels: %q", name, stderr)
		}
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Error("--out wrote a file")
	}
	if _, err := os.Stat(outDir); err == nil {
		t.Error("--out-dir created a directory")
	}
	if got := readFile(t, existing); got != labelJSON("a1", "skipped", "", "", 0, 0)+"\n" {
		t.Errorf("--labels file was changed: %q", got)
	}
}

func TestWebPullInvalidLabelReportIsCapped(t *testing.T) {
	var labels []fakeLabel
	texts := make([]string, 60)
	for i := range texts {
		texts[i] = "foo"
		labels = append(labels, fakeLabel{"alice", int64(i), labelJSON("a"+strconv.Itoa(i+1), "complete", "a", "bar", 0, 3)})
	}
	dir := webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	f.seed("big", texts, []string{"alice"}, labels...)

	code, _, stderr := runWebCmd(t, "web", "pull", "--project", "big", "--user", "alice", "--out", filepath.Join(dir, "o.jsonl"))
	if code != 1 || strings.Count(stderr, "alice a") != 50 || !strings.Contains(stderr, "… and 10 more") || !strings.Contains(stderr, "60 invalid label(s)") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr)
	}
}

func TestWebNeverPrintsSecretsOnFailure(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	addRemote(t, f)
	t.Setenv(web.EnvClientID, "env-id")
	t.Setenv(web.EnvClientSecret, secretValue)
	f.status = http.StatusForbidden
	for _, args := range [][]string{{"web", "list"}, {"web", "pull", "--project", "p", "--all", "--out-dir", t.TempDir() + "/x"}, {"web", "remote", "list"}} {
		_, stdout, stderr := runWebCmd(t, args...)
		if strings.Contains(stdout+stderr, secretValue) {
			t.Errorf("quet %s printed the secret:\n%s\n%s", strings.Join(args, " "), stdout, stderr)
		}
	}
}

func TestPushSummaryFormats(t *testing.T) {
	tests := []struct {
		name      string
		proposals bool
		res       web.PushResult
		want      string
	}{
		{"created with proposals", true, web.PushResult{Created: true, Inserted: 120, Proposals: 40}, "pushed project expenses: created, 120 items (120 new), 40 proposals"},
		{"mixed update", false, web.PushResult{Inserted: 1, Updated: 2, Unchanged: 3}, "pushed project expenses: updated, 6 items (1 new, 2 updated, 3 unchanged)"},
		{"all unchanged, proposals cleared", true, web.PushResult{Unchanged: 5}, "pushed project expenses: updated, 5 items (5 unchanged), 0 proposals"},
	}
	for _, tt := range tests {
		if got := pushSummary("expenses", tt.proposals, tt.res); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}
