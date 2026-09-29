package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/corpus"
	"github.com/8bu/quet/internal/review"
	"github.com/8bu/quet/internal/storage"
)

// OpenFunc turns a file picked in the browser into a review session. It is
// the gate: an error keeps the browser open and is shown to the user.
type OpenFunc func(path string) (*review.Session, error)

// Browse starts the full-screen TUI on a file browser rooted at dir. Opening a
// file that passes open switches to the review screen in the same program.
// It returns the session that was opened, or nil if the user quit from the
// browser; the caller must close a non-nil session.
func Browse(dir string, open OpenFunc, opt Options) (*review.Session, error) {
	b, err := newBrowser(dir, open, opt)
	if err != nil {
		return nil, err
	}
	final, err := tea.NewProgram(b, tea.WithAltScreen()).Run()
	m, reviewing := final.(model)
	if !reviewing {
		return nil, err
	}
	if err == nil {
		err = m.quitErr
	}
	return m.sess, err
}

// entry is one row of the browser: the parent link, a directory or a
// supported corpus file.
type entry struct {
	name     string
	dir      bool
	size     int64
	reviewed bool // a Quet sidecar sits next to the file
}

const parentName = ".."

// browser lists the directories and supported corpus files of one directory.
type browser struct {
	dir     string
	entries []entry
	cursor  int
	hidden  bool // show dot-prefixed entries

	open     OpenFunc
	opt      Options // handed to the review model on a successful open
	checking string  // path being opened, "" when idle

	width, height int

	status    string
	statusErr bool
	statusSeq int

	updateVersion string // newer release, handed to the review model
}

// openedMsg reports the result of running the gate on a picked file.
type openedMsg struct {
	path string
	sess *review.Session
	err  error
}

func newBrowser(dir string, open OpenFunc, opt Options) (browser, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return browser{}, err
	}
	b := browser{dir: abs, open: open, opt: opt, width: 100, height: 30}
	if err := b.load(""); err != nil {
		return browser{}, err
	}
	return b, nil
}

// load reads b.dir and puts the cursor on the entry named focus, else on the
// first entry after the parent link.
func (b *browser) load(focus string) error {
	entries, err := readEntries(b.dir, b.hidden)
	if err != nil {
		return err
	}
	b.entries = entries
	b.cursor = 0
	if len(entries) > 1 && entries[0].name == parentName {
		b.cursor = 1
	}
	for i, e := range entries {
		if e.name == focus {
			b.cursor = i
			break
		}
	}
	return nil
}

// readEntries lists dir: the parent link (unless dir is the root), then
// directories, then supported corpus files, each sorted case-insensitively.
// Symlinks are followed; broken ones are skipped.
func readEntries(dir string, hidden bool) ([]entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(des))
	for _, de := range des {
		names[de.Name()] = true
	}
	var dirs, files []entry
	for _, de := range des {
		name := de.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, entry{name: name, dir: true})
		case info.Mode().IsRegular() && corpus.Supported(name):
			files = append(files, entry{
				name:     name,
				size:     info.Size(),
				reviewed: names[filepath.Base(storage.SidecarPath(name))],
			})
		}
	}
	byName := func(es []entry) {
		sort.Slice(es, func(i, j int) bool {
			return strings.ToLower(es[i].name) < strings.ToLower(es[j].name)
		})
	}
	byName(dirs)
	byName(files)
	out := make([]entry, 0, len(dirs)+len(files)+1)
	if filepath.Dir(dir) != dir {
		out = append(out, entry{name: parentName, dir: true})
	}
	out = append(out, dirs...)
	return append(out, files...), nil
}

// Init implements tea.Model: it starts the optional update check. The review
// model the browser hands off to never repeats it.
func (b browser) Init() tea.Cmd { return checkUpdate(b.opt.UpdateCheck) }

// Update implements tea.Model. A successful open returns the review model,
// which takes over the program.
func (b browser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width, b.height = msg.Width, msg.Height
		return b, nil
	case statusExpireMsg:
		if msg.seq == b.statusSeq {
			b.status = ""
		}
		return b, nil
	case updateAvailableMsg:
		b.updateVersion = msg.version
		return b, nil
	case openedMsg:
		return b.opened(msg)
	case tea.KeyMsg:
		return b.updateKey(msg)
	}
	return b, nil
}

func (b browser) updateKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Paste || (k.Type == tea.KeyRunes && len(k.Runes) > 1) {
		return b, nil
	}
	if b.checking != "" {
		if k.String() == "ctrl+c" {
			return b, tea.Quit
		}
		return b, nil
	}
	switch k.String() {
	case "q", "esc", "ctrl+c":
		return b, tea.Quit
	case "up", "k", "w":
		if b.cursor > 0 {
			b.cursor--
		}
	case "down", "j", "s":
		if b.cursor < len(b.entries)-1 {
			b.cursor++
		}
	case "g", "home":
		b.cursor = 0
	case "G", "end":
		b.cursor = max(len(b.entries)-1, 0)
	case "enter", "right", "l", "d":
		return b.enter()
	case "backspace", "left", "h", "a":
		return b.parent()
	case ".":
		b.hidden = !b.hidden
		focus := ""
		if b.cursor < len(b.entries) {
			focus = b.entries[b.cursor].name
		}
		if err := b.load(focus); err != nil {
			return b.fail("%v", err)
		}
		if b.hidden {
			return b.note("Showing hidden files")
		}
		return b.note("Hiding hidden files")
	}
	return b, nil
}

// enter opens the entry under the cursor: a directory is listed, a file is
// handed to the gate asynchronously so a large corpus never freezes the UI.
func (b browser) enter() (tea.Model, tea.Cmd) {
	if b.cursor >= len(b.entries) {
		return b, nil
	}
	e := b.entries[b.cursor]
	if e.name == parentName {
		return b.parent()
	}
	path := filepath.Join(b.dir, e.name)
	if e.dir {
		prev := b.dir
		b.dir = path
		if err := b.load(""); err != nil {
			b.dir = prev
			return b.fail("Can't open %s: %v", e.name, err)
		}
		b.status = ""
		return b, nil
	}
	b.checking = path
	b.status, b.statusErr = "Checking "+e.name+"…", false
	open := b.open
	return b, func() tea.Msg {
		s, err := open(path)
		return openedMsg{path: path, sess: s, err: err}
	}
}

// parent lists the parent directory with the cursor on the one just left.
func (b browser) parent() (tea.Model, tea.Cmd) {
	up := filepath.Dir(b.dir)
	if up == b.dir {
		return b, nil
	}
	prev := b.dir
	b.dir = up
	if err := b.load(filepath.Base(prev)); err != nil {
		b.dir = prev
		return b.fail("Can't open %s: %v", up, err)
	}
	b.status = ""
	return b, nil
}

// opened finishes the gate: failures stay in the browser, success hands the
// session to the review model at the current terminal size.
func (b browser) opened(msg openedMsg) (tea.Model, tea.Cmd) {
	b.checking = ""
	if msg.err != nil {
		return b.fail("Can't open %s: %v", filepath.Base(msg.path), msg.err)
	}
	m := newModel(msg.sess, b.opt)
	m.updateVersion = b.updateVersion
	m, _ = m.update(tea.WindowSizeMsg{Width: b.width, Height: b.height})
	hint := m.startupHint()
	m, cmd := m.setStatus("Opened %s  %d records", filepath.Base(msg.path), msg.sess.Len())
	if hint != "" {
		m.status += "  ·  " + hint
	}
	return m, cmd
}

// fail shows an error until the next action replaces it.
func (b browser) fail(format string, args ...any) (tea.Model, tea.Cmd) {
	b.status, b.statusErr = fmt.Sprintf(format, args...), true
	b.statusSeq++
	return b, nil
}

// note shows a transient confirmation.
func (b browser) note(format string, args ...any) (tea.Model, tea.Cmd) {
	b.status, b.statusErr = fmt.Sprintf(format, args...), false
	b.statusSeq++
	seq := b.statusSeq
	return b, tea.Tick(statusTTL, func(_ time.Time) tea.Msg { return statusExpireMsg{seq: seq} })
}

// maxErrorRows caps how many lines a gate error may take, so the reason for a
// rejection stays readable without pushing the listing off screen.
const maxErrorRows = 3

// View implements tea.Model: one panel with the path and the listing, then
// the status (errors wrap to maxErrorRows lines) or the update notice, and the
// footer.
func (b browser) View() string {
	w, h := b.width, b.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	footer := truncateLine(styleFooter.Render(browserFooter), w)
	if h <= 1 {
		return footer
	}
	var status []string
	if b.status != "" && h >= 3 {
		if b.statusErr {
			status = wrapRows([]string{b.status}, w)
			status = status[:min(len(status), maxErrorRows, h-2)]
			for i, line := range status {
				status[i] = styleError.Render(line)
			}
		} else {
			status = []string{truncateLine(styleStatus.Render(b.status), w)}
		}
	} else if b.updateVersion != "" && h >= 3 {
		status = []string{truncateLine(styleAccent.Render(updateNotice(b.updateVersion)), w)}
	}
	rows := make([]string, 0, h)
	if bodyH := h - 1 - len(status); bodyH > 0 {
		rows = append(rows, strings.Split(b.panel(w, bodyH), "\n")...)
	}
	rows = append(rows, status...)
	rows = append(rows, footer)
	if len(rows) > h {
		rows = rows[:h]
	}
	return strings.Join(rows, "\n")
}

const browserFooter = "w/s move  enter/d open  a/backspace up  . hidden  q quit"

func (b browser) panel(w, h int) string {
	iw := max(w-2, 1)
	rows := []string{styleMuted.Render(truncatePath(b.dir, iw)), ""}
	if len(b.entries) == 0 || (len(b.entries) == 1 && b.entries[0].name == parentName) {
		rows = append(rows, styleMuted.Render("No folders or corpus files here (.jsonl, .json, .txt)"))
	}
	list, sel := listRows(len(b.entries), b.cursor, max(h-2-len(rows), 0), func(i int) string {
		return b.entries[i].render(iw - 2)
	})
	rows, sel = appendList(rows, list, sel)
	return box("Open corpus", true, w, h, rows, sel)
}

// render draws an entry in w cells: the name on the left, size and review
// marker on the right.
func (e entry) render(w int) string {
	if e.dir {
		return truncateLine(e.name+"/", w)
	}
	right := humanSize(e.size)
	if e.reviewed {
		right = "● in review  " + right
	}
	pad := w - lipgloss.Width(e.name) - lipgloss.Width(right)
	if pad < 2 {
		return truncateLine(e.name, w)
	}
	return e.name + strings.Repeat(" ", pad) + right
}

// humanSize formats a byte count as B, KB, MB or GB.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, suffix := float64(n)/unit, "KB"
	for _, s := range []string{"MB", "GB"} {
		if v < unit {
			break
		}
		v, suffix = v/unit, s
	}
	return fmt.Sprintf("%.1f %s", v, suffix)
}

// truncatePath keeps the end of a long path, which is the part that matters.
func truncatePath(p string, w int) string {
	r := []rune(p)
	if lipgloss.Width(p) <= w || w < 2 {
		return truncateLine(p, w)
	}
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}
