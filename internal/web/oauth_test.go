package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// protectedResourcePath is where the fake serves the protected resource metadata, as Cloudflare Access does.
const protectedResourcePath = "/.well-known/cloudflare-access-protected-resource/api/admin/whoami"

// fakeOAuth models Cloudflare Access Managed OAuth with two hosts. The embedded Server is the quet-web app: the
// admin whoami behind a Bearer token, whose 401 points at the protected resource metadata (and which has no
// authorization server metadata of its own). AS is the authorization server (the team domain): metadata, dynamic
// client registration, an authorize endpoint that redirects straight to the callback (so tests need no browser),
// and a token and revocation endpoint.
type fakeOAuth struct {
	*httptest.Server
	AS *httptest.Server

	mu sync.Mutex
	// Behaviour.
	metadata     bool   // the app host also serves /.well-known/oauth-authorization-server (default false)
	resourceMeta bool   // the 401 carries resource_metadata, and that document names asURL (default true)
	asURL        string // the issuer named by the protected resource metadata (default AS.URL)
	revoked      []url.Values
	denyAll      bool   // whoami rejects every token
	whoamiStatus int    // when non-zero, whoami answers this status whatever the credentials
	authorize    string // "" = valid redirect, "badstate", "error"
	expiresIn    int    // expires_in of issued access tokens (0 = omit)
	rotate       bool   // refresh answers rotate the refresh token (default true)
	// State.
	clients      map[string]string // client_id -> registered redirect URI
	codes        map[string]string // code -> PKCE challenge
	access       map[string]bool   // valid access tokens
	refresh      string            // the one valid refresh token
	issued       int
	registered   []map[string]any
	authorizeQ   []url.Values
	tokenForms   []url.Values
	whoamiAuth   []string
	whoamiHeader []http.Header
}

// newFakeOAuth starts a fake server that is closed when the test ends.
func newFakeOAuth(t *testing.T) *fakeOAuth {
	t.Helper()
	f := &fakeOAuth{
		rotate: true, expiresIn: 900,
		clients: map[string]string{}, codes: map[string]string{}, access: map[string]bool{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(w, r, false) }))
	f.AS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(w, r, true) }))
	t.Cleanup(f.Close)
	t.Cleanup(f.AS.Close)
	f.asURL = f.AS.URL
	f.resourceMeta = true
	return f
}

// resource is the resource identifier the protected resource metadata announces.
func (f *fakeOAuth) resource() string { return f.URL + "/api/admin/whoami" }

func (f *fakeOAuth) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// serve routes one request to the app host (fromAS false) or the authorization server host (fromAS true).
func (f *fakeOAuth) serve(w http.ResponseWriter, r *http.Request, fromAS bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/admin/whoami":
		f.whoami(w, r)
	case protectedResourcePath:
		f.writeJSON(w, 200, map[string]any{"resource": f.resource(), "protected": true, "team_domain": "x.cloudflareaccess.com",
			"authorization_servers": []string{f.asURL}, "authentication_methods": []string{"oauth"}})
	case "/.well-known/oauth-authorization-server":
		if !fromAS && !f.metadata {
			http.NotFound(w, r)
			return
		}
		f.writeJSON(w, 200, map[string]any{
			"issuer":                                f.AS.URL,
			"authorization_endpoint":                f.AS.URL + "/authorize",
			"token_endpoint":                        f.AS.URL + "/token",
			"registration_endpoint":                 f.AS.URL + "/register",
			"revocation_endpoint":                   f.AS.URL + "/revoke",
			"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	case "/revoke":
		_ = r.ParseForm()
		f.revoked = append(f.revoked, r.PostForm)
		w.WriteHeader(200)
	case "/register":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.registered = append(f.registered, body)
		id := fmt.Sprintf("client-%d", len(f.registered))
		uris, _ := body["redirect_uris"].([]any)
		if len(uris) == 1 {
			f.clients[id], _ = uris[0].(string)
		}
		f.writeJSON(w, 201, map[string]any{"client_id": id})
	case "/authorize":
		f.authorizeEndpoint(w, r)
	case "/token":
		f.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

// whoami answers the admin whoami endpoint.
func (f *fakeOAuth) whoami(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	f.whoamiAuth = append(f.whoamiAuth, auth)
	f.whoamiHeader = append(f.whoamiHeader, r.Header.Clone())
	switch {
	case f.whoamiStatus == 200:
		f.writeJSON(w, 200, map[string]string{"identity": "dev@example.com"})
	case f.whoamiStatus == 302:
		w.Header().Set("Location", "https://login.example.com/")
		w.WriteHeader(302)
	case !f.denyAll && f.access[strings.TrimPrefix(auth, "Bearer ")] && strings.HasPrefix(auth, "Bearer "):
		f.writeJSON(w, 200, map[string]string{"identity": "me@example.com"})
	default:
		if f.resourceMeta {
			w.Header().Set("WWW-Authenticate", `Bearer realm="OAuth", error="invalid_token", error_description="Missing or invalid access token", resource_metadata="`+f.URL+protectedResourcePath+`"`)
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte("unauthorized"))
	}
}

// authorizeEndpoint checks the authorization request and redirects to the callback with a code and the state.
func (f *fakeOAuth) authorizeEndpoint(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.authorizeQ = append(f.authorizeQ, q)
	redirect := q.Get("redirect_uri")
	if f.clients[q.Get("client_id")] != redirect || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorization request", 400)
		return
	}
	code := fmt.Sprintf("code-%d", len(f.codes)+1)
	f.codes[code] = q.Get("code_challenge")
	cb := url.Values{"state": {q.Get("state")}}
	switch f.authorize {
	case "badstate":
		cb.Set("state", "forged")
		cb.Set("code", code)
	case "error":
		cb.Set("error", "access_denied")
		cb.Set("error_description", "the user said no")
	default:
		cb.Set("code", code)
	}
	http.Redirect(w, r, redirect+"?"+cb.Encode(), http.StatusFound)
}

// issue creates a new access token and (when rotating) a new refresh token.
func (f *fakeOAuth) issue() map[string]any {
	f.issued++
	at := fmt.Sprintf("access-%d", f.issued)
	f.access[at] = true
	if f.rotate || f.refresh == "" {
		f.refresh = fmt.Sprintf("refresh-%d", f.issued)
	}
	out := map[string]any{"access_token": at, "token_type": "bearer", "refresh_token": f.refresh}
	if !f.rotate {
		delete(out, "refresh_token")
	}
	if f.expiresIn > 0 {
		out["expires_in"] = f.expiresIn
	}
	return out
}

// token answers the authorization code and refresh token grants.
func (f *fakeOAuth) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	form := r.PostForm
	f.tokenForms = append(f.tokenForms, form)
	fail := func(code string) { f.writeJSON(w, 400, map[string]string{"error": code, "error_description": "nope"}) }
	switch form.Get("grant_type") {
	case "authorization_code":
		challenge, ok := f.codes[form.Get("code")]
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || challenge != base64.RawURLEncoding.EncodeToString(sum[:]) || f.clients[form.Get("client_id")] != form.Get("redirect_uri") {
			fail("invalid_grant")
			return
		}
		delete(f.codes, form.Get("code"))
		f.writeJSON(w, 200, f.issue())
	case "refresh_token":
		if f.refresh == "" || form.Get("refresh_token") != f.refresh || f.clients[form.Get("client_id")] == "" {
			fail("invalid_grant")
			return
		}
		f.writeJSON(w, 200, f.issue())
	default:
		fail("unsupported_grant_type")
	}
}

// refreshes returns how many refresh token grants the token endpoint saw.
func (f *fakeOAuth) refreshes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, form := range f.tokenForms {
		if form.Get("grant_type") == "refresh_token" {
			n++
		}
	}
	return n
}

// session registers a client with a live access and refresh token and returns the matching stored login.
func (f *fakeOAuth) session(expiresIn time.Duration) *OAuthToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients["client-x"] = "http://127.0.0.1:1/callback"
	f.access["access-0"] = true
	f.refresh = "refresh-0"
	return &OAuthToken{
		ClientID: "client-x", TokenEndpoint: f.AS.URL + "/token", RevocationEndpoint: f.AS.URL + "/revoke", Resource: f.resource(),
		AccessToken: "access-0", RefreshToken: "refresh-0", ExpiresAt: time.Now().Add(expiresIn),
	}
}

// browser is an OpenBrowser that performs the GET on the authorization URL, following the redirect to the callback.
func browser(t *testing.T, body *string) func(string) error {
	t.Helper()
	return func(u string) error {
		resp, err := http.Get(u)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if body != nil {
			*body = string(b)
		}
		return nil
	}
}

func TestLoginFullFlow(t *testing.T) {
	f := newFakeOAuth(t)
	var notified, page string
	tok, err := Login(context.Background(), Remote{Name: "fake", URL: f.URL}, LoginOptions{
		OpenBrowser: browser(t, &page),
		Notify:      func(u string) { notified = u },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, "Quet is logged in. You can close this tab.") {
		t.Errorf("callback page = %q", page)
	}
	if tok.ClientID != "client-1" || tok.AccessToken != "access-1" || tok.RefreshToken != "refresh-1" ||
		tok.TokenEndpoint != f.AS.URL+"/token" || tok.RevocationEndpoint != f.AS.URL+"/revoke" || tok.Resource != f.resource() {
		t.Errorf("token = %+v", tok)
	}
	if d := time.Until(tok.ExpiresAt); d < 800*time.Second || d > 900*time.Second {
		t.Errorf("expires in %v, want about 900s", d)
	}

	// Registration: a public client for the loopback callback.
	reg := f.registered[0]
	uris, _ := reg["redirect_uris"].([]any)
	if reg["client_name"] != "Quet" || reg["token_endpoint_auth_method"] != "none" || len(uris) != 1 ||
		!strings.HasPrefix(uris[0].(string), "http://127.0.0.1:") || !strings.HasSuffix(uris[0].(string), "/callback") {
		t.Errorf("registration = %v", reg)
	}
	if g, _ := reg["grant_types"].([]any); len(g) != 2 || g[0] != "authorization_code" || g[1] != "refresh_token" {
		t.Errorf("grant_types = %v", reg["grant_types"])
	}
	if r, _ := reg["response_types"].([]any); len(r) != 1 || r[0] != "code" {
		t.Errorf("response_types = %v", reg["response_types"])
	}

	// Authorization request: PKCE S256, state, resource, and the URL passed to Notify.
	q := f.authorizeQ[0]
	if !strings.HasPrefix(notified, f.AS.URL+"/authorize?") || q.Get("resource") != f.resource() ||
		q.Get("state") == "" || len(q.Get("code_challenge")) != 43 || q.Get("client_id") != "client-1" {
		t.Errorf("authorize URL %q, query %v", notified, q)
	}

	// Token exchange.
	form := f.tokenForms[0]
	for k, want := range map[string]string{"grant_type": "authorization_code", "code": "code-1", "client_id": "client-1", "resource": f.resource(), "redirect_uri": uris[0].(string)} {
		if form.Get(k) != want {
			t.Errorf("token form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if len(form.Get("code_verifier")) < 43 {
		t.Errorf("code_verifier = %q", form.Get("code_verifier"))
	}

	// The token works as a Bearer token, without service token headers.
	c, err := NewClient(Remote{Name: "fake", URL: f.URL, OAuth: tok})
	if err != nil {
		t.Fatal(err)
	}
	who, err := c.Whoami(context.Background())
	if err != nil || who != "me@example.com" {
		t.Fatalf("Whoami = %q, %v", who, err)
	}
	last := f.whoamiHeader[len(f.whoamiHeader)-1]
	if last.Get("Authorization") != "Bearer access-1" || last.Get("CF-Access-Client-Id") != "" || last.Get("CF-Access-Client-Secret") != "" {
		t.Errorf("headers = %v", last)
	}
}

func TestLoginRejectsStateMismatch(t *testing.T) {
	f := newFakeOAuth(t)
	f.authorize = "badstate"
	var page string
	tok, err := Login(context.Background(), Remote{URL: f.URL}, LoginOptions{OpenBrowser: browser(t, &page)})
	if err == nil || !strings.Contains(err.Error(), "state") || tok != nil {
		t.Fatalf("Login = %+v, %v", tok, err)
	}
	if len(f.tokenForms) != 0 {
		t.Error("the code was exchanged despite the state mismatch")
	}
	if strings.Contains(page, "logged in") {
		t.Errorf("page = %q", page)
	}
}

func TestLoginRejectsErrorCallback(t *testing.T) {
	f := newFakeOAuth(t)
	f.authorize = "error"
	_, err := Login(context.Background(), Remote{URL: f.URL}, LoginOptions{OpenBrowser: browser(t, nil)})
	if err == nil || !strings.Contains(err.Error(), "access_denied") || !strings.Contains(err.Error(), "the user said no") {
		t.Fatalf("Login error = %v", err)
	}
	if len(f.tokenForms) != 0 {
		t.Error("a token was requested after an error callback")
	}
}

func TestLoginTimeoutAndCancel(t *testing.T) {
	f := newFakeOAuth(t)
	old := loginTimeout
	loginTimeout = 50 * time.Millisecond
	t.Cleanup(func() { loginTimeout = old })
	_, err := Login(context.Background(), Remote{URL: f.URL}, LoginOptions{OpenBrowser: func(string) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("timeout error = %v", err)
	}

	loginTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	_, err = Login(ctx, Remote{URL: f.URL}, LoginOptions{OpenBrowser: func(string) error { cancel(); return nil }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancel error = %v", err)
	}
}

func TestLoginOpenBrowserFailure(t *testing.T) {
	f := newFakeOAuth(t)
	fail := func(string) error { return errors.New("no browser here") }
	if _, err := Login(context.Background(), Remote{URL: f.URL}, LoginOptions{OpenBrowser: fail}); err == nil || !strings.Contains(err.Error(), "no browser here") {
		t.Errorf("without Notify: error = %v", err)
	}
	// With Notify the URL is shown, so the user may open it by hand: Login keeps waiting.
	var once sync.Once
	tok, err := Login(context.Background(), Remote{URL: f.URL}, LoginOptions{
		OpenBrowser: fail,
		Notify: func(u string) {
			once.Do(func() {
				go func() {
					resp, err := http.Get(u)
					if err == nil {
						resp.Body.Close()
					}
				}()
			})
		},
	})
	if err != nil || tok.AccessToken == "" {
		t.Errorf("Login with Notify = %+v, %v", tok, err)
	}
}

func TestDiscoverViaWWWAuthenticate(t *testing.T) {
	f := newFakeOAuth(t) // the app host has no authorization server metadata: the header must lead to f.AS
	got, err := Discover(context.Background(), f.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	want := OAuthServer{
		Resource:              f.resource(),
		AuthorizationEndpoint: f.AS.URL + "/authorize", TokenEndpoint: f.AS.URL + "/token",
		RegistrationEndpoint: f.AS.URL + "/register", RevocationEndpoint: f.AS.URL + "/revoke",
	}
	if *got != want {
		t.Errorf("Discover = %+v, want %+v", *got, want)
	}
}

func TestDiscoverFallsBackToWellKnownOfTheBase(t *testing.T) {
	f := newFakeOAuth(t)
	f.metadata = true
	f.resourceMeta = false // a bare 401: no WWW-Authenticate
	got, err := Discover(context.Background(), f.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenEndpoint != f.AS.URL+"/token" || got.Resource != f.URL {
		t.Errorf("Discover = %+v", *got)
	}

	// A header whose issuer is unusable falls back too, keeping the resource of the metadata.
	f.resourceMeta = true
	f.asURL = "http://127.0.0.1:1"
	if got, err = Discover(context.Background(), f.URL); err != nil || got.RegistrationEndpoint != f.AS.URL+"/register" || got.Resource != f.resource() {
		t.Errorf("Discover with a dead issuer = %+v, %v", got, err)
	}

	f.metadata = false
	if _, err = Discover(context.Background(), f.URL); err == nil || !strings.Contains(err.Error(), "discover login") {
		t.Errorf("no metadata anywhere: error = %v", err)
	}
}

func TestAuthParams(t *testing.T) {
	h := `Bearer realm="OAuth", error="invalid_token", error_description="Missing, or invalid \"access\" token", resource_metadata="https://h.example/.well-known/x", scope=a`
	got := authParams(h)
	for k, want := range map[string]string{
		"realm": "OAuth", "error": "invalid_token", "error_description": `Missing, or invalid "access" token`,
		"resource_metadata": "https://h.example/.well-known/x", "scope": "a",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if u := resourceMetadataURL(http.Header{"Www-Authenticate": {`Basic realm="x"`, h}}); u != "https://h.example/.well-known/x" {
		t.Errorf("resourceMetadataURL = %q", u)
	}
	if u := resourceMetadataURL(http.Header{"Www-Authenticate": {`Bearer realm="OAuth"`}}); u != "" {
		t.Errorf("resourceMetadataURL without the parameter = %q", u)
	}
}

func TestDiscoverStatuses(t *testing.T) {
	f := newFakeOAuth(t)
	f.whoamiStatus = 200
	if _, err := Discover(context.Background(), f.URL); !errors.Is(err, ErrNoAuthNeeded) {
		t.Errorf("200: error = %v, want ErrNoAuthNeeded", err)
	}
	if _, err := Login(context.Background(), Remote{URL: f.URL}, LoginOptions{}); !errors.Is(err, ErrNoAuthNeeded) {
		t.Errorf("Login on 200: error = %v, want ErrNoAuthNeeded", err)
	}
	f.whoamiStatus = 302
	_, err := Discover(context.Background(), f.URL)
	if !errors.Is(err, ErrOAuthNotEnabled) || !strings.Contains(err.Error(), "quet web login --service-token") {
		t.Errorf("302: error = %v", err)
	}
	if _, err := Discover(context.Background(), ""); err == nil {
		t.Error("empty URL accepted")
	}
}

func TestDiscoverRequiresRegistrationAndS256(t *testing.T) {
	for name, doc := range map[string]map[string]any{
		"no registration": {"authorization_endpoint": "http://x/a", "token_endpoint": "http://x/t"},
		"no S256": {"authorization_endpoint": "http://x/a", "token_endpoint": "http://x/t", "registration_endpoint": "http://x/r",
			"code_challenge_methods_supported": []string{"plain"}},
		"bad scheme": {"authorization_endpoint": "javascript:alert(1)", "token_endpoint": "http://x/t", "registration_endpoint": "http://x/r"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == wellKnownAS {
				_ = json.NewEncoder(w).Encode(doc)
				return
			}
			w.WriteHeader(401)
		}))
		_, err := Discover(context.Background(), srv.URL)
		srv.Close()
		if err == nil {
			t.Errorf("%s: Discover succeeded", name)
		}
	}
}

func TestCallbackAcceptsExactlyOneCallback(t *testing.T) {
	s, err := newCallbackServer("st")
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	get := func(path string) (int, string) {
		resp, err := http.Get(strings.TrimSuffix(s.redirectURI(), "/callback") + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := get("/other"); code != 404 {
		t.Errorf("/other = %d", code)
	}
	if code, body := get("/callback?state=st&code=abc"); code != 200 || !strings.Contains(body, "Quet is logged in. You can close this tab.") {
		t.Errorf("callback = %d %q", code, body)
	}
	if code, _ := get("/callback?state=st&code=again"); code != http.StatusGone {
		t.Errorf("second callback = %d, want 410", code)
	}
	if res := <-s.result; res.err != nil || res.code != "abc" {
		t.Errorf("result = %+v", res)
	}
}

func TestExpiringTokenIsRefreshedBeforeTheRequestAndPersisted(t *testing.T) {
	dir := isolate(t)
	f := newFakeOAuth(t)
	tok := f.session(30 * time.Second) // expires within the 60 s margin

	remotes := &Remotes{}
	rem := Remote{Name: "fake", URL: f.URL, ClientID: "old-id", ClientSecret: "old-secret"}
	rem.SetOAuth(tok)
	remotes.Set(rem)
	if err := remotes.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(RemotesPath(), 0o644); err != nil { // the saver must restore 0600
		t.Fatal(err)
	}

	c, err := NewClient(rem, WithTokenSaver(TokenSaver("fake")))
	if err != nil {
		t.Fatal(err)
	}
	who, err := c.Whoami(context.Background())
	if err != nil || who != "me@example.com" {
		t.Fatalf("Whoami = %q, %v", who, err)
	}
	if n := f.refreshes(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
	if len(f.whoamiAuth) != 1 || f.whoamiAuth[0] != "Bearer access-1" {
		t.Errorf("whoami auth = %v (the stale token must never be sent)", f.whoamiAuth)
	}
	form := f.tokenForms[0]
	if form.Get("refresh_token") != "refresh-0" || form.Get("client_id") != "client-x" || form.Get("resource") != f.resource() {
		t.Errorf("refresh form = %v", form)
	}

	// A second request reuses the fresh token.
	if _, err := c.Whoami(context.Background()); err != nil || f.refreshes() != 1 {
		t.Errorf("second Whoami: %v, refreshes %d", err, f.refreshes())
	}

	// The rotated refresh token is on disk, the file is 0600, and the service token stayed cleared.
	loaded, err := LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := loaded.Get("fake")
	if got.OAuth == nil || got.OAuth.RefreshToken != "refresh-1" || got.OAuth.AccessToken != "access-1" || got.ClientID != "" || got.ClientSecret != "" {
		t.Errorf("stored remote = %+v / %+v", got, got.OAuth)
	}
	info, err := os.Stat(RemotesPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("remotes.yaml mode = %v, want 0600", info.Mode().Perm())
	}
	_ = dir
}

func TestRejectedTokenRefreshesOnceAndRetries(t *testing.T) {
	f := newFakeOAuth(t)
	tok := f.session(time.Hour)
	f.mu.Lock()
	delete(f.access, "access-0") // the server revoked it: the client still thinks it is valid
	f.mu.Unlock()

	var saved []*OAuthToken
	c, err := NewClient(Remote{Name: "fake", URL: f.URL, OAuth: tok}, WithTokenSaver(func(t *OAuthToken) error {
		saved = append(saved, t)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if who, err := c.Whoami(context.Background()); err != nil || who != "me@example.com" {
		t.Fatalf("Whoami = %q, %v", who, err)
	}
	if n := f.refreshes(); n != 1 {
		t.Errorf("refreshes = %d, want 1", n)
	}
	if got := f.whoamiAuth; len(got) != 2 || got[0] != "Bearer access-0" || got[1] != "Bearer access-1" {
		t.Errorf("whoami auth = %v", got)
	}
	if len(saved) != 1 || saved[0].RefreshToken != "refresh-1" {
		t.Errorf("saved = %+v", saved)
	}

	// A token that stays rejected after the refresh is reported, not refreshed again.
	f.mu.Lock()
	f.denyAll = true
	f.mu.Unlock()
	_, err = c.Whoami(context.Background())
	var api *APIError
	if !errors.As(err, &api) || api.Status != 401 || !strings.Contains(err.Error(), "run `quet web login`") {
		t.Errorf("rejected after refresh: error = %v", err)
	}
	if n := f.refreshes(); n != 2 {
		t.Errorf("refreshes = %d, want 2 (one per request)", n)
	}
}

func TestRedirectToLoginRefreshesToo(t *testing.T) {
	f := newFakeOAuth(t)
	tok := f.session(time.Hour)
	var calls int
	inner := f.Config.Handler
	f.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/whoami" {
			calls++
			if calls == 1 {
				w.Header().Set("Location", "https://login.example.com/")
				w.WriteHeader(302)
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
	c, _ := NewClient(Remote{URL: f.URL, OAuth: tok})
	if who, err := c.Whoami(context.Background()); err != nil || who != "me@example.com" || f.refreshes() != 1 {
		t.Errorf("Whoami = %q, %v, refreshes %d", who, err, f.refreshes())
	}
}

func TestInvalidGrantMeansSessionExpired(t *testing.T) {
	f := newFakeOAuth(t)
	tok := f.session(10 * time.Second)
	tok.RefreshToken = "revoked-refresh-token-value"
	c, _ := NewClient(Remote{URL: f.URL, OAuth: tok})
	_, err := c.Whoami(context.Background())
	if !errors.Is(err, ErrSessionExpired) || !strings.Contains(err.Error(), "session expired: run `quet web login`") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "revoked-refresh-token-value") {
		t.Errorf("error leaks the refresh token: %v", err)
	}
	if len(f.whoamiAuth) != 0 {
		t.Error("a request was sent after the failed refresh")
	}

	// The same through the 401 path.
	tok = f.session(time.Hour)
	f.mu.Lock()
	delete(f.access, "access-0")
	f.mu.Unlock()
	tok.RefreshToken = "revoked-too"
	c, _ = NewClient(Remote{URL: f.URL, OAuth: tok})
	if _, err = c.Whoami(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("401 path: error = %v", err)
	}
}

func TestRefreshKeepsTheRefreshTokenWhenNotRotated(t *testing.T) {
	f := newFakeOAuth(t)
	f.rotate = false
	tok := f.session(5 * time.Second)
	var saved *OAuthToken
	c, _ := NewClient(Remote{URL: f.URL, OAuth: tok}, WithTokenSaver(func(t *OAuthToken) error { saved = t; return nil }))
	if _, err := c.Whoami(context.Background()); err != nil {
		t.Fatal(err)
	}
	if saved == nil || saved.RefreshToken != "refresh-0" || saved.AccessToken != "access-1" {
		t.Errorf("saved = %+v", saved)
	}
}

func TestSaverFailureIsReported(t *testing.T) {
	f := newFakeOAuth(t)
	tok := f.session(5 * time.Second)
	c, _ := NewClient(Remote{URL: f.URL, OAuth: tok}, WithTokenSaver(func(*OAuthToken) error { return errors.New("disk full") }))
	if _, err := c.Whoami(context.Background()); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error = %v", err)
	}
}

func TestCredentialPrecedence(t *testing.T) {
	isolate(t)
	f := newFakeOAuth(t)
	tok := f.session(time.Hour)

	// Stored OAuth beats a stored service token (a hand-edited file can hold both).
	c, _ := NewClient(Remote{URL: f.URL, ClientID: "sid", ClientSecret: "ssecret", OAuth: tok})
	if _, err := c.Whoami(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := f.whoamiHeader[0]
	if h.Get("Authorization") != "Bearer access-0" || h.Get("CF-Access-Client-Id") != "" || h.Get("CF-Access-Client-Secret") != "" {
		t.Errorf("OAuth + service token sent %v", h)
	}

	// A stored service token alone.
	c, _ = NewClient(Remote{URL: f.URL, ClientID: "sid", ClientSecret: "ssecret"})
	_, _ = c.Whoami(context.Background())
	h = f.whoamiHeader[1]
	if h.Get("Authorization") != "" || h.Get("CF-Access-Client-Id") != "sid" || h.Get("CF-Access-Client-Secret") != "ssecret" {
		t.Errorf("service token sent %v", h)
	}

	// Nothing stored: no credentials at all.
	c, _ = NewClient(Remote{URL: f.URL})
	_, _ = c.Whoami(context.Background())
	if h = f.whoamiHeader[2]; h.Get("Authorization") != "" || h.Get("CF-Access-Client-Id") != "" {
		t.Errorf("anonymous request sent %v", h)
	}

	// The environment service token (both variables) wins over stored OAuth: Resolve drops the login.
	r := &Remotes{}
	stored := Remote{Name: "prod", URL: f.URL}
	stored.SetOAuth(tok)
	r.Set(stored)
	t.Setenv(EnvClientID, "env-id")
	got, err := r.Resolve("prod")
	if err != nil || got.OAuth == nil || got.ClientID != "env-id" {
		t.Fatalf("one variable: %+v, %v (OAuth must stay: both variables are required)", got, err)
	}
	t.Setenv(EnvClientSecret, "env-secret")
	got, _ = r.Resolve("prod")
	if got.OAuth != nil || got.ClientID != "env-id" || got.ClientSecret != "env-secret" {
		t.Fatalf("both variables: %+v", got)
	}
	c, _ = NewClient(got)
	_, _ = c.Whoami(context.Background())
	h = f.whoamiHeader[3]
	if h.Get("Authorization") != "" || h.Get("CF-Access-Client-Id") != "env-id" || h.Get("CF-Access-Client-Secret") != "env-secret" {
		t.Errorf("env token sent %v", h)
	}
	if r.List[0].OAuth == nil {
		t.Error("Resolve changed the stored remote")
	}
}

func TestResolveDropsOAuthWhenTheURLIsOverridden(t *testing.T) {
	isolate(t)
	r := &Remotes{}
	rem := Remote{Name: "prod", URL: "https://quet.example.com"}
	rem.SetOAuth(&OAuthToken{AccessToken: "a", RefreshToken: "r"})
	r.Set(rem)
	t.Setenv(EnvURL, "https://quet.example.com/")
	if got, _ := r.Resolve("prod"); got.OAuth == nil {
		t.Error("the same URL dropped the login")
	}
	t.Setenv(EnvURL, "http://127.0.0.1:9")
	if got, _ := r.Resolve("prod"); got.OAuth != nil {
		t.Error("a different URL kept the login: the token would go to another server")
	}
}

func TestCredentialKindsClearEachOther(t *testing.T) {
	var r Remote
	r.SetServiceToken("id", "secret")
	r.SetOAuth(&OAuthToken{AccessToken: "a"})
	if r.ClientID != "" || r.ClientSecret != "" || r.OAuth == nil {
		t.Errorf("SetOAuth left %+v", r)
	}
	r.SetServiceToken("id", "secret")
	if r.OAuth != nil || r.ClientID != "id" {
		t.Errorf("SetServiceToken left %+v", r)
	}
	r.ClearCredentials()
	if r.OAuth != nil || r.ClientID != "" || r.ClientSecret != "" {
		t.Errorf("ClearCredentials left %+v", r)
	}
}

func TestRemotesYAMLOAuthBlock(t *testing.T) {
	isolate(t)
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	r := &Remotes{}
	a := Remote{Name: "a", URL: "https://quet.example.com"}
	a.SetOAuth(&OAuthToken{ClientID: "cid", TokenEndpoint: "https://quet.example.com/token", Resource: "https://quet.example.com",
		AccessToken: "acc", RefreshToken: "ref", ExpiresAt: exp})
	r.Set(a)
	r.Set(Remote{Name: "b", URL: "http://127.0.0.1:9", ClientID: "id", ClientSecret: "sec"})
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(RemotesPath())
	for _, want := range []string{"oauth:", "client_id: cid", "token_endpoint:", "access_token: acc", "refresh_token: ref", "expires_at: 2030-01-02T03:04:05Z"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("remotes.yaml misses %q:\n%s", want, raw)
		}
	}
	if strings.Count(string(raw), "oauth:") != 1 {
		t.Errorf("the service-token remote got an oauth block:\n%s", raw)
	}
	loaded, err := LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	gotA, _ := loaded.Get("a")
	if gotA.OAuth == nil || *gotA.OAuth != *a.OAuth || !gotA.OAuth.ExpiresAt.Equal(exp) {
		t.Errorf("a = %+v", gotA.OAuth)
	}
	if gotB, _ := loaded.Get("b"); gotB.OAuth != nil || gotB.ClientID != "id" {
		t.Errorf("b = %+v", gotB)
	}
	if err := SaveOAuth("ghost", a.OAuth); err == nil || !strings.Contains(err.Error(), `unknown remote "ghost"`) {
		t.Errorf("SaveOAuth(ghost) = %v", err)
	}
}

func TestRevoke(t *testing.T) {
	f := newFakeOAuth(t)
	tok := f.session(time.Hour)
	if err := tok.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.revoked) != 1 || f.revoked[0].Get("token") != "refresh-0" || f.revoked[0].Get("token_type_hint") != "refresh_token" || f.revoked[0].Get("client_id") != "client-x" {
		t.Errorf("revocation request = %v", f.revoked)
	}
	none := *tok
	none.RevocationEndpoint = ""
	if err := none.Revoke(context.Background()); err != nil || len(f.revoked) != 1 {
		t.Errorf("without an endpoint: %v, %d requests", err, len(f.revoked))
	}
	bad := *tok
	bad.RevocationEndpoint = f.URL + "/nope"
	if err := bad.Revoke(context.Background()); err == nil || strings.Contains(err.Error(), "refresh-0") {
		t.Errorf("failing revocation error = %v", err)
	}
}
