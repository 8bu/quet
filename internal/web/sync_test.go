package web

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/annotate"
)

const (
	multiSchemaPath    = "../../examples/annotation/schema-multispan.yaml"
	multiSchemaGolden  = `{"version":"expense-v1","types":[{"name":"expense","description":"Money the writer paid out."},{"name":"income","description":"Money the writer received."},{"name":"transfer","description":"Money moved between the writer's own accounts or people; there is no counterparty."}],"statuses":[{"name":"complete","description":"Type and every span confidently determined."},{"name":"uncertain","description":"The text does not settle the type or a span."},{"name":"skipped","description":"Not a money note, or not worth labelling."}],"null_label_statuses":["skipped"],"implicit_target":false,"spans":[{"name":"target","description":"Counterparty. Minimal span as typed.","null_for_types":["transfer"],"statuses":[]},{"name":"value","description":"Monetary amount. Exact substring, no normalization.","null_for_types":[],"statuses":["complete","uncertain"]}]}`
	implicitSchemaYAML = "types: [a, b]\nstatuses:\n  complete: Confident\n  skipped:\nnull_target_types: [b]\n"
	implicitGolden     = `{"version":null,"types":[{"name":"a","description":""},{"name":"b","description":""}],"statuses":[{"name":"complete","description":"Confident"},{"name":"skipped","description":""}],"null_label_statuses":["skipped"],"implicit_target":true,"spans":[{"name":"target","description":"","null_for_types":["b"],"statuses":[]}]}`
)

func TestSchemaJSONGolden(t *testing.T) {
	multi, err := annotate.LoadSchema(multiSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	implicit, err := annotate.ParseSchema([]byte(implicitSchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	// No skipped status and no null_label_statuses: [] not null; no null_target_types: [] not null.
	bare, err := annotate.ParseSchema([]byte("version: \"v<1>&\"\ntypes: [a]\nstatuses: [complete]\n"))
	if err != nil {
		t.Fatal(err)
	}
	const bareGolden = `{"version":"v<1>&","types":[{"name":"a","description":""}],"statuses":[{"name":"complete","description":""}],"null_label_statuses":[],"implicit_target":true,"spans":[{"name":"target","description":"","null_for_types":[],"statuses":[]}]}`

	tests := []struct {
		name   string
		schema *annotate.Schema
		want   string
	}{
		{"multi-span example", multi, multiSchemaGolden},
		{"implicit target", implicit, implicitGolden},
		{"empty lists and unescaped HTML", bare, bareGolden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SchemaJSON(tt.schema)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("SchemaJSON =\n%s\nwant\n%s", got, tt.want)
			}
			if !json.Valid(got) {
				t.Error("not valid JSON")
			}
		})
	}
}

// writeTemp writes content to <tempdir>/name and returns the path.
func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// pushFixture holds the files of a push of the multi-span example.
type pushFixture struct {
	dir, queue, proposals string
}

func newPushFixture(t *testing.T) pushFixture {
	t.Helper()
	dir := t.TempDir()
	return pushFixture{
		dir: dir,
		queue: writeTemp(t, dir, "queue.jsonl",
			`{"id":"m1","text":"ăn với Nam ở Pizza 4P 300k","extra":1}`+"\n\n"+`{"id":"m2","text":"Nam trả 50k"}`+"\n"+`{"id":"m3","text":"chuyển 1tr"}`+"\n"),
		proposals: writeTemp(t, dir, "proposals.jsonl",
			`{"id":"m1",  "annotation_status":"complete","type":"expense","confidence":0.9,"reason":"ok"}`+"\n\n  \n"+
				`{"id":"zz","annotation_status":"uncertain"}`+"\n"),
	}
}

func (p pushFixture) input(project string) PushInput {
	return PushInput{QueuePath: p.queue, SchemaPath: multiSchemaPath, Project: project}
}

func TestPushNewProject(t *testing.T) {
	f := newFakeServer(t)
	c := f.client(t, "", "")
	fx := newPushFixture(t)
	in := fx.input("expenses")
	in.ProposalsPath = fx.proposals

	res, err := Push(bg, c, in)
	if err != nil {
		t.Fatal(err)
	}
	want := PushResult{Created: true, Inserted: 3, Proposals: 1, IgnoredProposals: []string{"zz"}}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	wantCalls := []string{
		"GET /api/admin/projects/expenses", // existence check for the name rule
		"PUT /api/admin/projects/expenses",
		"POST /api/admin/projects/expenses/items",
		"DELETE /api/admin/projects/expenses/proposals",
		"POST /api/admin/projects/expenses/proposals",
	}
	if got := f.calls(); !reflect.DeepEqual(got, wantCalls) {
		t.Errorf("calls = %v\nwant    %v", got, wantCalls)
	}

	var put map[string]json.RawMessage
	if err := json.Unmarshal(f.requests("PUT /api/admin/projects/expenses")[0].Body, &put); err != nil {
		t.Fatal(err)
	}
	if string(put["name"]) != `"expenses"` {
		t.Errorf("name on create = %s, want the slug", put["name"])
	}
	if string(put["schema"]) != multiSchemaGolden {
		t.Errorf("schema sent = %s", put["schema"])
	}
	yaml, _ := os.ReadFile(multiSchemaPath)
	var sentYAML string
	_ = json.Unmarshal(put["schema_yaml"], &sentYAML)
	if sentYAML != string(yaml) {
		t.Error("schema_yaml is not the schema file verbatim")
	}

	p := f.project("expenses")
	if p.items["m2"].position != 1 || p.items["m3"].position != 2 || p.items["m1"].text != "ăn với Nam ở Pizza 4P 300k" {
		t.Errorf("items = %+v %+v %+v", p.items["m1"], p.items["m2"], p.items["m3"])
	}
	// Proposals are sent raw: members and spacing kept as in the file line.
	if got := string(p.proposals["m1"]); got != `{"id":"m1","annotation_status":"complete","type":"expense","confidence":0.9,"reason":"ok"}` {
		t.Errorf("proposal m1 = %s", got)
	}
}

func TestPushExistingKeepsNameAndProposals(t *testing.T) {
	f := newFakeServer(t)
	c := f.client(t, "", "")
	fx := newPushFixture(t)
	if _, err := Push(bg, c, PushInput{QueuePath: fx.queue, SchemaPath: multiSchemaPath, Project: "p", Name: "Pretty name", ProposalsPath: fx.proposals}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.reqs = nil
	f.mu.Unlock()

	// Edit one record, then re-push without a name and without proposals.
	writeTemp(t, fx.dir, "queue.jsonl", `{"id":"m1","text":"edited"}`+"\n"+`{"id":"m2","text":"Nam trả 50k"}`+"\n"+`{"id":"m3","text":"chuyển 1tr"}`+"\n")
	res, err := Push(bg, c, fx.input("p"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.Inserted != 0 || res.Updated != 1 || res.Unchanged != 2 || res.Proposals != 0 || res.IgnoredProposals != nil {
		t.Errorf("result = %+v", res)
	}
	wantCalls := []string{"GET /api/admin/projects/p", "PUT /api/admin/projects/p", "POST /api/admin/projects/p/items"}
	if got := f.calls(); !reflect.DeepEqual(got, wantCalls) {
		t.Errorf("calls = %v, want %v (no proposals call: they stay untouched)", got, wantCalls)
	}
	var put map[string]json.RawMessage
	_ = json.Unmarshal(f.requests("PUT /api/admin/projects/p")[0].Body, &put)
	if _, has := put["name"]; has {
		t.Errorf("name sent on update without --name: %s", put["name"])
	}
	p := f.project("p")
	if p.name != "Pretty name" || len(p.proposals) != 1 {
		t.Errorf("name = %q, proposals = %d; want kept", p.name, len(p.proposals))
	}
}

func TestPushExistingWithNameSkipsLookup(t *testing.T) {
	f := newFakeServer(t)
	c := f.client(t, "", "")
	fx := newPushFixture(t)
	f.seed("p", &fakeProject{name: "old"})
	in := fx.input("p")
	in.Name = "New name"
	res, err := Push(bg, c, in)
	if err != nil || res.Created {
		t.Fatalf("Push = %+v, %v", res, err)
	}
	if got := f.calls(); got[0] != "PUT /api/admin/projects/p" {
		t.Errorf("calls = %v; a given name needs no existence check", got)
	}
	if f.project("p").name != "New name" {
		t.Errorf("name = %q", f.project("p").name)
	}
}

func TestPushEmptyProposalsFileClears(t *testing.T) {
	f := newFakeServer(t)
	c := f.client(t, "", "")
	fx := newPushFixture(t)
	f.seed("p", &fakeProject{name: "p", proposals: map[string]json.RawMessage{"old": json.RawMessage(`{}`)}})
	in := fx.input("p")
	in.ProposalsPath = writeTemp(t, fx.dir, "empty.jsonl", "\n")
	if _, err := Push(bg, c, in); err != nil {
		t.Fatal(err)
	}
	if len(f.project("p").proposals) != 0 {
		t.Error("an empty proposals file must clear the server proposals")
	}
}

func TestPushValidatesLocallyFirst(t *testing.T) {
	fx := newPushFixture(t)
	badSchema := writeTemp(t, fx.dir, "bad.yaml", "types: [a]\n")
	dupQueue := writeTemp(t, fx.dir, "dup.jsonl", `{"id":"a","text":"x"}`+"\n"+`{"id":"a","text":"y"}`+"\n")
	prop := func(name, content string) string { return writeTemp(t, fx.dir, name, content) }

	tests := []struct {
		name string
		mod  func(in *PushInput)
		want string
	}{
		{"slug", func(in *PushInput) { in.Project = "Bad Slug" }, "invalid project slug"},
		{"missing schema", func(in *PushInput) { in.SchemaPath = filepath.Join(fx.dir, "none.yaml") }, "read schema"},
		{"invalid schema", func(in *PushInput) { in.SchemaPath = badSchema }, "statuses"},
		{"missing queue", func(in *PushInput) { in.QueuePath = filepath.Join(fx.dir, "none.jsonl") }, "read queue"},
		{"duplicate queue ids", func(in *PushInput) { in.QueuePath = dupQueue }, "duplicate"},
		{"missing proposals", func(in *PushInput) { in.ProposalsPath = filepath.Join(fx.dir, "none.jsonl") }, "read proposals"},
		{"proposal not an object", func(in *PushInput) { in.ProposalsPath = prop("p1.jsonl", "[1]\n") }, "p1.jsonl:1"},
		{"proposal invalid JSON", func(in *PushInput) { in.ProposalsPath = prop("p2.jsonl", "{\n") }, "invalid JSON"},
		{"proposal id not a string", func(in *PushInput) {
			in.ProposalsPath = prop("p3.jsonl", `{"id":"a","annotation_status":"complete"}`+"\n"+`{"id":5,"annotation_status":"complete"}`+"\n")
		}, `p3.jsonl:2: field "id"`},
		{"proposal status missing", func(in *PushInput) { in.ProposalsPath = prop("p4.jsonl", `{"id":"a"}`+"\n") }, `field "annotation_status"`},
		{"proposal status null", func(in *PushInput) { in.ProposalsPath = prop("p5.jsonl", `{"id":"a","annotation_status":null}`+"\n") }, `field "annotation_status"`},
		{"proposal empty id", func(in *PushInput) { in.ProposalsPath = prop("p6.jsonl", `{"id":"","annotation_status":"x"}`+"\n") }, "empty id"},
		{"proposal duplicate id", func(in *PushInput) {
			in.ProposalsPath = prop("p7.jsonl", `{"id":"a","annotation_status":"x"}`+"\n"+`{"id":"a","annotation_status":"y"}`+"\n")
		}, "duplicate id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeServer(t)
			in := fx.input("p")
			tt.mod(&in)
			_, err := Push(bg, f.client(t, "", ""), in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if calls := f.calls(); len(calls) != 0 {
				t.Errorf("server was called before local validation passed: %v", calls)
			}
		})
	}
}

func TestPushServerRejection(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p"})
	f.rejectUpdate = []Invalid{{"alice", "m1", "bad"}}
	_, err := Push(bg, f.client(t, "", ""), newPushFixture(t).input("p"))
	var api *APIError
	if !errors.As(err, &api) || api.Status != 409 || len(api.Invalid) != 1 {
		t.Fatalf("err = %v, want 409 APIError with invalid list", err)
	}
	for _, c := range f.calls() {
		if strings.Contains(c, "/items") {
			t.Errorf("items pushed after the project update failed: %v", f.calls())
		}
	}
}

// Pull fixtures: the multi-span example schema and four records.
const (
	pullText1 = "ăn với Nam ở Pizza 4P 300k"
	pullText2 = "Nam trả 50k"
)

func seedPullProject(t *testing.T, f *fakeServer) {
	t.Helper()
	yaml, err := os.ReadFile(multiSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]*fakeItem{
		"m3": {"m3", "chuyển 1tr", 2},
		"m1": {"m1", pullText1, 0},
		"m2": {"m2", pullText2, 1},
	}
	f.seed("p", &fakeProject{name: "p", schemaYAML: string(yaml), items: items})
	add := func(who, id string, at int64, raw string) {
		p := f.project("p")
		p.labels = append(p.labels, fakeLabel{who, id, at, json.RawMessage(raw)})
	}
	// alice: a valid multi-span label, and one with an undeclared status.
	add("alice", "m1", 1, `{"id":"m1","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21},"value":{"text":"300k","start":22,"end":26},"span_status":{"value":"uncertain"},"note":"ghi chú"}`)
	add("alice", "m2", 2, `{"id":"m2","annotation_status":"bogus","type":"income","target":null,"value":null}`)
	// bob: valid, span text that is not in the record, unknown record, undecodable ones.
	add("bob", "m1", 3, `{"id":"m1","annotation_status":"skipped","type":null,"target":null,"value":null}`)
	add("bob", "m2", 4, `{"id":"m2","annotation_status":"complete","type":"income","target":{"text":"Nam","start":0,"end":3},"value":{"text":"50k","start":8,"end":11}}`)
	add("bob", "m3", 5, `{"id":"m3","annotation_status":"complete","type":"transfer","target":null,"value":{"text":"zzz","start":0,"end":3}}`)
	add("bob", "m9", 6, `{"id":"m9","annotation_status":"skipped","type":null,"target":null,"value":null}`)
	add("bob", "m4", 7, `{"id":"m4","annotation_status":5}`)
	add("bob", "m5", 8, `"not an object"`)
}

func TestPull(t *testing.T) {
	f := newFakeServer(t)
	seedPullProject(t, f)
	got, err := Pull(bg, f.client(t, "", ""), "p", "")
	if err != nil {
		t.Fatal(err)
	}

	if got.Project.Slug != "p" || got.Schema.ImplicitTarget || len(got.Schema.Spans) != 2 {
		t.Errorf("project/schema = %+v / %+v", got.Project, got.Schema)
	}
	wantItems := []annotate.Item{{ID: "m1", Text: pullText1}, {ID: "m2", Text: pullText2}, {ID: "m3", Text: "chuyển 1tr"}}
	if !reflect.DeepEqual(got.Items, wantItems) {
		t.Errorf("Items = %+v", got.Items)
	}

	// Valid labels: alice m1 (a multi-span label), bob m1 (skipped, all null), bob m2.
	if len(got.Labels) != 2 || len(got.Labels["alice"]) != 1 || len(got.Labels["bob"]) != 2 {
		t.Fatalf("Labels = %+v", got.Labels)
	}
	a := got.Labels["alice"]["m1"]
	if a.Status != "complete" || a.Type == nil || *a.Type != "expense" || a.Note != "ghi chú" ||
		a.Spans["target"] == nil || a.Spans["target"].Text != "Pizza 4P" || a.Spans["target"].Start != 13 ||
		a.Spans["value"] == nil || a.Spans["value"].End != 26 || a.SpanStatus["value"] != "uncertain" {
		t.Errorf("alice m1 = %+v", a)
	}
	if b := got.Labels["bob"]["m1"]; b.Status != "skipped" || b.Type != nil {
		t.Errorf("bob m1 = %+v", b)
	}

	// Everything else lands in Invalid, in server order, and never in Labels.
	wantInvalid := map[[2]string]string{
		{"alice", "m2"}: "annotation_status",
		{"bob", "m3"}:   "value",
		{"bob", "m9"}:   "not in the project",
		{"bob", "m4"}:   "",
		{"bob", ""}:     "invalid JSON object",
	}
	if len(got.Invalid) != len(wantInvalid) {
		t.Fatalf("Invalid = %+v", got.Invalid)
	}
	for _, inv := range got.Invalid {
		want, ok := wantInvalid[[2]string{inv.Collaborator, inv.ID}]
		if !ok || inv.Error == "" || !strings.Contains(inv.Error, want) {
			t.Errorf("unexpected Invalid %+v (want error containing %q)", inv, want)
		}
		if _, ok := got.Labels[inv.Collaborator][inv.ID]; ok {
			t.Errorf("invalid label %s/%s also in Labels", inv.Collaborator, inv.ID)
		}
	}
	if got.Invalid[0].Collaborator != "alice" || got.Invalid[len(got.Invalid)-1].Collaborator != "bob" || got.Invalid[len(got.Invalid)-1].ID != "" {
		t.Errorf("Invalid order = %+v", got.Invalid)
	}
}

func TestPullOneCollaborator(t *testing.T) {
	f := newFakeServer(t)
	seedPullProject(t, f)
	got, err := Pull(bg, f.client(t, "", ""), "p", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 1 || len(got.Labels["alice"]) != 1 || len(got.Invalid) != 1 || got.Invalid[0].ID != "m2" {
		t.Errorf("Labels = %+v, Invalid = %+v", got.Labels, got.Invalid)
	}
	if got, err := Pull(bg, f.client(t, "", ""), "p", "nobody"); err != nil || got.Labels == nil || len(got.Labels) != 0 || len(got.Invalid) != 0 {
		t.Errorf("Pull(nobody) = %+v, %v", got, err)
	}
}

func TestPullErrors(t *testing.T) {
	f := newFakeServer(t)
	c := f.client(t, "", "")
	if _, err := Pull(bg, c, "missing", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project: err = %v, want ErrNotFound", err)
	}
	f.seed("broken", &fakeProject{name: "b", schemaYAML: "types: [a]\n"})
	if _, err := Pull(bg, c, "broken", ""); err == nil || !strings.Contains(err.Error(), "schema of project broken") {
		t.Errorf("broken schema: err = %v", err)
	}
}

// TestPullImplicitSchema decodes labels of a schema without spans (single implicit target).
func TestPullImplicitSchema(t *testing.T) {
	f := newFakeServer(t)
	f.seed("p", &fakeProject{name: "p", schemaYAML: implicitSchemaYAML, items: map[string]*fakeItem{"a": {"a", "cho Nam vay 500k", 0}}})
	p := f.project("p")
	p.labels = []fakeLabel{{"alice", "a", 1, json.RawMessage(`{"id":"a","annotation_status":"complete","type":"a","target":{"text":"Nam","start":4,"end":7}}`)}}
	got, err := Pull(bg, f.client(t, "", ""), "p", "")
	if err != nil || !got.Schema.ImplicitTarget || len(got.Invalid) != 0 || got.Labels["alice"]["a"].Spans["target"].Text != "Nam" {
		t.Fatalf("Pull = %+v, %v", got, err)
	}
}
