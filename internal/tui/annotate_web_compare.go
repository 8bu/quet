package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/8bu/quet/internal/annotate"
	"github.com/8bu/quet/internal/web"
)

// compareKind classifies one record by what the collaborators' labels say about it.
type compareKind int

// Record kinds of the compare mode.
const (
	// compareNone: no collaborator has a valid label for the record.
	compareNone compareKind = iota
	// compareAgreed: every remote label equals the saved local label; nothing to do.
	compareAgreed
	// compareUnanimous: at least one remote label, all equal, and no local label; A accepts these.
	compareUnanimous
	// compareDisagree: remote labels differ among themselves, or a local label is set and differs.
	compareDisagree
)

// compareState is the data of the compare mode: the pulled labels of every collaborator and the
// bulk-accept preview.
type compareState struct {
	name    string                               // "remote/project" the labels come from
	users   []string                             // collaborators with at least one label, in username order
	labels  map[string]map[string]annotate.Label // collaborator → item id → valid label
	invalid map[string]map[string]string         // collaborator → item id → why the label is invalid
	ignored int                                  // pulled labels whose id is not in the local queue

	previewing bool  // the A preview is open
	preview    []int // queue indexes the preview would accept, ascending
}

// candidate is one row of the compare list: the collaborators whose labels are equal (or one collaborator
// whose label is invalid, then invalid says why and label is unused).
type candidate struct {
	users   []string
	label   annotate.Label
	invalid string
}

// openCompare starts pulling the labels of every collaborator of the linked project as a tea.Cmd; a session
// without a link goes through the Remote & project flow first.
func (m annotModel) openCompare() (annotModel, tea.Cmd) {
	if !m.web.linked {
		return m.startLink(webAfterCompare)
	}
	m.web.after = webAfterNone
	link := m.web.link
	m, ctx, seq := m.beginWeb("pulling collaborators from " + linkName(link) + "…")
	return m, pullCmd(ctx, seq, link)
}

// pullCmd pulls every collaborator's labels of the linked project.
func pullCmd(ctx context.Context, seq int, link web.Link) tea.Cmd {
	return func() tea.Msg {
		c, _, err := webClientFor(link.Remote)
		if err != nil {
			return webPullMsg{seq: seq, err: err}
		}
		p, err := web.Pull(ctx, c, link.Project, "")
		return webPullMsg{seq: seq, pulled: p, err: err}
	}
}

// onPull enters the compare mode over the pulled labels, with the cursor on the first disagreement (else the
// first record that has a remote label), or reports why there is nothing to compare.
func (m annotModel) onPull(msg webPullMsg) (annotModel, tea.Cmd) {
	if msg.seq != m.web.seq {
		return m, nil
	}
	m = m.endWeb()
	if msg.err != nil {
		return m.webDone(true, "%v", msg.err)
	}
	cs := m.newCompare(msg.pulled)
	if len(cs.users) == 0 {
		return m.webDone(false, "no collaborator labels in %s yet", cs.name)
	}
	m.web.compare = cs
	m.mode = annotCompare
	counts := m.compareCounts()
	if i, ok := m.findCompare(0, 1, func(i int) bool { return m.compareKind(i) == compareDisagree }); ok {
		m.sess.SetCursor(i)
	} else if i, ok := m.findCompare(0, 1, m.hasRemote); ok {
		m.sess.SetCursor(i)
	}
	summary := fmt.Sprintf("pulled %d collaborators · %d to decide · %d unanimous · %d agreed",
		len(cs.users), counts.disagree, counts.unanimous, counts.agreed)
	if cs.ignored > 0 {
		summary += fmt.Sprintf(" · %d ignored (not in queue)", cs.ignored)
	}
	return m.setWebStatus(false, "%s", summary)
}

// newCompare indexes the pulled labels: the collaborators, their valid labels and invalid ones, and how many
// pulled labels name an id that is not in the local queue (those are ignored).
func (m annotModel) newCompare(p *web.Pulled) compareState {
	cs := compareState{
		name:    linkName(m.web.link),
		labels:  p.Labels,
		invalid: map[string]map[string]string{},
	}
	seen := map[string]bool{}
	for user, labels := range p.Labels {
		seen[user] = true
		for id := range labels {
			if _, ok := m.sess.IndexOf(id); !ok {
				cs.ignored++
			}
		}
	}
	for _, inv := range p.Invalid {
		seen[inv.Collaborator] = true
		if cs.invalid[inv.Collaborator] == nil {
			cs.invalid[inv.Collaborator] = map[string]string{}
		}
		if _, ok := m.sess.IndexOf(inv.ID); !ok {
			cs.ignored++
			continue
		}
		cs.invalid[inv.Collaborator][inv.ID] = inv.Error
	}
	for user := range seen {
		cs.users = append(cs.users, user)
	}
	sort.Strings(cs.users)
	return cs
}

// remoteCandidates returns the rows of record i's remote labels: equal valid labels grouped on one row, an
// invalid label on a row of its own; rows are ordered by their first collaborator in username order.
func (m annotModel) remoteCandidates(i int) []candidate {
	id := m.sess.Item(i).ID
	cs := m.web.compare
	var rows []candidate
	for _, user := range cs.users {
		if why, ok := cs.invalid[user][id]; ok {
			rows = append(rows, candidate{users: []string{user}, invalid: why})
			continue
		}
		l, ok := cs.labels[user][id]
		if !ok {
			continue
		}
		grouped := false
		for k := range rows {
			if rows[k].invalid == "" && annotate.LabelsEqual(rows[k].label, l) {
				rows[k].users = append(rows[k].users, user)
				grouped = true
				break
			}
		}
		if !grouped {
			rows = append(rows, candidate{users: []string{user}, label: l})
		}
	}
	return rows
}

// validCandidates returns the distinct valid remote labels of record i, one per row.
func (m annotModel) validCandidates(i int) []candidate {
	var out []candidate
	for _, c := range m.remoteCandidates(i) {
		if c.invalid == "" {
			out = append(out, c)
		}
	}
	return out
}

// compareKind classifies record i: disagreement is at least one remote label and either differing remote
// labels or a differing local label; unanimous is at least one remote label, all equal, local unset; agreed is
// all remote labels equal to the local label.
func (m annotModel) compareKind(i int) compareKind {
	valid := m.validCandidates(i)
	switch {
	case len(valid) == 0:
		return compareNone
	case len(valid) > 1:
		return compareDisagree
	}
	local, ok := m.sess.Label(i)
	switch {
	case !ok:
		return compareUnanimous
	case annotate.LabelsEqual(local, valid[0].label):
		return compareAgreed
	}
	return compareDisagree
}

// hasRemote reports whether any collaborator has a label (valid or invalid) for record i.
func (m annotModel) hasRemote(i int) bool {
	id := m.sess.Item(i).ID
	cs := m.web.compare
	for _, user := range cs.users {
		if _, ok := cs.labels[user][id]; ok {
			return true
		}
		if _, ok := cs.invalid[user][id]; ok {
			return true
		}
	}
	return false
}

// compareTotals are the record counts of the compare header.
type compareTotals struct{ disagree, unanimous, agreed, invalid int }

// compareCounts counts the records of each kind, and the records with an invalid remote label.
func (m annotModel) compareCounts() compareTotals {
	var t compareTotals
	for i := range m.sess.Len() {
		switch m.compareKind(i) {
		case compareDisagree:
			t.disagree++
		case compareUnanimous:
			t.unanimous++
		case compareAgreed:
			t.agreed++
		}
		for _, c := range m.remoteCandidates(i) {
			if c.invalid != "" {
				t.invalid++
				break
			}
		}
	}
	return t
}

// findCompare returns the first record from from, stepping by dir, for which pred holds.
func (m annotModel) findCompare(from, dir int, pred func(int) bool) (int, bool) {
	for i := from; i >= 0 && i < m.sess.Len(); i += dir {
		if pred(i) {
			return i, true
		}
	}
	return 0, false
}

// stepCompare moves the cursor to the previous (dir -1) or next (dir 1) record satisfying pred, or reports that
// there is none (what names the records in the message).
func (m annotModel) stepCompare(dir int, pred func(int) bool, what string) (annotModel, tea.Cmd) {
	if m.sess.Len() > 0 {
		if i, ok := m.findCompare(m.sess.Cursor()+dir, dir, pred); ok {
			m.sess.SetCursor(i)
			return m, nil
		}
	}
	if dir < 0 {
		return m.setStatus("no previous %s", what)
	}
	return m.setStatus("no next %s", what)
}

// updateCompare handles annotCompare: 1-9 pick a candidate, [ ] jump between disagreements, a/d between
// records with remote labels, A previews the unanimous records, z undoes, esc goes back.
func (m annotModel) updateCompare(key string) (annotModel, tea.Cmd) {
	if m.web.compare.previewing {
		return m.updateComparePreview(key)
	}
	disagree := func(i int) bool { return m.compareKind(i) == compareDisagree }
	switch key {
	case "esc", "q":
		m.mode = annotMain
		return m, nil
	case "?":
		return m.openHelp(), nil
	case "[":
		return m.stepCompare(-1, disagree, "disagreement")
	case "]":
		return m.stepCompare(1, disagree, "disagreement")
	case "a", "h", "left":
		return m.stepCompare(-1, m.hasRemote, "record with remote labels")
	case "d", "l", "right":
		return m.stepCompare(1, m.hasRemote, "record with remote labels")
	case "z", "Z":
		return m.undo()
	case "A":
		return m.openPreview()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.pickCandidate(int(key[0] - '0'))
	}
	return m, nil
}

// pickCandidate saves candidate n of the current record as its label. An invalid candidate, a missing one and
// one equal to the saved label change nothing.
func (m annotModel) pickCandidate(n int) (annotModel, tea.Cmd) {
	if m.sess.Len() == 0 {
		return m, nil
	}
	i := m.sess.Cursor()
	id := m.sess.Item(i).ID
	rows := m.remoteCandidates(i)
	if n > len(rows) {
		return m.setError("no candidate %d: record %s has %d", n, id, len(rows))
	}
	c := rows[n-1]
	who := strings.Join(c.users, ", ")
	if c.invalid != "" {
		return m.setError("candidate %d (%s) is invalid and cannot be picked: %s", n, who, squeeze(c.invalid))
	}
	if local, ok := m.sess.Label(i); ok && annotate.LabelsEqual(local, c.label) {
		return m.setStatus("record %s already has %s's label", id, who)
	}
	if err := m.sess.SaveLabel(i, c.label); err != nil {
		return m.setError("%s: %v", who, err)
	}
	return m.setStatus("saved %s's label for %s (z undoes)", who, id)
}

// unanimousRecords returns the queue indexes of the unanimous records, ascending.
func (m annotModel) unanimousRecords() []int {
	var idx []int
	for i := range m.sess.Len() {
		if m.compareKind(i) == compareUnanimous {
			idx = append(idx, i)
		}
	}
	return idx
}

// openPreview opens the bulk-accept preview over the unanimous records.
func (m annotModel) openPreview() (annotModel, tea.Cmd) {
	idx := m.unanimousRecords()
	if len(idx) == 0 {
		return m.setStatus("no unanimous records to accept")
	}
	m.web.compare.preview = idx
	m.web.compare.previewing = true
	return m, nil
}

// updateComparePreview handles the bulk-accept preview: enter applies, esc cancels.
func (m annotModel) updateComparePreview(key string) (annotModel, tea.Cmd) {
	switch key {
	case "esc", "q", "n":
		m.web.compare.previewing = false
		m.web.compare.preview = nil
		return m, nil
	case "enter", "y":
		return m.acceptUnanimous()
	}
	return m, nil
}

// acceptUnanimous saves the previewed records' remote labels with one write and one undo entry.
func (m annotModel) acceptUnanimous() (annotModel, tea.Cmd) {
	idx := m.web.compare.preview
	m.web.compare.previewing = false
	m.web.compare.preview = nil
	labels := make(map[int]annotate.Label, len(idx))
	for _, i := range idx {
		valid := m.validCandidates(i)
		if len(valid) != 1 {
			return m.setError("record %s changed since the preview; nothing saved", m.sess.Item(i).ID)
		}
		labels[i] = valid[0].label
	}
	if err := m.sess.SaveLabels(labels); err != nil {
		return m.setError("accept unanimous: %v — nothing changed", err)
	}
	return m.setStatus("accepted %d unanimous labels (z undoes them all)", len(labels))
}
