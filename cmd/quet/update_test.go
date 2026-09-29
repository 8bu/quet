package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/version"
)

// setVersion makes the binary report v for the rest of the test.
func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

// releasesServer serves a releases site whose latest release is v<latest> and
// points QUET_RELEASES_URL at it. Any request other than /latest fails the test.
func releasesServer(t *testing.T, latest string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, srv.URL+"/tag/v"+latest, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("QUET_RELEASES_URL", srv.URL)
}

func TestUpdateUpToDate(t *testing.T) {
	setVersion(t, "1.2.3")
	releasesServer(t, "1.2.3")

	code, stdout, stderr := runCLI("update")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if want := "quet 1.2.3 is up to date\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestUpdateCheckReportsNewerRelease(t *testing.T) {
	setVersion(t, "1.2.3-4-gabc1234-dirty")
	releasesServer(t, "1.3.0")

	code, stdout, stderr := runCLI("update", "--check")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want := "quet 1.3.0 is available (you have 1.2.3-4-gabc1234-dirty); run: quet update\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestUpdateFailsWhenLatestIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	t.Setenv("QUET_RELEASES_URL", srv.URL)

	code, stdout, stderr := runCLI("update", "--check")
	if code != 1 {
		t.Fatalf("exit %d, want 1 (stdout %q)", code, stdout)
	}
	if !strings.HasPrefix(stderr, "quet: update: ") {
		t.Errorf("stderr = %q, want a quet: update: error", stderr)
	}
}

func TestUpdateCheckOffMakesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("update check made a request to %s with update.check off", r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("QUET_RELEASES_URL", srv.URL)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	if p := startUpdateCheck(config.Default()); p != nil {
		t.Fatal("startUpdateCheck with update.check off started a check")
	}
	if code, _, stderr := runCLI("init"); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, stderr)
	}
}

func TestUpdateCheckOnReportsNewerRelease(t *testing.T) {
	setVersion(t, "1.2.3")
	releasesServer(t, "2.0.0")
	cfg := config.Default()
	cfg.Update.Check = true

	p := startUpdateCheck(cfg)
	if latest, ok := p.tuiCheck()(); !ok || latest != "2.0.0" {
		t.Fatalf("tuiCheck() = %q, %v, want 2.0.0, true", latest, ok)
	}
	var stderr bytes.Buffer
	p.notify(&stderr)
	want := "quet: 2.0.0 is available (you have 1.2.3); run: quet update\n"
	if stderr.String() != want {
		t.Errorf("notice = %q, want %q", stderr.String(), want)
	}
}
