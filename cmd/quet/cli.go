package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/8bu/quet/internal/review"
)

// command is a parsed command line: what to do, with which corpus, and the flags for it.
type command struct {
	kind   string // "review" (default), "stats", "export", "init" or "update"
	corpus string

	help    bool
	version bool

	// review flags
	filter         string
	hasFilter      bool
	noSkipReviewed bool

	// shared flags
	flagsFile    string
	hasFlagsFile bool
	configPath   string
	hasConfig    bool

	// export flags
	status     string
	hasStatus  bool
	output     string
	hasOutput  bool
	withReview bool
	format     string
	hasFormat  bool

	// export and init flags
	force bool

	// init flags
	global bool

	// update flags
	check bool
}

// usageError is a command line mistake: reported on stderr with the usage text, exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// parseArgs parses a command line. The corpus argument and flags may appear in any order;
// --flag value and --flag=value are both accepted. The first positional "stats" or "export"
// followed by a corpus argument selects that subcommand; "init" (no corpus) writes starter
// config files, "update" (no corpus) updates the binary and "help" prints the help. With no
// corpus argument the review TUI opens on the file browser.
func parseArgs(args []string) (command, error) {
	cmd := command{kind: "review", status: "approved"}

	var positional []string
	onlyPositional := false
	pending := "" // value flag waiting for its value

	for _, arg := range args {
		if pending != "" {
			if err := cmd.setValueFlag(pending, arg); err != nil {
				return cmd, err
			}
			pending = ""
			continue
		}
		if onlyPositional || arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		if arg == "--" {
			onlyPositional = true
			continue
		}
		name, value, inline := splitFlag(arg)
		if valueFlag(name) {
			if inline {
				if err := cmd.setValueFlag(name, value); err != nil {
					return cmd, err
				}
				continue
			}
			pending = name
			continue
		}
		if inline {
			return cmd, usagef("flag %s does not take a value", name)
		}
		if err := cmd.setBoolFlag(name); err != nil {
			return cmd, err
		}
	}
	if pending != "" {
		return cmd, usagef("flag %s needs a value", pending)
	}

	if cmd.help || cmd.version {
		return cmd, nil
	}

	var rest []string
	switch {
	case len(positional) == 0:
		// No corpus: the TUI starts on the file browser.
		return cmd, cmd.validate()
	case positional[0] == "help":
		if len(positional) > 1 {
			return cmd, usagef("unexpected argument %q", positional[1])
		}
		cmd.help = true
		return cmd, nil
	case positional[0] == "init" || positional[0] == "update":
		cmd.kind = positional[0]
		rest = positional[1:]
	case positional[0] == "stats" || positional[0] == "export":
		if len(positional) < 2 {
			return cmd, usagef("missing corpus file")
		}
		cmd.kind = positional[0]
		cmd.corpus = positional[1]
		rest = positional[2:]
	default:
		cmd.corpus = positional[0]
		rest = positional[1:]
	}
	if len(rest) > 0 {
		return cmd, usagef("unexpected argument %q", rest[0])
	}
	return cmd, cmd.validate()
}

// splitFlag splits "--name=value" into its parts.
func splitFlag(arg string) (name, value string, hasValue bool) {
	if i := strings.IndexByte(arg, '='); i >= 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

// valueFlag reports whether name takes a value.
func valueFlag(name string) bool {
	switch name {
	case "--filter", "--flags-file", "--config", "--status", "-o", "--output", "--format":
		return true
	}
	return false
}

func (c *command) setValueFlag(name, value string) error {
	switch name {
	case "--filter":
		c.filter, c.hasFilter = value, true
	case "--flags-file":
		c.flagsFile, c.hasFlagsFile = value, true
	case "--config":
		c.configPath, c.hasConfig = value, true
	case "--status":
		c.status, c.hasStatus = value, true
	case "-o", "--output":
		c.output, c.hasOutput = value, true
	case "--format":
		c.format, c.hasFormat = value, true
	default:
		return usagef("unknown flag %s", name)
	}
	return nil
}

func (c *command) setBoolFlag(name string) error {
	switch name {
	case "-h", "--help":
		c.help = true
	case "--version":
		c.version = true
	case "--no-skip-reviewed":
		c.noSkipReviewed = true
	case "--with-review":
		c.withReview = true
	case "-f", "--force":
		c.force = true
	case "--global":
		c.global = true
	case "--check":
		c.check = true
	default:
		return usagef("unknown flag %s", name)
	}
	return nil
}

// validate rejects flags that do not belong to the selected subcommand.
func (c *command) validate() error {
	if c.check && c.kind != "update" {
		return usagef("--check is only valid with `quet update`")
	}
	switch c.kind {
	case "init":
		return c.validateOnly("--global", "--force")
	case "update":
		return c.validateOnly("--check")
	}
	if c.global {
		return usagef("--global is only valid with `quet init`")
	}
	switch c.kind {
	case "stats":
		if c.hasFilter {
			return usagef("--filter is only valid when reviewing")
		}
		if c.noSkipReviewed {
			return usagef("--no-skip-reviewed is only valid when reviewing")
		}
	case "export":
		if c.hasFilter {
			return usagef("--filter is only valid when reviewing")
		}
		if c.noSkipReviewed {
			return usagef("--no-skip-reviewed is only valid when reviewing")
		}
		if _, err := statusPreset(c.status); err != nil {
			return usagef("%s", err)
		}
		if c.hasFormat && c.format != "jsonl" && c.format != "txt" {
			return usagef("unknown format %q (want jsonl or txt)", c.format)
		}
	case "review":
		if c.hasFilter {
			if _, err := review.ParseFilter(c.filter); err != nil {
				return usagef("%s", err)
			}
		}
		switch {
		case c.output != "" || c.hasOutput:
			return usagef("--output is only valid with `quet export`")
		case c.withReview:
			return usagef("--with-review is only valid with `quet export`")
		case c.hasFormat:
			return usagef("--format is only valid with `quet export`")
		case c.force:
			return usagef("--force is only valid with `quet export` or `quet init`")
		case c.hasStatus:
			return usagef("--status is only valid with `quet export`")
		}
	}
	return nil
}

// validateOnly rejects every given flag except allowed for `quet <kind>`.
func (c *command) validateOnly(allowed ...string) error {
	for _, flag := range c.givenFlags() {
		if !slices.Contains(allowed, flag) {
			return usagef("%s is not valid with `quet %s`", flag, c.kind)
		}
	}
	return nil
}

// givenFlags lists the subcommand flags present on the command line, in a fixed order.
func (c *command) givenFlags() []string {
	var flags []string
	add := func(given bool, name string) {
		if given {
			flags = append(flags, name)
		}
	}
	add(c.hasFilter, "--filter")
	add(c.noSkipReviewed, "--no-skip-reviewed")
	add(c.hasConfig, "--config")
	add(c.hasFlagsFile, "--flags-file")
	add(c.hasStatus, "--status")
	add(c.hasOutput, "--output")
	add(c.withReview, "--with-review")
	add(c.hasFormat, "--format")
	add(c.force, "--force")
	add(c.global, "--global")
	add(c.check, "--check")
	return flags
}
