package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Settings is a loaded configuration together with the manual flag taxonomy it resolved to.
type Settings struct {
	Config    Config
	FlagsPath string // flags file Flags came from; "" when none was found
	Flags     []FlagDef
}

// QuetDir returns the per-user Quet directory (~/.config/quet, $XDG_CONFIG_HOME respected), or "".
func QuetDir() string {
	dir := userConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "quet")
}

// StarterConfig is a commented quet.yaml whose values equal Default().
const StarterConfig = `# Quet configuration. Missing keys keep their built-in defaults.
# Reference: https://github.com/8bu/quet/blob/main/docs/configuration.md

review:
  # Auto-advance past records that already have a status.
  skip_reviewed: true

checks:
  # Records longer than this many characters get the too_long flag.
  max_chars: 160
  # A character repeated this many times in a row gets repeated_chars.
  repeated_char_threshold: 5
  # Share of symbols above which a record gets weird_symbols.
  weird_symbol_ratio: 0.35
  # A normalized shape seen this many times gets possible_template.
  template_min_occurrences: 8

# Manual flags file, relative to this file. Without it Quet looks for ./flags.yaml,
# then flags.yaml next to the corpus, then ~/.config/quet/flags.yaml.
# flags_file: ./flags.yaml
`

// StarterFlags is a commented flags.yaml with a generic manual flag taxonomy.
const StarterFlags = `# Quet manual flags: the choices of the flag picker (f), in this order.
# Each key is a flag name; description is shown next to it.

manual:
  typo:
    description: Natural typo, abbreviation, or shorthand.

  ambiguous:
    description: Meaning is genuinely ambiguous.

  unnatural:
    description: Unlikely wording for a real user.

  synthetic-looking:
    description: Looks strongly generated or templated.

  unusual:
    description: Valid but uncommon example worth revisiting.
`

// ErrExists is returned by WriteStarter when path exists and force is false.
var ErrExists = errors.New("already exists")

// WriteStarter writes content to path, creating parent directories (0o755). An existing
// file is only replaced when force is set; otherwise the error wraps ErrExists.
func WriteStarter(path, content string, force bool) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("config: create directory %s: %w", dir, err)
		}
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("config: %s %w", path, ErrExists)
		}
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}
