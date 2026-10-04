package web

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// isolate points the Quet config directory at a fresh temp dir and clears the override variables.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, k := range []string{EnvURL, EnvClientID, EnvClientSecret} {
		t.Setenv(k, "")
	}
	return dir
}

func TestRemotesPath(t *testing.T) {
	dir := isolate(t)
	if got, want := RemotesPath(), filepath.Join(dir, "quet", "remotes.yaml"); got != want {
		t.Errorf("RemotesPath = %q, want %q", got, want)
	}
}

func TestLoadRemotesMissing(t *testing.T) {
	isolate(t)
	r, err := LoadRemotes()
	if err != nil || r.Default != "" || len(r.List) != 0 {
		t.Fatalf("LoadRemotes = %+v, %v", r, err)
	}
}

func TestRemotesRoundTrip(t *testing.T) {
	isolate(t)
	r := &Remotes{}
	for _, rem := range []Remote{
		{Name: "zeta", URL: "https://quet.example.com", ClientID: "id.access", ClientSecret: "sec"},
		{Name: "alpha", URL: "http://localhost:8787/"}, // trailing slash normalized; no credentials
		{Name: "mid", URL: "https://x.example.com:8443", ClientID: "only-id"},
	} {
		if err := r.Add(rem); err != nil {
			t.Fatal(err)
		}
	}
	if r.Default != "zeta" {
		t.Errorf("Default = %q, want the first remote", r.Default)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(RemotesPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if dirInfo, _ := os.Stat(filepath.Dir(RemotesPath())); dirInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("config dir mode = %v, want no group/other access", dirInfo.Mode().Perm())
	}

	got, err := LoadRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, r)
	}
	if names := []string{got.List[0].Name, got.List[1].Name, got.List[2].Name}; !reflect.DeepEqual(names, []string{"zeta", "alpha", "mid"}) {
		t.Errorf("file order lost: %v", names)
	}
	if got.List[1].URL != "http://localhost:8787" {
		t.Errorf("URL = %q", got.List[1].URL)
	}
	if rem, ok := got.Get("mid"); !ok || rem.ClientID != "only-id" || rem.ClientSecret != "" {
		t.Errorf("Get(mid) = %+v, %v", rem, ok)
	}
	if _, ok := got.Get("nope"); ok {
		t.Error("Get(nope) found a remote")
	}
}

func TestRemotesSaveFixesLooseMode(t *testing.T) {
	isolate(t)
	r := &Remotes{}
	if err := r.Add(Remote{Name: "o", URL: "https://a.example"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(RemotesPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(RemotesPath()); info.Mode().Perm() != 0o600 {
		t.Errorf("mode after re-save = %v, want 0600", info.Mode().Perm())
	}
	// No temp files are left behind.
	entries, _ := os.ReadDir(filepath.Dir(RemotesPath()))
	if len(entries) != 1 {
		t.Errorf("config dir holds %d entries, want only remotes.yaml", len(entries))
	}
}

func TestRemotesFileFormat(t *testing.T) {
	isolate(t)
	r := &Remotes{}
	_ = r.Add(Remote{Name: "origin", URL: "https://quet.example.com", ClientID: "xxxx.access", ClientSecret: "yyyy"})
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(RemotesPath())
	want := "default: origin\nremotes:\n    origin:\n        url: https://quet.example.com\n        client_id: xxxx.access\n        client_secret: yyyy\n"
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
}

func TestLoadRemotesRejects(t *testing.T) {
	tests := []struct{ name, content, want string }{
		{"not yaml", "default: [\n", "remotes"},
		{"remotes not a mapping", "remotes: [a]\n", "must be a mapping"},
		{"duplicate remote", "remotes:\n  a: {url: 'http://x'}\n  a: {url: 'http://y'}\n", "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolate(t)
			if err := os.MkdirAll(filepath.Dir(RemotesPath()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(RemotesPath(), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRemotes(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestRemotesAddValidation(t *testing.T) {
	tests := []struct {
		name, url string
		ok        bool
	}{
		{"origin", "https://quet.example.com", true},
		{"a", "http://localhost:8787", true},
		{"A1._-x", "https://example.com/", true},
		{strings.Repeat("a", 32), "https://example.com", true},
		{"", "https://example.com", false},
		{strings.Repeat("a", 33), "https://example.com", false},
		{"-lead", "https://example.com", false},
		{".lead", "https://example.com", false},
		{"has space", "https://example.com", false},
		{"sl/ash", "https://example.com", false},
		{"ok", "", false},
		{"ok", "quet.example.com", false},
		{"ok", "ftp://example.com", false},
		{"ok", "https://", false},
		{"ok", "https://example.com/api", false},
		{"ok", "https://example.com?x=1", false},
		{"ok", "https://example.com/?", false},
		{"ok", "https://example.com#frag", false},
		{"ok", "https://user:pw@example.com", false},
		{"ok", "https://exa mple.com", false},
	}
	for _, tt := range tests {
		r := &Remotes{}
		err := r.Add(Remote{Name: tt.name, URL: tt.url})
		if (err == nil) != tt.ok {
			t.Errorf("Add(%q, %q): err = %v, want ok=%v", tt.name, tt.url, err, tt.ok)
		}
		if err != nil && (len(r.List) != 0 || r.Default != "") {
			t.Errorf("Add(%q, %q) failed but changed the remotes: %+v", tt.name, tt.url, r)
		}
	}
}

func TestRemotesAddDuplicateSetRemove(t *testing.T) {
	r := &Remotes{}
	if err := r.Add(Remote{Name: "a", URL: "https://a.example"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Remote{Name: "b", URL: "https://b.example"}); err != nil {
		t.Fatal(err)
	}
	if r.Default != "a" {
		t.Errorf("Default = %q", r.Default)
	}
	if err := r.Add(Remote{Name: "a", URL: "https://other.example"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate Add: err = %v", err)
	}

	r.Set(Remote{Name: "b", URL: "https://b2.example/", ClientID: "i", ClientSecret: "s"})
	r.Set(Remote{Name: "c", URL: "https://c.example"})
	if got, _ := r.Get("b"); got.URL != "https://b2.example" || got.ClientSecret != "s" || len(r.List) != 3 || r.List[1].Name != "b" {
		t.Errorf("after Set: %+v", r.List)
	}

	if err := r.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if r.Default != "" || len(r.List) != 2 || r.List[0].Name != "b" {
		t.Errorf("after Remove(default): %+v", r)
	}
	r.Default = "c"
	if err := r.Remove("b"); err != nil || r.Default != "c" {
		t.Errorf("Remove(b): %v, Default = %q", err, r.Default)
	}
	if err := r.Remove("nope"); err == nil {
		t.Error("Remove(unknown): want error")
	}

	// Set on an empty list makes the remote the default; Save refuses what Set let through.
	e := &Remotes{}
	e.Set(Remote{Name: "bad name", URL: "nope"})
	isolate(t)
	if err := e.Save(); err == nil {
		t.Error("Save of an invalid remote: want error")
	}
	if _, err := os.Stat(RemotesPath()); err == nil {
		t.Error("an invalid Save wrote a file")
	}
	d := &Remotes{Default: "ghost", List: []Remote{{Name: "a", URL: "https://a.example"}}}
	if err := d.Save(); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("Save with unknown default: err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	r := &Remotes{Default: "prod", List: []Remote{
		{Name: "prod", URL: "https://prod.example", ClientID: "pid", ClientSecret: "psec"},
		{Name: "dev", URL: "http://localhost:8787"},
	}}

	t.Run("named wins over default", func(t *testing.T) {
		isolate(t)
		got, err := r.Resolve("dev")
		if err != nil || got != (Remote{Name: "dev", URL: "http://localhost:8787"}) {
			t.Errorf("Resolve(dev) = %+v, %v", got, err)
		}
	})
	t.Run("default", func(t *testing.T) {
		isolate(t)
		got, err := r.Resolve("")
		if err != nil || got.Name != "prod" || got.ClientSecret != "psec" {
			t.Errorf("Resolve() = %+v, %v", got, err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		isolate(t)
		if _, err := r.Resolve("nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
			t.Errorf("err = %v", err)
		}
		if _, err := (&Remotes{Default: "ghost"}).Resolve(""); err == nil {
			t.Error("dangling default: want error")
		}
	})
	t.Run("synthetic env remote", func(t *testing.T) {
		isolate(t)
		if _, err := (&Remotes{}).Resolve(""); err == nil || !strings.Contains(err.Error(), "no remote configured") {
			t.Errorf("Resolve() with nothing configured: err = %v", err)
		}
		t.Setenv(EnvURL, "http://127.0.0.1:9")
		t.Setenv(EnvClientID, "eid")
		t.Setenv(EnvClientSecret, "esec")
		got, err := (&Remotes{}).Resolve("")
		if err != nil {
			t.Fatal(err)
		}
		if got != (Remote{Name: "env", URL: "http://127.0.0.1:9", ClientID: "eid", ClientSecret: "esec"}) {
			t.Errorf("with env = %+v", got)
		}
	})
	t.Run("env overrides each field, only when non-empty", func(t *testing.T) {
		isolate(t)
		t.Setenv(EnvClientID, "override-id")
		got, _ := r.Resolve("")
		if got != (Remote{Name: "prod", URL: "https://prod.example", ClientID: "override-id", ClientSecret: "psec"}) {
			t.Errorf("id override = %+v", got)
		}
		t.Setenv(EnvURL, "https://other.example")
		t.Setenv(EnvClientSecret, "override-sec")
		got, _ = r.Resolve("dev")
		if got != (Remote{Name: "dev", URL: "https://other.example", ClientID: "override-id", ClientSecret: "override-sec"}) {
			t.Errorf("all overrides = %+v", got)
		}
	})
	t.Run("resolve does not mutate", func(t *testing.T) {
		isolate(t)
		t.Setenv(EnvClientID, "x")
		_, _ = r.Resolve("prod")
		if r.List[0].ClientID != "pid" {
			t.Error("Resolve changed the stored remote")
		}
	})
}

func TestSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	labels := filepath.Join(dir, "labels.jsonl")
	if got, want := SidecarPath(labels), labels+".quet-web.yaml"; got != want {
		t.Errorf("SidecarPath = %q", got)
	}
	if _, ok, err := LoadLink(labels); ok || err != nil {
		t.Fatalf("LoadLink of a missing sidecar = ok %v, err %v", ok, err)
	}

	want := Link{Remote: "origin", Project: "expenses-2026"}
	if err := SaveLink(labels, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SidecarPath(labels))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	data, _ := os.ReadFile(SidecarPath(labels))
	if string(data) != "remote: origin\nproject: expenses-2026\n" {
		t.Errorf("file = %q", data)
	}
	got, ok, err := LoadLink(labels)
	if err != nil || !ok || got != want {
		t.Errorf("LoadLink = %+v, %v, %v", got, ok, err)
	}

	// Overwrite; a link without a remote keeps working (the default remote is used).
	if err := SaveLink(labels, Link{Project: "p2"}); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := LoadLink(labels); err != nil || !ok || got != (Link{Project: "p2"}) {
		t.Errorf("LoadLink after overwrite = %+v, %v, %v", got, ok, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("dir holds %d entries, want only the sidecar", len(entries))
	}
}

func TestSidecarErrors(t *testing.T) {
	dir := t.TempDir()
	labels := filepath.Join(dir, "labels.jsonl")
	if err := SaveLink(labels, Link{Remote: "o"}); err == nil {
		t.Error("SaveLink without project: want error")
	}
	for _, content := range []string{"project: [\n", "remote: origin\n"} {
		if err := os.WriteFile(SidecarPath(labels), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadLink(labels); err == nil {
			t.Errorf("LoadLink(%q): want error", content)
		}
	}
	if err := SaveLink(filepath.Join(dir, "missing-dir", "l.jsonl"), Link{Project: "p"}); err == nil {
		t.Error("SaveLink into a missing directory: want error")
	}
}
