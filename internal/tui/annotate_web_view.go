package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/web"
)

// webMaxWidth caps the width of the Web overlay panels.
const webMaxWidth = 76

// webModeBody draws the body of the web modes (the menu, the link flow, publish and the busy screen) as one
// centred overlay panel.
func (m annotModel) webModeBody(w, h int) string {
	ow := min(w, webMaxWidth)
	iw, ih := ow-2, h-2
	var title string
	var rows []string
	sel := -1
	switch m.mode {
	case annotWebRemotes:
		title = "Remote"
		rows, sel = m.webRemotesContent(iw, ih)
	case annotWebForm:
		title = "Add remote"
		rows = m.webFormContent(iw)
	case annotWebProjects:
		title = "Project"
		rows, sel = m.webProjectsContent(iw, ih)
	case annotWebConfirm:
		title = "Publish"
		rows = m.webConfirmContent(iw)
	case annotWebBusy:
		title = "Web"
		rows = wrapRows([]string{m.web.busy}, max(iw, 1))
	default:
		title = "Web"
		rows, sel = m.webMenuContent(iw)
	}
	return overlayBox(title, w, h, ow, rows, sel)
}

// webMenuContent renders the Web menu: the link status, the four actions and the last result in full.
func (m annotModel) webMenuContent(iw int) ([]string, int) {
	iw = max(iw, 1)
	link := styleMuted.Render("Link: not linked")
	if m.web.linked {
		link = "Link: " + styleAccent.Render(linkName(m.web.link))
	}
	rows := []string{truncateLine(link, iw), ""}
	items := []string{"Remote & project…  (r)", "Publish  (p)", "Compare collaborators  (c)", "Log in again  (l)"}
	list, sel := listRows(len(items), m.web.cursor, len(items), func(i int) string { return items[i] })
	rows, sel = appendList(rows, list, sel)
	if m.web.note != "" {
		style := styleStatus
		if m.web.noteErr {
			style = styleError
		}
		rows = append(rows, "")
		for _, row := range wrapRows([]string{m.web.note}, iw) {
			rows = append(rows, style.Render(row))
		}
	}
	return rows, sel
}

// webRemotesContent renders the remote picker: the saved remotes, then the "add a remote" row.
func (m annotModel) webRemotesContent(iw, maxRows int) ([]string, int) {
	iw, maxRows = max(iw, 1), max(maxRows, 1)
	remotes := m.web.remotes.List
	rows := []string{styleMuted.Render("Pick a remote, or add one")}
	list, sel := listRows(len(remotes)+1, m.web.remoteCursor, max(maxRows-1, 1), func(i int) string {
		if i == len(remotes) {
			return styleAccent.Render("+ add a remote…")
		}
		r := remotes[i]
		creds := "no credentials"
		switch {
		case r.OAuth != nil:
			creds = "browser login"
		case r.ClientID != "" && r.ClientSecret != "":
			creds = "credentials set"
		}
		line := fmt.Sprintf("%s  %s  %s", r.Name, r.URL, styleMuted.Render(creds))
		if r.Name == m.web.remotes.Default {
			line += "  " + styleMuted.Render("(default)")
		}
		return truncateLine(line, iw-2)
	})
	return appendList(rows, list, sel)
}

// webFormContent renders the add-remote form: one line per field (the secret masked, the focused field with a
// caret), the login choice and a hint about what happens on save. The credential fields are muted while the
// browser login is chosen.
func (m annotModel) webFormContent(iw int) []string {
	iw = max(iw, 1)
	f := m.web.form
	var rows []string
	for k := range formFields {
		label := fmt.Sprintf("%-13s", formLabels[k])
		var value string
		switch k {
		case formAuth:
			value = "(•) Browser  ( ) Service token"
			if f.token {
				value = "( ) Browser  (•) Service token"
			}
		case formSecret:
			value = strings.Repeat("•", len(f.fields[k]))
		default:
			value = string(f.fields[k])
		}
		if k == f.focus {
			label = styleAccent.Render(label)
			if k != formAuth {
				value += "▏"
			}
		} else if !f.active(k) {
			label, value = styleMuted.Render(label), styleMuted.Render(value)
		}
		rows = append(rows, truncateLine(label+" "+value, iw))
	}
	hint := "Enter opens your browser to log in with Cloudflare Access. The remote is saved when you finish."
	if f.token {
		hint = "A service token is for CI. Enter saves the remote with the token."
	}
	hint += " A name that exists replaces that remote. Saved to " + web.RemotesPath() + " (mode 0600)."
	rows = append(rows, "")
	rows = append(rows, wrapRows([]string{hint}, iw)...)
	for i := len(rows) - 1; i >= 0 && rows[i] != ""; i-- {
		rows[i] = styleMuted.Render(rows[i])
	}
	return rows
}

// webProjectsContent renders the project picker: the server's projects, then the "new project" row with its
// editable slug.
func (m annotModel) webProjectsContent(iw, maxRows int) ([]string, int) {
	iw, maxRows = max(iw, 1), max(maxRows, 1)
	projects := m.web.projects
	rows := []string{styleMuted.Render("Projects on " + m.web.remote + " — pick one, or create a new one")}
	list, sel := listRows(len(projects)+1, m.web.projCursor, max(maxRows-1, 1), func(i int) string {
		if i == len(projects) {
			slug := string(m.web.newSlug)
			if i == m.web.projCursor {
				slug += "▏"
			}
			return truncateLine(styleAccent.Render("new project: ")+slug, iw-2)
		}
		p := projects[i]
		name := ""
		if p.Name != "" && p.Name != p.Slug {
			name = " " + p.Name
		}
		stats := fmt.Sprintf("%d items · %d collaborators", p.Items, p.Collaborators)
		return truncateLine(p.Slug+name+"  "+styleMuted.Render(stats), iw-2)
	})
	return appendList(rows, list, sel)
}

// webConfirmContent renders the publish confirmation: the target, the files and what happens to proposals.
func (m annotModel) webConfirmContent(iw int) []string {
	iw = max(iw, 1)
	proposals := "none — the server's proposals stay untouched"
	if p := m.sess.ProposalsSource(); p != "" {
		proposals = filepath.Base(p) + " — replaces the server's proposals"
	}
	lines := []string{
		"Publish to " + styleAccent.Render(linkName(m.web.link)),
		"",
		fmt.Sprintf("Queue:     %s (%d records)", filepath.Base(m.sess.QueuePath()), m.sess.Len()),
		"Schema:    " + filepath.Base(m.sess.SchemaPath()),
		"Proposals: " + proposals,
		"",
		styleMuted.Render("Creates the project or updates its schema and items."),
		styleMuted.Render("enter publishes · esc cancels"),
	}
	return wrapRows(lines, iw)
}

// compareBody draws the compare mode: a header with the counts, then the record and its candidates; the
// bulk-accept preview replaces it as an overlay.
func (m annotModel) compareBody(w, h int) string {
	if m.web.compare.previewing {
		ow := min(w, webMaxWidth)
		return overlayBox("Accept unanimous", w, h, ow, m.previewContent(ow-2, h-2), -1)
	}
	header := m.compareHeader()
	headH := len(header) + 2
	if h < headH+3 {
		return m.compareBox(w, h)
	}
	return box("Quet", false, w, headH, header, -1) + "\n" + m.compareBox(w, h-headH)
}

// compareHeader renders the compare header rows: where the labels come from and the counts per record kind.
func (m annotModel) compareHeader() []string {
	cs := m.web.compare
	t := m.compareCounts()
	title := styleTitle.Render("Quet — compare") + " · " + cs.name + " · " + strings.Join(cs.users, ", ")
	cur := 0
	if m.sess.Len() > 0 {
		cur = m.sess.Cursor() + 1
	}
	counts := fmt.Sprintf("%d / %d  ·  to decide %d · unanimous %d · agreed %d", cur, m.sess.Len(), t.disagree, t.unanimous, t.agreed)
	if t.invalid > 0 {
		counts += fmt.Sprintf(" · ⚠ invalid %d", t.invalid)
	}
	if cs.ignored > 0 {
		counts += fmt.Sprintf(" · ignored %d (not in queue)", cs.ignored)
	}
	return []string{title, counts}
}

// compareKindBadge names a record kind in the compare box title.
func compareKindBadge(k compareKind) string {
	switch k {
	case compareDisagree:
		return "to decide"
	case compareUnanimous:
		return "unanimous"
	case compareAgreed:
		return "✓ agreed"
	}
	return "no remote labels"
}

// compareBox draws the record panel of the compare mode.
func (m annotModel) compareBox(w, h int) string {
	if m.sess.Len() == 0 {
		return box("Compare", true, w, h, []string{"no records"}, -1)
	}
	iw, ih := w-2, h-2
	if w < 4 || h < 3 {
		iw, ih = w, h
	}
	i := m.sess.Cursor()
	title := fmt.Sprintf("Compare %d / %d · %s", i+1, m.sess.Len(), compareKindBadge(m.compareKind(i)))
	return box(title, true, w, h, m.compareRows(i, max(iw, 1), ih), -1)
}

// compareRows renders record i for the compare box: its text, the Local row and one row per remote candidate.
// Each candidate shows the record text with its spans highlighted; when that does not fit in ih rows the
// candidates list their spans in text form instead.
func (m annotModel) compareRows(i, iw, ih int) []string {
	item := m.sess.Item(i)
	runes := []rune(item.Text)
	text, _ := selectionRows(runes, iw, nil, -1, lipgloss.Style{}, -1)
	head := append(text, styleMuted.Render("ID: "+item.ID), "")
	local, hasLocal := m.sess.Label(i)
	cands := m.remoteCandidates(i)

	build := func(full bool) []string {
		rows := append([]string(nil), head...)
		if hasLocal {
			rows = append(rows, m.candidateRows("L", "Local", "", local, "", runes, iw, full)...)
		} else {
			rows = append(rows, styleMuted.Render(truncateLine(" L Local · not labelled", iw)))
		}
		for k, c := range cands {
			num := "·"
			if k < 9 && c.invalid == "" {
				num = styleAccent.Render(fmt.Sprint(k + 1))
			}
			tag := ""
			if hasLocal && c.invalid == "" && annotate.LabelsEqual(local, c.label) {
				tag = styleMuted.Render("  (same as Local)")
			}
			rows = append(rows, m.candidateRows(num, strings.Join(c.users, ", "), tag, c.label, c.invalid, runes, iw, full)...)
		}
		return rows
	}
	if rows := build(true); len(rows) <= ih {
		return rows
	}
	return build(false)
}

// candidateRows renders one candidate: a header (number, who, status and type, note), then in full mode the
// record text with the label's spans in each span's colour, then the span fields (always in compact mode,
// otherwise only when the schema has several fields or span statuses). An invalid candidate is one ⚠ row.
func (m annotModel) candidateRows(num, who, tag string, l annotate.Label, invalid string, runes []rune, iw int, full bool) []string {
	if invalid != "" {
		line := fmt.Sprintf(" %s ⚠ %s — invalid: %s", styleError.Render("·"), who, squeeze(invalid))
		var rows []string
		for _, row := range wrapRows([]string{line}, iw) {
			rows = append(rows, styleError.Render(row))
		}
		return rows
	}
	headLine := fmt.Sprintf(" %s %s · %s%s", num, styleTitle.Render(who), labelHead(l), tag)
	rows := wrapRows([]string{headLine}, iw)
	if m.sess.Schema().NullLabel(l.Status) {
		return rows
	}
	const indent = "    "
	if full {
		var marks []spanMark
		for k, sp := range m.sess.Schema().Spans {
			if t := l.Spans[sp.Name]; t != nil && t.Start >= 0 && t.End > t.Start && t.End <= len(runes) {
				marks = append(marks, spanMark{t.Start, t.End - 1, spanStyle(k)})
			}
		}
		text, _ := selectionRows(runes, max(iw-len(indent), 1), marks, -1, lipgloss.Style{}, -1)
		for _, row := range text {
			rows = append(rows, indent+row)
		}
	}
	if !full || m.spanUI() {
		for k, sp := range m.sess.Schema().Spans {
			value := spanValue(l.Spans[sp.Name])
			if st := l.SpanStatus[sp.Name]; st != "" && l.Spans[sp.Name] != nil {
				value += " · " + st
			}
			name := capitalize(sp.Name) + ":"
			if m.multiSpan() {
				name = spanStyle(k).Render(name)
			}
			rows = append(rows, truncateLine(indent+name+" "+value, iw))
		}
	}
	return rows
}

// labelHead summarises a label on one line: its status, type and note.
func labelHead(l annotate.Label) string {
	s := statusBadge(l.Status)
	if l.Type != nil {
		s += " · " + *l.Type
	}
	if l.Note != "" {
		s += " · note: " + snippet(l.Note, 40)
	}
	return s
}

// previewContent renders the bulk-accept preview: how many records it accepts and the first rows (id, status,
// type, spans).
func (m annotModel) previewContent(iw, ih int) []string {
	iw, ih = max(iw, 1), max(ih, 1)
	idx := m.web.compare.preview
	rows := []string{
		styleTitle.Render(fmt.Sprintf("Accept %d unanimous records", len(idx))),
		styleMuted.Render("Every collaborator who labelled them agrees and Local has no label."),
		"",
	}
	footer := []string{"", styleMuted.Render("enter accepts all (one z undoes the batch) · esc cancels")}
	avail := max(ih-len(rows)-len(footer), 1)
	show := len(idx)
	if show > avail {
		show = max(avail-1, 1) // leave a row for "… and N more"
	}
	idW := 2
	for _, i := range idx[:show] {
		idW = max(idW, len([]rune(m.sess.Item(i).ID)))
	}
	for _, i := range idx[:show] {
		valid := m.validCandidates(i)
		if len(valid) == 0 {
			continue
		}
		rows = append(rows, truncateLine(fmt.Sprintf("%-*s  %s", idW, m.sess.Item(i).ID, m.previewLabel(valid[0].label)), iw))
	}
	if show < len(idx) {
		rows = append(rows, styleMuted.Render(fmt.Sprintf("… and %d more", len(idx)-show)))
	}
	return append(rows, footer...)
}

// previewLabel renders a label for a preview row: status, type and the non-null span fields.
func (m annotModel) previewLabel(l annotate.Label) string {
	parts := []string{l.Status}
	if l.Type != nil {
		parts = append(parts, *l.Type)
	}
	for _, sp := range m.sess.Schema().Spans {
		if t := l.Spans[sp.Name]; t != nil {
			parts = append(parts, fmt.Sprintf("%s=%q", sp.Name, t.Text))
		}
	}
	return strings.Join(parts, " · ")
}

// webFooterItems returns the footer shortcuts of the web and compare modes.
func (m annotModel) webFooterItems() []string {
	switch m.mode {
	case annotWebRemotes:
		return []string{"j/k move", "enter pick", "n add remote", "esc back"}
	case annotWebForm:
		return []string{"type to edit", "tab/↑/↓ field", "space/←/→ browser or token", "enter next/save", "esc back"}
	case annotWebProjects:
		return []string{"↑/↓ move", "type to name a new project", "enter link", "esc back"}
	case annotWebConfirm:
		return []string{"enter publish", "esc cancel"}
	case annotWebBusy:
		return []string{"esc cancel", "ctrl+c quit"}
	case annotCompare:
		if m.web.compare.previewing {
			return []string{fmt.Sprintf("enter accept %d", len(m.web.compare.preview)), "esc cancel"}
		}
		return []string{"1-9 pick", "[/] prev/next disagreement", "a/d prev/next with remote labels",
			"A accept unanimous", "z undo", "? help", "esc back"}
	}
	return []string{"j/k move", "enter choose", "r remote & project", "p publish", "c compare", "l log in again", "esc close"}
}

// webHelpGroups returns the help groups of the web integration: the menu key and the compare keys.
func webHelpGroups() []helpGroup {
	return []helpGroup{
		{"Web", []helpItem{
			{"w", "Web menu: link, publish, compare, log in again"},
		}},
		{"Compare", []helpItem{
			{"1-9", "Pick candidate N"},
			{"[/]", "Prev/next disagreement"},
			{"a/d", "Prev/next with remote labels"},
			{"A", "Preview unanimous; enter applies"},
			{"z", "Undo; one for a batch"},
			{"esc", "Back"},
		}},
	}
}
