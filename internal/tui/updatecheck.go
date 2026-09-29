package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// updateAvailableMsg reports that a newer Quet release than the running one
// exists.
type updateAvailableMsg struct{ version string }

// checkUpdate returns a command that runs check off the UI goroutine and
// reports a newer release, or nil when check is nil. A check that finds no
// update produces no message.
func checkUpdate(check func() (string, bool)) tea.Cmd {
	if check == nil {
		return nil
	}
	return func() tea.Msg {
		latest, ok := check()
		if !ok || latest == "" {
			return nil
		}
		return updateAvailableMsg{version: latest}
	}
}

// updateNotice is the persistent notice shown while a newer release exists.
func updateNotice(version string) string {
	return fmt.Sprintf("quet %s available — run: quet update", version)
}
