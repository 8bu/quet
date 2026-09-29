package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/8bu/quet/internal/checks"
)

// defaultChecks are the documented built-in check defaults, independent of checks.DefaultOptions.
var defaultChecks = checks.Options{
	MaxChars:               160,
	RepeatedCharThreshold:  5,
	WeirdSymbolRatio:       0.35,
	TemplateMinOccurrences: 8,
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("chdir back to %s: %v", old, err)
		}
	})
}

// isolateConfig points HOME and XDG_CONFIG_HOME at fresh temp dirs so tests never touch real user config.
func isolateConfig(t *testing.T) (home, xdg string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	xdg = filepath.Join(root, "xdg")
	for _, dir := range []string{home, xdg} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	return home, xdg
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefault(t *testing.T) {
	cfg := Default()
	if !cfg.Review.SkipReviewed {
		t.Error("Review.SkipReviewed = false, want true")
	}
	if cfg.Checks != defaultChecks {
		t.Errorf("Checks = %+v, want %+v", cfg.Checks, defaultChecks)
	}
	if cfg.FlagsFile != "" {
		t.Errorf("FlagsFile = %q, want empty", cfg.FlagsFile)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty", cfg.Path)
	}
}

func TestLoadDefaultsWithoutFiles(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	chdir(t, dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	if cfg != want {
		t.Errorf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadLocalConfig(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	chdir(t, dir)
	write(t, filepath.Join(dir, "quet.yaml"), `
review:
  skip_reviewed: false
checks:
  max_chars: 200
unknown_key: 5
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "quet.yaml" {
		t.Errorf("Path = %q, want quet.yaml", cfg.Path)
	}
	if cfg.Review.SkipReviewed {
		t.Error("skip_reviewed = true, want false from config file")
	}
	if cfg.Checks.MaxChars != 200 {
		t.Errorf("max_chars = %d, want 200", cfg.Checks.MaxChars)
	}
	if cfg.Checks.RepeatedCharThreshold != defaultChecks.RepeatedCharThreshold ||
		cfg.Checks.WeirdSymbolRatio != defaultChecks.WeirdSymbolRatio ||
		cfg.Checks.TemplateMinOccurrences != defaultChecks.TemplateMinOccurrences {
		t.Errorf("unspecified checks keys lost defaults: %+v", cfg.Checks)
	}
	if cfg.FlagsFile != "" {
		t.Errorf("FlagsFile = %q, want empty", cfg.FlagsFile)
	}
}

func TestLoadUserConfigXDG(t *testing.T) {
	_, xdg := isolateConfig(t)
	chdir(t, t.TempDir())
	path := write(t, filepath.Join(xdg, "quet", "config.yaml"), "review:\n  skip_reviewed: false\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	if cfg.Review.SkipReviewed {
		t.Error("skip_reviewed = true, want false")
	}
}

func TestLoadUserConfigHome(t *testing.T) {
	home, _ := isolateConfig(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	chdir(t, t.TempDir())
	path := write(t, filepath.Join(home, ".config", "quet", "config.yaml"), "flags_file: \"custom.yaml\"\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q (spec: ~/.config/quet/config.yaml)", cfg.Path, path)
	}
	if cfg.FlagsFile != "custom.yaml" {
		t.Errorf("FlagsFile = %q, want custom.yaml", cfg.FlagsFile)
	}
	if !cfg.Review.SkipReviewed {
		t.Error("skip_reviewed = false, want default true")
	}
}

func TestLoadPrecedenceLocalOverUser(t *testing.T) {
	_, xdg := isolateConfig(t)
	dir := t.TempDir()
	chdir(t, dir)
	write(t, filepath.Join(xdg, "quet", "config.yaml"), "review:\n  skip_reviewed: false\nchecks:\n  max_chars: 999\n")
	write(t, filepath.Join(dir, "quet.yaml"), "checks:\n  max_chars: 111\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "quet.yaml" {
		t.Errorf("Path = %q, want quet.yaml", cfg.Path)
	}
	if cfg.Checks.MaxChars != 111 {
		t.Errorf("max_chars = %d, want 111 from local config", cfg.Checks.MaxChars)
	}
	if !cfg.Review.SkipReviewed {
		t.Error("local config did not reset user config values")
	}
}

func TestLoadMalformedConfig(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	chdir(t, dir)
	write(t, filepath.Join(dir, "quet.yaml"), "review: {skip_reviewed: true\n")

	cfg, err := Load()
	if err == nil {
		t.Fatalf("Load: want error, got %+v", cfg)
	}
	if !strings.Contains(err.Error(), "quet.yaml") {
		t.Errorf("error %q does not mention the config path", err)
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error %q does not mention parsing", err)
	}
}

func TestResolveFlagsFileFromConfig(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, "conf")
	cfgPath := write(t, filepath.Join(confDir, "quet.yaml"), "")
	flagsPath := write(t, filepath.Join(confDir, "flags.yaml"), "manual: {}\n")

	for _, name := range []string{"flags.yaml", "./flags.yaml"} {
		cfg := Config{FlagsFile: name, Path: cfgPath}
		if got := ResolveFlagsFile(cfg, ""); got != flagsPath {
			t.Errorf("ResolveFlagsFile(FlagsFile=%q) = %q, want %q", name, got, flagsPath)
		}
	}

	abs := write(t, filepath.Join(root, "abs.yaml"), "manual: {}\n")
	cfg := Config{FlagsFile: abs, Path: cfgPath}
	if got := ResolveFlagsFile(cfg, ""); got != abs {
		t.Errorf("ResolveFlagsFile(absolute) = %q, want %q", got, abs)
	}
}

func TestResolveFlagsFileFromCWDRelativeToConfigPath(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	confDir := filepath.Join(root, "conf")
	cfgPath := write(t, filepath.Join(confDir, "quet.yaml"), "")
	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	// flags.yaml only exists relative to the working directory, not to the config file.
	cwdFlags := write(t, filepath.Join(cwd, "flags.yaml"), "manual: {}\n")
	chdir(t, cwd)

	cfg := Config{FlagsFile: "missing.yaml", Path: cfgPath}
	if got := ResolveFlagsFile(cfg, ""); got != filepath.Base(cwdFlags) {
		t.Errorf("ResolveFlagsFile = %q, want %q", got, filepath.Base(cwdFlags))
	}
}

func TestResolveFlagsFileDefaultLocations(t *testing.T) {
	_, xdg := isolateConfig(t)
	root := t.TempDir()
	corpusDir := filepath.Join(root, "corpus")
	emptyDir := filepath.Join(root, "empty")
	for _, dir := range []string{corpusDir, emptyDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	corpusPath := filepath.Join(corpusDir, "corpus.jsonl")
	userFlags := write(t, filepath.Join(xdg, "quet", "flags.yaml"), "manual: {}\n")

	// Only the user config dir has a flags file.
	cwd := t.TempDir()
	chdir(t, cwd)
	if got := ResolveFlagsFile(Config{}, filepath.Join(emptyDir, "corpus.jsonl")); got != userFlags {
		t.Errorf("ResolveFlagsFile = %q, want %q", got, userFlags)
	}

	// Corpus dir beats the user config dir.
	corpusFlags := write(t, filepath.Join(corpusDir, "flags.yaml"), "manual: {}\n")
	if got := ResolveFlagsFile(Config{}, corpusPath); got != corpusFlags {
		t.Errorf("ResolveFlagsFile = %q, want %q", got, corpusFlags)
	}

	// CWD beats the corpus dir.
	cwdFlags := write(t, filepath.Join(cwd, "flags.yaml"), "manual: {}\n")
	if got := ResolveFlagsFile(Config{}, corpusPath); got != filepath.Base(cwdFlags) {
		t.Errorf("ResolveFlagsFile = %q, want %q", got, filepath.Base(cwdFlags))
	}
}

func TestResolveFlagsFileNoneFound(t *testing.T) {
	isolateConfig(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	home := os.Getenv("HOME")
	chdir(t, t.TempDir())
	corpusPath := filepath.Join(t.TempDir(), "corpus.jsonl")
	cfg := Config{Path: filepath.Join(home, "conf", "quet.yaml")}
	if got := ResolveFlagsFile(cfg, corpusPath); got != "" {
		t.Errorf("ResolveFlagsFile = %q, want empty", got)
	}
	if got := ResolveFlagsFile(Config{}, ""); got != "" {
		t.Errorf("ResolveFlagsFile = %q, want empty", got)
	}
}

func TestLoadFlagsOrderAndFallback(t *testing.T) {
	path := write(t, filepath.Join(t.TempDir(), "flags.yaml"), `manual:
  slang:
    description: Notable slang.
  typo: plain description
  bare:
  unusual:
    description: 3
`)
	defs, err := LoadFlags(path)
	if err != nil {
		t.Fatalf("LoadFlags: %v", err)
	}
	want := []FlagDef{
		{Name: "slang", Description: "Notable slang."},
		{Name: "typo", Description: "plain description"},
		{Name: "bare", Description: ""},
		{Name: "unusual", Description: "3"},
	}
	if !reflect.DeepEqual(defs, want) {
		t.Errorf("defs = %+v\nwant %+v", defs, want)
	}
}

func TestLoadFlagsEmptyInputs(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
	}{
		{"empty path", ""},
		{"no manual key", write(t, filepath.Join(dir, "a.yaml"), "other: 1\n")},
		{"null manual", write(t, filepath.Join(dir, "b.yaml"), "manual:\n")},
		{"empty manual", write(t, filepath.Join(dir, "c.yaml"), "manual: {}\n")},
		{"empty document", write(t, filepath.Join(dir, "d.yaml"), "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defs, err := LoadFlags(tt.path)
			if err != nil {
				t.Fatalf("LoadFlags: %v", err)
			}
			if len(defs) != 0 {
				t.Errorf("defs = %+v, want empty", defs)
			}
		})
	}
	if defs, err := LoadFlags(""); defs != nil || err != nil {
		t.Errorf(`LoadFlags("") = %v, %v; want nil, nil`, defs, err)
	}
}

func TestLoadFlagsErrors(t *testing.T) {
	dir := t.TempDir()
	bad := write(t, filepath.Join(dir, "bad.yaml"), "manual: [\n")
	if _, err := LoadFlags(bad); err == nil || !strings.Contains(err.Error(), "bad.yaml") {
		t.Fatalf("LoadFlags(malformed) = %v, want error naming the path", err)
	}
	missing := filepath.Join(dir, "missing.yaml")
	if _, err := LoadFlags(missing); err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("LoadFlags(missing) = %v, want error naming the path", err)
	}
}

func TestLoadFlagsRepoFixture(t *testing.T) {
	defs, err := LoadFlags("../../flags.yaml")
	if err != nil {
		t.Fatalf("LoadFlags: %v", err)
	}
	want := []string{"slang", "typo", "ambiguous", "unnatural", "synthetic-looking", "unusual"}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if d.Description == "" {
			t.Errorf("flag %q has no description", d.Name)
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("flag names = %v, want %v", names, want)
	}
}

func TestShippedExampleConfigAndFlags(t *testing.T) {
	isolateConfig(t)
	// Read the shipped examples before changing directory, then mirror their relative layout.
	conf, err := os.ReadFile("../../configs/quet.yaml")
	if err != nil {
		t.Fatal(err)
	}
	flags, err := os.ReadFile("../../flags.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	chdir(t, dir)
	write(t, filepath.Join(dir, "quet.yaml"), string(conf))
	write(t, filepath.Join(dir, "flags.yaml"), string(flags))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "quet.yaml" || cfg.FlagsFile != "./flags.yaml" {
		t.Errorf("config = %+v, want the shipped example config", cfg)
	}
	if !cfg.Review.SkipReviewed || cfg.Checks != defaultChecks {
		t.Errorf("shipped config changed defaults: %+v", cfg)
	}
	flagsPath := ResolveFlagsFile(cfg, filepath.Join(dir, "corpus.jsonl"))
	if filepath.Clean(flagsPath) != "flags.yaml" {
		t.Fatalf("ResolveFlagsFile = %q, want the copied flags.yaml", flagsPath)
	}
	defs, err := LoadFlags(flagsPath)
	if err != nil {
		t.Fatalf("LoadFlags: %v", err)
	}
	if len(defs) != 6 {
		t.Errorf("len(defs) = %d, want 6", len(defs))
	}
}

func TestResolveFlagsFileRepoFixture(t *testing.T) {
	// cfg.Path == "" resolves FlagsFile relative to the working directory (the package dir under go test).
	cfg := Config{FlagsFile: "../../flags.yaml"}
	got := ResolveFlagsFile(cfg, "")
	if filepath.Base(got) != "flags.yaml" {
		t.Fatalf("ResolveFlagsFile = %q, want the repo flags.yaml", got)
	}
	if _, err := LoadFlags(got); err != nil {
		t.Fatalf("LoadFlags(%q): %v", got, err)
	}
}
