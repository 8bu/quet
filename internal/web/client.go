package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/8bu/quet/internal/annotate"
)

// pageSize is the largest page / chunk the server accepts.
const pageSize = 1000

// maxErrorBody bounds how much of an error response is read; maxMessage bounds a non-JSON error message.
const (
	maxErrorBody = 64 << 10
	maxMessage   = 200
)

// accessHint is appended to errors that look like Cloudflare Access rejecting the service token.
const accessHint = "Cloudflare Access rejected the credentials (run `quet web login`)"

// ErrNotFound is wrapped by the error of a request for something that does not exist (HTTP 404).
var ErrNotFound = errors.New("not found")

// Invalid is one stored label the server refused to keep (an update of a project schema, 409).
type Invalid struct {
	Collaborator string `json:"collaborator"`
	ID           string `json:"id"`
	Error        string `json:"error"`
}

// APIError is a non-2xx response of the server.
type APIError struct {
	Status  int
	Message string
	Invalid []Invalid // the first invalid entries of a 409, when the server lists them
}

// Error describes the response, listing at most three invalid entries.
func (e *APIError) Error() string {
	s := fmt.Sprintf("quet-web: %s (HTTP %d)", e.Message, e.Status)
	for i, inv := range e.Invalid {
		if i == 3 {
			s += fmt.Sprintf("; and %d more", len(e.Invalid)-3)
			break
		}
		s += fmt.Sprintf("; %s %s: %s", inv.Collaborator, inv.ID, inv.Error)
	}
	return s
}

// Is makes a 404 response match ErrNotFound.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// ProjectSummary is one entry of the project list.
type ProjectSummary struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Items         int    `json:"items"`
	Proposals     int    `json:"proposals"`
	Collaborators int    `json:"collaborators"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

// CollabProgress is one collaborator's progress on a project.
type CollabProgress struct {
	Username    string `json:"username"`
	Disabled    bool   `json:"disabled"`
	Labelled    int    `json:"labelled"`
	Complete    int    `json:"complete"`
	Uncertain   int    `json:"uncertain"`
	Skipped     int    `json:"skipped"`
	LastLabelAt *int64 `json:"last_label_at"`
}

// Project is a server project with its schema and the progress of its collaborators.
type Project struct {
	Slug          string           `json:"slug"`
	Name          string           `json:"name"`
	SchemaYAML    string           `json:"schema_yaml"`
	Schema        json.RawMessage  `json:"schema"`
	Items         int              `json:"items"`
	Proposals     int              `json:"proposals"`
	CreatedAt     int64            `json:"created_at"`
	UpdatedAt     int64            `json:"updated_at"`
	Collaborators []CollabProgress `json:"collaborators"`
}

// WebItem is one queue record stored on the server.
type WebItem struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Text     string `json:"text"`
}

// RemoteLabel is one collaborator's label of a record, as a Quet label JSON object.
type RemoteLabel struct {
	Collaborator string          `json:"collaborator"`
	UpdatedAt    int64           `json:"updated_at"`
	Label        json.RawMessage `json:"label"`
}

// Client talks to the admin API of one quet-web server. It is safe for concurrent use.
type Client struct {
	base, id, secret string
	http             *http.Client

	// OAuth state: oauth is set when requests carry the stored browser login tok (otherwise a service token or
	// nothing) and saver persists it after each refresh; mu guards tok.
	oauth bool
	mu    sync.Mutex
	tok   *OAuthToken
	saver func(*OAuthToken) error
}

// ClientOption customizes NewClient.
type ClientOption func(*Client)

// WithTokenSaver sets the function that persists the OAuth token after each refresh (the refresh token rotates, so
// the new one must be stored at once). The CLI and the TUI pass a saver that updates remotes.yaml.
func WithTokenSaver(save func(*OAuthToken) error) ClientOption {
	return func(c *Client) { c.saver = save }
}

// NewClient returns a client for r. An empty URL is an error; a missing credential is allowed (a local development
// server accepts unauthenticated admin requests). A stored OAuth token (r.OAuth) is sent as a Bearer token and
// takes precedence over the service token of r (ClientID and ClientSecret); Remotes.Resolve drops the OAuth token
// in favour of a service token from the environment.
func NewClient(r Remote, opts ...ClientOption) (*Client, error) {
	base := normalizeURL(strings.TrimSpace(r.URL))
	if base == "" {
		return nil, errors.New("remote URL is empty")
	}
	c := &Client{
		base:   base,
		id:     r.ClientID,
		secret: r.ClientSecret,
		http: &http.Client{
			Timeout: 60 * time.Second,
			// Access answers rejected credentials with a redirect to its login page: report it, do not follow it.
			CheckRedirect: noRedirect,
		},
	}
	if r.OAuth != nil && (r.OAuth.AccessToken != "" || r.OAuth.RefreshToken != "") {
		tok := *r.OAuth
		c.tok, c.oauth = &tok, true
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// roundTrip sends one admin API request with the credentials of the client and returns the response and the OAuth
// access token it was sent with ("" when none). data, when non-nil, is the JSON body.
func (c *Client) roundTrip(ctx context.Context, method, target, path string, data []byte) (*http.Response, string, error) {
	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	access := ""
	if c.oauth {
		if access, err = c.accessToken(ctx); err != nil {
			return nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+access)
	} else {
		// Header keys are set verbatim (not canonicalized) to match the names Cloudflare documents.
		if c.id != "" {
			req.Header["CF-Access-Client-Id"] = []string{c.id}
		}
		if c.secret != "" {
			req.Header["CF-Access-Client-Secret"] = []string{c.secret}
		}
	}
	req.Header.Set("Accept", "application/json")
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, access, nil
}

// do sends one admin API request. A non-nil in is sent as the JSON body; a non-nil out receives the JSON response.
// With an OAuth login the access token is refreshed first when it is about to expire, and once more (with one retry
// of the request) when the server answers 401 or 302.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var data []byte
	if in != nil {
		var err error
		if data, err = json.Marshal(in); err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	resp, used, err := c.roundTrip(ctx, method, target, path, data)
	if err != nil {
		return err
	}
	if c.oauth && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusFound) {
		retry, err := c.refreshAfterReject(ctx, used)
		if err != nil {
			resp.Body.Close()
			return err
		}
		if retry {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
			resp.Body.Close()
			if resp, _, err = c.roundTrip(ctx, method, target, path, data); err != nil {
				return err
			}
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeAPIError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

// decodeAPIError builds the APIError of a non-2xx response: the server's {"error","invalid"} body when it has one,
// else the trimmed body (a Cloudflare Access page, say) with a hint for the statuses Access uses to reject.
func decodeAPIError(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	e := &APIError{Status: resp.StatusCode}
	var body struct {
		Error   string    `json:"error"`
		Invalid []Invalid `json:"invalid"`
	}
	if json.Unmarshal(raw, &body) == nil && (body.Error != "" || len(body.Invalid) > 0) {
		e.Message, e.Invalid = body.Error, body.Invalid
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return e
	}
	e.Message = truncateUTF8(strings.TrimSpace(string(raw)), maxMessage)
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusFound, http.StatusUnauthorized, http.StatusForbidden:
		e.Message += " - " + accessHint
	}
	return e
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// projectPath returns the admin API path of project slug plus the optional sub-path suffix.
func projectPath(slug, suffix string) string {
	return "/api/admin/projects/" + url.PathEscape(slug) + suffix
}

// Whoami returns the identity the server sees (an email or a service token name).
func (c *Client) Whoami(ctx context.Context) (string, error) {
	var out struct {
		Identity string `json:"identity"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/admin/whoami", nil, nil, &out); err != nil {
		return "", err
	}
	return out.Identity, nil
}

// Projects lists the projects of the server.
func (c *Client) Projects(ctx context.Context) ([]ProjectSummary, error) {
	var out struct {
		Projects []ProjectSummary `json:"projects"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/admin/projects", nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Projects, nil
}

// Project returns project slug with its collaborators' progress. A missing project is an error wrapping
// ErrNotFound.
func (c *Client) Project(ctx context.Context, slug string) (*Project, error) {
	var out struct {
		Project       Project          `json:"project"`
		Collaborators []CollabProgress `json:"collaborators"`
	}
	if err := c.do(ctx, http.MethodGet, projectPath(slug, ""), nil, nil, &out); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status == http.StatusNotFound {
			return nil, fmt.Errorf("project %q: %w", slug, ErrNotFound)
		}
		return nil, err
	}
	p := out.Project
	p.Collaborators = out.Collaborators
	return &p, nil
}

// PutProject creates or updates project slug with the normalized schema and the schema YAML. An empty name omits
// the field: the server names a new project after its slug and keeps the name of an existing one.
func (c *Client) PutProject(ctx context.Context, slug, name string, schema json.RawMessage, schemaYAML string) (created bool, err error) {
	in := struct {
		Schema     json.RawMessage `json:"schema"`
		SchemaYAML string          `json:"schema_yaml"`
		Name       string          `json:"name,omitempty"`
	}{schema, schemaYAML, name}
	var out struct {
		Created bool `json:"created"`
	}
	if err := c.do(ctx, http.MethodPut, projectPath(slug, ""), nil, in, &out); err != nil {
		return false, err
	}
	return out.Created, nil
}

// PushItems upserts the queue records in chunks of 1000; each record's position is its index in items.
func (c *Client) PushItems(ctx context.Context, slug string, items []annotate.Item) (inserted, updated, unchanged int, err error) {
	type wireItem struct {
		ID       string `json:"id"`
		Text     string `json:"text"`
		Position int    `json:"position"`
	}
	for start := 0; start < len(items); start += pageSize {
		end := min(start+pageSize, len(items))
		chunk := make([]wireItem, 0, end-start)
		for i := start; i < end; i++ {
			chunk = append(chunk, wireItem{items[i].ID, items[i].Text, i})
		}
		var out struct {
			Inserted  int `json:"inserted"`
			Updated   int `json:"updated"`
			Unchanged int `json:"unchanged"`
		}
		if err := c.do(ctx, http.MethodPost, projectPath(slug, "/items"), nil, map[string]any{"items": chunk}, &out); err != nil {
			return inserted, updated, unchanged, err
		}
		inserted += out.Inserted
		updated += out.Updated
		unchanged += out.Unchanged
	}
	return inserted, updated, unchanged, nil
}

// ReplaceProposals deletes every proposal of the project, then uploads raw (Quet proposal objects) in chunks of
// 1000. ignored lists the ids the server did not know as items.
func (c *Client) ReplaceProposals(ctx context.Context, slug string, raw []json.RawMessage) (upserted int, ignored []string, err error) {
	if err := c.do(ctx, http.MethodDelete, projectPath(slug, "/proposals"), nil, nil, nil); err != nil {
		return 0, nil, err
	}
	for start := 0; start < len(raw); start += pageSize {
		end := min(start+pageSize, len(raw))
		var out struct {
			Upserted int      `json:"upserted"`
			Ignored  []string `json:"ignored"`
		}
		if err := c.do(ctx, http.MethodPost, projectPath(slug, "/proposals"), nil, map[string]any{"proposals": raw[start:end]}, &out); err != nil {
			return upserted, ignored, err
		}
		upserted += out.Upserted
		ignored = append(ignored, out.Ignored...)
	}
	return upserted, ignored, nil
}

// Items returns every record of the project in position order.
func (c *Client) Items(ctx context.Context, slug string) ([]WebItem, error) {
	var all []WebItem
	for {
		q := url.Values{"offset": {strconv.Itoa(len(all))}, "limit": {strconv.Itoa(pageSize)}}
		var out struct {
			Items []WebItem `json:"items"`
			Total int       `json:"total"`
		}
		if err := c.do(ctx, http.MethodGet, projectPath(slug, "/items"), q, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if len(out.Items) == 0 || len(all) >= out.Total {
			return all, nil
		}
	}
}

// Labels returns the labels of collaborator (every collaborator when empty), following the pagination cursor.
func (c *Client) Labels(ctx context.Context, slug, collaborator string) ([]RemoteLabel, error) {
	var all []RemoteLabel
	cursor := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}}
		if collaborator != "" {
			q.Set("collaborator", collaborator)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var out struct {
			Labels     []RemoteLabel `json:"labels"`
			NextCursor *string       `json:"next_cursor"`
		}
		if err := c.do(ctx, http.MethodGet, projectPath(slug, "/labels"), q, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Labels...)
		if out.NextCursor == nil || *out.NextCursor == "" {
			return all, nil
		}
		if *out.NextCursor == cursor {
			return nil, fmt.Errorf("labels of %q: the server repeated pagination cursor %q", slug, cursor)
		}
		cursor = *out.NextCursor
	}
}
