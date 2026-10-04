package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/web"
)

// webStatusTTL is how long the status row keeps the result or error of a network call: longer than
// statusTTL because these messages (an error with its hint, a push summary) take time to read.
const webStatusTTL = 10 * time.Second

// webAfter is what the Remote & project flow resumes once the session is linked to a project.
type webAfter int

// Follow-ups of the link flow.
const (
	// webAfterNone returns to the Web menu.
	webAfterNone webAfter = iota
	// webAfterPublish opens the publish confirmation.
	webAfterPublish
	// webAfterCompare starts pulling the collaborators' labels.
	webAfterCompare
)

// Form field indexes of remoteForm.
const (
	formName = iota
	formURL
	formClientID
	formSecret
	formFields
)

// formLabels are the displayed names of the remote form fields.
var formLabels = [formFields]string{"Name", "URL", "Client ID", "Client secret"}

// remoteForm is the state of the add-remote form: one rune buffer per field and the focused field.
type remoteForm struct {
	fields [formFields][]rune
	focus  int
}

// webState is everything the web integration keeps in the annotation model: the link, the state of the
// Remote & project flow, the in-flight network call and the compare data.
type webState struct {
	link   web.Link // the saved sidecar link (meaningful when linked)
	linked bool
	cursor int // Web menu cursor
	after  webAfter

	note    string // the last result or error, shown in full in the Web menu
	noteErr bool

	remotes      *web.Remotes
	remoteCursor int
	form         remoteForm
	remote       string // remote chosen in the link flow

	projects   []web.ProjectSummary
	projCursor int // index into projects; len(projects) is the "new project" row
	newSlug    []rune

	busy     string // what the in-flight call does; "" when idle
	busyBack annotMode
	seq      int // identifies the in-flight call; results with another seq are stale
	cancel   context.CancelFunc

	compare compareState
}

// webProjectsMsg is the result of listing the server's projects.
type webProjectsMsg struct {
	seq      int
	projects []web.ProjectSummary
	err      error
}

// webPublishMsg is the result of a push.
type webPublishMsg struct {
	seq int
	res web.PushResult
	err error
}

// webPullMsg is the result of pulling every collaborator's labels.
type webPullMsg struct {
	seq    int
	pulled *web.Pulled
	err    error
}

// linkName renders a link as "remote/project", or "default remote" when the sidecar names no remote.
func linkName(l web.Link) string {
	if l.Remote == "" {
		return "default remote/" + l.Project
	}
	return l.Remote + "/" + l.Project
}

// slugify turns s into a project slug: lowercase ASCII letters and digits, runs of anything else becoming one
// '-', at most 63 characters, never starting or ending with '-'. It is "" when s has no letter or digit.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	out := b.String()
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// defaultSlug is the project slug suggested for the session: the queue file name without its extension,
// slugified (the labels file name for a session without a queue file).
func defaultSlug(s *annotate.Session) string {
	path := s.QueuePath()
	if path == "" {
		path = s.OutPath()
	}
	base := filepath.Base(path)
	return slugify(strings.TrimSuffix(base, filepath.Ext(base)))
}

// slugRune maps a typed rune to the slug rune it contributes after cur, or 0 when it is not allowed: letters
// are lowercased, space and '_' become '-', a slug cannot start with '-' or exceed 63 characters.
func slugRune(cur []rune, r rune) rune {
	if len(cur) >= 63 {
		return 0
	}
	switch {
	case r >= 'A' && r <= 'Z':
		r += 'a' - 'A'
	case r == ' ' || r == '_':
		r = '-'
	}
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return r
	case r == '-' && len(cur) > 0:
		return r
	}
	return 0
}

// webClientFor loads the remotes file and returns a client for the remote called name ("" = the default one)
// together with the resolved remote's name.
func webClientFor(name string) (*web.Client, string, error) {
	remotes, err := web.LoadRemotes()
	if err != nil {
		return nil, "", err
	}
	rem, err := remotes.Resolve(name)
	if err != nil {
		return nil, "", err
	}
	c, err := web.NewClient(rem)
	if err != nil {
		return nil, "", err
	}
	return c, rem.Name, nil
}

// setWebStatus is setStatus (or setError) with the longer web TTL.
func (m annotModel) setWebStatus(isErr bool, format string, args ...any) (annotModel, tea.Cmd) {
	m.status = fmt.Sprintf(format, args...)
	m.statusErr = isErr
	m.statusSeq++
	seq := m.statusSeq
	return m, tea.Tick(webStatusTTL, func(time.Time) tea.Msg { return statusExpireMsg{seq: seq} })
}

// webDone records the outcome of a web action: the note shown in the Web menu and the status row. It returns
// to the Web menu.
func (m annotModel) webDone(isErr bool, format string, args ...any) (annotModel, tea.Cmd) {
	m.web.note = fmt.Sprintf(format, args...)
	m.web.noteErr = isErr
	m.mode = annotWebMenu
	return m.setWebStatus(isErr, "%s", m.web.note)
}

// beginWeb marks a network call as in flight: the screen shows busy until the result message with the returned
// sequence number arrives or esc cancels it. It returns the call's context.
func (m annotModel) beginWeb(busy string) (annotModel, context.Context, int) {
	if m.web.cancel != nil {
		m.web.cancel()
	}
	m.web.seq++
	ctx, cancel := context.WithCancel(context.Background())
	m.web.cancel = cancel
	m.web.busy = busy
	m.web.busyBack = annotWebMenu
	m.web.note = ""
	m.mode = annotWebBusy
	m.status, m.statusErr = busy, false
	m.statusSeq++
	return m, ctx, m.web.seq
}

// endWeb clears the in-flight call after its result arrived.
func (m annotModel) endWeb() annotModel {
	if m.web.cancel != nil {
		m.web.cancel()
		m.web.cancel = nil
	}
	if m.status == m.web.busy {
		m.status = ""
	}
	m.web.busy = ""
	return m
}

// openWeb enters the Web menu, reading the link sidecar of the labels file.
func (m annotModel) openWeb() (annotModel, tea.Cmd) {
	link, ok, err := web.LoadLink(m.sess.OutPath())
	m.web.link, m.web.linked = link, ok
	m.web.cursor, m.web.note, m.web.noteErr = 0, "", false
	m.mode = annotWebMenu
	if err != nil {
		return m.webDone(true, "%v", err)
	}
	return m, nil
}

// webMenuItems is the number of rows of the Web menu.
const webMenuItems = 3

// updateWebMenu handles annotWebMenu: j/k move, enter or r/p/c choose, esc closes.
func (m annotModel) updateWebMenu(key string) (annotModel, tea.Cmd) {
	switch key {
	case "esc", "q", "w":
		m.mode = annotMain
		return m, nil
	case "j", "down":
		m.web.cursor = clampIndex(m.web.cursor+1, webMenuItems)
		return m, nil
	case "k", "up":
		m.web.cursor = clampIndex(m.web.cursor-1, webMenuItems)
		return m, nil
	case "r":
		return m.startLink(webAfterNone)
	case "p":
		return m.openPublish()
	case "c":
		return m.openCompare()
	case "enter":
		switch m.web.cursor {
		case 0:
			return m.startLink(webAfterNone)
		case 1:
			return m.openPublish()
		default:
			return m.openCompare()
		}
	}
	return m, nil
}

// startLink opens the Remote & project flow: the remote picker, or straight the add-remote form when no remote
// is saved. When the flow ends with a project chosen, after says what comes next.
func (m annotModel) startLink(after webAfter) (annotModel, tea.Cmd) {
	m.web.after = after
	remotes, err := web.LoadRemotes()
	if err != nil {
		return m.webDone(true, "%v", err)
	}
	m.web.remotes = remotes
	if len(remotes.List) == 0 {
		return m.openRemoteForm(), nil
	}
	m.web.remoteCursor = 0
	want := remotes.Default
	if m.web.linked && m.web.link.Remote != "" {
		want = m.web.link.Remote
	}
	for i, r := range remotes.List {
		if r.Name == want {
			m.web.remoteCursor = i
		}
	}
	m.mode = annotWebRemotes
	return m, nil
}

// updateWebRemotes handles annotWebRemotes: j/k move over the saved remotes and the "add" row, enter picks, n
// adds, esc goes back to the Web menu.
func (m annotModel) updateWebRemotes(key string) (annotModel, tea.Cmd) {
	n := len(m.web.remotes.List) + 1
	switch key {
	case "esc", "q":
		m.mode = annotWebMenu
		return m, nil
	case "j", "down", "ctrl+n":
		m.web.remoteCursor = clampIndex(m.web.remoteCursor+1, n)
	case "k", "up", "ctrl+p":
		m.web.remoteCursor = clampIndex(m.web.remoteCursor-1, n)
	case "n", "a":
		return m.openRemoteForm(), nil
	case "enter":
		if m.web.remoteCursor >= len(m.web.remotes.List) {
			return m.openRemoteForm(), nil
		}
		return m.fetchProjects(m.web.remotes.List[m.web.remoteCursor].Name)
	}
	return m, nil
}

// remoteURLPrefill is the URL field's starting text in the add-remote form.
const remoteURLPrefill = "https://"

// openRemoteForm opens the add-remote form, prefilled with a default name and the URL scheme.
func (m annotModel) openRemoteForm() annotModel {
	m.web.form = remoteForm{}
	if m.web.remotes == nil || len(m.web.remotes.List) == 0 {
		m.web.form.fields[formName] = []rune("origin")
	}
	m.web.form.fields[formURL] = []rune(remoteURLPrefill)
	m.mode = annotWebForm
	return m
}

// updateWebForm handles annotWebForm: typing edits the focused field, tab/shift+tab/↑/↓ move, enter goes to the
// next field and saves from the last, ctrl+s saves, esc goes back.
func (m annotModel) updateWebForm(k tea.KeyMsg) (annotModel, tea.Cmd) {
	f := &m.web.form
	switch k.String() {
	case "esc":
		if m.web.remotes != nil && len(m.web.remotes.List) > 0 {
			m.mode = annotWebRemotes
		} else {
			m.mode = annotWebMenu
		}
		return m, nil
	case "tab", "down", "ctrl+n":
		f.focus = (f.focus + 1) % formFields
		return m, nil
	case "shift+tab", "up", "ctrl+p":
		f.focus = (f.focus + formFields - 1) % formFields
		return m, nil
	case "enter":
		if f.focus < formFields-1 {
			f.focus++
			return m, nil
		}
		return m.saveRemote()
	case "ctrl+s":
		return m.saveRemote()
	case "backspace":
		if n := len(f.fields[f.focus]); n > 0 {
			f.fields[f.focus] = f.fields[f.focus][:n-1]
		}
		return m, nil
	}
	switch k.Type {
	case tea.KeyRunes:
		m.formAppend(k.Runes)
	case tea.KeySpace:
		m.formAppend([]rune{' '})
	}
	return m, nil
}

// formAppend appends typed or pasted runes to the focused form field; control characters (a pasted newline)
// are dropped.
func (m *annotModel) formAppend(runes []rune) {
	f := &m.web.form
	for _, r := range runes {
		if r >= ' ' && r != 0x7f {
			f.fields[f.focus] = append(f.fields[f.focus], r)
		}
	}
}

// saveRemote validates the form and saves it in remotes.yaml (replacing a remote of the same name), then goes
// on to the project picker of that remote.
func (m annotModel) saveRemote() (annotModel, tea.Cmd) {
	f := m.web.form
	rem := web.Remote{
		Name:         strings.TrimSpace(string(f.fields[formName])),
		URL:          strings.TrimSpace(string(f.fields[formURL])),
		ClientID:     strings.TrimSpace(string(f.fields[formClientID])),
		ClientSecret: strings.TrimSpace(string(f.fields[formSecret])),
	}
	if err := rem.Validate(); err != nil {
		return m.setError("%v", err)
	}
	remotes, err := web.LoadRemotes()
	if err != nil {
		return m.setError("%v", err)
	}
	remotes.Set(rem)
	if err := remotes.Save(); err != nil {
		return m.setError("%v", err)
	}
	m.web.remotes = remotes
	return m.fetchProjects(rem.Name)
}

// fetchProjects lists the server's projects of remote name as a tea.Cmd and shows the busy screen meanwhile.
func (m annotModel) fetchProjects(name string) (annotModel, tea.Cmd) {
	m.web.remote = name
	m, ctx, seq := m.beginWeb("loading projects from " + name + "…")
	return m, func() tea.Msg {
		c, _, err := webClientFor(name)
		if err != nil {
			return webProjectsMsg{seq: seq, err: err}
		}
		list, err := c.Projects(ctx)
		return webProjectsMsg{seq: seq, projects: list, err: err}
	}
}

// onProjects handles the project list: it opens the picker with the cursor on the linked project, else on the
// project named like the queue, else on the "new project" row.
func (m annotModel) onProjects(msg webProjectsMsg) (annotModel, tea.Cmd) {
	if msg.seq != m.web.seq {
		return m, nil
	}
	m = m.endWeb()
	if msg.err != nil {
		return m.webDone(true, "%v", msg.err)
	}
	m.web.projects = msg.projects
	m.web.newSlug = []rune(defaultSlug(m.sess))
	m.web.projCursor = len(msg.projects)
	for i, p := range msg.projects {
		if m.web.linked && m.web.link.Remote == m.web.remote && p.Slug == m.web.link.Project {
			m.web.projCursor = i
			break
		}
		if p.Slug == string(m.web.newSlug) {
			m.web.projCursor = i
		}
	}
	m.mode = annotWebProjects
	return m, nil
}

// updateWebProjects handles annotWebProjects: ↑/↓ move (j/k too, except on the "new project" row where letters
// edit its slug), enter links the chosen project, esc goes back.
func (m annotModel) updateWebProjects(k tea.KeyMsg) (annotModel, tea.Cmd) {
	n := len(m.web.projects) + 1
	onNew := m.web.projCursor == len(m.web.projects)
	key := k.String()
	switch key {
	case "esc":
		m.mode = annotWebMenu
		return m, nil
	case "down", "ctrl+n":
		m.web.projCursor = clampIndex(m.web.projCursor+1, n)
		return m, nil
	case "up", "ctrl+p":
		m.web.projCursor = clampIndex(m.web.projCursor-1, n)
		return m, nil
	case "enter":
		if !onNew {
			return m.linkProject(m.web.projects[m.web.projCursor].Slug)
		}
		if len(m.web.newSlug) == 0 {
			return m.setError("type a project slug: lowercase letters, digits and '-'")
		}
		return m.linkProject(string(m.web.newSlug))
	}
	if !onNew {
		switch key {
		case "j":
			m.web.projCursor = clampIndex(m.web.projCursor+1, n)
		case "k":
			m.web.projCursor = clampIndex(m.web.projCursor-1, n)
		}
		return m, nil
	}
	switch {
	case key == "backspace":
		if l := len(m.web.newSlug); l > 0 {
			m.web.newSlug = m.web.newSlug[:l-1]
		}
	case k.Type == tea.KeyRunes:
		m.slugAppend(k.Runes)
	case k.Type == tea.KeySpace:
		m.slugAppend([]rune{' '})
	}
	return m, nil
}

// slugAppend appends typed or pasted runes to the new-project slug, dropping those a slug cannot hold.
func (m *annotModel) slugAppend(runes []rune) {
	for _, r := range runes {
		if s := slugRune(m.web.newSlug, r); s != 0 {
			m.web.newSlug = append(m.web.newSlug, s)
		}
	}
}

// linkProject saves the link of the session's labels file to the chosen remote and project slug, then resumes
// what asked for the link.
func (m annotModel) linkProject(slug string) (annotModel, tea.Cmd) {
	link := web.Link{Remote: m.web.remote, Project: slug}
	if err := web.SaveLink(m.sess.OutPath(), link); err != nil {
		return m.setError("%v", err)
	}
	m.web.link, m.web.linked = link, true
	switch m.web.after {
	case webAfterPublish:
		return m.openPublish()
	case webAfterCompare:
		return m.openCompare()
	}
	return m.webDone(false, "linked to %s", linkName(link))
}

// openPublish enters the publish confirmation. A session without a queue file cannot publish, and a session
// without a link goes through the Remote & project flow first.
func (m annotModel) openPublish() (annotModel, tea.Cmd) {
	if m.sess.QueuePath() == "" {
		return m.webDone(true, "publish needs a queue file: this session has none (it was opened from a project)")
	}
	if !m.web.linked {
		return m.startLink(webAfterPublish)
	}
	m.web.after = webAfterNone
	m.mode = annotWebConfirm
	return m, nil
}

// updateWebConfirm handles annotWebConfirm: enter or y publishes, esc or n cancels.
func (m annotModel) updateWebConfirm(key string) (annotModel, tea.Cmd) {
	switch key {
	case "esc", "n", "q":
		m.mode = annotWebMenu
		return m, nil
	case "enter", "y":
		return m.publish()
	}
	return m, nil
}

// publish pushes the session's queue, schema and proposals to the linked project as a tea.Cmd.
func (m annotModel) publish() (annotModel, tea.Cmd) {
	link := m.web.link
	in := web.PushInput{
		QueuePath:     m.sess.QueuePath(),
		SchemaPath:    m.sess.SchemaPath(),
		ProposalsPath: m.sess.ProposalsSource(),
		Project:       link.Project,
	}
	m, ctx, seq := m.beginWeb("publishing to " + linkName(link) + "…")
	m.web.busyBack = annotWebConfirm
	return m, func() tea.Msg {
		c, _, err := webClientFor(link.Remote)
		if err != nil {
			return webPublishMsg{seq: seq, err: err}
		}
		res, err := web.Push(ctx, c, in)
		return webPublishMsg{seq: seq, res: res, err: err}
	}
}

// onPublish reports the result of a push.
func (m annotModel) onPublish(msg webPublishMsg) (annotModel, tea.Cmd) {
	if msg.seq != m.web.seq {
		return m, nil
	}
	m = m.endWeb()
	if msg.err != nil {
		return m.webDone(true, "%v", msg.err)
	}
	return m.webDone(false, "%s", pushSummary(linkName(m.web.link), msg.res))
}

// pushSummary renders a PushResult as one line.
func pushSummary(name string, r web.PushResult) string {
	verb := "updated"
	if r.Created {
		verb = "created"
	}
	s := fmt.Sprintf("published %s: %s, %d items (%d new, %d updated, %d unchanged)",
		name, verb, r.Inserted+r.Updated+r.Unchanged, r.Inserted, r.Updated, r.Unchanged)
	if r.Proposals > 0 {
		s += fmt.Sprintf(", %d proposals", r.Proposals)
	}
	if n := len(r.IgnoredProposals); n > 0 {
		s += fmt.Sprintf(", %d proposal ids ignored (not in the queue)", n)
	}
	return s
}

// updateWebBusy handles annotWebBusy: esc cancels a call that can be cancelled safely (everything but a
// publish); ctrl+c still quits (handled earlier).
func (m annotModel) updateWebBusy(key string) (annotModel, tea.Cmd) {
	if key != "esc" {
		return m, nil
	}
	if m.web.busyBack == annotWebConfirm {
		return m.setStatus("publishing — please wait")
	}
	if m.web.cancel != nil {
		m.web.cancel()
		m.web.cancel = nil
	}
	m.web.seq++
	m.status = ""
	m.web.busy = ""
	m.mode = m.web.busyBack
	return m.setStatus("cancelled")
}

// webPaste handles a paste or burst of runes in the modes that take text: the remote form and the new-project
// slug.
func (m annotModel) webPaste(runes []rune) annotModel {
	switch m.mode {
	case annotWebForm:
		m.formAppend(runes)
	case annotWebProjects:
		if m.web.projCursor == len(m.web.projects) {
			m.slugAppend(runes)
		}
	}
	return m
}
