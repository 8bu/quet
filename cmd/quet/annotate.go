package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/tui"
)

// launchAnnotate runs the annotation screen over a session. Tests replace it.
var launchAnnotate = tui.RunAnnotate

// runAnnotate labels the queue cmd.corpus against cmd.schemaPath, writing labels to
// cmd.outPath, or with --labels re-checks the queue as a subset of the existing labels
// file cmd.labelsPath. With --proposals the advisory suggestions in cmd.proposalsPath
// are loaded into the session. The inputs are loaded and validated before the terminal
// is checked, so a bad schema, queue, labels or proposals file is reported even without
// an interactive terminal. After the screen closes, proposal ids that are not in the
// queue are reported on stderr.
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
	var ignored []string
	if cmd.hasProposals {
		if ignored, err = session.LoadProposals(cmd.proposalsPath); err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
	}
	if !isInteractive() {
		fmt.Fprintln(stderr, "quet: quet annotate needs an interactive terminal")
		return 1
	}
	// The TUI restores the terminal before returning, including on error.
	err = launchAnnotate(session)
	if len(ignored) > 0 {
		fmt.Fprintf(stderr, "quet: %s\n", ignoredProposalsMessage(ignored))
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// maxListedIgnored is how many ignored proposal ids the stderr report names.
const maxListedIgnored = 10

// ignoredProposalsMessage reports proposal ids that are not in the queue, naming at most
// maxListedIgnored of them.
func ignoredProposalsMessage(ids []string) string {
	shown := ids
	if len(shown) > maxListedIgnored {
		shown = shown[:maxListedIgnored]
	}
	msg := fmt.Sprintf("ignored %d proposal(s) for ids not in the queue: %s", len(ids), strings.Join(shown, ", "))
	if more := len(ids) - len(shown); more > 0 {
		msg += fmt.Sprintf(", … (+%d more)", more)
	}
	return msg
}
