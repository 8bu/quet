package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/8bu/quet/internal/web"
)

// oauthSecrets are the token values of the fake; none of them may ever be printed.
var oauthSecrets = []string{"access-1", "refresh-1", "access-2", "refresh-2", "access-0", "refresh-0"}

// syncBuffer is a bytes.Buffer safe to read while quet is still writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeOAuthWeb is a quet-web server with Managed OAuth: one host that is the app, the protected resource
// metadata and the authorization server. Its authorize endpoint redirects straight to the callback.
type fakeOAuthWeb struct {
	*httptest.Server
	mu         sync.Mutex
	clients    map[string]string // client_id -> redirect URI
	codes      map[string]string // code -> PKCE challenge
	access     map[string]bool
	refresh    string
	issued     int
	expiresIn  int
	headers    []http.Header // headers of every admin request
	revoked    []url.Values
	badRefresh bool // the token endpoint refuses every refresh with invalid_grant
}

// newFakeOAuthWeb starts the fake; it is closed when the test ends.
func newFakeOAuthWeb(t *testing.T) *fakeOAuthWeb {
	t.Helper()
	f := &fakeOAuthWeb{clients: map[string]string{}, codes: map[string]string{}, access: map[string]bool{}, expiresIn: 900}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// issue creates the next access and refresh token.
func (f *fakeOAuthWeb) issue() map[string]any {
	f.issued++
	at := fmt.Sprintf("access-%d", f.issued)
	f.access[at] = true
	f.refresh = fmt.Sprintf("refresh-%d", f.issued)
	return map[string]any{"access_token": at, "token_type": "bearer", "expires_in": f.expiresIn, "refresh_token": f.refresh}
}

// serve answers one request of any role.
func (f *fakeOAuthWeb) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/admin/whoami", "/api/admin/projects":
		f.headers = append(f.headers, r.Header.Clone())
		auth := r.Header.Get("Authorization")
		switch {
		case r.Header.Get("CF-Access-Client-Id") != "":
			jsonReply(w, 200, map[string]any{"identity": "svc@example.com", "projects": []any{}})
		case strings.HasPrefix(auth, "Bearer ") && f.access[strings.TrimPrefix(auth, "Bearer ")]:
			jsonReply(w, 200, map[string]any{"identity": "me@example.com", "projects": []any{}})
		default:
			w.Header().Set("WWW-Authenticate", `Bearer realm="OAuth", error="invalid_token", error_description="Missing or invalid access token", resource_metadata="`+f.URL+`/.well-known/rm"`)
			w.WriteHeader(401)
		}
	case "/.well-known/rm":
		jsonReply(w, 200, map[string]any{"resource": f.URL + "/api/admin/whoami", "authorization_servers": []string{f.URL}})
	case "/.well-known/oauth-authorization-server":
		jsonReply(w, 200, map[string]any{"authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token",
			"registration_endpoint": f.URL + "/register", "revocation_endpoint": f.URL + "/revoke", "code_challenge_methods_supported": []string{"S256"}})
	case "/register":
		var body struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := fmt.Sprintf("client-%d", len(f.clients)+1)
		f.clients[id] = body.RedirectURIs[0]
		jsonReply(w, 201, map[string]any{"client_id": id})
	case "/authorize":
		q := r.URL.Query()
		code := fmt.Sprintf("code-%d", len(f.codes)+1)
		f.codes[code] = q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	case "/token":
		_ = r.ParseForm()
		form := r.PostForm
		switch form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(form.Get("code_verifier")))
			if f.codes[form.Get("code")] != base64.RawURLEncoding.EncodeToString(sum[:]) {
				jsonReply(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			jsonReply(w, 200, f.issue())
		case "refresh_token":
			if f.badRefresh || form.Get("refresh_token") != f.refresh {
				jsonReply(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			jsonReply(w, 200, f.issue())
		}
	case "/revoke":
		_ = r.ParseForm()
		f.revoked = append(f.revoked, r.PostForm)
	default:
		http.NotFound(w, r)
	}
}

// runLogin runs `quet web login args...` while a stub browser completes the flow: it waits for the authorization
// URL on stdout (or in the stubbed OpenBrowser) and does the GET that the redirect-following browser would.
func runLogin(t *testing.T, viaStub bool, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb syncBuffer
	urls := make(chan string, 1)
	if viaStub {
		old := loginOpenBrowser
		loginOpenBrowser = func(u string) error { urls <- u; return nil }
		t.Cleanup(func() { loginOpenBrowser = old })
	}
	done := make(chan int, 1)
	go func() { done <- run(append([]string{"web", "login"}, args...), &out, &errb) }()
	var authURL string
	if viaStub {
		select {
		case authURL = <-urls:
		case <-time.After(10 * time.Second):
			t.Fatalf("the browser was not opened; stdout %q stderr %q", out.String(), errb.String())
		}
	} else {
		deadline := time.Now().Add(10 * time.Second)
		for authURL == "" && time.Now().Before(deadline) {
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "/authorize?") {
					authURL = line
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		if authURL == "" {
			t.Fatalf("no authorization URL printed; stdout %q stderr %q", out.String(), errb.String())
		}
	}
	if resp, err := http.Get(authURL); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	select {
	case code = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("quet web login did not finish")
	}
	return code, out.String(), errb.String()
}

// noSecrets fails when output shows a token of the fake.
func noSecrets(t *testing.T, outputs ...string) {
	t.Helper()
	for _, o := range outputs {
		for _, s := range oauthSecrets {
			if strings.Contains(o, s) {
				t.Errorf("output shows %q:\n%s", s, o)
			}
		}
	}
}

func TestWebLoginNoBrowserEndToEnd(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	addRemote(t, &fakeWeb{Server: f.Server})

	code, stdout, stderr := runLogin(t, false, "--no-browser")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q, stdout %q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "Open this URL in your browser to log in:\n"+f.URL+"/authorize?") ||
		!strings.HasSuffix(stdout, "logged in to origin as me@example.com\n") {
		t.Errorf("stdout = %q", stdout)
	}
	noSecrets(t, stdout, stderr)

	info, err := os.Stat(web.RemotesPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("remotes.yaml mode = %v, want 0600", info.Mode().Perm())
	}
	yaml := readFile(t, web.RemotesPath())
	if !strings.Contains(yaml, "oauth:") || !strings.Contains(yaml, "refresh_token: refresh-1") || strings.Contains(yaml, "client_secret") {
		t.Errorf("remotes.yaml:\n%s", yaml)
	}
	list := mustRun(t, "web", "remote", "list")
	if !strings.Contains(list, "oauth") {
		t.Errorf("remote list:\n%s", list)
	}
	noSecrets(t, list)

	// Other commands use the stored login.
	mustRun(t, "web", "list")
	if got := f.headers[len(f.headers)-1].Get("Authorization"); got != "Bearer access-1" {
		t.Errorf("web list sent Authorization %q", got)
	}
}

func TestWebLoginOpensTheBrowser(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	addRemote(t, &fakeWeb{Server: f.Server})
	code, stdout, stderr := runLogin(t, true)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(stdout, "Opening the browser to log in…\n"+f.URL+"/authorize?") || !strings.HasSuffix(stdout, "logged in to origin as me@example.com\n") {
		t.Errorf("stdout = %q", stdout)
	}
	noSecrets(t, stdout, stderr)
}

func TestWebLoginNeedingNoAuth(t *testing.T) {
	webEnv(t)
	f := newFakeWeb(t) // answers whoami with 200 whatever the credentials
	addRemote(t, f)
	code, stdout, stderr := runWebCmd(t, "web", "login", "--no-browser")
	if code != 0 || !strings.Contains(stdout, "origin needs no login") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if strings.Contains(readFile(t, web.RemotesPath()), "oauth") {
		t.Error("a login was stored")
	}
}

func TestWebLoginWhenOAuthIsOff(t *testing.T) {
	webEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.example.com/", http.StatusFound)
	}))
	defer srv.Close()
	addRemote(t, &fakeWeb{Server: srv})
	code, _, stderr := runWebCmd(t, "web", "login", "--no-browser")
	if code != 1 || !strings.Contains(stderr, "quet web login --service-token") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestWebLogoutRevokesAndClearsBothKinds(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	addRemote(t, &fakeWeb{Server: f.Server})
	if code, _, stderr := runLogin(t, true); code != 0 {
		t.Fatalf("login: exit %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := runWebCmd(t, "web", "logout")
	if code != 0 || stdout != "logged out of origin\n" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if len(f.revoked) != 1 || f.revoked[0].Get("token") != "refresh-1" || f.revoked[0].Get("token_type_hint") != "refresh_token" {
		t.Errorf("revocation = %v", f.revoked)
	}
	yaml := readFile(t, web.RemotesPath())
	if strings.Contains(yaml, "oauth") || strings.Contains(yaml, "refresh-1") || !strings.Contains(yaml, f.URL) {
		t.Errorf("remotes.yaml after logout:\n%s", yaml)
	}
	if list := mustRun(t, "web", "remote", "list"); strings.Contains(list, "oauth") || !strings.Contains(list, "no") {
		t.Errorf("remote list:\n%s", list)
	}

	// A service token is cleared too, and an unknown remote is an error.
	stubLogin(t, "abc.access\n"+secretValue+"\n")
	mustRun(t, "web", "login", "--service-token")
	if out := mustRun(t, "web", "logout", "origin"); out != "logged out of origin\n" {
		t.Errorf("logout = %q", out)
	}
	if strings.Contains(readFile(t, web.RemotesPath()), secretValue) {
		t.Error("the service token survived logout")
	}
	if code, _, stderr := runWebCmd(t, "web", "logout", "ghost"); code != 1 || !strings.Contains(stderr, `unknown remote "ghost"`) {
		t.Errorf("unknown remote: exit %d, stderr %q", code, stderr)
	}
}

func TestWebLoginKindsReplaceEachOther(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	addRemote(t, &fakeWeb{Server: f.Server})

	stubLogin(t, "abc.access\n"+secretValue+"\n")
	mustRun(t, "web", "login", "--service-token")
	if code, _, stderr := runLogin(t, true); code != 0 {
		t.Fatalf("oauth login: exit %d, stderr %q", code, stderr)
	}
	yaml := readFile(t, web.RemotesPath())
	if strings.Contains(yaml, secretValue) || strings.Contains(yaml, "client_id: abc") || !strings.Contains(yaml, "oauth:") {
		t.Errorf("after the browser login:\n%s", yaml)
	}

	stubLogin(t, "abc.access\n"+secretValue+"\n")
	mustRun(t, "web", "login", "--service-token")
	yaml = readFile(t, web.RemotesPath())
	if strings.Contains(yaml, "oauth") || !strings.Contains(yaml, secretValue) {
		t.Errorf("after the service token login:\n%s", yaml)
	}
}

func TestWebEnvServiceTokenBeatsStoredLogin(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	addRemote(t, &fakeWeb{Server: f.Server})
	if code, _, stderr := runLogin(t, true); code != 0 {
		t.Fatalf("login: exit %d, stderr %q", code, stderr)
	}
	t.Setenv(web.EnvClientID, "env-id")
	t.Setenv(web.EnvClientSecret, "env-secret")
	mustRun(t, "web", "list")
	h := f.headers[len(f.headers)-1]
	if h.Get("Authorization") != "" || h.Get("CF-Access-Client-Id") != "env-id" {
		t.Errorf("sent %v", h)
	}
}

func TestWebRefreshedLoginIsPersisted(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	f.expiresIn = 30 // inside the 60 s refresh margin: every later command refreshes first
	addRemote(t, &fakeWeb{Server: f.Server})
	// The login's own whoami already refreshes once (access-2, refresh-2).
	if code, _, stderr := runLogin(t, true); code != 0 {
		t.Fatalf("login: exit %d, stderr %q", code, stderr)
	}
	if err := os.Chmod(web.RemotesPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, "web", "list")
	noSecrets(t, out)
	yaml := readFile(t, web.RemotesPath())
	if !strings.Contains(yaml, "refresh_token: refresh-3") || strings.Contains(yaml, "refresh-2") {
		t.Errorf("the rotated refresh token was not persisted:\n%s", yaml)
	}
	info, _ := os.Stat(web.RemotesPath())
	if info.Mode().Perm() != 0o600 {
		t.Errorf("remotes.yaml mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWebExpiredSessionMessage(t *testing.T) {
	webEnv(t)
	f := newFakeOAuthWeb(t)
	f.expiresIn = 30
	addRemote(t, &fakeWeb{Server: f.Server})
	if code, _, stderr := runLogin(t, true); code != 0 {
		t.Fatalf("login: exit %d, stderr %q", code, stderr)
	}
	f.badRefresh = true
	code, stdout, stderr := runWebCmd(t, "web", "list")
	if code != 1 || !strings.Contains(stderr, "session expired: run `quet web login`") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	noSecrets(t, stdout, stderr)
}
