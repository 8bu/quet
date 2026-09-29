// Package tui is the Bubble Tea interface for Quet.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/review"
)

// Options for Run and Browse.
type Options struct {
	// Settings re-reads the config and flags that apply to the corpus at
	// corpusPath. Nil disables in-app config creation, editing and reload.
	Settings func(corpusPath string) (config.Settings, error)
	// UpdateCheck reports a newer Quet release: the latest version and true
	// when an update is available. It blocks, so it runs once per program
	// inside a tea.Cmd, never on the UI goroutine. Nil disables the check.
	UpdateCheck func() (latest string, ok bool)
}

// Run starts the full-screen TUI (alt screen) and blocks until quit.
func Run(s *review.Session, opt Options) error {
	final, err := tea.NewProgram(newModel(s, opt), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	if m, ok := final.(model); ok {
		return m.quitErr
	}
	return nil
}
