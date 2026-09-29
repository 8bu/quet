package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/8bu/quet/internal/checks"
	"github.com/8bu/quet/internal/review"
	"github.com/8bu/quet/internal/storage"
)

// runStats prints corpus-wide review counts for `quet stats <corpus>`.
func runStats(cmd command, stdout, stderr io.Writer) int {
	session, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer session.Close()

	counts := session.Counts()
	fmt.Fprintf(stdout, "%-13s%7d\n", "Total:", counts.Total)
	fmt.Fprintf(stdout, "%-13s%7d\n", "Approved:", counts.Approved)
	fmt.Fprintf(stdout, "%-13s%7d\n", "Rejected:", counts.Rejected)
	fmt.Fprintf(stdout, "%-13s%7d\n", "Needs review:", counts.NeedsReview)
	fmt.Fprintf(stdout, "%-13s%7d\n", "Unreviewed:", counts.Unreviewed)
	fmt.Fprintf(stdout, "%-13s%7d\n", "Edited:", counts.Edited)
	if summary := diagnosticSummary(session); summary != "" {
		fmt.Fprintln(stdout, summary)
	}
	fmt.Fprintf(stdout, "Corpus: %s\n", cmd.corpus)
	fmt.Fprintf(stdout, "Sidecar: %s\n", storage.SidecarPath(cmd.corpus))
	return 0
}

// diagnosticSummary counts records per diagnostic, e.g.
// "Diagnostics: duplicate 2, empty 1". Empty when no record has a diagnostic.
func diagnosticSummary(s *review.Session) string {
	order := append([]string(nil), checks.All...)
	seen := make(map[string]bool, len(order))
	for _, name := range order {
		seen[name] = true
	}
	counts := make(map[string]int)

	for i := range s.Len() {
		for _, d := range s.Diagnostics(i) {
			if !seen[d.Name] {
				seen[d.Name] = true
				order = append(order, d.Name)
			}
			counts[d.Name]++
		}
	}
	if len(counts) == 0 {
		return ""
	}

	parts := make([]string, 0, len(counts))
	for _, name := range order {
		if n := counts[name]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", name, n))
		}
	}
	return "Diagnostics: " + strings.Join(parts, ", ")
}
