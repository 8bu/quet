// Package review holds review state and the in-memory review Session.
package review

import (
	"encoding/json"
	"fmt"
)

type ReviewStatus string

const (
	Unreviewed  ReviewStatus = "unreviewed"
	Approved    ReviewStatus = "approved"
	Rejected    ReviewStatus = "rejected"
	NeedsReview ReviewStatus = "needs_review"
)

var AllStatuses = []ReviewStatus{Unreviewed, Approved, Rejected, NeedsReview}

func ParseStatus(s string) (ReviewStatus, error) {
	switch s {
	case "unreviewed":
		return Unreviewed, nil
	case "approved":
		return Approved, nil
	case "rejected":
		return Rejected, nil
	case "needs_review", "needs-review", "review":
		return NeedsReview, nil
	}
	return "", fmt.Errorf("unknown status %q (want unreviewed, approved, rejected, needs_review)", s)
}

// State is the mutable review state of one record. The zero value (Status "") means Unreviewed.
type State struct {
	Status      ReviewStatus
	EditedText  *string         // nil = not edited; final text = *EditedText
	ManualFlags []string        // sorted, unique
	Annotations json.RawMessage // reserved for future structured annotations; nil for now
}

func (s State) EffectiveStatus() ReviewStatus {
	if s.Status == "" {
		return Unreviewed
	}
	return s.Status
}

func (s State) Edited() bool { return s.EditedText != nil }

// Clone deep-copies s.
func (s State) Clone() State {
	c := s
	if s.EditedText != nil {
		t := *s.EditedText
		c.EditedText = &t
	}
	c.ManualFlags = append([]string(nil), s.ManualFlags...)
	c.Annotations = append(json.RawMessage(nil), s.Annotations...)
	if len(s.Annotations) == 0 {
		c.Annotations = nil
	}
	return c
}
