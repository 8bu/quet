package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/annotate"
)

var bg = context.Background()

func TestNewClientEmptyURL(t *testing.T) {
	if _, err := NewClient(Remote{}); err == nil {
		t.Fatal("NewClient with empty URL: want error")
	}
	if _, err := NewClient(Remote{URL: "http://localhost:8787"}); err != nil {
		t.Fatalf("NewClient without credentials: %v", err)
	}
}

func TestHeadersSent(t *testing.T) {
	f := newFakeServer(t)
	c := f.client(t, "id.access", "s3cret")

	who, err := c.Whoami(bg)
	if err != nil || who != "svc@example.com" {
		t.Fatalf("Whoami = %q, %v", who, err)
	}
	if _, err := c.PutProject(bg, "p", "", json.RawMessage(`{}`), "y"); err != nil {
		t.Fatal(err)
	}

	get := f.requests("GET /api/admin/whoami")[0]
	if got := get.Header.Get("CF-Access-Client-Id"); got != "id.access" {
		t.Errorf("client id header = %q", got)
	}
	if got := get.Header.Get("CF-Access-Client-Secret"); got != "s3cret" {
		t.Errorf("client secret header = %q", got)
	}
	if got := get.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := get.Header.Get("Content-Type"); got != "" {
		t.Errorf("GET Content-Type = %q, want none", got)
	}
	put := f.requests("PUT /api/admin/projects/p")[0]
	if got := put.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("PUT Content-Type = %q", got)
	}
}

func TestHeadersOmittedWithoutCredentials(t *testing.T) {
	f := newFakeServer(t)
	if _, err := f.client(t, "", "").Whoami(bg); err != nil {
		t.Fatal(err)
	}
	h := f.requests("GET /api/admin/whoami")[0].Header
	if h.Get("CF-Access-Client-Id") != "" || h.Get("CF-Access-Client-Secret") != "" {
		t.Errorf("credential headers sent without credentials: %v", h)
	}
}

func TestProjectsAndProject(t *testing.T) {
	f := newFakeServer(t)
	f.seed("b", &fakeProject{name: "B", schemaJSON: json.RawMessage(`{"x":1}`), schemaYAML: "types: [a]"})
	f.seed("a", &fakeProject{name: "A"})
	c := f.client(t, "", "")

	list, err := c.Projects(bg)
	if err != nil || len(list) != 2 || list[0].Slug != "a" || list[1].Name != "B" || list[1].UpdatedAt != 2 {
		t.Fatalf("Projects = %+v, %v", list, err)
	}
	p, err := c.Project(bg, "b")
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "b" || p.Name != "B" || p.SchemaYAML != "types: [a]" || string(p.Schema) != `{"x":1}` {
		t.Errorf("Project = %+v", p)
	}
	if len(p.Collaborators) != 1 || p.Collaborators[0].Username != "alice" ||
		p.Collaborators[0].LastLabelAt == nil || *p.Collaborators[0].LastLabelAt != 5 {
		t.Errorf("Collaborators = %+v", p.Collaborators)
	}
}

func TestProjectNotFound(t *testing.T) {
	f := newFakeServer(t)
	_, err := f.client(t, "", "").Project(bg, "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAPIErrorInvalidList(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p"})
	f.rejectUpdate = []Invalid{
		{"alice", "r1", "annotation_status: \"x\" is not one of [complete]"},
		{"bob", "r2", "bad span"},
		{"bob", "r3", "bad span"},
		{"bob", "r4", "bad span"},
	}
	_, err := f.client(t, "", "").PutProject(bg, "p", "", json.RawMessage(`{}`), "y")
	var api *APIError
	if !errors.As(err, &api) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if api.Status != 409 || api.Message != "stored labels no longer valid" || len(api.Invalid) != 4 {
		t.Fatalf("APIError = %+v", api)
	}
	if api.Invalid[0] != f.rejectUpdate[0] {
		t.Errorf("Invalid[0] = %+v", api.Invalid[0])
	}
	msg := err.Error()
	for _, want := range []string{"409", "stored labels no longer valid", "alice r1", "and 1 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("409 must not match ErrNotFound")
	}
}

func TestAPIErrorAccessNonJSON(t *testing.T) {
	tests := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		status  int
		hint    bool
		body    string
	}{
		{"403 html", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(403)
			_, _ = w.Write([]byte("  <html>Forbidden: " + strings.Repeat("x", 500) + "</html>\n"))
		}, 403, true, "<html>Forbidden: "},
		{"302 login redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://team.cloudflareaccess.com/login", http.StatusFound)
		}, 302, true, ""},
		{"401 text", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no token", 401) }, 401, true, "no token"},
		{"500 text", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }, 500, false, "boom"},
		{"500 empty", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, 500, false, "Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.override = tt.handler
			_, err := f.client(t, "a", "b").Whoami(bg)
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if api.Status != tt.status || len(api.Invalid) != 0 {
				t.Errorf("APIError = %+v", api)
			}
			if got := strings.Contains(api.Message, "quet web login"); got != tt.hint {
				t.Errorf("hint present = %v, want %v (message %q)", got, tt.hint, api.Message)
			}
			if !strings.Contains(api.Message, tt.body) {
				t.Errorf("message %q lacks %q", api.Message, tt.body)
			}
			// The raw body part is at most 200 bytes: the message is that plus the hint.
			if before, _, _ := strings.Cut(api.Message, " - "); len(before) > 200 {
				t.Errorf("body part is %d bytes, want <= 200", len(before))
			}
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("ă", 150) // 300 bytes
	got := truncateUTF8(s, 200)
	if len(got) != 200 || got != strings.Repeat("ă", 100) {
		t.Errorf("len = %d", len(got))
	}
	if got := truncateUTF8(s, 199); len(got) != 198 {
		t.Errorf("cut mid-rune: len = %d, want 198", len(got))
	}
}

// manyItems returns n queue items i0..i(n-1).
func manyItems(n int) []annotate.Item {
	items := make([]annotate.Item, n)
	for i := range items {
		items[i] = annotate.Item{ID: "i" + strconv.Itoa(i), Text: "text " + strconv.Itoa(i)}
	}
	return items
}

func TestPushItemsChunking(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p"})
	c := f.client(t, "", "")
	items := manyItems(2500)

	ins, upd, same, err := c.PushItems(bg, "p", items)
	if err != nil || ins != 2500 || upd != 0 || same != 0 {
		t.Fatalf("PushItems = %d %d %d, %v", ins, upd, same, err)
	}
	posts := f.requests("POST /api/admin/projects/p/items")
	if len(posts) != 3 {
		t.Fatalf("%d POSTs, want 3", len(posts))
	}
	var sizes []int
	for _, r := range posts {
		var body struct {
			Items []struct {
				ID       string `json:"id"`
				Position int    `json:"position"`
			} `json:"items"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(body.Items))
		for _, it := range body.Items {
			if it.ID != "i"+strconv.Itoa(it.Position) {
				t.Fatalf("item %s has position %d", it.ID, it.Position)
			}
		}
	}
	if !reflect.DeepEqual(sizes, []int{1000, 1000, 500}) {
		t.Errorf("chunk sizes = %v", sizes)
	}

	// Re-push: one record edited, one moved.
	items[0].Text = "changed"
	items[1], items[2] = items[2], items[1]
	ins, upd, same, err = c.PushItems(bg, "p", items)
	if err != nil || ins != 0 || upd != 3 || same != 2497 {
		t.Errorf("re-push = %d %d %d, %v", ins, upd, same, err)
	}

	if _, _, _, err := c.PushItems(bg, "p", nil); err != nil {
		t.Errorf("PushItems(nil): %v", err)
	}
	if n := len(f.requests("POST /api/admin/projects/p/items")); n != 6 {
		t.Errorf("%d POSTs after empty push, want 6", n)
	}
}

func TestReplaceProposalsChunking(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p"})
	c := f.client(t, "", "")
	if _, _, _, err := c.PushItems(bg, "p", manyItems(2400)); err != nil {
		t.Fatal(err)
	}
	f.project("p").proposals["stale"] = json.RawMessage(`{"id":"stale"}`)

	var raw []json.RawMessage
	for i := range 2500 { // the last 100 are not items
		raw = append(raw, json.RawMessage(fmt.Sprintf(`{"id":"i%d","annotation_status":"complete","confidence":0.5}`, i)))
	}
	up, ignored, err := c.ReplaceProposals(bg, "p", raw)
	if err != nil || up != 2400 || len(ignored) != 100 || ignored[0] != "i2400" {
		t.Fatalf("ReplaceProposals = %d, %d ignored, %v", up, len(ignored), err)
	}
	calls := f.calls()
	want := []string{"DELETE /api/admin/projects/p/proposals"}
	for range 3 {
		want = append(want, "POST /api/admin/projects/p/proposals")
	}
	if got := calls[len(calls)-4:]; !reflect.DeepEqual(got, want) {
		t.Errorf("last calls = %v, want %v", got, want)
	}
	if _, stale := f.project("p").proposals["stale"]; stale {
		t.Error("stale proposal survived the replace")
	}
	// Extra members are sent as-is.
	if got := string(f.project("p").proposals["i7"]); !strings.Contains(got, `"confidence":0.5`) {
		t.Errorf("proposal i7 = %s", got)
	}

	// An empty list only clears.
	up, ignored, err = c.ReplaceProposals(bg, "p", nil)
	if err != nil || up != 0 || len(ignored) != 0 || len(f.project("p").proposals) != 0 {
		t.Errorf("empty replace = %d %v %v", up, ignored, err)
	}
}

func TestItemsPagination(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p"})
	c := f.client(t, "", "")
	// The fake serves items sorted by position, not by map order.
	items := manyItems(2500)
	if _, _, _, err := c.PushItems(bg, "p", items); err != nil {
		t.Fatal(err)
	}
	got, err := c.Items(bg, "p")
	if err != nil || len(got) != 2500 {
		t.Fatalf("Items = %d, %v", len(got), err)
	}
	for i, it := range got {
		if it.Position != i || it.ID != items[i].ID || it.Text != items[i].Text {
			t.Fatalf("item %d = %+v", i, it)
		}
	}
	gets := f.requests("GET /api/admin/projects/p/items")
	if len(gets) != 3 {
		t.Fatalf("%d GETs, want 3", len(gets))
	}
	for i, off := range []string{"0", "1000", "2000"} {
		if q := gets[i].Query; q["offset"][0] != off || q["limit"][0] != "1000" {
			t.Errorf("page %d query = %v", i, q)
		}
	}

	// A project without items needs one request.
	f.seed("empty", &fakeProject{name: "e"})
	if got, err := c.Items(bg, "empty"); err != nil || len(got) != 0 {
		t.Errorf("Items(empty) = %v, %v", got, err)
	}
}

// seedLabels gives project slug n labels "i0".. for alice and bob, with increasing update times.
func seedLabels(f *fakeServer, slug string, n int) {
	p := f.project(slug)
	for i := range n {
		id := "i" + strconv.Itoa(i)
		for _, who := range []string{"alice", "bob"} {
			p.labels = append(p.labels, fakeLabel{who, id, int64(i), json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))})
		}
	}
}

func TestLabelsPagination(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p"})
	seedLabels(f, "p", 1300) // 2600 labels
	c := f.client(t, "", "")

	all, err := c.Labels(bg, "p", "")
	if err != nil || len(all) != 2600 {
		t.Fatalf("Labels(all) = %d, %v", len(all), err)
	}
	gets := f.requests("GET /api/admin/projects/p/labels")
	if len(gets) != 3 {
		t.Fatalf("%d GETs, want 3", len(gets))
	}
	if _, has := gets[0].Query["cursor"]; has {
		t.Error("first page sent a cursor")
	}
	if _, has := gets[0].Query["collaborator"]; has {
		t.Error("collaborator sent for an everyone pull")
	}
	if gets[1].Query["cursor"][0] != "1000" || gets[2].Query["cursor"][0] != "2000" {
		t.Errorf("cursors = %v, %v", gets[1].Query["cursor"], gets[2].Query["cursor"])
	}
	if all[0].UpdatedAt != 0 || all[2599].UpdatedAt != 1299 {
		t.Errorf("order: first %d, last %d", all[0].UpdatedAt, all[2599].UpdatedAt)
	}

	one, err := c.Labels(bg, "p", "bob")
	if err != nil || len(one) != 1300 {
		t.Fatalf("Labels(bob) = %d, %v", len(one), err)
	}
	for _, l := range one {
		if l.Collaborator != "bob" {
			t.Fatalf("foreign label %+v", l)
		}
	}
	last := f.requests("GET /api/admin/projects/p/labels")
	if got := last[len(last)-1].Query["collaborator"]; len(got) != 1 || got[0] != "bob" {
		t.Errorf("collaborator query = %v", got)
	}
}

func TestLabelsRepeatedCursor(t *testing.T) {
	f := newFakeServer(t)
	f.override = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"labels": []any{}, "next_cursor": "same"})
	}
	if _, err := f.client(t, "", "").Labels(bg, "p", ""); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err = %v, want repeated cursor error", err)
	}
}

func TestContextCancelled(t *testing.T) {
	f := newFakeServer(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := f.client(t, "", "").Whoami(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
