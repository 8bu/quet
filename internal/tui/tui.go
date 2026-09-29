// Package tui is the Bubble Tea interface for Quet.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/review"
)

// Options for Run. The set is currently empty; it exists so callers can keep
// passing it while options are added.
type Options struct{}

// Run starts the full-screen TUI (alt screen) and blocks until quit.
func Run(s *review.Session, opt Options) error {
	final, err := tea.NewProgram(newModel(s), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	if m, ok := final.(model); ok {
		return m.quitErr
	}
	return nil
}
