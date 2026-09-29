// Command quet — Quick Utility for Evaluating Text.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/8bu/quet/internal/config"
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

// runReview opens the session and hands it to the TUI.
func runReview(cmd command, stdout, stderr io.Writer) int {
	session, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer session.Close()

	// --no-skip-reviewed is applied before the filter so the view's initial
	// position is computed with the intended skipping behaviour.
	if cmd.noSkipReviewed {
		session.SkipReviewed = false
	}
	if cmd.hasFilter {
		filter, err := review.ParseFilter(cmd.filter)
		if err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
		session.SetFilter(filter)
	}

	// tui.Run restores the terminal (leaves the alt screen, shows the cursor)
	// before returning, including when it returns an error.
	if err := tui.Run(session, tui.Options{}); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// openSession loads the configuration, manual flags and the review session for cmd.corpus.
func openSession(cmd command) (*review.Session, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	flagsPath := cmd.flagsFile
	if !cmd.hasFlagsFile {
		flagsPath = config.ResolveFlagsFile(cfg, cmd.corpus)
	}
	flags, err := config.LoadFlags(flagsPath)
	if err != nil {
		return nil, err
	}
	return review.Open(cmd.corpus, cfg, flags)
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
