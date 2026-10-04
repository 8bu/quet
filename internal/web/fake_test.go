package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeItem, fakeLabel and fakeProject are the in-memory state of the fake quet-web admin API.
type fakeItem struct {
	id, text string
	position int
}

type fakeLabel struct {
	collaborator, itemID string
	updatedAt            int64
	raw                  json.RawMessage
}

type fakeProject struct {
	name       string
	schemaJSON json.RawMessage
	schemaYAML string
	items      map[string]*fakeItem
	proposals  map[string]json.RawMessage
	labels     []fakeLabel
}

// fakeRequest is one request the fake server saw.
type fakeRequest struct {
	Method, Path string
	Query        map[string][]string
	Header       http.Header
	Body         []byte
}

// fakeServer implements the admin endpoints of the quet-web contract in memory and records every request.
type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	projects map[string]*fakeProject
	reqs     []fakeRequest
	// rejectUpdate, when set, makes an update of an existing project fail with a 409 and these invalid entries.
	rejectUpdate []Invalid
	// override, when set, answers every request instead of the API.
	override func(w http.ResponseWriter, r *http.Request)
}

// newFakeServer starts a fake server that is closed when the test ends.
func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{projects: map[string]*fakeProject{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// client returns a client for the fake server with the given credentials.
func (f *fakeServer) client(t *testing.T, id, secret string) *Client {
	t.Helper()
	c, err := NewClient(Remote{Name: "fake", URL: f.URL, ClientID: id, ClientSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// calls returns "METHOD path" of every request so far.
func (f *fakeServer) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.reqs))
	for i, r := range f.reqs {
		out[i] = r.Method + " " + r.Path
	}
	return out
}

// requests returns the requests whose "METHOD path" equals call.
func (f *fakeServer) requests(call string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRequest
	for _, r := range f.reqs {
		if r.Method+" "+r.Path == call {
			out = append(out, r)
		}
	}
	return out
}

// project returns the stored project (nil when absent).
func (f *fakeServer) project(slug string) *fakeProject {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.projects[slug]
}

// seed stores a project directly.
func (f *fakeServer) seed(slug string, p *fakeProject) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.items == nil {
		p.items = map[string]*fakeItem{}
	}
	if p.proposals == nil {
		p.proposals = map[string]json.RawMessage{}
	}
	f.projects[slug] = p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// serve answers one request.
func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.reqs = append(f.reqs, fakeRequest{r.Method, r.URL.Path, r.URL.Query(), r.Header.Clone(), body})
	if f.override != nil {
		f.override(w, r)
		return
	}

	rest, ok := strings.CutPrefix(r.URL.Path, "/api/admin/")
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	parts := strings.Split(rest, "/")
	switch {
	case rest == "whoami" && r.Method == "GET":
		writeJSON(w, 200, map[string]string{"identity": "svc@example.com"})
	case rest == "projects" && r.Method == "GET":
		f.listProjects(w)
	case len(parts) >= 2 && parts[0] == "projects":
		p := f.projects[parts[1]]
		f.projectRoute(w, r, parts[1], p, parts[2:], body)
	default:
		writeErr(w, 404, "not found")
	}
}

func (f *fakeServer) listProjects(w http.ResponseWriter) {
	slugs := make([]string, 0, len(f.projects))
	for s := range f.projects {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	list := []map[string]any{}
	for _, s := range slugs {
		p := f.projects[s]
		list = append(list, map[string]any{"slug": s, "name": p.name, "items": len(p.items), "proposals": len(p.proposals),
			"collaborators": 0, "created_at": 1, "updated_at": 2})
	}
	writeJSON(w, 200, map[string]any{"projects": list})
}

// projectRoute serves /api/admin/projects/<slug>/<sub...>.
func (f *fakeServer) projectRoute(w http.ResponseWriter, r *http.Request, slug string, p *fakeProject, sub []string, body []byte) {
	switch {
	case len(sub) == 0 && r.Method == "PUT":
		f.putProject(w, slug, p, body)
		return
	case p == nil:
		writeErr(w, 404, "project not found")
		return
	case len(sub) == 0 && r.Method == "GET":
		writeJSON(w, 200, map[string]any{
			"project": map[string]any{"slug": slug, "name": p.name, "schema": p.schemaJSON, "schema_yaml": p.schemaYAML,
				"items": len(p.items), "proposals": len(p.proposals), "created_at": 1, "updated_at": 2},
			"collaborators": []map[string]any{{"username": "alice", "disabled": false, "labelled": 1, "complete": 1,
				"uncertain": 0, "skipped": 0, "last_label_at": 5}},
		})
	case len(sub) == 1 && sub[0] == "items" && r.Method == "POST":
		f.postItems(w, p, body)
	case len(sub) == 1 && sub[0] == "items" && r.Method == "GET":
		f.getItems(w, r, p)
	case len(sub) == 1 && sub[0] == "proposals" && r.Method == "DELETE":
		p.proposals = map[string]json.RawMessage{}
		writeJSON(w, 200, map[string]any{"deleted": true})
	case len(sub) == 1 && sub[0] == "proposals" && r.Method == "POST":
		f.postProposals(w, p, body)
	case len(sub) == 1 && sub[0] == "labels" && r.Method == "GET":
		f.getLabels(w, r, p)
	default:
		writeErr(w, 404, "not found")
	}
}

func (f *fakeServer) putProject(w http.ResponseWriter, slug string, p *fakeProject, body []byte) {
	var in struct {
		Schema     json.RawMessage `json:"schema"`
		SchemaYAML string          `json:"schema_yaml"`
		Name       *string         `json:"name"`
	}
	if err := json.Unmarshal(body, &in); err != nil || len(in.Schema) == 0 {
		writeErr(w, 400, "invalid body")
		return
	}
	created := p == nil
	if created {
		p = &fakeProject{name: slug, items: map[string]*fakeItem{}, proposals: map[string]json.RawMessage{}}
		f.projects[slug] = p
	} else if len(f.rejectUpdate) > 0 {
		writeJSON(w, 409, map[string]any{"error": "stored labels no longer valid", "invalid": f.rejectUpdate})
		return
	}
	if in.Name != nil {
		p.name = *in.Name
	}
	p.schemaJSON, p.schemaYAML = in.Schema, in.SchemaYAML
	writeJSON(w, 200, map[string]any{"project": map[string]any{"slug": slug, "name": p.name}, "created": created})
}

func (f *fakeServer) postItems(w http.ResponseWriter, p *fakeProject, body []byte) {
	var in struct {
		Items []struct {
			ID       string `json:"id"`
			Text     string `json:"text"`
			Position int    `json:"position"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &in); err != nil || len(in.Items) > 1000 {
		writeErr(w, 400, "bad items body")
		return
	}
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
		p.items[it.ID] = &fakeItem{it.ID, it.Text, it.Position}
	}
	writeJSON(w, 200, map[string]int{"inserted": ins, "updated": upd, "unchanged": same})
}

func (f *fakeServer) getItems(w http.ResponseWriter, r *http.Request, p *fakeProject) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 1000 || offset < 0 {
		writeErr(w, 400, "bad paging")
		return
	}
	all := make([]*fakeItem, 0, len(p.items))
	for _, it := range p.items {
		all = append(all, it)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].position < all[j].position })
	page := []map[string]any{}
	for i := offset; i < min(offset+limit, len(all)); i++ {
		page = append(page, map[string]any{"id": all[i].id, "position": all[i].position, "text": all[i].text})
	}
	writeJSON(w, 200, map[string]any{"items": page, "total": len(all)})
}

func (f *fakeServer) postProposals(w http.ResponseWriter, p *fakeProject, body []byte) {
	var in struct {
		Proposals []json.RawMessage `json:"proposals"`
	}
	if err := json.Unmarshal(body, &in); err != nil || len(in.Proposals) > 1000 {
		writeErr(w, 400, "bad proposals body")
		return
	}
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
	writeJSON(w, 200, map[string]any{"upserted": upserted, "ignored": ignored})
}

func (f *fakeServer) getLabels(w http.ResponseWriter, r *http.Request, p *fakeProject) {
	q := r.URL.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > 1000 {
		writeErr(w, 400, "bad limit")
		return
	}
	start := 0
	if c := q.Get("cursor"); c != "" {
		if start, err = strconv.Atoi(c); err != nil {
			writeErr(w, 400, "bad cursor")
			return
		}
	}
	var all []fakeLabel
	for _, l := range p.labels {
		if who := q.Get("collaborator"); who == "" || who == l.collaborator {
			all = append(all, l)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].updatedAt != all[j].updatedAt {
			return all[i].updatedAt < all[j].updatedAt
		}
		return all[i].itemID < all[j].itemID
	})
	end := min(start+limit, len(all))
	page := []map[string]any{}
	for _, l := range all[min(start, end):end] {
		page = append(page, map[string]any{"collaborator": l.collaborator, "updated_at": l.updatedAt, "label": l.raw})
	}
	var next any
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	writeJSON(w, 200, map[string]any{"labels": page, "next_cursor": next})
}
