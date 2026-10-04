package web

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/8bu/quet/internal/config"
	"gopkg.in/yaml.v3"
)

// Environment variables that override the resolved remote (and name the fallback URL).
const (
	EnvURL          = "QUET_WEB_URL"
	EnvClientID     = "QUET_ACCESS_CLIENT_ID"
	EnvClientSecret = "QUET_ACCESS_CLIENT_SECRET"
)

// DefaultURL is the server used when no remote is configured and QUET_WEB_URL is unset.
const DefaultURL = "https://quet.8bu.dev"

// envRemoteName is the name of the synthetic remote Resolve builds when nothing is configured.
const envRemoteName = "env"

// nameRE is the syntax of a remote name.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// Remote is one named quet-web server with its Cloudflare Access service token (either may be empty for a local
// development server).
type Remote struct{ Name, URL, ClientID, ClientSecret string }

// Remotes is the contents of remotes.yaml: the default remote's name and the remotes in file order.
type Remotes struct {
	Default string
	List    []Remote
}

// remoteEntry is one remote as stored under its name in remotes.yaml.
type remoteEntry struct {
	URL          string `yaml:"url"`
	ClientID     string `yaml:"client_id,omitempty"`
	ClientSecret string `yaml:"client_secret,omitempty"`
}

// remoteMap is the remotes mapping of remotes.yaml; it keeps the file order.
type remoteMap []Remote

// UnmarshalYAML decodes the name→entry mapping in file order and rejects duplicate names.
func (m *remoteMap) UnmarshalYAML(n *yaml.Node) error {
	*m = nil
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: remotes must be a mapping of name to remote", n.Line)
	}
	seen := map[string]bool{}
	for k := 0; k+1 < len(n.Content); k += 2 {
		key := n.Content[k]
		if seen[key.Value] {
			return fmt.Errorf("line %d: duplicate remote %q", key.Line, key.Value)
		}
		seen[key.Value] = true
		var e remoteEntry
		if err := n.Content[k+1].Decode(&e); err != nil {
			return fmt.Errorf("remote %q: %w", key.Value, err)
		}
		*m = append(*m, Remote{Name: key.Value, URL: e.URL, ClientID: e.ClientID, ClientSecret: e.ClientSecret})
	}
	return nil
}

// MarshalYAML encodes the remotes as a name→entry mapping in list order.
func (m remoteMap) MarshalYAML() (any, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, r := range m {
		var val yaml.Node
		if err := val.Encode(remoteEntry{URL: r.URL, ClientID: r.ClientID, ClientSecret: r.ClientSecret}); err != nil {
			return nil, err
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: r.Name}, &val)
	}
	return root, nil
}

// remotesDoc is the on-disk shape of remotes.yaml.
type remotesDoc struct {
	Default string    `yaml:"default,omitempty"`
	Remotes remoteMap `yaml:"remotes,omitempty"`
}

// RemotesPath returns the path of remotes.yaml in the per-user Quet directory ("" when there is none).
func RemotesPath() string {
	dir := config.QuetDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "remotes.yaml")
}

// LoadRemotes reads remotes.yaml. A missing file is not an error: the result is empty.
func LoadRemotes() (*Remotes, error) {
	path := RemotesPath()
	if path == "" {
		return nil, errors.New("cannot locate the Quet config directory")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Remotes{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read remotes: %w", err)
	}
	var doc remotesDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("remotes %s: %w", path, err)
	}
	return &Remotes{Default: doc.Default, List: []Remote(doc.Remotes)}, nil
}

// Save validates the remotes and atomically writes remotes.yaml with mode 0600 (it holds secrets), creating the
// Quet directory when needed.
func (r *Remotes) Save() error {
	path := RemotesPath()
	if path == "" {
		return errors.New("cannot locate the Quet config directory")
	}
	seen := map[string]bool{}
	for _, rem := range r.List {
		if err := rem.Validate(); err != nil {
			return err
		}
		if seen[rem.Name] {
			return fmt.Errorf("duplicate remote %q", rem.Name)
		}
		seen[rem.Name] = true
	}
	if r.Default != "" && !seen[r.Default] {
		return fmt.Errorf("default remote %q does not exist", r.Default)
	}
	data, err := yaml.Marshal(remotesDoc{Default: r.Default, Remotes: remoteMap(r.List)})
	if err != nil {
		return fmt.Errorf("encode remotes: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// Validate reports whether the remote has a valid name and URL (ValidateName, ValidateURL).
func (r Remote) Validate() error {
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	return ValidateURL(r.URL)
}

// ValidateName requires a remote name of 1-32 characters: letters, digits, '.', '_' and '-', starting with a
// letter or digit.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid remote name %q (want letters, digits, '.', '_' and '-', starting with a letter or digit, at most 32 characters)", name)
	}
	return nil
}

// ValidateURL requires an http:// or https:// URL with a host and optional port, without user info, path, query or
// fragment (a single trailing slash is accepted).
func ValidateURL(raw string) error {
	bad := func(why string) error {
		return fmt.Errorf("invalid remote URL %q (%s)", raw, why)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("not a URL")
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return bad("want http:// or https://")
	case u.Host == "" || u.Hostname() == "":
		return bad("missing host")
	case u.User != nil:
		return bad("user info is not allowed")
	case strings.ContainsAny(raw, "?#"):
		return bad("query and fragment are not allowed")
	case u.Path != "" && u.Path != "/":
		return bad("path is not allowed")
	}
	return nil
}

// normalizeURL drops a single trailing slash.
func normalizeURL(u string) string { return strings.TrimSuffix(u, "/") }

// index returns the position of the remote called name in the list, or -1.
func (r *Remotes) index(name string) int {
	for i, rem := range r.List {
		if rem.Name == name {
			return i
		}
	}
	return -1
}

// Get returns the remote called name.
func (r *Remotes) Get(name string) (Remote, bool) {
	if i := r.index(name); i >= 0 {
		return r.List[i], true
	}
	return Remote{}, false
}

// Add appends a new remote. The name and URL must be valid and the name unused; the first remote becomes the
// default.
func (r *Remotes) Add(rem Remote) error {
	rem.URL = normalizeURL(rem.URL)
	if err := rem.Validate(); err != nil {
		return err
	}
	if r.index(rem.Name) >= 0 {
		return fmt.Errorf("remote %q already exists", rem.Name)
	}
	r.List = append(r.List, rem)
	if r.Default == "" {
		r.Default = rem.Name
	}
	return nil
}

// Set adds the remote or replaces the one with the same name (login and the TUI form). The first remote becomes
// the default. Set does not validate: Save does.
func (r *Remotes) Set(rem Remote) {
	rem.URL = normalizeURL(rem.URL)
	if i := r.index(rem.Name); i >= 0 {
		r.List[i] = rem
	} else {
		r.List = append(r.List, rem)
	}
	if r.Default == "" {
		r.Default = rem.Name
	}
}

// Remove deletes the remote called name and clears the default when it was that remote.
func (r *Remotes) Remove(name string) error {
	i := r.index(name)
	if i < 0 {
		return fmt.Errorf("unknown remote %q", name)
	}
	r.List = append(r.List[:i], r.List[i+1:]...)
	if r.Default == name {
		r.Default = ""
	}
	return nil
}

// Resolve picks the remote to use: the one called name; else the default; else a synthetic remote "env" with URL
// $QUET_WEB_URL or DefaultURL. QUET_WEB_URL, QUET_ACCESS_CLIENT_ID and QUET_ACCESS_CLIENT_SECRET, when non-empty,
// then override the URL, client id and client secret of the picked remote.
func (r *Remotes) Resolve(name string) (Remote, error) {
	var rem Remote
	switch {
	case name != "":
		got, ok := r.Get(name)
		if !ok {
			return Remote{}, fmt.Errorf("unknown remote %q", name)
		}
		rem = got
	case r.Default != "":
		got, ok := r.Get(r.Default)
		if !ok {
			return Remote{}, fmt.Errorf("default remote %q does not exist", r.Default)
		}
		rem = got
	default:
		rem = Remote{Name: envRemoteName, URL: DefaultURL}
	}
	if v := os.Getenv(EnvURL); v != "" {
		rem.URL = v
	}
	if v := os.Getenv(EnvClientID); v != "" {
		rem.ClientID = v
	}
	if v := os.Getenv(EnvClientSecret); v != "" {
		rem.ClientSecret = v
	}
	return rem, nil
}
