package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/config"
)

// noFlagsHint is the startup status shown when no flags file was found.
const noFlagsHint = "No flags file — press f to create one"

// editorDoneMsg reports that the external editor opened on path has exited.
type editorDoneMsg struct {
	path string
	err  error
}

// startupHint returns the status to show when the review screen opens, or "".
func (m model) startupHint() string {
	if m.opt.Settings != nil && m.sess.FlagsPath == "" {
		return noFlagsHint
	}
	return ""
}

// flagsTarget returns where a new flags file is created: the configured
// flags_file (relative to the config file's directory, like
// config.ResolveFlagsFile), else ./flags.yaml.
func (m model) flagsTarget() string {
	path := m.sess.Config.FlagsFile
	if path == "" {
		return "flags.yaml"
	}
	if filepath.IsAbs(path) {
		return path
	}
	base := "."
	if m.sess.Config.Path != "" {
		base = filepath.Dir(m.sess.Config.Path)
	}
	return filepath.Join(base, path)
}

// promptCreateFlags asks whether to create a starter flags file.
func (m model) promptCreateFlags() (model, tea.Cmd) {
	m.createPath = m.flagsTarget()
	m.mode = ModeCreateFlags
	return m, nil
}

// updateCreateFlags handles ModeCreateFlags: y creates the file, n/esc cancels.
func (m model) updateCreateFlags(k tea.KeyMsg) (model, tea.Cmd) {
	switch k.String() {
	case "y", "Y":
		return m.createFlags()
	case "n", "N", "esc":
		m.mode = ModeReview
		return m, nil
	}
	return m, nil
}

// createFlags writes the starter flags file, reloads the settings and opens
// the flag picker on the new taxonomy.
func (m model) createFlags() (model, tea.Cmd) {
	path := m.createPath
	m.mode = ModeReview
	if err := writeStarter(path, config.StarterFlags); err != nil {
		return m.setStatus("Create failed: %v", err)
	}
	if err := m.reloadSettings(); err != nil {
		return m.setStatus("Config error: %v", err)
	}
	if m.sess.Current() >= 0 && m.flags.open(m.sess) {
		m.mode = ModeFlags
	}
	return m.setStatus("Created %s", path)
}

// writeStarter creates path with content; an existing file is left untouched.
func writeStarter(path, content string) error {
	if err := config.WriteStarter(path, content, false); err != nil && !errors.Is(err, config.ErrExists) {
		return err
	}
	return nil
}

// editConfig opens the config file in the external editor, creating a starter
// quet.yaml first when none was loaded.
func (m model) editConfig() (model, tea.Cmd) {
	if m.opt.Settings == nil {
		return m.setStatus("Config editing unavailable")
	}
	path := m.sess.Config.Path
	if path == "" {
		path = "quet.yaml"
		if err := writeStarter(path, config.StarterConfig); err != nil {
			return m.setStatus("Create failed: %v", err)
		}
	}
	return m, editorCmd(path)
}

// editFlagsFile opens the flags file in the external editor, creating a
// starter one first when none was found.
func (m model) editFlagsFile() (model, tea.Cmd) {
	if m.opt.Settings == nil {
		return m.setStatus("Config editing unavailable")
	}
	path := m.sess.FlagsPath
	if path == "" {
		path = m.flagsTarget()
		if err := writeStarter(path, config.StarterFlags); err != nil {
			return m.setStatus("Create failed: %v", err)
		}
	}
	return m, editorCmd(path)
}

// editorArgs returns the editor command line: $VISUAL, else $EDITOR, else vi.
func editorArgs() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if args := strings.Fields(os.Getenv(env)); len(args) > 0 {
			return args
		}
	}
	return []string{"vi"}
}

// editorCmd suspends the TUI, runs the editor on path and reports its exit.
func editorCmd(path string) tea.Cmd {
	args := editorArgs()
	c := exec.Command(args[0], append(args[1:], path)...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return editorDoneMsg{path: path, err: err} })
}

// editorDone reloads the settings after the editor exits cleanly.
func (m model) editorDone(msg editorDoneMsg) (model, tea.Cmd) {
	if msg.err != nil {
		return m.setStatus("Editor failed: %v", msg.err)
	}
	return m.reload()
}

// reload re-reads the config and flags and reports the outcome.
func (m model) reload() (model, tea.Cmd) {
	if m.opt.Settings == nil {
		return m.setStatus("Config editing unavailable")
	}
	if err := m.reloadSettings(); err != nil {
		return m.setStatus("Config error: %v", err)
	}
	return m.setStatus("Reloaded config (%d flags)", len(m.sess.FlagDefs))
}

// reloadSettings applies freshly loaded settings to the session; on error the
// old settings stay in effect.
func (m *model) reloadSettings() error {
	set, err := m.opt.Settings(m.sess.Corpus.Path)
	if err != nil {
		return err
	}
	m.sess.Reconfigure(set)
	m.refreshFacets()
	m.flags.cursor = clampIndex(m.flags.cursor, len(m.flags.rows))
	return nil
}
