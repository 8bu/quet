package main

import (
	"fmt"
	"io"

	"github.com/8bu/quet/internal/export"
)

// runExport writes the records selected by --status to --output (atomic write, never over the corpus).
func runExport(cmd command, stdout, stderr io.Writer) int {
	preset, err := statusPreset(cmd.status)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 2
	}

	session, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer session.Close()

	opt := preset.Options
	opt.Format = cmd.format
	if cmd.withReview {
		opt.WithReview = true
	}
	out := cmd.output
	if !cmd.hasOutput {
		out = export.DefaultPath(cmd.corpus, preset.Suffix)
	}

	written, err := export.ToFile(session, out, opt, cmd.force)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %d records to %s\n", written, out)
	return 0
}

// statusPreset maps a --status value to the export preset it selects.
func statusPreset(status string) (export.Preset, error) {
	name := status
	if name == "needs-review" {
		name = "needs_review"
	}
	for _, preset := range export.Presets() {
		if preset.Name == name {
			return preset, nil
		}
	}
	return export.Preset{}, fmt.Errorf("unknown status %q (want approved, rejected, needs_review, all)", status)
}
