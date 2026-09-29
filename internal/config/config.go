// Package config loads Quet configuration (quet.yaml) and manual flag definitions (flags.yaml).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/8bu/quet/internal/checks"
)

type Review struct {
	SkipReviewed bool `yaml:"skip_reviewed"`
}

// Update controls the release check. It is opt-in: Quet only goes online when Check is set.
type Update struct {
	Check bool `yaml:"check"` // look for a newer release on every run
}

type Config struct {
	Review    Review         `yaml:"review"`
	Checks    checks.Options `yaml:"checks"`
	Update    Update         `yaml:"update"`
	FlagsFile string         `yaml:"flags_file"`
	Path      string         `yaml:"-"` // config file actually loaded ("" = defaults)
}

// Default returns built-in defaults: skip_reviewed=true, checks.DefaultOptions(), flags_file "".
func Default() Config {
	return Config{
		Review: Review{SkipReviewed: true},
		Checks: checks.DefaultOptions(),
	}
}

// Load reads the first existing of ./quet.yaml, ~/.config/quet/config.yaml over Default(); missing keys keep defaults.
func Load() (Config, error) {
	cfg := Default()
	for _, path := range configPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return cfg, fmt.Errorf("config: read %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("config: parse %s: %w", path, err)
		}
		cfg.Path = path
		return cfg, nil
	}
	cfg.Path = ""
	return cfg, nil
}

// configPaths lists candidate config files in precedence order.
func configPaths() []string {
	paths := []string{"quet.yaml"}
	if dir := userConfigDir(); dir != "" {
		paths = append(paths, filepath.Join(dir, "quet", "config.yaml"))
	}
	return paths
}

// userConfigDir returns the per-user config directory: $XDG_CONFIG_HOME, else ~/.config.
func userConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

// FlagDef is one manual flag from flags.yaml ("manual:" mapping), in file order.
type FlagDef struct {
	Name        string
	Description string
}

// ResolveFlagsFile picks the flags file: cfg.FlagsFile if set (relative to config file dir), else first existing of
// ./flags.yaml, <corpus dir>/flags.yaml, ~/.config/quet/flags.yaml. Returns "" if none.
func ResolveFlagsFile(cfg Config, corpusPath string) string {
	if cfg.FlagsFile != "" {
		path := cfg.FlagsFile
		if !filepath.IsAbs(path) {
			base := "."
			if cfg.Path != "" {
				base = filepath.Dir(cfg.Path)
			}
			path = filepath.Join(base, path)
		}
		if fileExists(path) {
			return path
		}
	}
	candidates := []string{"flags.yaml"}
	if corpusPath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(corpusPath), "flags.yaml"))
	}
	if dir := userConfigDir(); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "quet", "flags.yaml"))
	}
	for _, path := range candidates {
		if fileExists(path) {
			return path
		}
	}
	return ""
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LoadFlags parses a flags.yaml; path "" returns nil, nil.
func LoadFlags(path string) ([]FlagDef, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("flags: read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("flags: parse %s: %w", path, err)
	}
	defs := []FlagDef{}
	manual := rootMapping(&doc)
	if manual == nil {
		return defs, nil
	}
	for i := 0; i+1 < len(manual.Content); i += 2 {
		if manual.Content[i].Value != "manual" {
			continue
		}
		defs = append(defs, manualDefs(manual.Content[i+1])...)
		break
	}
	return defs, nil
}

// rootMapping returns the top-level mapping node of a parsed YAML document, or nil.
func rootMapping(doc *yaml.Node) *yaml.Node {
	node := doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

// manualDefs reads the flag definitions of a "manual:" node, preserving file order.
func manualDefs(node *yaml.Node) []FlagDef {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	defs := make([]FlagDef, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		defs = append(defs, FlagDef{Name: key.Value, Description: flagDescription(node.Content[i+1])})
	}
	return defs
}

// flagDescription reads a flag's "description:" value, falling back to the node's own string value.
func flagDescription(node *yaml.Node) string {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "description" {
				return node.Content[i+1].Value
			}
		}
		return ""
	}
	if node.Kind == yaml.ScalarNode && node.Tag != "!!null" {
		return node.Value
	}
	return ""
}
