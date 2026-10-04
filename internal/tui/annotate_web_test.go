package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/web"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"queue":                        "queue",
		"Expenses 2026":                "expenses-2026",
		"  --Gidi__notes!!v2--  ":      "gidi-notes-v2",
		"Chi tiêu":                     "chi-ti-u",
		"???":                          "",
		strings.Repeat("a", 80):        strings.Repeat("a", 63),
		strings.Repeat("a", 62) + "-b": strings.Repeat("a", 62),
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWebMenuShowsLinkStatus(t *testing.T) {
	webEnv(t)
	m, labelsPath := annotTestModel(t)
	m, _ = webPress(t, m, "w")
	if m.mode != annotWebMenu {
		t.Fatalf("mode = %d, want the web menu", m.mode)
	}
	v := m.View()
	for _, want := range []string{"Web", "Link: not linked", "Remote & project…", "Publish", "Compare collaborators"} {
		if !strings.Contains(v, want) {
			t.Errorf("menu lacks %q:\n%s", want, v)
		}
	}
	m, _ = webPress(t, m, "esc")
	if m.mode != annotMain {
		t.Fatalf("esc left mode %d", m.mode)
	}
	if err := web.SaveLink(labelsPath, web.Link{Remote: "origin", Project: "expenses"}); err != nil {
		t.Fatal(err)
	}
	m, _ = webPress(t, m, "w")
	if v := m.View(); !strings.Contains(v, "Link: origin/expenses") {
		t.Errorf("menu does not show the link:\n%s", v)
	}
}

func TestWebLinkFormSavesRemoteAndSidecar(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.projects["other"] = &fakeWebProject{name: "Other", proposals: map[string]bool{}}
	m, labelsPath := annotTestModel(t)

	m, _ = webPress(t, m, "w", "enter") // Remote & project…; no remote yet opens the form
	if m.mode != annotWebForm {
		t.Fatalf("mode = %d, want the remote form", m.mode)
	}
	v := m.View()
	for _, want := range []string{"Add remote", "Name", "URL", "Client ID", "Client secret", "origin", remoteURLPrefill} {
		if !strings.Contains(v, want) {
			t.Errorf("form lacks %q:\n%s", want, v)
		}
	}
	m, _ = webPress(t, m, "enter") // name → URL
	for range remoteURLPrefill {
		m, _ = webPress(t, m, "backspace")
	}
	m, _ = sendAnnot(m, pasteKey(f.URL), webKey("enter"), webKey("right"), webKey("enter"), pasteKey("id.access"), webKey("enter"), pasteKey("sekret"))
	v = m.View()
	if !strings.Contains(v, "••••••") || strings.Contains(v, "sekret") {
		t.Errorf("secret not masked:\n%s", v)
	}

	m, cmd := webPress(t, m, "enter") // save
	data, err := os.ReadFile(web.RemotesPath())
	if err != nil {
		t.Fatalf("remotes.yaml not saved: %v", err)
	}
	for _, want := range []string{"default: origin", "url: " + f.URL, "client_id: id.access", "client_secret: sekret"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("remotes.yaml lacks %q:\n%s", want, data)
		}
	}
	if st, _ := os.Stat(web.RemotesPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("remotes.yaml mode = %v, want 0600", st.Mode().Perm())
	}
	if m.mode != annotWebBusy || !strings.Contains(m.View(), "loading projects from origin") {
		t.Errorf("no loading screen while the projects load (mode %d):\n%s", m.mode, m.View())
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Fatalf("Update made network calls itself: %v", calls)
	}

	m = runWeb(t, m, cmd)
	if m.mode != annotWebProjects {
		t.Fatalf("mode = %d, want the project picker; status %q", m.mode, m.status)
	}
	v = m.View()
	for _, want := range []string{"other", "new project: queue"} {
		if !strings.Contains(v, want) {
			t.Errorf("project picker lacks %q:\n%s", want, v)
		}
	}
	m, _ = webPress(t, m, "enter") // the "new project: queue" row
	if got := sidecar(t, labelsPath); got != "remote: origin\nproject: queue\n" {
		t.Errorf("sidecar = %q", got)
	}
	if m.mode != annotWebMenu || !strings.Contains(m.View(), "linked to origin/queue") {
		t.Errorf("not back on the menu with the link (mode %d):\n%s", m.mode, m.View())
	}
	if !m.web.linked || m.web.link != (web.Link{Remote: "origin", Project: "queue"}) {
		t.Errorf("link = %+v linked=%v", m.web.link, m.web.linked)
	}
}

func TestWebFormRejectsBadRemote(t *testing.T) {
	webEnv(t)
	m, _ := annotTestModel(t)
	m, _ = webPress(t, m, "w", "enter")
	m, _ = webPress(t, m, "tab")
	m, _ = sendAnnot(m, pasteKey("not a url"))
	m, _ = webPress(t, m, "ctrl+s")
	if m.mode != annotWebForm || !m.statusErr {
		t.Errorf("a bad URL was accepted (mode %d, status %q)", m.mode, m.status)
	}
	if _, err := os.Stat(web.RemotesPath()); err == nil {
		t.Error("remotes.yaml written for an invalid remote")
	}
}

func TestWebPicksExistingRemoteAndProject(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.compareFixture()
	f.projects["zeta"] = &fakeWebProject{name: "Zeta", proposals: map[string]bool{}}
	saveFakeRemote(t, f)
	m, labelsPath := annotTestModel(t)

	m, _ = webPress(t, m, "w", "r")
	if m.mode != annotWebRemotes {
		t.Fatalf("mode = %d, want the remote picker", m.mode)
	}
	v := m.View()
	for _, want := range []string{"origin", f.URL, "credentials set", "(default)", "+ add a remote…"} {
		if !strings.Contains(v, want) {
			t.Errorf("remote picker lacks %q:\n%s", want, v)
		}
	}
	m, cmd := webPress(t, m, "enter")
	m = runWeb(t, m, cmd)
	// The queue is queue.jsonl, so the picker starts on the existing project "queue".
	if m.mode != annotWebProjects || m.web.projCursor != 0 {
		t.Fatalf("mode %d cursor %d, want the picker on project queue", m.mode, m.web.projCursor)
	}
	m, _ = webPress(t, m, "j", "enter") // zeta
	if got := sidecar(t, labelsPath); got != "remote: origin\nproject: zeta\n" {
		t.Errorf("sidecar = %q", got)
	}

	// The slug of the new project is editable: letters are lowercased and cleaned.
	m, cmd = webPress(t, m, "r", "enter")
	m = runWeb(t, m, cmd)
	if m.web.projCursor != 1 {
		t.Fatalf("cursor = %d, want the linked project zeta", m.web.projCursor)
	}
	m, _ = webPress(t, m, "down") // zeta → new project row
	if m.web.projCursor != 2 {
		t.Fatalf("cursor = %d, want the new project row", m.web.projCursor)
	}
	for range "queue" {
		m, _ = webPress(t, m, "backspace")
	}
	m, _ = sendAnnot(m, typed("My Proj_1")...)
	if got := string(m.web.newSlug); got != "my-proj-1" {
		t.Fatalf("slug = %q, want my-proj-1", got)
	}
	m, _ = webPress(t, m, "enter")
	if got := sidecar(t, labelsPath); got != "remote: origin\nproject: my-proj-1\n" {
		t.Errorf("sidecar = %q", got)
	}
}

func TestWebPublishPushesQueue(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	saveFakeRemote(t, f)
	m, labelsPath := annotTestModel(t)
	if err := web.SaveLink(labelsPath, web.Link{Remote: "origin", Project: "fresh"}); err != nil {
		t.Fatal(err)
	}

	m, _ = webPress(t, m, "w", "p")
	if m.mode != annotWebConfirm {
		t.Fatalf("mode = %d, want the publish confirmation", m.mode)
	}
	v := m.View()
	for _, want := range []string{"Publish to origin/fresh", "(5 records)", "queue.jsonl", "schema.yaml", "stay untouched"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, v)
		}
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Fatalf("calls before the confirmation: %v", calls)
	}

	m, cmd := webPress(t, m, "enter")
	if m.mode != annotWebBusy || !strings.Contains(m.status, "publishing to origin/fresh") {
		t.Errorf("no publishing status (mode %d, status %q)", m.mode, m.status)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Fatalf("Update made network calls itself: %v", calls)
	}
	m = runWeb(t, m, cmd)

	calls := f.callList()
	for _, want := range []string{"PUT /api/admin/projects/fresh", "POST /api/admin/projects/fresh/items"} {
		if !slices.Contains(calls, want) {
			t.Errorf("calls %v lack %q", calls, want)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, "proposals") {
			t.Errorf("proposals touched without a proposals file: %v", calls)
		}
	}
	p := f.project("fresh")
	if p == nil || len(p.items) != len(annotQueue) || p.schemaYAML != annotSchemaYAML {
		t.Fatalf("server project = %+v", p)
	}
	if m.mode != annotWebMenu || !strings.Contains(m.web.note, "published origin/fresh: created, 5 items (5 new, 0 updated, 0 unchanged)") {
		t.Errorf("result not reported (mode %d): %q", m.mode, m.web.note)
	}
	if !strings.Contains(m.View(), "published origin/fresh") {
		t.Errorf("menu does not show the result:\n%s", m.View())
	}
}

func TestWebPublishSendsProposals(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	saveFakeRemote(t, f)
	m, labelsPath := annotProposalModel(t, annotProposalsJSONL(t))
	if err := web.SaveLink(labelsPath, web.Link{Remote: "origin", Project: "fresh"}); err != nil {
		t.Fatal(err)
	}
	m, _ = webPress(t, m, "w", "p")
	if v := m.View(); !strings.Contains(v, "proposals.jsonl") || !strings.Contains(v, "replaces the server's proposals") {
		t.Errorf("confirmation does not name the proposals:\n%s", v)
	}
	m, cmd := webPress(t, m, "enter")
	m = runWeb(t, m, cmd)
	if !slices.Contains(f.callList(), "POST /api/admin/projects/fresh/proposals") {
		t.Errorf("proposals not pushed: %v", f.callList())
	}
	if !strings.Contains(m.web.note, "3 proposals") || !strings.Contains(m.web.note, "1 proposal ids ignored") {
		t.Errorf("note = %q", m.web.note)
	}
}

func TestWebPublishRefusedWithoutQueueFile(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	saveFakeRemote(t, f)
	labels := filepath.Join(t.TempDir(), "labels.jsonl")
	if err := os.WriteFile(labels, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	schema, err := annotate.ParseSchema([]byte(annotSchemaYAML))
	if err != nil {
		t.Fatal(err)
	}
	s, err := annotate.OpenRecheckItems(schema, annotQueue, labels)
	if err != nil {
		t.Fatal(err)
	}
	m := newAnnotModel(s)
	if err := web.SaveLink(labels, web.Link{Remote: "origin", Project: "fresh"}); err != nil {
		t.Fatal(err)
	}
	m, cmd := webPress(t, m, "w", "p")
	if cmd == nil || m.mode != annotWebMenu || !m.web.noteErr || !strings.Contains(m.web.note, "publish needs a queue file") {
		t.Errorf("publish not refused (mode %d): %q", m.mode, m.web.note)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Errorf("calls: %v", calls)
	}
}

func TestWebPublishWithoutLinkOpensLinkFlowFirst(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.compareFixture()
	saveFakeRemote(t, f)
	m, labelsPath := annotTestModel(t)
	m, _ = webPress(t, m, "w", "p")
	if m.mode != annotWebRemotes {
		t.Fatalf("mode = %d, want the remote picker first", m.mode)
	}
	m, cmd := webPress(t, m, "enter")
	m = runWeb(t, m, cmd)
	m, _ = webPress(t, m, "enter") // project queue
	if m.mode != annotWebConfirm {
		t.Fatalf("mode = %d, want the publish confirmation after linking", m.mode)
	}
	if got := sidecar(t, labelsPath); got != "remote: origin\nproject: queue\n" {
		t.Errorf("sidecar = %q", got)
	}
}

func TestWebErrorsShowVerbatim(t *testing.T) {
	m, f, _ := linkedModel(t)
	f.rejectStatus, f.rejectBody = 500, `{"error":"boom"}`
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	if m.mode != annotWebMenu || !m.web.noteErr || !strings.Contains(m.web.note, "quet-web: boom (HTTP 500)") {
		t.Errorf("JSON error not shown (mode %d): %q", m.mode, m.web.note)
	}
	if !m.statusErr || !strings.Contains(m.status, "quet-web: boom (HTTP 500)") {
		t.Errorf("status row = %q", m.status)
	}

	f.rejectStatus, f.rejectBody = 403, "<html>Access denied</html>"
	m, cmd = webPress(t, m, "c")
	m = runWeb(t, m, cmd)
	if !strings.Contains(m.web.note, "HTTP 403") || !strings.Contains(m.web.note, "Cloudflare Access rejected the credentials (run `quet web login`)") {
		t.Errorf("Access hint missing: %q", m.web.note)
	}
	if !strings.Contains(m.View(), "Cloudflare Access") {
		t.Errorf("the menu does not show the hint:\n%s", m.View())
	}
}

func TestWebBusyEscCancelsAndDropsLateResult(t *testing.T) {
	m, f, _ := linkedModel(t)
	m, cmd := webPress(t, m, "w", "c")
	if m.mode != annotWebBusy || !strings.Contains(m.status, "pulling collaborators from origin/queue") {
		t.Fatalf("no pulling status (mode %d): %q", m.mode, m.status)
	}
	if calls := f.callList(); len(calls) != 0 {
		t.Fatalf("Update made network calls itself: %v", calls)
	}
	m, _ = webPress(t, m, "esc")
	if m.mode != annotWebMenu {
		t.Fatalf("esc did not leave the busy screen: mode %d", m.mode)
	}
	next, _ := m.update(cmd())
	if next.mode != annotWebMenu || next.web.note != "" {
		t.Errorf("a cancelled pull still changed the screen: mode %d note %q", next.mode, next.web.note)
	}
}

func TestWebLegacyMainRenderingUnchanged(t *testing.T) {
	webEnv(t)
	m, _ := annotTestModel(t)
	legacy := []string{"t type", "x target", "n null", "enter complete", "u uncertain", "s skip", "a/d prev/next",
		"[/] prev/next unfinished", "z undo", ": go to", "f filter", "? help", "q quit"}
	got := slices.DeleteFunc(m.footerItems(), func(s string) bool { return s == "w web" })
	if !slices.Equal(got, legacy) {
		t.Errorf("footer without the w item = %q, want %q", got, legacy)
	}
	if n := strings.Count(strings.Join(m.footerItems(), "|"), "w web"); n != 1 {
		t.Errorf("footer has %d w web items", n)
	}
	before := m.View()
	for _, banned := range []string{"Link", "Compare", "quet-web", "Publish"} {
		if strings.Contains(before, banned) {
			t.Errorf("main view mentions %q:\n%s", banned, before)
		}
	}
	m, _ = webPress(t, m, "w", "esc")
	if after := m.View(); after != before {
		t.Errorf("opening and closing the web menu changed the main view:\n--- before\n%s\n--- after\n%s", before, after)
	}
	m, _ = webPress(t, m, "?")
	if v := m.View(); !strings.Contains(v, "Web menu") {
		t.Errorf("help lacks the w entry:\n%s", v)
	}
}

func TestWebModesRenderOnSmallScreens(t *testing.T) {
	m, _, _ := openCompareModel(t)
	for _, size := range [][2]int{{80, 24}, {40, 10}, {20, 5}, {10, 3}, {5, 1}} {
		m, _ = m.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, mode := range []annotMode{annotWebMenu, annotWebRemotes, annotWebForm, annotWebProjects, annotWebConfirm, annotWebBusy, annotCompare} {
			m.mode = mode
			m.web.remotes = &web.Remotes{List: []web.Remote{{Name: "origin", URL: "http://x"}}}
			_ = m.View()
		}
	}
}
