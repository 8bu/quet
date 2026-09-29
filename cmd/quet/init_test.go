package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/config"
)

// runCLI runs a command line and returns its exit code and output.
func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// readFile returns the contents of path, failing the test when it cannot be read.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitWritesLoadableStarterFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	code, stdout, stderr := runCLI("init")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, name := range []string{"quet.yaml", "flags.yaml"} {
		if !strings.Contains(stdout, "wrote "+name) {
			t.Errorf("stdout %q does not report writing %s", stdout, name)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "quet.yaml" {
		t.Errorf("Path = %q, want quet.yaml", cfg.Path)
	}
	def := config.Default()
	if cfg.Checks != def.Checks || cfg.Review != def.Review {
		t.Errorf("starter config = %+v, want the defaults %+v", cfg, def)
	}
	flags, err := config.LoadFlags("flags.yaml")
	if err != nil {
		t.Fatalf("LoadFlags: %v", err)
	}
	if len(flags) != 5 {
		t.Errorf("starter flags = %d, want 5", len(flags))
	}
}

func TestInitKeepsExistingFilesUnlessForced(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if code, _, stderr := runCLI("init"); code != 0 {
		t.Fatalf("first init: exit %d, stderr %q", code, stderr)
	}
	const custom = "review:\n  skip_reviewed: false\n"
	if err := os.WriteFile("quet.yaml", []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI("init")
	if code != 0 {
		t.Fatalf("second init: exit %d, stderr %q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("second init stdout = %q, want nothing written", stdout)
	}
	if !strings.Contains(stderr, "kept quet.yaml (already exists; --force to overwrite)") {
		t.Errorf("stderr %q does not report keeping quet.yaml", stderr)
	}
	if got := readFile(t, "quet.yaml"); got != custom {
		t.Errorf("quet.yaml = %q, want the user's edit kept", got)
	}

	if code, _, stderr := runCLI("init", "--force"); code != 0 {
		t.Fatalf("forced init: exit %d, stderr %q", code, stderr)
	}
	if got := readFile(t, "quet.yaml"); got != config.StarterConfig {
		t.Errorf("quet.yaml after --force = %q, want the starter config", got)
	}
}

func TestInitGlobalWritesUserConfigDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cwd := t.TempDir()
	t.Chdir(cwd)

	code, _, stderr := runCLI("init", "--global")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	dir := filepath.Join(xdg, "quet")
	if got := readFile(t, filepath.Join(dir, "config.yaml")); got != config.StarterConfig {
		t.Errorf("config.yaml = %q, want the starter config", got)
	}
	if got := readFile(t, filepath.Join(dir, "flags.yaml")); got != config.StarterFlags {
		t.Errorf("flags.yaml = %q, want the starter flags", got)
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Errorf("working directory entries = %v (err %v), want none", entries, err)
	}
}
