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
	kind   string   // "review" (default), "stats", "export", "init", "update", "annotate", "web", or a scripting command: "list", "show", "set", "flag", "suggest", "edit", "undo"
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
	schemaPath    string
	hasSchema     bool
	outPath       string
	hasOut        bool
	labelsPath    string
	hasLabels     bool
	proposalsPath string
	hasProposals  bool

	// web flags (`quet web ...`); the queue file of `web push` is webArgs[0]
	webAction    string   // "remote", "login", "logout", "push", "list" or "pull"
	webSub       string   // the `web remote` subcommand: "add", "list", "remove" or "default"
	webArgs      []string // positional arguments after the web subcommand
	project      string
	hasProject   bool
	projectName  string
	hasName      bool
	remote       string
	hasRemote    bool
	user         string
	hasUser      bool
	all          bool
	outDir       string
	hasOutDir    bool
	noBrowser    bool // `web login --no-browser`: print the URL instead of opening the browser
	serviceToken bool // `web login --service-token`: prompt for a Cloudflare Access service token
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
// "annotate" followed by a queue file labels it against --schema into --out, or re-checks
// it as a subset of the existing canonical labels file given with --labels.
// "init" (no corpus) writes starter config files, "update" (no corpus) updates the binary
// and "help" prints the help. "web" followed by a subcommand talks to a quet-web server
// (see parseWeb). With no corpus argument the review TUI opens on the file browser.
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
	case positional[0] == "web":
		cmd.kind = "web"
		return cmd, cmd.parseWeb(positional[1:])
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
		"--limit", "--add", "--remove", "--text", "--text-file", "--schema", "--out", "--labels", "--proposals",
		"--project", "--name", "--remote", "--user", "--out-dir":
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
	case "--labels":
		c.labelsPath, c.hasLabels = value, true
	case "--proposals":
		c.proposalsPath, c.hasProposals = value, true
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
	case "--project":
		c.project, c.hasProject = value, true
	case "--name":
		c.projectName, c.hasName = value, true
	case "--remote":
		c.remote, c.hasRemote = value, true
	case "--user":
		c.user, c.hasUser = value, true
	case "--out-dir":
		c.outDir, c.hasOutDir = value, true
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
	case "--all":
		c.all = true
	case "--no-browser":
		c.noBrowser = true
	case "--service-token":
		c.serviceToken = true
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
	if c.kind != "web" {
		if err := c.rejectWebFlags(); err != nil {
			return err
		}
	}
	if c.kind != "annotate" && c.kind != "web" {
		if c.hasSchema {
			return usagef("--schema is only valid with `quet annotate`")
		}
		if c.hasOut {
			return usagef("--out is only valid with `quet annotate`")
		}
		if c.hasLabels {
			return usagef("--labels is only valid with `quet annotate`")
		}
		if c.hasProposals {
			return usagef("--proposals is only valid with `quet annotate`")
		}
	}
	switch c.kind {
	case "init":
		return c.validateOnly("--global", "--force")
	case "update":
		return c.validateOnly("--check")
	case "web":
		return c.validateWeb()
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

// validateAnnotate checks the flags of `quet annotate`: only --schema plus exactly one of
// --out (label the queue) and --labels (re-check it against an existing labels file),
// and optionally --proposals (advisory suggestions, combinable with either).
func (c *command) validateAnnotate() error {
	if err := c.validateOnly("--schema", "--out", "--labels", "--proposals"); err != nil {
		return err
	}
	if c.schemaPath == "" {
		return usagef("`quet annotate` needs --schema <schema.yaml>")
	}
	if c.hasOut && c.hasLabels {
		return usagef("--out and --labels are mutually exclusive")
	}
	if c.outPath == "" && c.labelsPath == "" {
		return usagef("`quet annotate` needs --out <labels.jsonl> or --labels <labels.jsonl>")
	}
	if c.hasProposals && c.proposalsPath == "" {
		return usagef("`quet annotate` needs a file for --proposals <proposals.jsonl>")
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
	add(c.hasLabels, "--labels")
	add(c.hasProposals, "--proposals")
	add(c.hasProject, "--project")
	add(c.hasName, "--name")
	add(c.hasRemote, "--remote")
	add(c.hasUser, "--user")
	add(c.all, "--all")
	add(c.hasOutDir, "--out-dir")
	add(c.noBrowser, "--no-browser")
	add(c.serviceToken, "--service-token")
	return flags
}

// webOnlyFlags are the flags only `quet web` takes.
var webOnlyFlags = []string{"--project", "--name", "--remote", "--user", "--all", "--out-dir", "--no-browser", "--service-token"}

// rejectWebFlags rejects the web-only flags given to a command other than `quet web`.
func (c *command) rejectWebFlags() error {
	for _, flag := range c.givenFlags() {
		if slices.Contains(webOnlyFlags, flag) {
			return usagef("%s is only valid with `quet web`", flag)
		}
	}
	return nil
}

// parseWeb parses what follows `quet web`: remote add|list|remove|default, login, logout, push, list or
// pull, with the positional arguments each takes (the flags were parsed already), then validates
// the whole command.
func (c *command) parseWeb(args []string) error {
	if len(args) == 0 {
		return usagef("`quet web` needs a subcommand: remote, login, logout, push, list or pull")
	}
	c.webAction, args = args[0], args[1:]
	switch c.webAction {
	case "remote":
		if len(args) == 0 {
			return usagef("`quet web remote` needs a subcommand: add, list, remove or default")
		}
		c.webSub, args = args[0], args[1:]
		if !slices.Contains([]string{"add", "list", "remove", "default"}, c.webSub) {
			return usagef("unknown subcommand `quet web remote %s` (want add, list, remove or default)", c.webSub)
		}
	case "login", "logout", "push", "list", "pull":
	default:
		return usagef("unknown subcommand `quet web %s` (want remote, login, logout, push, list or pull)", c.webAction)
	}
	if len(args) > 0 {
		c.webArgs = args
	}
	return c.validate()
}

// webLabel names the web subcommand for error messages, such as "quet web remote add".
func (c *command) webLabel() string {
	if c.webSub != "" {
		return "quet web " + c.webAction + " " + c.webSub
	}
	return "quet web " + c.webAction
}

// validateWeb checks the arguments and flags of a `quet web` subcommand.
func (c *command) validateWeb() error {
	label := c.webLabel()
	var allowed []string
	switch c.webAction {
	case "remote":
		want := map[string]int{"add": 2, "list": 0, "remove": 1, "default": 1}[c.webSub]
		switch {
		case c.webSub == "add" && len(c.webArgs) < 2:
			return usagef("`%s` needs a name and a URL: quet web remote add <name> <url>", label)
		case c.webSub != "add" && c.webSub != "list" && len(c.webArgs) == 0:
			return usagef("`%s` needs a remote name", label)
		case len(c.webArgs) > want:
			return usagef("unexpected argument %q", c.webArgs[want])
		}
	case "login":
		if len(c.webArgs) > 1 {
			return usagef("unexpected argument %q", c.webArgs[1])
		}
		if c.noBrowser && c.serviceToken {
			return usagef("--no-browser and --service-token are mutually exclusive")
		}
		allowed = []string{"--no-browser", "--service-token"}
	case "logout":
		if len(c.webArgs) > 1 {
			return usagef("unexpected argument %q", c.webArgs[1])
		}
	case "push":
		switch {
		case len(c.webArgs) == 0:
			return usagef("missing queue file")
		case len(c.webArgs) > 1:
			return usagef("unexpected argument %q", c.webArgs[1])
		}
		allowed = []string{"--schema", "--proposals", "--project", "--name", "--remote"}
	case "list":
		if len(c.webArgs) > 0 {
			return usagef("unexpected argument %q", c.webArgs[0])
		}
		allowed = []string{"--remote", "--json"}
	case "pull":
		if len(c.webArgs) > 0 {
			return usagef("unexpected argument %q", c.webArgs[0])
		}
		allowed = []string{"--project", "--user", "--all", "--remote", "--out", "--labels", "--out-dir", "--force"}
	}
	for _, flag := range c.givenFlags() {
		if !slices.Contains(allowed, flag) {
			return usagef("%s is not valid with `%s`", flag, label)
		}
	}
	for _, v := range []struct {
		given bool
		value string
		flag  string
	}{
		{c.hasProject, c.project, "--project <slug>"},
		{c.hasName, c.projectName, "--name <name>"},
		{c.hasRemote, c.remote, "--remote <name>"},
		{c.hasUser, c.user, "--user <name>"},
		{c.hasSchema, c.schemaPath, "--schema <schema.yaml>"},
		{c.hasProposals, c.proposalsPath, "--proposals <proposals.jsonl>"},
		{c.hasOut, c.outPath, "--out <labels.jsonl>"},
		{c.hasLabels, c.labelsPath, "--labels <labels.jsonl>"},
		{c.hasOutDir, c.outDir, "--out-dir <dir>"},
	} {
		if v.given && v.value == "" {
			return usagef("`%s` needs a value for %s", label, v.flag)
		}
	}
	switch c.webAction {
	case "push":
		if !c.hasSchema {
			return usagef("`quet web push` needs --schema <schema.yaml>")
		}
		if !c.hasProject {
			return usagef("`quet web push` needs --project <slug>")
		}
	case "pull":
		return c.validateWebPull()
	}
	return nil
}

// validateWebPull checks the flag combinations of `quet web pull`: one of --user and --all, and the
// output that goes with it. --project may be left out only with --labels, whose sidecar can supply it.
func (c *command) validateWebPull() error {
	switch {
	case c.hasUser && c.all:
		return usagef("--user and --all are mutually exclusive")
	case !c.hasUser && !c.all:
		return usagef("`quet web pull` needs --user <name> or --all")
	}
	if c.all {
		if c.hasOut || c.hasLabels {
			return usagef("--all writes one file per collaborator: use --out-dir <dir>, not --out or --labels")
		}
		if !c.hasOutDir {
			return usagef("`quet web pull --all` needs --out-dir <dir>")
		}
	} else {
		switch {
		case c.hasOutDir:
			return usagef("--out-dir is only valid with --all")
		case c.hasOut && c.hasLabels:
			return usagef("--out and --labels are mutually exclusive")
		case !c.hasOut && !c.hasLabels:
			return usagef("`quet web pull --user` needs --out <labels.jsonl> or --labels <labels.jsonl>")
		}
	}
	if c.force && c.hasLabels {
		return usagef("--force is not valid with --labels (it merges into the existing file)")
	}
	if !c.hasProject && !c.hasLabels {
		return usagef("`quet web pull` needs --project <slug>")
	}
	return nil
}
