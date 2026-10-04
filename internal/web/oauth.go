package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// The browser login of `quet web login` uses Cloudflare Access Managed OAuth: discovery (RFC 9728 and RFC 8414),
// dynamic client registration (RFC 7591), authorization code with PKCE S256 (RFC 7636) and the resource indicator
// (RFC 8707), then refresh tokens for later sessions.

// whoamiPath is the admin endpoint that is probed for discovery and used to show who is logged in.
const whoamiPath = "/api/admin/whoami"

// wellKnownAS is the path of the authorization server metadata document (RFC 8414).
const wellKnownAS = "/.well-known/oauth-authorization-server"

// refreshMargin is how long before its expiry an access token is refreshed.
const refreshMargin = 60 * time.Second

// maxOAuthBody bounds how much of a discovery, registration or token response is read.
const maxOAuthBody = 1 << 20

// oauthHTTPTimeout is the timeout of each discovery, registration and token request.
const oauthHTTPTimeout = 30 * time.Second

// loginTimeout is how long Login waits for the browser to come back. Tests shorten it.
var loginTimeout = 5 * time.Minute

// ErrNoAuthNeeded is returned by Discover and Login when the server answers admin requests without credentials (a
// local development server), so there is nothing to log in to.
var ErrNoAuthNeeded = errors.New("this server needs no login: it accepts admin requests without credentials")

// ErrOAuthNotEnabled is wrapped by the error of Discover and Login when the server redirects an unauthenticated
// request to a login page, which means Managed OAuth is not enabled for the Access application.
var ErrOAuthNotEnabled = errors.New("this server does not offer browser login (Cloudflare Access Managed OAuth is not enabled for it); use a service token: `quet web login --service-token`")

// ErrSessionExpired is returned when the refresh token was refused (invalid_grant): only a new login helps.
var ErrSessionExpired = errors.New("session expired: run `quet web login`")

// OAuthToken is a browser login: the dynamically registered client, the endpoint and resource it was issued for, and
// the current tokens. It is stored under `oauth:` in the remote's entry of remotes.yaml.
type OAuthToken struct {
	ClientID      string `yaml:"client_id"`
	TokenEndpoint string `yaml:"token_endpoint"`
	// RevocationEndpoint (RFC 7009) is where logout revokes the refresh token ("" when the server has none).
	RevocationEndpoint string    `yaml:"revocation_endpoint,omitempty"`
	Resource           string    `yaml:"resource,omitempty"`
	AccessToken        string    `yaml:"access_token"`
	RefreshToken       string    `yaml:"refresh_token,omitempty"`
	ExpiresAt          time.Time `yaml:"expires_at,omitempty"`
}

// LoginOptions tune Login.
type LoginOptions struct {
	// OpenBrowser opens the authorization URL; nil opens the platform default browser (open on macOS, xdg-open on
	// Linux). The CLI's --no-browser passes a function that does nothing.
	OpenBrowser func(url string) error
	// Notify is called with the authorization URL before Login waits, so the caller can show it (the CLI prints it,
	// the TUI puts it in the status).
	Notify func(url string)
}

// OAuthServer is what Discover learns about the authorization server of a quet-web server.
type OAuthServer struct {
	// Resource is the protected resource identifier sent as the resource parameter (the server's own URL when its
	// metadata does not name one).
	Resource                                                                       string
	AuthorizationEndpoint, TokenEndpoint, RegistrationEndpoint, RevocationEndpoint string
}

// oauthError is an OAuth error response: the error code and description of a token or registration endpoint.
type oauthError struct {
	what        string
	status      int
	code, descr string
}

// Error describes the response without the raw body (it could echo credentials).
func (e *oauthError) Error() string {
	s := fmt.Sprintf("%s: HTTP %d", e.what, e.status)
	if e.code != "" {
		s = fmt.Sprintf("%s: %s", e.what, e.code)
		if e.descr != "" {
			s += ": " + e.descr
		}
		s += fmt.Sprintf(" (HTTP %d)", e.status)
	}
	return s
}

// noRedirect makes an HTTP client report a redirect instead of following it.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// readOAuthError builds the oauthError of a failed response, reading the standard error and error_description.
func readOAuthError(resp *http.Response, what string) *oauthError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &oauthError{what: what, status: resp.StatusCode}
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) == nil {
		e.code, e.descr = truncateUTF8(body.Error, maxMessage), truncateUTF8(body.Description, maxMessage)
	}
	return e
}

// checkEndpoint requires raw to be an absolute http or https URL.
func checkEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the server's %s %q is not an http(s) URL", name, raw)
	}
	return nil
}

// getJSON fetches a JSON document with a GET and decodes it into out.
func getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: oauthHTTPTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", target, err)
	}
	return nil
}

// authParams parses the auth-params of a WWW-Authenticate challenge (key=value pairs; a value is a token or a
// quoted string that may hold spaces, commas and escaped quotes). Keys are lower-cased; the scheme is skipped.
func authParams(v string) map[string]string {
	params := map[string]string{}
	i := 0
	for i < len(v) {
		for i < len(v) && (v[i] == ' ' || v[i] == ',' || v[i] == '\t') {
			i++
		}
		start := i
		for i < len(v) && v[i] != '=' && v[i] != ' ' && v[i] != ',' && v[i] != '\t' {
			i++
		}
		key := strings.ToLower(v[start:i])
		if i >= len(v) || v[i] != '=' {
			continue // the scheme, or a token68 without a value
		}
		i++
		var val strings.Builder
		if i < len(v) && v[i] == '"' {
			i++
			for i < len(v) && v[i] != '"' {
				if v[i] == '\\' && i+1 < len(v) {
					i++
				}
				val.WriteByte(v[i])
				i++
			}
			i++ // the closing quote
		} else {
			for i < len(v) && v[i] != ',' && v[i] != ' ' && v[i] != '\t' {
				val.WriteByte(v[i])
				i++
			}
		}
		if _, dup := params[key]; !dup && key != "" {
			params[key] = val.String()
		}
	}
	return params
}

// resourceMetadataURL returns the resource_metadata parameter of the WWW-Authenticate challenges (RFC 9728), or "".
func resourceMetadataURL(h http.Header) string {
	for _, v := range h.Values("WWW-Authenticate") {
		if u := authParams(v)["resource_metadata"]; u != "" {
			return u
		}
	}
	return ""
}

// fetchServerMetadata reads and validates the authorization server metadata at issuer (RFC 8414).
func fetchServerMetadata(ctx context.Context, issuer string) (*OAuthServer, error) {
	var doc struct {
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		RegistrationEndpoint  string   `json:"registration_endpoint"`
		RevocationEndpoint    string   `json:"revocation_endpoint"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	if err := getJSON(ctx, issuer+wellKnownAS, &doc); err != nil {
		return nil, err
	}
	for _, f := range []struct{ name, value string }{
		{"authorization_endpoint", doc.AuthorizationEndpoint},
		{"token_endpoint", doc.TokenEndpoint},
		{"registration_endpoint", doc.RegistrationEndpoint},
	} {
		if f.value == "" {
			return nil, fmt.Errorf("the authorization server metadata at %s has no %s", issuer, f.name)
		}
		if err := checkEndpoint(f.name, f.value); err != nil {
			return nil, err
		}
	}
	if len(doc.CodeChallengeMethods) > 0 {
		ok := false
		for _, m := range doc.CodeChallengeMethods {
			ok = ok || m == "S256"
		}
		if !ok {
			return nil, fmt.Errorf("the authorization server at %s does not support PKCE S256", issuer)
		}
	}
	if doc.RevocationEndpoint != "" && checkEndpoint("revocation_endpoint", doc.RevocationEndpoint) != nil {
		doc.RevocationEndpoint = ""
	}
	return &OAuthServer{
		AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint:         doc.TokenEndpoint,
		RegistrationEndpoint:  doc.RegistrationEndpoint,
		RevocationEndpoint:    doc.RevocationEndpoint,
	}, nil
}

// Discover finds the OAuth endpoints of the quet-web server at baseURL. It asks the admin whoami endpoint without
// credentials: a 401 carries a resource_metadata pointer (RFC 9728) to the resource's authorization server, whose
// metadata (RFC 8414) is then read, falling back to the well-known document of baseURL itself. A 200 means no
// login is needed (ErrNoAuthNeeded); a redirect means Managed OAuth is off (ErrOAuthNotEnabled).
func Discover(ctx context.Context, baseURL string) (*OAuthServer, error) {
	base := normalizeURL(strings.TrimSpace(baseURL))
	if base == "" {
		return nil, errors.New("remote URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+whoamiPath, nil)
	if err != nil {
		return nil, fmt.Errorf("discover login: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: oauthHTTPTimeout, CheckRedirect: noRedirect}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("discover login: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxOAuthBody))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		return nil, ErrNoAuthNeeded
	case resp.StatusCode >= 300 && resp.StatusCode <= 399:
		return nil, fmt.Errorf("discover login: %w", ErrOAuthNotEnabled)
	case resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden:
		return nil, fmt.Errorf("discover login: %s%s answered HTTP %d", base, whoamiPath, resp.StatusCode)
	}

	resource, issuer := base, base
	if meta := resourceMetadataURL(resp.Header); meta != "" {
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		if getJSON(ctx, meta, &doc) == nil {
			if doc.Resource != "" {
				resource = doc.Resource
			}
			if len(doc.AuthorizationServers) > 0 && doc.AuthorizationServers[0] != "" {
				issuer = normalizeURL(doc.AuthorizationServers[0])
			}
		}
	}
	server, err := fetchServerMetadata(ctx, issuer)
	if err != nil && issuer != base {
		var fallbackErr error
		if server, fallbackErr = fetchServerMetadata(ctx, base); fallbackErr == nil {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("discover login: %w", err)
	}
	server.Resource = resource
	return server, nil
}

// postForm sends a form (the token endpoint) and decodes the JSON answer into out.
func postForm(ctx context.Context, endpoint, what string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return doOAuth(req, what, out, http.StatusOK)
}

// doOAuth sends req with a client that does not follow redirects and decodes the JSON answer into out. Any status
// other than the accepted ones is an oauthError.
func doOAuth(req *http.Request, what string, out any, accepted ...int) error {
	resp, err := (&http.Client{Timeout: oauthHTTPTimeout, CheckRedirect: noRedirect}).Do(req)
	if err != nil {
		// url.Error repeats the request URL only, never the form.
		return fmt.Errorf("%s: %w", what, err)
	}
	defer resp.Body.Close()
	for _, ok := range accepted {
		if resp.StatusCode == ok {
			if err := json.NewDecoder(io.LimitReader(resp.Body, maxOAuthBody)).Decode(out); err != nil {
				return fmt.Errorf("%s: decode response: %w", what, err)
			}
			return nil
		}
	}
	return readOAuthError(resp, what)
}

// register registers a public client with the redirect URI (RFC 7591) and returns its client id.
func register(ctx context.Context, endpoint, redirectURI string) (string, error) {
	data, err := json.Marshal(map[string]any{
		"client_name":                "Quet",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("register client: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := doOAuth(req, "register client", &out, http.StatusOK, http.StatusCreated); err != nil {
		return "", err
	}
	if out.ClientID == "" {
		return "", errors.New("register client: the server returned no client_id")
	}
	return out.ClientID, nil
}

// tokenResponse is the JSON answer of the token endpoint.
type tokenResponse struct {
	AccessToken  string      `json:"access_token"`
	TokenType    string      `json:"token_type"`
	ExpiresIn    json.Number `json:"expires_in"`
	RefreshToken string      `json:"refresh_token"`
}

// requestToken posts form to the token endpoint and validates the answer.
func requestToken(ctx context.Context, endpoint, what string, form url.Values) (*tokenResponse, error) {
	var tr tokenResponse
	if err := postForm(ctx, endpoint, what, form, &tr); err != nil {
		return nil, err
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("%s: the server returned no access_token", what)
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "bearer") {
		return nil, fmt.Errorf("%s: unsupported token type %q", what, truncateUTF8(tr.TokenType, 32))
	}
	return &tr, nil
}

// expiry returns when the access token of tr expires (zero when the server did not say).
func (tr *tokenResponse) expiry() time.Time {
	if secs, err := tr.ExpiresIn.Int64(); err == nil && secs > 0 {
		return time.Now().Add(time.Duration(secs) * time.Second).UTC().Truncate(time.Second)
	}
	return time.Time{}
}

// randomString returns n random bytes in unpadded base64url.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// callbackResult is how the loopback listener ends: the authorization code or the reason for failing.
type callbackResult struct {
	code string
	err  error
}

// callbackServer is the loopback listener that receives the browser's redirect. It accepts exactly one /callback.
type callbackServer struct {
	ln     net.Listener
	srv    *http.Server
	state  string
	done   atomic.Bool
	result chan callbackResult
}

// newCallbackServer listens on 127.0.0.1 with a free port and serves /callback for the given state.
func newCallbackServer(state string) (*callbackServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start the local login listener: %w", err)
	}
	s := &callbackServer{ln: ln, state: state, result: make(chan callbackResult, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", s.handle)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// redirectURI is the callback URL registered with the authorization server.
func (s *callbackServer) redirectURI() string {
	return "http://" + s.ln.Addr().String() + "/callback"
}

// close stops the listener, letting the answer to the last request finish.
func (s *callbackServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if s.srv.Shutdown(ctx) != nil {
		_ = s.srv.Close()
	}
}

// page answers with a small HTML page.
func page(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><body style=\"font-family:sans-serif;margin:3em\"><h1>%s</h1><p>%s</p></body>\n",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg))
}

// handle checks one callback request: the state first, then an error parameter, then the code.
func (s *callbackServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.done.CompareAndSwap(false, true) {
		page(w, http.StatusGone, "Quet", "This login was already completed.")
		return
	}
	q := r.URL.Query()
	var res callbackResult
	switch {
	case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(s.state)) != 1:
		page(w, http.StatusBadRequest, "Quet login failed", "The state of the response did not match; start the login again.")
		res.err = errors.New("login failed: the state of the callback did not match (start the login again)")
	case q.Get("error") != "":
		msg := truncateUTF8(q.Get("error"), maxMessage)
		if d := truncateUTF8(q.Get("error_description"), maxMessage); d != "" {
			msg += ": " + d
		}
		page(w, http.StatusBadRequest, "Quet login failed", msg)
		res.err = fmt.Errorf("login refused by the authorization server: %q", msg)
	case q.Get("code") == "":
		page(w, http.StatusBadRequest, "Quet login failed", "The response has no authorization code.")
		res.err = errors.New("login failed: the callback has no authorization code")
	default:
		page(w, http.StatusOK, "Quet is logged in", "Quet is logged in. You can close this tab.")
		res.code = q.Get("code")
	}
	s.result <- res
}

// openDefaultBrowser opens url with the platform's opener without waiting for it.
func openDefaultBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	default:
		return fmt.Errorf("opening a browser is not supported on %s", runtime.GOOS)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// authorizeURL adds the authorization request parameters to the authorization endpoint.
func authorizeURL(endpoint string, params url.Values) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Login runs the whole browser login for rem.URL and returns the token; it does not save it. It discovers the
// OAuth endpoints, starts a loopback listener, registers a client, opens the authorization URL in the browser (after
// passing it to opt.Notify) and waits up to five minutes for the redirect, then exchanges the code (PKCE S256). A
// failure to open the browser is not fatal when opt.Notify shows the URL. ErrNoAuthNeeded means the server asks for
// no login.
func Login(ctx context.Context, rem Remote, opt LoginOptions) (*OAuthToken, error) {
	server, err := Discover(ctx, rem.URL)
	if err != nil {
		return nil, err
	}
	state, err := randomString(16)
	if err != nil {
		return nil, err
	}
	verifier, err := randomString(32)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	cb, err := newCallbackServer(state)
	if err != nil {
		return nil, err
	}
	defer cb.close()
	redirectURI := cb.redirectURI()
	clientID, err := register(ctx, server.RegistrationEndpoint, redirectURI)
	if err != nil {
		return nil, err
	}
	authURL, err := authorizeURL(server.AuthorizationEndpoint, url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {server.Resource},
	})
	if err != nil {
		return nil, fmt.Errorf("authorization URL: %w", err)
	}

	if opt.Notify != nil {
		opt.Notify(authURL)
	}
	open := opt.OpenBrowser
	if open == nil {
		open = openDefaultBrowser
	}
	if err := open(authURL); err != nil && opt.Notify == nil {
		return nil, fmt.Errorf("open the browser: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	var code string
	select {
	case res := <-cb.result:
		if res.err != nil {
			return nil, res.err
		}
		code = res.code
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("timed out after %s waiting for the browser login", loginTimeout)
	}

	tr, err := requestToken(ctx, server.TokenEndpoint, "exchange the authorization code", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {server.Resource},
	})
	if err != nil {
		return nil, err
	}
	return &OAuthToken{
		ClientID:           clientID,
		TokenEndpoint:      server.TokenEndpoint,
		Resource:           server.Resource,
		RevocationEndpoint: server.RevocationEndpoint,
		AccessToken:        tr.AccessToken,
		RefreshToken:       tr.RefreshToken,
		ExpiresAt:          tr.expiry(),
	}, nil
}

// Revoke asks the authorization server to revoke the refresh token of the login (RFC 7009), so a copy of
// remotes.yaml stops working. It does nothing without a revocation endpoint or a refresh token. Callers treat a
// failure as a warning: the token is dropped locally anyway.
func (t *OAuthToken) Revoke(ctx context.Context) error {
	if t == nil || t.RevocationEndpoint == "" || t.RefreshToken == "" {
		return nil
	}
	form := url.Values{"token": {t.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {t.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("revoke the login: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: oauthHTTPTimeout, CheckRedirect: noRedirect}).Do(req)
	if err != nil {
		return fmt.Errorf("revoke the login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return readOAuthError(resp, "revoke the login")
	}
	return nil
}

// accessToken returns the access token to send, refreshing it first when it is missing or expires within
// refreshMargin and a refresh token is stored.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tok
	expiring := !t.ExpiresAt.IsZero() && time.Until(t.ExpiresAt) < refreshMargin
	if t.RefreshToken != "" && (t.AccessToken == "" || expiring) {
		if err := c.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	return c.tok.AccessToken, nil
}

// refreshAfterReject refreshes the login after the server rejected a request sent with access token used. It
// returns false when there is no refresh token to try; when another request already refreshed, it only reports true.
func (c *Client) refreshAfterReject(ctx context.Context, used string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok.AccessToken != used {
		return true, nil
	}
	if c.tok.RefreshToken == "" {
		return false, nil
	}
	return true, c.refreshLocked(ctx)
}

// refreshLocked exchanges the refresh token for a new access token and persists the (rotated) refresh token through
// the token saver at once. The caller holds c.mu. An invalid_grant answer is ErrSessionExpired.
func (c *Client) refreshLocked(ctx context.Context) error {
	t := c.tok
	if t.TokenEndpoint == "" || t.ClientID == "" {
		return errors.New("the stored login is incomplete: run `quet web login`")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {t.ClientID},
	}
	if t.Resource != "" {
		form.Set("resource", t.Resource)
	}
	tr, err := requestToken(ctx, t.TokenEndpoint, "refresh the login", form)
	if err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && oe.code == "invalid_grant" {
			return ErrSessionExpired
		}
		return err
	}
	next := *t
	next.AccessToken = tr.AccessToken
	next.ExpiresAt = tr.expiry()
	if tr.RefreshToken != "" {
		next.RefreshToken = tr.RefreshToken
	}
	c.tok = &next
	if c.saver != nil {
		saved := next
		if err := c.saver(&saved); err != nil {
			return fmt.Errorf("save the refreshed login: %w", err)
		}
	}
	return nil
}
