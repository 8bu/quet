package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/8bu/quet/internal/review"
)

// command is a parsed command line: what to do, with which corpus, and the flags for it.
type command struct {
	kind   string   // "review" (default), "stats", "export", "init", "update", "annotate", or a scripting command: "list", "show", "set", "flag", "suggest", "edit", "undo"
	corpus string   // the corpus, or the queue file for "annotate"
	ids    []string // record IDs after the corpus (scripting commands)

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

	// export flags (--status is also the review status for `quet set`)
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

	// scripting flags
	json        bool
	limit       int
	hasLimit    bool
	add         []string
	hasAdd      bool
	remove      []string
	hasRemove   bool
	text        string
	hasText     bool
	textFile    string
	hasTextFile bool
	revert      bool

	// annotate flags
	schemaPath string
	hasSchema  bool
	outPath    string
	hasOut     bool
}

// usageError is a command line mistake: reported on stderr with the usage text, exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// parseArgs parses a command line. The corpus argument and flags may appear in any order;
// --flag value and --flag=value are both accepted. The first positional "stats", "export"
// or a scripting command (list, show, set, flag, suggest, edit, undo) followed by a corpus
// argument selects that subcommand; the scripting commands take record IDs after the corpus.
// "annotate" followed by a queue file labels it against --schema into --out.
// "init" (no corpus) writes starter config files, "update" (no corpus) updates the binary
// and "help" prints the help. With no corpus argument the review TUI opens on the file browser.
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
	case positional[0] == "annotate":
		if len(positional) < 2 {
			return cmd, usagef("missing queue file")
		}
		cmd.kind = positional[0]
		cmd.corpus = positional[1]
		rest = positional[2:]
	case slices.Contains(scriptingKinds, positional[0]):
		if len(positional) < 2 {
			return cmd, usagef("missing corpus file")
		}
		cmd.kind = positional[0]
		cmd.corpus = positional[1]
		if len(positional) > 2 {
			cmd.ids = positional[2:]
		}
		if err := cmd.checkIDs(); err != nil {
			return cmd, err
		}
	default:
		cmd.corpus = positional[0]
		rest = positional[1:]
	}
	if len(rest) > 0 {
		return cmd, usagef("unexpected argument %q", rest[0])
	}
	return cmd, cmd.validate()
}

// scriptingKinds are the commands that read or change review state without the TUI.
var scriptingKinds = []string{"list", "show", "set", "flag", "suggest", "edit", "undo"}

// scriptingFlags are the flags only the scripting commands (and `quet stats --json`) take.
var scriptingFlags = []string{"--limit", "--json", "--add", "--remove", "--text", "--text-file", "--revert"}

// checkIDs enforces how many record IDs each scripting command takes.
func (c *command) checkIDs() error {
	switch c.kind {
	case "list", "undo":
		if len(c.ids) > 0 {
			return usagef("unexpected argument %q", c.ids[0])
		}
	case "show", "edit":
		if len(c.ids) == 0 {
			return usagef("`quet %s` needs a record id", c.kind)
		}
		if len(c.ids) > 1 {
			return usagef("`quet %s` takes exactly one record id, got %d", c.kind, len(c.ids))
		}
	default:
		if len(c.ids) == 0 {
			return usagef("`quet %s` needs at least one record id", c.kind)
		}
	}
	return nil
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
	case "--filter", "--flags-file", "--config", "--status", "-o", "--output", "--format",
		"--limit", "--add", "--remove", "--text", "--text-file", "--schema", "--out":
		return true
	}
	return false
}

// setValueFlag stores the value of a value flag.
func (c *command) setValueFlag(name, value string) error {
	switch name {
	case "--filter":
		c.filter, c.hasFilter = value, true
	case "--flags-file":
		c.flagsFile, c.hasFlagsFile = value, true
	case "--config":
		c.configPath, c.hasConfig = value, true
	case "--schema":
		c.schemaPath, c.hasSchema = value, true
	case "--out":
		c.outPath, c.hasOut = value, true
	case "--status":
		c.status, c.hasStatus = value, true
	case "-o", "--output":
		c.output, c.hasOutput = value, true
	case "--format":
		c.format, c.hasFormat = value, true
	case "--limit":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return usagef("--limit needs a non-negative whole number, got %q", value)
		}
		c.limit, c.hasLimit = n, true
	case "--add":
		c.add, c.hasAdd = append(c.add, splitList(value)...), true
	case "--remove":
		c.remove, c.hasRemove = append(c.remove, splitList(value)...), true
	case "--text":
		c.text, c.hasText = value, true
	case "--text-file":
		c.textFile, c.hasTextFile = value, true
	default:
		return usagef("unknown flag %s", name)
	}
	return nil
}

// splitList splits a comma-separated flag value into trimmed, non-empty names.
func splitList(value string) []string {
	var names []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// setBoolFlag records a flag that takes no value.
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
	case "--json":
		c.json = true
	case "--revert":
		c.revert = true
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
	if c.kind != "annotate" {
		if c.hasSchema {
			return usagef("--schema is only valid with `quet annotate`")
		}
		if c.hasOut {
			return usagef("--out is only valid with `quet annotate`")
		}
	}
	switch c.kind {
	case "init":
		return c.validateOnly("--global", "--force")
	case "update":
		return c.validateOnly("--check")
	case "annotate":
		return c.validateAnnotate()
	case "list", "show", "set", "flag", "suggest", "edit", "undo":
		return c.validateScripting()
	}
	if c.global {
		return usagef("--global is only valid with `quet init`")
	}
	switch c.kind {
	case "stats":
		if c.hasFilter {
			return usagef("--filter is only valid when reviewing or with `quet list`")
		}
		if c.noSkipReviewed {
			return usagef("--no-skip-reviewed is only valid when reviewing")
		}
		if err := c.rejectScripting("with `quet stats`", "--json"); err != nil {
			return err
		}
	case "export":
		if c.hasFilter {
			return usagef("--filter is only valid when reviewing or with `quet list`")
		}
		if c.noSkipReviewed {
			return usagef("--no-skip-reviewed is only valid when reviewing")
		}
		if err := c.rejectScripting("with `quet export`"); err != nil {
			return err
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
		if err := c.rejectScripting("when reviewing"); err != nil {
			return err
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
			return usagef("--status is only valid with `quet export` or `quet set`")
		}
	}
	return nil
}

// validateAnnotate checks the flags of `quet annotate`: only --schema and --out, both required.
func (c *command) validateAnnotate() error {
	if err := c.validateOnly("--schema", "--out"); err != nil {
		return err
	}
	if c.schemaPath == "" {
		return usagef("`quet annotate` needs --schema <schema.yaml>")
	}
	if c.outPath == "" {
		return usagef("`quet annotate` needs --out <labels.jsonl>")
	}
	return nil
}

// validateScripting checks the flags of a scripting command: each takes --json, --config
// and --flags-file plus its own flags, and the writing commands need something to do.
func (c *command) validateScripting() error {
	shared := []string{"--json", "--config", "--flags-file"}
	switch c.kind {
	case "list":
		if err := c.validateOnly(append(shared, "--filter", "--limit")...); err != nil {
			return err
		}
		if c.hasFilter {
			if _, err := review.ParseFilter(c.filter); err != nil {
				return usagef("%s", err)
			}
		}
	case "show", "undo":
		return c.validateOnly(shared...)
	case "set":
		if err := c.validateOnly(append(shared, "--status")...); err != nil {
			return err
		}
		if !c.hasStatus {
			return usagef("`quet set` needs --status (unreviewed, approved, rejected or needs_review)")
		}
		if _, err := review.ParseStatus(c.status); err != nil {
			return usagef("%s", err)
		}
	case "flag", "suggest":
		if err := c.validateOnly(append(shared, "--add", "--remove")...); err != nil {
			return err
		}
		if len(c.add) == 0 && len(c.remove) == 0 {
			return usagef("`quet %s` needs --add or --remove with at least one name", c.kind)
		}
	case "edit":
		if err := c.validateOnly(append(shared, "--text", "--text-file", "--revert")...); err != nil {
			return err
		}
		given := 0
		for _, set := range []bool{c.hasText, c.hasTextFile, c.revert} {
			if set {
				given++
			}
		}
		if given != 1 {
			return usagef("`quet edit` needs exactly one of --text, --text-file or --revert")
		}
	}
	return nil
}

// rejectScripting rejects the scripting-only flags given to a command that does not take
// them, except those in allowed; where completes the message ("with `quet stats`").
func (c *command) rejectScripting(where string, allowed ...string) error {
	for _, flag := range c.givenFlags() {
		if slices.Contains(scriptingFlags, flag) && !slices.Contains(allowed, flag) {
			return usagef("%s is not valid %s", flag, where)
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
	add(c.hasLimit, "--limit")
	add(c.json, "--json")
	add(c.hasAdd, "--add")
	add(c.hasRemove, "--remove")
	add(c.hasText, "--text")
	add(c.hasTextFile, "--text-file")
	add(c.revert, "--revert")
	add(c.hasSchema, "--schema")
	add(c.hasOut, "--out")
	return flags
}
