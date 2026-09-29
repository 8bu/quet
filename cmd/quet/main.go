// Command quet — Quick Utility for Evaluating Text.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mattn/go-isatty"
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
	case "update":
		return runUpdate(cmd, stdout, stderr)
	case "review":
		return runReview(cmd, stdout, stderr)
	default:
		// --json output is for machines: skip the update notice so nothing but the
		// result is printed.
		var check *pendingCheck
		if !cmd.json {
			check = startCommandCheck(cmd)
		}
		code := runOneShot(cmd, stdout, stderr)
		check.notify(stderr)
		return code
	}
}

// runOneShot runs a command that prints its result and exits: stats, export, init or
// a scripting command (list, show, set, flag, suggest, edit, undo).
func runOneShot(cmd command, stdout, stderr io.Writer) int {
	switch cmd.kind {
	case "stats":
		return runStats(cmd, stdout, stderr)
	case "export":
		return runExport(cmd, stdout, stderr)
	case "list":
		return runList(cmd, stdout, stderr)
	case "show":
		return runShow(cmd, stdout, stderr)
	case "set":
		return runSet(cmd, stdout, stderr)
	case "flag":
		return runFlag(cmd, stdout, stderr)
	case "suggest":
		return runSuggest(cmd, stdout, stderr)
	case "edit":
		return runEdit(cmd, stdout, stderr)
	case "undo":
		return runUndo(cmd, stdout, stderr)
	default:
		return runInit(cmd, stdout, stderr)
	}
}

// isInteractive reports whether stdin and stdout are both terminals, which the review
// screen needs. Tests replace it.
var isInteractive = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// runReview opens cmd.corpus in the TUI, or the file browser when no corpus is
// given or it names a directory. Without an interactive terminal it points to the
// scripting commands instead of opening anything.
func runReview(cmd command, stdout, stderr io.Writer) int {
	if !isInteractive() {
		fmt.Fprintln(stderr, "quet: the review screen needs an interactive terminal; use quet list/show/set/flag/suggest/edit for scripted review (see quet help)")
		return 1
	}
	check := startCommandCheck(cmd)
	if dir, ok := browseDir(cmd.corpus); ok {
		return runBrowse(cmd, dir, check, stderr)
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
	if err := tui.Run(session, reviewOptions(cmd, check)); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// reviewOptions lets the TUI re-read the configuration and flags the same way
// the command line loaded them, so edits and reloads honour --config and --flags-file,
// and hands it the pending update check (nil when update.check is off).
func reviewOptions(cmd command, check *pendingCheck) tui.Options {
	return tui.Options{
		Settings:    func(p string) (config.Settings, error) { return loadSettings(cmd, p) },
		UpdateCheck: check.tuiCheck(),
	}
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
func runBrowse(cmd command, dir string, check *pendingCheck, stderr io.Writer) int {
	cwd, _ := os.Getwd()
	open := func(path string) (*review.Session, error) {
		// Relative paths keep gate errors short; the sidecar lands in the same place.
		if rel, err := filepath.Rel(cwd, path); err == nil && cwd != "" {
			path = rel
		}
		return openPicked(cmd, path)
	}
	session, err := tui.Browse(dir, open, reviewOptions(cmd, check))
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
	set, err := loadSettings(cmd, path)
	if err != nil {
		return nil, err
	}
	session, err := review.OpenCorpus(c, set.Config, set.Flags)
	if err != nil {
		return nil, err
	}
	session.FlagsPath = set.FlagsPath
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
	set, err := loadSettings(cmd, cmd.corpus)
	if err != nil {
		return nil, err
	}
	session, err := review.Open(cmd.corpus, set.Config, set.Flags)
	if err != nil {
		return nil, err
	}
	session.FlagsPath = set.FlagsPath
	return session, nil
}

// loadSettings loads the configuration and the manual flag taxonomy for corpusPath.
func loadSettings(cmd command, corpusPath string) (config.Settings, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return config.Settings{Config: cfg}, err
	}
	set := config.Settings{Config: cfg, FlagsPath: cmd.flagsFile}
	if !cmd.hasFlagsFile {
		set.FlagsPath = config.ResolveFlagsFile(cfg, corpusPath)
	}
	set.Flags, err = config.LoadFlags(set.FlagsPath)
	return set, err
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
