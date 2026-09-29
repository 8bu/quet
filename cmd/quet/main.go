// Command quet — Quick Utility for Evaluating Text.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/corpus"
	"github.com/8bu/quet/internal/review"
	"github.com/8bu/quet/internal/tui"
	"github.com/8bu/quet/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command line and returns the process exit code:
// 0 success, 1 runtime failure, 2 command line error.
func run(args []string, stdout, stderr io.Writer) int {
	cmd, err := parseArgs(args)
	if err != nil {
		var usage *usageError
		if errors.As(err, &usage) {
			fmt.Fprintf(stderr, "quet: %s\n\n%s", usage.msg, helpText())
			return 2
		}
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	switch {
	case cmd.help:
		fmt.Fprint(stdout, helpText())
		return 0
	case cmd.version:
		fmt.Fprintf(stdout, "quet %s\n", version.String())
		return 0
	}

	switch cmd.kind {
	case "stats":
		return runStats(cmd, stdout, stderr)
	case "export":
		return runExport(cmd, stdout, stderr)
	default:
		return runReview(cmd, stdout, stderr)
	}
}

// runReview opens cmd.corpus in the TUI, or the file browser when no corpus is
// given or it names a directory.
func runReview(cmd command, stdout, stderr io.Writer) int {
	if dir, ok := browseDir(cmd.corpus); ok {
		return runBrowse(cmd, dir, stderr)
	}
	session, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer session.Close()
	if err := applyReviewOptions(cmd, session); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}

	// tui.Run restores the terminal (leaves the alt screen, shows the cursor)
	// before returning, including when it returns an error.
	if err := tui.Run(session, tui.Options{}); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// browseDir reports the directory to browse: the working directory when no
// corpus is given, or corpus itself when it is a directory.
func browseDir(corpusArg string) (string, bool) {
	if corpusArg == "" {
		return ".", true
	}
	if info, err := os.Stat(corpusArg); err == nil && info.IsDir() {
		return corpusArg, true
	}
	return "", false
}

// runBrowse runs the file browser; the file it opens is reviewed in the same TUI.
func runBrowse(cmd command, dir string, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	open := func(path string) (*review.Session, error) {
		// Relative paths keep gate errors short; the sidecar lands in the same place.
		if rel, err := filepath.Rel(cwd, path); err == nil && cwd != "" {
			path = rel
		}
		return openPicked(cmd, path)
	}
	session, err := tui.Browse(dir, open, tui.Options{})
	if session != nil {
		defer session.Close()
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// openPicked is the browser's gate. A file is only opened (and its sidecar
// only created) once it parses and holds at least one record with text.
func openPicked(cmd command, path string) (*review.Session, error) {
	c, err := corpus.Load(path)
	if err != nil {
		return nil, err
	}
	if err := c.Usable(); err != nil {
		return nil, err
	}
	cfg, flags, err := loadSettings(cmd, path)
	if err != nil {
		return nil, err
	}
	session, err := review.OpenCorpus(c, cfg, flags)
	if err != nil {
		return nil, err
	}
	if err := applyReviewOptions(cmd, session); err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}

// applyReviewOptions applies --no-skip-reviewed and --filter to a review session.
// --no-skip-reviewed goes first so the view's initial position is computed with
// the intended skipping behaviour.
func applyReviewOptions(cmd command, session *review.Session) error {
	if cmd.noSkipReviewed {
		session.SkipReviewed = false
	}
	if cmd.hasFilter {
		filter, err := review.ParseFilter(cmd.filter)
		if err != nil {
			return err
		}
		session.SetFilter(filter)
	}
	return nil
}

// openSession loads the configuration, manual flags and the review session for cmd.corpus.
func openSession(cmd command) (*review.Session, error) {
	cfg, flags, err := loadSettings(cmd, cmd.corpus)
	if err != nil {
		return nil, err
	}
	return review.Open(cmd.corpus, cfg, flags)
}

// loadSettings loads the configuration and the manual flag taxonomy for corpusPath.
func loadSettings(cmd command, corpusPath string) (config.Config, []config.FlagDef, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return cfg, nil, err
	}
	flagsPath := cmd.flagsFile
	if !cmd.hasFlagsFile {
		flagsPath = config.ResolveFlagsFile(cfg, corpusPath)
	}
	flags, err := config.LoadFlags(flagsPath)
	return cfg, flags, err
}

// loadConfig reads the --config file when given (missing keys keep defaults), else
// ./quet.yaml or ~/.config/quet/config.yaml.
func loadConfig(cmd command) (config.Config, error) {
	if !cmd.hasConfig {
		return config.Load()
	}
	cfg := config.Default()
	data, err := os.ReadFile(cmd.configPath)
	if err != nil {
		return cfg, fmt.Errorf("config %s: %w", cmd.configPath, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", cmd.configPath, err)
	}
	cfg.Path = cmd.configPath
	return cfg, nil
}
