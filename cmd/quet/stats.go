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

	if cmd.json {
		if err := newEncoder(stdout).Encode(newStatsOut(cmd, session)); err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
		return 0
	}

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

// statsOut is the JSON shape of `quet stats --json`.
type statsOut struct {
	Corpus      string         `json:"corpus"`
	Sidecar     string         `json:"sidecar"`
	Total       int            `json:"total"`
	Approved    int            `json:"approved"`
	Rejected    int            `json:"rejected"`
	NeedsReview int            `json:"needs_review"`
	Unreviewed  int            `json:"unreviewed"`
	Edited      int            `json:"edited"`
	Diagnostics map[string]int `json:"diagnostics"`
	ManualFlags []flagCountOut `json:"manual_flags"`
}

// flagCountOut is one manual flag of the flags.yaml taxonomy with its usage count.
type flagCountOut struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Count       int    `json:"count"`
}

// newStatsOut collects the review counts, diagnostic counts and the manual flag
// taxonomy (flags.yaml order) with usage counts.
func newStatsOut(cmd command, s *review.Session) statsOut {
	counts := s.Counts()
	out := statsOut{
		Corpus:      cmd.corpus,
		Sidecar:     storage.SidecarPath(cmd.corpus),
		Total:       counts.Total,
		Approved:    counts.Approved,
		Rejected:    counts.Rejected,
		NeedsReview: counts.NeedsReview,
		Unreviewed:  counts.Unreviewed,
		Edited:      counts.Edited,
		Diagnostics: make(map[string]int),
		ManualFlags: make([]flagCountOut, 0, len(s.FlagDefs)),
	}
	for _, f := range s.DiagnosticFacets() {
		out.Diagnostics[f.Value] = f.Count
	}
	used := make(map[string]int)
	for _, f := range s.ManualFacets() {
		used[f.Value] = f.Count
	}
	for _, def := range s.FlagDefs {
		out.ManualFlags = append(out.ManualFlags, flagCountOut{Name: def.Name, Description: def.Description, Count: used[def.Name]})
	}
	return out
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
