package main

import (
	"fmt"
	"io"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/tui"
)

// launchAnnotate runs the annotation screen over a session. Tests replace it.
var launchAnnotate = tui.RunAnnotate

// runAnnotate labels the queue cmd.corpus against cmd.schemaPath, writing labels to
// cmd.outPath, or with --labels re-checks the queue as a subset of the existing labels
// file cmd.labelsPath. The inputs are loaded and validated before the terminal is
// checked, so a bad schema, queue or labels file is reported even without an
// interactive terminal.
func runAnnotate(cmd command, _, stderr io.Writer) int {
	var session *annotate.Session
	var err error
	if cmd.hasLabels {
		session, err = annotate.OpenRecheck(cmd.corpus, cmd.schemaPath, cmd.labelsPath)
	} else {
		session, err = annotate.Open(cmd.corpus, cmd.schemaPath, cmd.outPath)
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	if !isInteractive() {
		fmt.Fprintln(stderr, "quet: quet annotate needs an interactive terminal")
		return 1
	}
	// The TUI restores the terminal before returning, including on error.
	if err := launchAnnotate(session); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}
