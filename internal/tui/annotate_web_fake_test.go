package tui

import (
	"encoding/json"
	"fmt"
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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/web"
)

// fakeWebLabel is one stored collaborator label: the wire JSON of a labels-file line.
type fakeWebLabel struct {
	user string
	raw  string
}

// fakeWebProject is the in-memory state of one project of the fake quet-web admin API.
type fakeWebProject struct {
	name       string
	schemaYAML string
	items      []annotate.Item // position order
	proposals  map[string]bool
	labels     []fakeWebLabel
}

// fakeWeb is a minimal in-memory quet-web admin API that records "METHOD path" of every request.
type fakeWeb struct {
	*httptest.Server
	mu       sync.Mutex
	projects map[string]*fakeWebProject
	calls    []string
	// reject, when set, answers every request with this status and body instead of the API.
	rejectStatus int
	rejectBody   string
}

// newFakeWeb starts the fake server, closed when the test ends.
func newFakeWeb(t *testing.T) *fakeWeb {
	t.Helper()
	f := &fakeWeb{projects: map[string]*fakeWebProject{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// callList returns the recorded "METHOD path" requests.
func (f *fakeWeb) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// project returns the stored project, or nil.
func (f *fakeWeb) project(slug string) *fakeWebProject {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.projects[slug]
}

// serve answers one request of the admin API.
func (f *fakeWeb) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if f.rejectStatus != 0 {
		w.WriteHeader(f.rejectStatus)
		_, _ = io.WriteString(w, f.rejectBody)
		return
	}
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	rest, _ := strings.CutPrefix(r.URL.Path, "/api/admin/")
	parts := strings.Split(rest, "/")
	if rest == "projects" && r.Method == "GET" {
		slugs := make([]string, 0, len(f.projects))
		for s := range f.projects {
			slugs = append(slugs, s)
		}
		sort.Strings(slugs)
		list := []map[string]any{}
		for _, s := range slugs {
			p := f.projects[s]
			list = append(list, map[string]any{"slug": s, "name": p.name, "items": len(p.items), "proposals": len(p.proposals), "collaborators": 2})
		}
		reply(200, map[string]any{"projects": list})
		return
	}
	if len(parts) < 2 || parts[0] != "projects" {
		reply(404, map[string]string{"error": "not found"})
		return
	}
	slug, sub := parts[1], parts[2:]
	p := f.projects[slug]
	if len(sub) == 0 && r.Method == "PUT" {
		var in struct {
			SchemaYAML string  `json:"schema_yaml"`
			Name       *string `json:"name"`
		}
		_ = json.Unmarshal(body, &in)
		created := p == nil
		if created {
			p = &fakeWebProject{name: slug, proposals: map[string]bool{}}
			f.projects[slug] = p
		}
		if in.Name != nil {
			p.name = *in.Name
		}
		p.schemaYAML = in.SchemaYAML
		reply(200, map[string]any{"project": map[string]any{"slug": slug}, "created": created})
		return
	}
	if p == nil {
		reply(404, map[string]string{"error": "project not found"})
		return
	}
	switch {
	case len(sub) == 0 && r.Method == "GET":
		reply(200, map[string]any{
			"project":       map[string]any{"slug": slug, "name": p.name, "schema": json.RawMessage(`{}`), "schema_yaml": p.schemaYAML, "items": len(p.items)},
			"collaborators": []map[string]any{},
		})
	case len(sub) == 1 && sub[0] == "items" && r.Method == "POST":
		var in struct {
			Items []struct {
				ID, Text string
				Position int
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &in)
		for _, it := range in.Items {
			p.items = append(p.items, annotate.Item{ID: it.ID, Text: it.Text})
		}
		reply(200, map[string]int{"inserted": len(in.Items), "updated": 0, "unchanged": 0})
	case len(sub) == 1 && sub[0] == "items" && r.Method == "GET":
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		page := []map[string]any{}
		for i := offset; i < len(p.items); i++ {
			page = append(page, map[string]any{"id": p.items[i].ID, "position": i, "text": p.items[i].Text})
		}
		reply(200, map[string]any{"items": page, "total": len(p.items)})
	case len(sub) == 1 && sub[0] == "proposals" && r.Method == "DELETE":
		p.proposals = map[string]bool{}
		reply(200, map[string]bool{"deleted": true})
	case len(sub) == 1 && sub[0] == "proposals" && r.Method == "POST":
		var in struct {
			Proposals []struct {
				ID string `json:"id"`
			} `json:"proposals"`
		}
		_ = json.Unmarshal(body, &in)
		known := map[string]bool{}
		for _, it := range p.items {
			known[it.ID] = true
		}
		upserted, ignored := 0, []string{}
		for _, pr := range in.Proposals {
			if known[pr.ID] {
				p.proposals[pr.ID] = true
				upserted++
			} else {
				ignored = append(ignored, pr.ID)
			}
		}
		reply(200, map[string]any{"upserted": upserted, "ignored": ignored})
	case len(sub) == 1 && sub[0] == "labels" && r.Method == "GET":
		out := []map[string]any{}
		for _, l := range p.labels {
			out = append(out, map[string]any{"collaborator": l.user, "updated_at": 1, "label": json.RawMessage(l.raw)})
		}
		reply(200, map[string]any{"labels": out, "next_cursor": nil})
	default:
		reply(404, map[string]string{"error": "not found"})
	}
}

// wireLabel returns the wire JSON of a label with an implicit target (target "" = null).
func wireLabel(id, status, typ, text, target string) string {
	typeJSON := "null"
	if typ != "" {
		typeJSON = strconv.Quote(typ)
	}
	targetJSON := "null"
	if target != "" {
		start := len([]rune(text[:strings.Index(text, target)]))
		targetJSON = fmt.Sprintf(`{"text":%q,"start":%d,"end":%d}`, target, start, start+len([]rune(target)))
	}
	return fmt.Sprintf(`{"id":%q,"annotation_status":%q,"type":%s,"target":%s}`, id, status, typeJSON, targetJSON)
}

// compareFixture seeds project "queue" with the queue fixture plus an item x1 that is not in the local queue:
//
//	r1  alice, bob: complete lend "anh Nam"; carol: complete borrow "anh Nam"   → to decide
//	r2  alice, bob: complete expense "Pizza 4P"                                  → unanimous
//	r3  alice: complete transfer, no target; carol: invalid type                 → unanimous (invalid ignored)
//	r4  bob: complete income "Mẹ"                                                → unanimous
//	r5  nothing
//	x1  alice: complete expense, no target (item not in the local queue)         → ignored
//	zz9 alice: label of an unknown record                                        → ignored (invalid)
func (f *fakeWeb) compareFixture() {
	t := func(i int) string { return annotQueue[i].Text }
	p := &fakeWebProject{name: "Queue", schemaYAML: annotSchemaYAML, proposals: map[string]bool{}}
	p.items = append(append([]annotate.Item(nil), annotQueue...), annotate.Item{ID: "x1", Text: "extra record"})
	add := func(user, raw string) { p.labels = append(p.labels, fakeWebLabel{user, raw}) }
	add("alice", wireLabel("r1", "complete", "lend", t(0), "anh Nam"))
	add("bob", wireLabel("r1", "complete", "lend", t(0), "anh Nam"))
	add("carol", wireLabel("r1", "complete", "borrow", t(0), "anh Nam"))
	add("alice", wireLabel("r2", "complete", "expense", t(1), "Pizza 4P"))
	add("bob", wireLabel("r2", "complete", "expense", t(1), "Pizza 4P"))
	add("alice", wireLabel("r3", "complete", "transfer", t(2), ""))
	add("carol", wireLabel("r3", "complete", "bogus", t(2), ""))
	add("bob", wireLabel("r4", "complete", "income", t(3), "Mẹ"))
	add("alice", wireLabel("x1", "complete", "expense", "extra record", ""))
	add("alice", wireLabel("zz9", "complete", "expense", "", ""))
	f.mu.Lock()
	f.projects["queue"] = p
	f.mu.Unlock()
}

// webEnv isolates the config directory in a temp dir and clears the QUET_WEB_* overrides.
func webEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{web.EnvURL, web.EnvClientID, web.EnvClientSecret} {
		t.Setenv(k, "")
	}
}

// saveFakeRemote saves remote "origin" pointing at the fake server in remotes.yaml.
func saveFakeRemote(t *testing.T, f *fakeWeb) {
	t.Helper()
	remotes, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	remotes.Set(web.Remote{Name: "origin", URL: f.URL, ClientID: "id.access", ClientSecret: "s3cret"})
	if err := remotes.Save(); err != nil {
		t.Fatal(err)
	}
}

// linkedModel returns a model over the queue fixture linked to origin/queue on a fake server seeded by
// compareFixture, plus the fake and the labels path.
func linkedModel(t *testing.T) (annotModel, *fakeWeb, string) {
	t.Helper()
	webEnv(t)
	f := newFakeWeb(t)
	f.compareFixture()
	saveFakeRemote(t, f)
	m, labelsPath := annotTestModel(t)
	if err := web.SaveLink(labelsPath, web.Link{Remote: "origin", Project: "queue"}); err != nil {
		t.Fatal(err)
	}
	return m, f, labelsPath
}

// webKey is a key message by name: "enter", "esc", "tab", "ctrl+s", "backspace", "down", "up", "left", "right",
// "space" or one rune.
func webKey(name string) tea.Msg {
	switch name {
	case "enter":
		return enterKey
	case "esc":
		return escKey
	case "tab":
		return specialKey(tea.KeyTab)
	case "ctrl+s":
		return specialKey(tea.KeyCtrlS)
	case "backspace":
		return specialKey(tea.KeyBackspace)
	case "down":
		return specialKey(tea.KeyDown)
	case "up":
		return specialKey(tea.KeyUp)
	case "right":
		return specialKey(tea.KeyRight)
	case "left":
		return specialKey(tea.KeyLeft)
	case "space":
		return specialKey(tea.KeySpace)
	}
	return runeKey([]rune(name)[0])
}

// pasteKey is a pasted text message.
func pasteKey(s string) tea.Msg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

// webPress feeds the named keys in order and returns the model and the command of the last key.
func webPress(t *testing.T, m annotModel, names ...string) (annotModel, tea.Cmd) {
	t.Helper()
	msgs := make([]tea.Msg, len(names))
	for i, n := range names {
		msgs[i] = webKey(n)
	}
	return sendAnnot(m, msgs...)
}

// runWeb runs a network command synchronously and feeds its result message back, the way the program would.
func runWeb(t *testing.T, m annotModel, cmd tea.Cmd) annotModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a network command, got none")
	}
	msg := cmd()
	switch msg.(type) {
	case webProjectsMsg, webPublishMsg, webPullMsg:
	default:
		t.Fatalf("expected a web result message, got %T", msg)
	}
	m, _ = m.update(msg)
	return m
}

// openCompareModel links the fixture and opens the compare mode on the pulled labels.
func openCompareModel(t *testing.T) (annotModel, *fakeWeb, string) {
	t.Helper()
	m, f, labelsPath := linkedModel(t)
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	if m.mode != annotCompare {
		t.Fatalf("mode = %d, want compare; status %q", m.mode, m.status)
	}
	return m, f, labelsPath
}

// readFile returns the content of path.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// labelsPathExists reports whether the labels file exists.
func labelsPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// sidecar returns the content of the link sidecar of labelsPath.
func sidecar(t *testing.T, labelsPath string) string {
	t.Helper()
	return readFile(t, filepath.Clean(web.SidecarPath(labelsPath)))
}
