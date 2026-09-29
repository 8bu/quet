// Package export writes reviewed corpora. It never writes to the source corpus path.
package export

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/8bu/quet/internal/review"
)

// Options selects which records and what shape to export.
type Options struct {
	Statuses   []review.ReviewStatus // empty = all statuses
	WithReview bool                  // include a "quet" object: {id,status,edited,original_text,manual_flags,suggested_flags}
	Format     string                // "jsonl" (default) | "txt" (final text, one per line)
}

// Preset is a named export offered in the TUI palette and CLI.
type Preset struct {
	Name    string // "approved" | "rejected" | "needs_review" | "all" | "clean"
	Label   string // e.g. "Export approved"
	Options Options
	Suffix  string // default output suffix, e.g. ".approved.jsonl"
}

// Presets returns the five built-in exports, in display order.
//
// approved, rejected and needs_review are the review decision exports: each one
// carries the final text plus the original imported metadata, and excludes
// reviewer internals (status, flags, edit history). clean is the explicit
// training-corpus preset: the same approved record set and shape as approved,
// under a name that says what the output is meant for. all is the archival
// export and is the only preset that embeds review metadata.
func Presets() []Preset {
	return []Preset{
		{
			Name:    "approved",
			Label:   "Export approved",
			Options: Options{Statuses: []review.ReviewStatus{review.Approved}},
			Suffix:  ".approved.jsonl",
		},
		{
			Name:    "rejected",
			Label:   "Export rejected",
			Options: Options{Statuses: []review.ReviewStatus{review.Rejected}},
			Suffix:  ".rejected.jsonl",
		},
		{
			Name:    "needs_review",
			Label:   "Export needs review",
			Options: Options{Statuses: []review.ReviewStatus{review.NeedsReview}},
			Suffix:  ".needs_review.jsonl",
		},
		{
			Name:  "all",
			Label: "Export all with review metadata",
			Options: Options{
				Statuses:   append([]review.ReviewStatus(nil), review.AllStatuses...),
				WithReview: true,
			},
			Suffix: ".reviewed.jsonl",
		},
		{
			Name:    "clean",
			Label:   "Export clean approved corpus",
			Options: Options{Statuses: []review.ReviewStatus{review.Approved}},
			Suffix:  ".clean.jsonl",
		},
	}
}

// DefaultPath returns a non-existing path next to the corpus: <corpus-without-ext><suffix>,
// adding -1, -2... if taken. corpus.jsonl + ".approved.jsonl" => corpus.approved.jsonl.
func DefaultPath(corpusPath, suffix string) string {
	base := strings.TrimSuffix(corpusPath, filepath.Ext(corpusPath))
	path := base + suffix
	if !exists(path) {
		return path
	}
	// corpus.approved.jsonl => corpus.approved-1.jsonl, corpus.approved-2.jsonl, ...
	ext := filepath.Ext(suffix)
	stem := strings.TrimSuffix(suffix, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s%s-%d%s", base, stem, i, ext)
		if !exists(candidate) {
			return candidate
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
