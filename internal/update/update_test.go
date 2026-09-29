package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releases serves a fake releases site: /latest redirects to tag, and
// /download/v<version>/ serves files by name.
func releases(t *testing.T, tag string, version string, files map[string][]byte) *httptest.Server {
	t.Helper()
	prefix := "/download/v" + version + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, prefix)
		body, found := files[name]
		if !ok || !found {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// hostAsset is the asset name for the test host, skipping unsupported hosts.
func hostAsset(t *testing.T) string {
	t.Helper()
	name, err := Asset()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	return name
}

// sumOf renders a SHA256SUMS line for data under name.
func sumOf(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// writeExe creates an "executable" with the given contents and returns its path.
func writeExe(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "quet")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertNoTemp fails if an update temp file was left in dir.
func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, ".quet-update-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestNewReadsReleasesURLFromEnv(t *testing.T) {
	t.Setenv(releasesEnv, "https://mirror.example/quet/releases/")
	if got := New().base(); got != "https://mirror.example/quet/releases" {
		t.Fatalf("base = %q", got)
	}
	t.Setenv(releasesEnv, "")
	if got := New().base(); got != DefaultReleasesURL {
		t.Fatalf("base = %q, want default", got)
	}
}

func TestLatestParsesRedirect(t *testing.T) {
	srv := releases(t, "v0.3.1", "0.3.1", nil)
	c := &Client{ReleasesURL: srv.URL + "/"}
	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.3.1" {
		t.Fatalf("Latest = %q, want 0.3.1", got)
	}
}

func TestLatestRejectsNonReleaseRedirect(t *testing.T) {
	srv := releases(t, "nightly", "0.3.1", nil)
	c := &Client{ReleasesURL: srv.URL}
	if got, err := c.Latest(context.Background()); err == nil {
		t.Fatalf("Latest = %q, want error for a non-version tag", got)
	}
}

func TestLatestErrorsWithoutRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("latest release page"))
	}))
	t.Cleanup(srv.Close)
	c := &Client{ReleasesURL: srv.URL}
	if got, err := c.Latest(context.Background()); err == nil {
		t.Fatalf("Latest = %q, want error for a 200 response", got)
	}
}

func TestInstallReplacesExecutable(t *testing.T) {
	name := hostAsset(t)
	bin := []byte("#!/bin/sh\necho new quet\n")
	srv := releases(t, "v0.2.0", "0.2.0", map[string][]byte{
		name:      bin,
		sumsAsset: []byte(sumOf([]byte("other"), "quet_plan9_mips") + sumOf(bin, "*"+name)),
	})
	dir := t.TempDir()
	exe := writeExe(t, dir, "old quet")
	if err := os.Chmod(exe, 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Client{ReleasesURL: srv.URL}
	if err := c.Install(context.Background(), "0.2.0", exe); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bin) {
		t.Fatalf("exe = %q, want %q", got, bin)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("mode = %v, want 0755", perm)
	}
	assertNoTemp(t, dir)
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	name := hostAsset(t)
	bin := []byte("tampered quet")
	cases := map[string]string{
		"mismatch":      sumOf([]byte("genuine quet"), name),
		"missing entry": sumOf(bin, "quet_plan9_mips"),
		"missing file":  "",
	}
	for label, sums := range cases {
		t.Run(label, func(t *testing.T) {
			files := map[string][]byte{name: bin}
			if sums != "" {
				files[sumsAsset] = []byte(sums)
			}
			srv := releases(t, "v0.2.0", "0.2.0", files)
			dir := t.TempDir()
			exe := writeExe(t, dir, "old quet")

			c := &Client{ReleasesURL: srv.URL}
			if err := c.Install(context.Background(), "0.2.0", exe); err == nil {
				t.Fatal("Install succeeded, want checksum error")
			}

			got, err := os.ReadFile(exe)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "old quet" {
				t.Fatalf("exe = %q, want it untouched", got)
			}
			assertNoTemp(t, dir)
		})
	}
}

func TestInstallFollowsSymlink(t *testing.T) {
	name := hostAsset(t)
	bin := []byte("new quet")
	srv := releases(t, "v0.2.0", "0.2.0", map[string][]byte{
		name:      bin,
		sumsAsset: []byte(sumOf(bin, name)),
	})
	root := t.TempDir()
	realDir := filepath.Join(root, "cellar")
	binDir := filepath.Join(root, "bin")
	for _, d := range []string{realDir, binDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := writeExe(t, realDir, "old quet")
	link := filepath.Join(binDir, "quet")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	c := &Client{ReleasesURL: srv.URL}
	if err := c.Install(context.Background(), "v0.2.0", link); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink", link)
	}
	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Fatalf("link points to %q (%v), want %q", dest, err, target)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bin) {
		t.Fatalf("target = %q, want %q", got, bin)
	}
	assertNoTemp(t, realDir)
	assertNoTemp(t, binDir)
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.2", "0.1.3", true},
		{"0.1.2", "0.1.2", false},
		{"0.2.0", "0.1.9", false},
		{"0.1.2-3-gabc-dirty", "0.1.3", true},
		{"0.1.2-3-gabc", "0.1.2", false},
		{"garbage", "0.1.3", false},
		{"0.1.2", "garbage", false},
		{"0.1.10", "0.1.9", false},
		{"0.1.9", "0.1.10", true},
		{"v0.9.9", "v1.0.0", true},
	}
	for _, tc := range cases {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestAssetRejectsUnreleasedPlatforms(t *testing.T) {
	if got, err := asset("linux", "arm64"); err != nil || got != "quet_linux_arm64" {
		t.Fatalf("asset(linux, arm64) = %q, %v", got, err)
	}
	for _, p := range [][2]string{{"windows", "amd64"}, {"linux", "386"}} {
		if got, err := asset(p[0], p[1]); err == nil {
			t.Errorf("asset(%s, %s) = %q, want error", p[0], p[1], got)
		}
	}
}
