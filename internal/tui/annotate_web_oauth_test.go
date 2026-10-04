package tui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/web"
)

// fakeAccess is a fake Cloudflare Access Managed OAuth server in front of the fake admin API: RFC 9728 and RFC
// 8414 discovery, RFC 7591 registration, an authorize endpoint that redirects straight to the callback (so no
// browser is needed), and an RFC 6749 token endpoint with PKCE and refresh token rotation. Every /api/admin
// request needs a live bearer token, else it answers 401 with a resource_metadata challenge.
type fakeAccess struct {
	*httptest.Server
	api *fakeWeb

	mu      sync.Mutex
	n       int
	clients map[string]string     // registered client id → redirect URI
	codes   map[string]accessCode // authorization code → what it was issued for
	access  map[string]bool       // live access tokens
	refresh map[string]string     // live refresh token → client id
	opened  []string              // URLs handed to the injected browser
}

// accessCode is one issued authorization code.
type accessCode struct{ clientID, challenge, redirect string }

// newFakeAccess starts the fake server over a fresh fake admin API, closed when the test ends.
func newFakeAccess(t *testing.T) *fakeAccess {
	t.Helper()
	a := &fakeAccess{
		api:     &fakeWeb{projects: map[string]*fakeWebProject{}},
		clients: map[string]string{},
		codes:   map[string]accessCode{},
		access:  map[string]bool{},
		refresh: map[string]string{},
	}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.Close)
	return a
}

// browser is the injected OpenBrowser: it follows the authorize URL like a logged-in browser would, in the
// background because the callback only answers once Login reads it.
func (a *fakeAccess) browser(u string) error {
	a.mu.Lock()
	a.opened = append(a.opened, u)
	a.mu.Unlock()
	go func() {
		resp, err := http.Get(u)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	return nil
}

// openedURLs returns the URLs handed to the injected browser.
func (a *fakeAccess) openedURLs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.opened)
}

// seed registers a refresh token of client clientID, as if an earlier login had issued it.
func (a *fakeAccess) seed(clientID, refreshToken string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh[refreshToken] = clientID
}

// mint issues a new access and refresh token pair for clientID. The caller holds a.mu.
func (a *fakeAccess) mint(clientID string) (accessToken, refreshToken string) {
	a.n++
	accessToken, refreshToken = fmt.Sprintf("at-%d", a.n), fmt.Sprintf("rt-%d", a.n)
	a.access[accessToken] = true
	a.refresh[refreshToken] = clientID
	return accessToken, refreshToken
}

// serve answers one request of the fake server.
func (a *fakeAccess) serve(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource":
		reply(200, map[string]any{"resource": a.URL, "authorization_servers": []string{a.URL}})
	case "/.well-known/oauth-authorization-server":
		reply(200, map[string]any{
			"issuer":                           a.URL,
			"authorization_endpoint":           a.URL + "/authorize",
			"token_endpoint":                   a.URL + "/token",
			"registration_endpoint":            a.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	case "/register":
		var in struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.RedirectURIs) != 1 ||
			!strings.HasPrefix(in.RedirectURIs[0], "http://127.0.0.1:") {
			reply(400, map[string]string{"error": "invalid_redirect_uri"})
			return
		}
		a.mu.Lock()
		a.n++
		id := fmt.Sprintf("client-%d", a.n)
		a.clients[id] = in.RedirectURIs[0]
		a.mu.Unlock()
		reply(201, map[string]any{"client_id": id, "redirect_uris": in.RedirectURIs})
	case "/authorize":
		a.authorize(w, r)
	case "/token":
		a.token(w, r)
	case "/api/admin/whoami":
		if a.bearerOK(r) {
			reply(200, map[string]string{"identity": "admin@example.com"})
			return
		}
		a.challenge(w)
	default:
		if !a.bearerOK(r) {
			a.challenge(w)
			return
		}
		a.api.serve(w, r)
	}
}

// bearerOK reports whether the request carries a live access token.
func (a *fakeAccess) bearerOK(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	a.mu.Lock()
	defer a.mu.Unlock()
	return ok && a.access[tok]
}

// challenge answers 401 with the RFC 9728 resource_metadata pointer.
func (a *fakeAccess) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, a.URL))
	w.WriteHeader(http.StatusUnauthorized)
}

// authorize redirects straight to the registered redirect URI with a code and the state, like a browser that is
// already logged in.
func (a *fakeAccess) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.mu.Lock()
	redirect, ok := a.clients[q.Get("client_id")]
	a.mu.Unlock()
	if !ok || q.Get("redirect_uri") != redirect || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.n++
	code := fmt.Sprintf("code-%d", a.n)
	a.codes[code] = accessCode{clientID: q.Get("client_id"), challenge: q.Get("code_challenge"), redirect: redirect}
	a.mu.Unlock()
	to := redirect + "?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	http.Redirect(w, r, to, http.StatusFound)
}

// token exchanges an authorization code (checking PKCE) or rotates a refresh token.
func (a *fakeAccess) token(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if err := r.ParseForm(); err != nil {
		reply(400, map[string]string{"error": "invalid_request"})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var clientID string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		c, ok := a.codes[r.PostForm.Get("code")]
		delete(a.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || c.clientID != r.PostForm.Get("client_id") || c.redirect != r.PostForm.Get("redirect_uri") ||
			c.challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
			reply(400, map[string]string{"error": "invalid_grant"})
			return
		}
		clientID = c.clientID
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		id, ok := a.refresh[rt]
		if !ok || id != r.PostForm.Get("client_id") {
			reply(400, map[string]string{"error": "invalid_grant"})
			return
		}
		delete(a.refresh, rt)
		clientID = id
	default:
		reply(400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	at, rt := a.mint(clientID)
	reply(200, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": 3600, "refresh_token": rt})
}

// loginPump feeds the messages of a browser login back into the model, the way the program would, until the
// login result arrived. It returns the model after the result, the command that result produced and the URLs the
// login announced. A login that does not finish within five seconds fails the test.
func loginPump(t *testing.T, m annotModel, cmd tea.Cmd) (annotModel, tea.Cmd, []string) {
	t.Helper()
	type step struct {
		m    annotModel
		cmd  tea.Cmd
		urls []string
		done bool
	}
	out := make(chan step, 1)
	go func() {
		var urls []string
		for cmd != nil {
			msg := cmd()
			switch msg := msg.(type) {
			case webLoginURLMsg:
				urls = append(urls, msg.url)
			case webLoginMsg:
				m, cmd = m.update(msg)
				out <- step{m, cmd, urls, true}
				return
			default:
				t.Errorf("unexpected message %T during the login", msg)
				out <- step{m, nil, urls, false}
				return
			}
			m, cmd = m.update(msg)
			// The status row shows the URL as soon as it is known.
			if len(urls) > 0 && !strings.Contains(m.status, urls[0]) {
				t.Errorf("status %q lacks the login URL %q", m.status, urls[0])
			}
		}
		out <- step{m, nil, urls, false}
	}()
	select {
	case s := <-out:
		if !s.done {
			t.Fatal("the login ended without a result message")
		}
		return s.m, s.cmd, s.urls
	case <-time.After(5 * time.Second):
		t.Fatal("the browser login did not finish")
	}
	return m, nil, nil
}

// remotesYAML returns the content of remotes.yaml.
func remotesYAML(t *testing.T) string {
	t.Helper()
	return readFile(t, web.RemotesPath())
}

func TestWebFormBrowserLoginSavesOAuthRemote(t *testing.T) {
	webEnv(t)
	a := newFakeAccess(t)
	a.api.projects["other"] = &fakeWebProject{name: "Other", proposals: map[string]bool{}}
	m, labelsPath := annotTestModel(t)
	m.web.openBrowser = a.browser

	m, _ = webPress(t, m, "w", "enter") // Remote & project…; no remote yet opens the form
	if m.mode != annotWebForm {
		t.Fatalf("mode = %d, want the remote form", m.mode)
	}
	v := m.View()
	for _, want := range []string{"Log in with", "(•) Browser", "( ) Service token"} {
		if !strings.Contains(v, want) {
			t.Errorf("form lacks %q:\n%s", want, v)
		}
	}
	m, _ = webPress(t, m, "enter") // name → URL
	for range remoteURLPrefill {
		m, _ = webPress(t, m, "backspace")
	}
	m, _ = sendAnnot(m, pasteKey(a.URL), webKey("enter")) // URL → login row; browser stays chosen
	if m.web.form.focus != formAuth || m.web.form.token {
		t.Fatalf("focus %d token %v, want the login row on browser", m.web.form.focus, m.web.form.token)
	}
	m, cmd := webPress(t, m, "enter") // save: the browser login starts

	if m.mode != annotWebBusy || !strings.Contains(m.status, "waiting for browser login") {
		t.Fatalf("no login status (mode %d): %q", m.mode, m.status)
	}
	if _, err := os.Stat(web.RemotesPath()); err == nil {
		t.Error("remotes.yaml written before the login finished")
	}
	if got := a.openedURLs(); len(got) != 0 || len(a.api.callList()) != 0 {
		t.Fatalf("Update did network work itself: opened %v, api %v", got, a.api.callList())
	}

	m, cmd, urls := loginPump(t, m, cmd)
	if len(urls) != 1 || !strings.HasPrefix(urls[0], a.URL+"/authorize?") {
		t.Fatalf("announced URLs = %v, want one authorize URL", urls)
	}
	if got := a.openedURLs(); len(got) != 1 || got[0] != urls[0] {
		t.Errorf("browser opened %v, announced %v", got, urls)
	}
	if m.mode != annotWebBusy || !strings.Contains(m.status, "loading projects from origin") {
		t.Fatalf("not loading projects after the login (mode %d): %q", m.mode, m.status)
	}

	rems, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	rem, ok := rems.Get("origin")
	if !ok || rem.URL != a.URL || rem.OAuth == nil || !strings.HasPrefix(rem.OAuth.AccessToken, "at-") || rem.OAuth.RefreshToken == "" {
		t.Fatalf("saved remote = %+v", rem)
	}
	if rem.ClientID != "" || rem.ClientSecret != "" {
		t.Errorf("a service token was stored next to the login: %+v", rem)
	}
	yaml := remotesYAML(t)
	if !strings.Contains(yaml, "oauth:") || strings.Contains(yaml, "client_secret") {
		t.Errorf("remotes.yaml = %s", yaml)
	}
	if st, _ := os.Stat(web.RemotesPath()); st.Mode().Perm() != 0o600 {
		t.Errorf("remotes.yaml mode = %v, want 0600", st.Mode().Perm())
	}

	m = runWeb(t, m, cmd)
	if m.mode != annotWebProjects {
		t.Fatalf("mode = %d, want the project picker; status %q", m.mode, m.status)
	}
	if !slices.Contains(a.api.callList(), "GET /api/admin/projects") {
		t.Errorf("api calls = %v, want the project list sent with the bearer token", a.api.callList())
	}
	m, _ = webPress(t, m, "enter") // the "new project: queue" row
	if got := sidecar(t, labelsPath); got != "remote: origin\nproject: queue\n" {
		t.Errorf("sidecar = %q", got)
	}
	if m.mode != annotWebMenu {
		t.Errorf("mode = %d, want the menu", m.mode)
	}
}

func TestWebFormBrowserLoginReplacesServiceToken(t *testing.T) {
	webEnv(t)
	a := newFakeAccess(t)
	rems := &web.Remotes{}
	rems.Set(web.Remote{Name: "origin", URL: a.URL, ClientID: "id.access", ClientSecret: "s3cret"})
	if err := rems.Save(); err != nil {
		t.Fatal(err)
	}
	m, _ := annotTestModel(t)
	m.web.openBrowser = a.browser

	m, _ = webPress(t, m, "w", "r", "n") // the form, over an existing remote: the name starts empty
	if got := string(m.web.form.fields[formName]); got != "" {
		t.Fatalf("name = %q, want it empty", got)
	}
	m, _ = sendAnnot(m, pasteKey("origin"), webKey("enter")) // name → URL
	for range remoteURLPrefill {
		m, _ = webPress(t, m, "backspace")
	}
	m, _ = sendAnnot(m, pasteKey(a.URL), webKey("enter")) // URL → login row
	m, cmd := webPress(t, m, "enter")
	m, cmd, _ = loginPump(t, m, cmd)
	if cmd == nil {
		t.Fatalf("no project load after the login (mode %d): %q", m.mode, m.status)
	}
	got, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := got.Get("origin")
	if rem.OAuth == nil || rem.ClientID != "" || rem.ClientSecret != "" {
		t.Errorf("the remote keeps its service token or lacks the login: %+v", rem)
	}
	if len(got.List) != 1 {
		t.Errorf("remotes = %+v, want the one remote replaced", got.List)
	}
}

func TestWebFormEscCancelsBrowserLogin(t *testing.T) {
	webEnv(t)
	a := newFakeAccess(t)
	m, _ := annotTestModel(t)
	// A browser that never completes the login: the URL only gets recorded.
	m.web.openBrowser = func(u string) error {
		a.mu.Lock()
		a.opened = append(a.opened, u)
		a.mu.Unlock()
		return nil
	}
	m, _ = webPress(t, m, "w", "enter", "enter")
	for range remoteURLPrefill {
		m, _ = webPress(t, m, "backspace")
	}
	m, cmd := sendAnnot(m, pasteKey(a.URL), webKey("ctrl+s"))

	// The first message is the announced URL; the login then waits for the browser.
	msg := cmd()
	urlMsg, ok := msg.(webLoginURLMsg)
	if !ok {
		t.Fatalf("first message = %T, want the login URL", msg)
	}
	m, wait := m.update(urlMsg)
	if m.mode != annotWebBusy || !strings.Contains(m.status, "waiting for browser login") || !strings.Contains(m.status, urlMsg.url) {
		t.Fatalf("no URL in the status (mode %d): %q", m.mode, m.status)
	}
	if v := m.View(); !strings.Contains(v, "waiting for browser login") || !strings.Contains(v, "esc cancel") {
		t.Errorf("busy screen lacks the login text or the esc hint:\n%s", v)
	}

	m, _ = webPress(t, m, "esc")
	if m.mode != annotWebForm || m.status != "cancelled" {
		t.Fatalf("esc: mode %d status %q, want the form and cancelled", m.mode, m.status)
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case msg := <-done:
		res, ok := msg.(webLoginMsg)
		if !ok || res.err == nil || res.tok != nil {
			t.Fatalf("after esc the login ended with %#v, want an error", msg)
		}
		next, _ := m.update(res) // a late result of a cancelled login is dropped
		if next.mode != annotWebForm || next.web.note != "" {
			t.Errorf("a cancelled login still changed the screen: mode %d note %q", next.mode, next.web.note)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("esc did not stop the login")
	}
	if _, err := os.Stat(web.RemotesPath()); err == nil {
		t.Errorf("a cancelled login saved remotes.yaml:\n%s", remotesYAML(t))
	}
}

func TestWebFormLoginFailureReturnsToForm(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	f.rejectStatus, f.rejectBody = 302, ""
	m, _ := annotTestModel(t)
	m.web.openBrowser = func(string) error { t.Error("the browser opened for a server without Managed OAuth"); return nil }
	m, _ = webPress(t, m, "w", "enter", "enter")
	for range remoteURLPrefill {
		m, _ = webPress(t, m, "backspace")
	}
	m, cmd := sendAnnot(m, pasteKey(f.URL), webKey("ctrl+s"))
	m, _, _ = loginPump(t, m, cmd)
	if m.mode != annotWebForm || !m.statusErr || !strings.Contains(m.status, "service token") {
		t.Errorf("mode %d status %q, want the form with a hint for a service token", m.mode, m.status)
	}
	if _, err := os.Stat(web.RemotesPath()); err == nil {
		t.Error("a failed login saved remotes.yaml")
	}
}

func TestWebFormServiceTokenSkipsBrowser(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t)
	m, _ := annotTestModel(t)
	m.web.openBrowser = func(string) error { t.Error("the browser opened for a service token"); return nil }
	m, _ = webPress(t, m, "w", "enter", "enter") // form, then name → URL
	for range remoteURLPrefill {
		m, _ = webPress(t, m, "backspace")
	}
	m, _ = sendAnnot(m, pasteKey(f.URL), webKey("enter"), webKey("space")) // login row: space switches to the token
	if !m.web.form.token {
		t.Fatal("space did not switch to the service token")
	}
	if v := m.View(); !strings.Contains(v, "(•) Service token") {
		t.Errorf("form does not show the token choice:\n%s", v)
	}
	m, cmd := sendAnnot(m, webKey("enter"), pasteKey("id.access"), webKey("enter"), pasteKey("sekret"), webKey("enter"))
	if m.mode != annotWebBusy || !strings.Contains(m.status, "loading projects from origin") {
		t.Fatalf("mode %d status %q, want loading projects at once", m.mode, m.status)
	}
	m = runWeb(t, m, cmd)
	if m.mode != annotWebProjects {
		t.Fatalf("mode = %d, want the project picker; status %q", m.mode, m.status)
	}
	rems, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := rems.Get("origin")
	if rem.ClientID != "id.access" || rem.ClientSecret != "sekret" || rem.OAuth != nil {
		t.Errorf("saved remote = %+v", rem)
	}
	if strings.Contains(remotesYAML(t), "oauth") {
		t.Errorf("remotes.yaml has an oauth entry:\n%s", remotesYAML(t))
	}
}

func TestWebFormLoginRowKeys(t *testing.T) {
	webEnv(t)
	m, _ := annotTestModel(t)
	m, _ = webPress(t, m, "w", "enter", "tab", "tab") // name, URL, login row
	if m.web.form.focus != formAuth {
		t.Fatalf("focus = %d, want the login row", m.web.form.focus)
	}
	m, _ = webPress(t, m, "tab") // the credential fields are skipped for the browser login
	if m.web.form.focus != formName {
		t.Errorf("tab on the login row went to field %d, want to wrap to the name", m.web.form.focus)
	}
	m, _ = webPress(t, m, "up")
	if m.web.form.focus != formAuth {
		t.Fatalf("up from the name went to field %d, want the login row", m.web.form.focus)
	}
	m, _ = webPress(t, m, "right")
	if !m.web.form.token {
		t.Error("right did not choose the service token")
	}
	m, _ = webPress(t, m, "left")
	if m.web.form.token {
		t.Error("left did not choose the browser")
	}
	m, _ = webPress(t, m, "s")
	m, _ = webPress(t, m, "tab")
	if m.web.form.focus != formClientID {
		t.Errorf("tab after choosing the token went to field %d, want Client ID", m.web.form.focus)
	}
	m, _ = sendAnnot(m, pasteKey("abc"))
	if got := string(m.web.form.fields[formClientID]); got != "abc" {
		t.Errorf("Client ID = %q", got)
	}
}

func TestWebRemotePickerShowsBrowserLogin(t *testing.T) {
	webEnv(t)
	rems := &web.Remotes{}
	rems.Set(web.Remote{Name: "origin", URL: "http://127.0.0.1:1", OAuth: &web.OAuthToken{ClientID: "c", TokenEndpoint: "http://127.0.0.1:1/token", AccessToken: "at"}})
	if err := rems.Save(); err != nil {
		t.Fatal(err)
	}
	m, _ := annotTestModel(t)
	m, _ = webPress(t, m, "w", "r")
	if v := m.View(); !strings.Contains(v, "browser login") {
		t.Errorf("remote picker does not show the browser login:\n%s", v)
	}
}

// staleOAuthModel returns a model linked to origin/queue on the fake Access server, whose stored login has an
// access token the server does not know and a refresh token it refuses, and a service token next to it.
func staleOAuthModel(t *testing.T, a *fakeAccess) (annotModel, string) {
	t.Helper()
	webEnv(t)
	a.api.compareFixture()
	rems := &web.Remotes{}
	rems.Set(web.Remote{
		Name: "origin", URL: a.URL, ClientID: "id.access", ClientSecret: "s3cret",
		OAuth: &web.OAuthToken{
			ClientID: "client-gone", TokenEndpoint: a.URL + "/token", Resource: a.URL,
			AccessToken: "stale", RefreshToken: "stale-rt", ExpiresAt: time.Now().Add(time.Hour),
		},
	})
	if err := rems.Save(); err != nil {
		t.Fatal(err)
	}
	m, labelsPath := annotTestModel(t)
	if err := web.SaveLink(labelsPath, web.Link{Remote: "origin", Project: "queue"}); err != nil {
		t.Fatal(err)
	}
	m.web.openBrowser = a.browser
	return m, labelsPath
}

func TestWebLogInAgainAfterExpiredSession(t *testing.T) {
	a := newFakeAccess(t)
	m, _ := staleOAuthModel(t, a)

	// A call with the dead login fails and the status says how to log in again.
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	if m.mode != annotWebMenu || !m.web.noteErr || !strings.Contains(m.web.note, "session expired") ||
		!strings.Contains(m.web.note, "Log in again") {
		t.Fatalf("no log-in-again hint (mode %d): %q", m.mode, m.web.note)
	}
	if !strings.Contains(m.status, "Log in again") || !m.statusErr {
		t.Errorf("status row = %q", m.status)
	}
	if v := m.View(); !strings.Contains(v, "Log in again  (l)") {
		t.Errorf("menu lacks the Log in again item:\n%s", v)
	}

	m, cmd = webPress(t, m, "l")
	if m.mode != annotWebBusy || !strings.Contains(m.status, "waiting for browser login") {
		t.Fatalf("no login status (mode %d): %q", m.mode, m.status)
	}
	m, _, urls := loginPump(t, m, cmd)
	if len(urls) != 1 {
		t.Fatalf("announced URLs = %v", urls)
	}
	if m.mode != annotWebMenu || m.web.noteErr || !strings.Contains(m.web.note, "logged in to origin") {
		t.Fatalf("login not reported (mode %d): %q", m.mode, m.web.note)
	}
	rems, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := rems.Get("origin")
	if rem.OAuth == nil || rem.OAuth.AccessToken == "stale" || rem.OAuth.ClientID == "client-gone" {
		t.Errorf("the stored login was not replaced: %+v", rem.OAuth)
	}
	if rem.ClientID != "" || rem.ClientSecret != "" {
		t.Errorf("the service token survived the new login: %+v", rem)
	}

	// The new login works.
	m, cmd = webPress(t, m, "c")
	m = runWeb(t, m, cmd)
	if m.mode != annotCompare {
		t.Fatalf("mode = %d after logging in again; status %q note %q", m.mode, m.status, m.web.note)
	}
}

func TestWebLogInAgainNeedsALink(t *testing.T) {
	webEnv(t)
	m, _ := annotTestModel(t)
	m, _ = webPress(t, m, "w", "l")
	if m.mode != annotWebMenu || !m.web.noteErr || !strings.Contains(m.web.note, "not linked") {
		t.Errorf("mode %d note %q, want a not-linked error", m.mode, m.web.note)
	}
}

func TestWebLogInAgainFromMenuRowAndEscCancels(t *testing.T) {
	a := newFakeAccess(t)
	m, _ := staleOAuthModel(t, a)
	m.web.openBrowser = func(string) error { return nil }
	m, _ = webPress(t, m, "w", "down", "down", "down")
	if m.web.cursor != 3 {
		t.Fatalf("cursor = %d, want the Log in again row", m.web.cursor)
	}
	m, cmd := webPress(t, m, "enter")
	if m.mode != annotWebBusy {
		t.Fatalf("mode = %d, want the busy screen", m.mode)
	}
	urlMsg, ok := cmd().(webLoginURLMsg)
	if !ok {
		t.Fatal("no login URL announced")
	}
	m, wait := m.update(urlMsg)
	m, _ = webPress(t, m, "esc")
	if m.mode != annotWebMenu {
		t.Errorf("esc returned to mode %d, want the menu", m.mode)
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("esc did not stop the login")
	}
	rems, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if rem, _ := rems.Get("origin"); rem.OAuth == nil || rem.OAuth.AccessToken != "stale" {
		t.Errorf("a cancelled login changed the stored login: %+v", rem.OAuth)
	}
}

func TestWebRefreshedTokenIsSaved(t *testing.T) {
	webEnv(t)
	a := newFakeAccess(t)
	a.api.compareFixture()
	a.seed("client-seed", "rt-seed")
	rems := &web.Remotes{}
	rems.Set(web.Remote{
		Name: "origin", URL: a.URL,
		OAuth: &web.OAuthToken{
			ClientID: "client-seed", TokenEndpoint: a.URL + "/token", Resource: a.URL,
			AccessToken: "expired", RefreshToken: "rt-seed", ExpiresAt: time.Now().Add(-time.Hour),
		},
	})
	if err := rems.Save(); err != nil {
		t.Fatal(err)
	}
	m, labelsPath := annotTestModel(t)
	if err := web.SaveLink(labelsPath, web.Link{Remote: "origin", Project: "queue"}); err != nil {
		t.Fatal(err)
	}
	m, cmd := webPress(t, m, "w", "c")
	m = runWeb(t, m, cmd)
	if m.mode != annotCompare {
		t.Fatalf("mode = %d, want compare; status %q", m.mode, m.status)
	}
	got, err := web.LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	rem, _ := got.Get("origin")
	if rem.OAuth == nil || rem.OAuth.RefreshToken == "rt-seed" || !strings.HasPrefix(rem.OAuth.AccessToken, "at-") {
		t.Errorf("the refreshed token was not saved: %+v", rem.OAuth)
	}
}
