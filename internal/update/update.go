// Package update finds the latest quet release and replaces the running binary
// with it.
//
// It talks to the plain releases site rather than the GitHub API, so checks are
// not subject to API rate limits: the latest version comes from the redirect of
// <releases>/latest, and assets come from <releases>/download/v<version>/. The
// base URL honours $QUET_RELEASES_URL, the same variable install.sh reads, so a
// mirror serves both.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DefaultReleasesURL is where quet releases are published.
const DefaultReleasesURL = "https://github.com/8bu/quet/releases"

// releasesEnv overrides the releases base URL, e.g. for a mirror.
const releasesEnv = "QUET_RELEASES_URL"

// sumsAsset is the checksum manifest published with every release.
const sumsAsset = "SHA256SUMS"

// Client talks to a releases site. A nil HTTP means http.DefaultClient;
// timeouts are the caller's business, via the context.
type Client struct {
	HTTP        *http.Client
	ReleasesURL string
}

// New returns a Client for $QUET_RELEASES_URL (trailing "/" trimmed), or for
// DefaultReleasesURL when the variable is unset or empty.
func New() *Client {
	return &Client{ReleasesURL: strings.TrimRight(os.Getenv(releasesEnv), "/")}
}

// base is the releases URL without a trailing slash, defaulted when empty.
func (c *Client) base() string {
	if u := strings.TrimRight(c.ReleasesURL, "/"); u != "" {
		return u
	}
	return DefaultReleasesURL
}

// client is the HTTP client to use, http.DefaultClient when none is set.
func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Latest reports the newest released version, without a leading "v". It reads
// the Location of the <releases>/latest redirect instead of following it.
func (c *Client) Latest(ctx context.Context) (string, error) {
	url := c.base() + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	hc := *c.client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: checking latest release: %w", err)
	}
	defer drain(resp.Body)
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", fmt.Errorf("update: GET %s: expected a redirect to the latest release, got %s", url, resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("update: GET %s: redirect without a usable Location: %w", url, err)
	}
	return tagVersion(loc.Path)
}

// tagVersion extracts the version from a release page path ending in
// "/tag/v<version>", rejecting anything that is not a release tag.
func tagVersion(path string) (string, error) {
	i := strings.LastIndex(path, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("update: latest release redirect %q does not name a release tag", path)
	}
	tag := path[i+len("/tag/"):]
	v, ok := strings.CutPrefix(tag, "v")
	if !ok || strings.Contains(v, "/") {
		return "", fmt.Errorf("update: latest release tag %q is not of the form v<version>", tag)
	}
	if _, ok := parse(v); !ok {
		return "", fmt.Errorf("update: latest release tag %q is not a semantic version", tag)
	}
	return v, nil
}

// Newer reports whether latest is a later release than current, comparing
// major.minor.patch numerically. Pre-release and build suffixes (including a
// git-describe suffix on a dev build) are ignored; unparsable input is never
// newer.
func Newer(current, latest string) bool {
	cur, ok := parse(current)
	if !ok {
		return false
	}
	lat, ok := parse(latest)
	if !ok {
		return false
	}
	for i := range cur {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

// parse reads the major.minor.patch core of a version, after an optional
// leading "v" and before any '-' or '+' suffix.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != len(out) {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Asset names the release asset for this platform, "quet_<GOOS>_<GOARCH>".
// Only the platforms quet is released for are supported.
func Asset() (string, error) {
	return asset(runtime.GOOS, runtime.GOARCH)
}

// asset names the release asset for goos/goarch, or reports that no such
// release is published.
func asset(goos, goarch string) (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("update: no quet release for operating system %s", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("update: no quet release for architecture %s/%s", goos, goarch)
	}
	return "quet_" + goos + "_" + goarch, nil
}

// Install downloads version's asset for this platform, verifies it against the
// release's SHA256SUMS, and atomically replaces exePath with it. A symlinked
// exePath replaces the file it points to. On any error exePath is untouched.
func (c *Client) Install(ctx context.Context, version, exePath string) error {
	name, err := Asset()
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("update: resolving %s: %w", exePath, err)
	}
	dir := c.base() + "/download/v" + strings.TrimPrefix(version, "v") + "/"
	sums, err := c.fetch(ctx, dir+sumsAsset)
	if err != nil {
		return err
	}
	want, err := checksum(sums, name)
	if err != nil {
		return err
	}
	bin, err := c.fetch(ctx, dir+name)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("update: checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return replace(target, bin)
}

// fetch GETs url and returns its body, failing on anything but 200 OK.
func (c *Client) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: downloading %s: %w", url, err)
	}
	defer drain(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("update: downloading %s: %w", url, err)
	}
	return body, nil
}

// checksum finds name's lowercase hex SHA-256 in a SHA256SUMS manifest of
// "<hex>  <name>" lines; a binary-mode "*<name>" is accepted too.
func checksum(sums []byte, name string) (string, error) {
	for line := range strings.Lines(string(sums)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
			return "", fmt.Errorf("update: %s has a malformed checksum for %s", sumsAsset, name)
		}
		return sum, nil
	}
	return "", fmt.Errorf("update: %s has no checksum for %s", sumsAsset, name)
}

// replace atomically swaps target's contents for bin, via an executable temp
// file in the same directory. The temp file is removed on any failure.
func replace(target string, bin []byte) (err error) {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".quet-update-*")
	if err != nil {
		return writeErr(dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(bin); err != nil {
		f.Close()
		return writeErr(tmp, err)
	}
	if err := f.Close(); err != nil {
		return writeErr(tmp, err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return writeErr(tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return writeErr(target, err)
	}
	return nil
}

// writeErr wraps a failure to write path, pointing permission problems at the
// ways around them.
func writeErr(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("update: cannot write %s: %w (re-run with sufficient permissions, e.g. with sudo, or reinstall with the install script)", path, err)
	}
	return fmt.Errorf("update: writing %s: %w", path, err)
}

// drain discards and closes a response body so the connection can be reused.
func drain(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	body.Close()
}
