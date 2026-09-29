package tui

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/8bu/quet/internal/checks"
	"github.com/8bu/quet/internal/export"
	"github.com/8bu/quet/internal/review"
)

// searchLimit caps how many search results the TUI keeps.
const searchLimit = 200

func clampIndex(i, n int) int {
	if n <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// filterRow is one selectable row of the filter list.
type filterRow struct {
	label string
	f     review.Filter
	count int
}

// facetList is the filter/facet list shown in the corpus panel: every filterable
// value with its count.
type facetList struct {
	rows   []filterRow
	cursor int
}

func (f *facetList) clampCursor() {
	f.cursor = clampIndex(f.cursor, len(f.rows))
}

func (f *facetList) move(delta int) {
	f.cursor = clampIndex(f.cursor+delta, len(f.rows))
}

func (f facetList) current() (filterRow, bool) {
	if f.cursor < 0 || f.cursor >= len(f.rows) {
		return filterRow{}, false
	}
	return f.rows[f.cursor], true
}

// selectActive places the cursor on the row of the active filter, if present.
func (f *facetList) selectActive(active review.Filter) {
	for i, r := range f.rows {
		if r.f == active {
			f.cursor = i
			return
		}
	}
}

// facetCounts maps facet values to their counts.
func facetCounts(facets []review.Facet) map[string]int {
	out := make(map[string]int, len(facets))
	for _, f := range facets {
		out[f.Value] = f.Count
	}
	return out
}

// buildFilterRows lists every filter value with its count: statuses, edited,
// then auto/manual/suggested flags, sources and batches.
func buildFilterRows(s *review.Session) []filterRow {
	rows := make([]filterRow, 0, 32)
	c := s.Counts()
	add := func(label, spec string, count int) {
		f, err := review.ParseFilter(spec)
		if err != nil {
			return
		}
		rows = append(rows, filterRow{label: label, f: f, count: count})
	}

	add("all", "all", s.Len())
	add("unreviewed", "unreviewed", c.Unreviewed)
	add("approved", "approved", c.Approved)
	add("rejected", "rejected", c.Rejected)
	add("needs_review", "needs_review", c.NeedsReview)
	add("edited", "edited", c.Edited)

	auto := facetCounts(s.AutoFlagFacets())
	for _, name := range checks.AllFlags {
		add("auto:"+name, "auto:"+name, auto[name])
	}
	for _, name := range slices.Sorted(maps.Keys(auto)) {
		if !slices.Contains(checks.AllFlags, name) {
			add("auto:"+name, "auto:"+name, auto[name])
		}
	}

	manual := facetCounts(s.ManualFacets())
	defined := make(map[string]bool, len(s.FlagDefs))
	for _, fd := range s.FlagDefs {
		if defined[fd.Name] {
			continue
		}
		defined[fd.Name] = true
		add("manual:"+fd.Name, "manual:"+fd.Name, manual[fd.Name])
	}
	for _, name := range slices.Sorted(maps.Keys(manual)) {
		if !defined[name] {
			add("manual:"+name, "manual:"+name, manual[name])
		}
	}

	for _, f := range s.SuggestedFacets() {
		add("suggested:"+f.Value, "suggested:"+f.Value, f.Count)
	}
	for _, f := range s.Sources() {
		add("source:"+f.Value, "source:"+f.Value, f.Count)
	}
	for _, f := range s.Batches() {
		add("batch:"+f.Value, "batch:"+f.Value, f.Count)
	}
	return rows
}

// filterPicker selects a filter value.
type filterPicker struct {
	cursor int
}

func (p filterPicker) move(delta, n int) filterPicker {
	return filterPicker{cursor: clampIndex(p.cursor+delta, n)}
}

func (p *filterPicker) clamp(n int) { p.cursor = clampIndex(p.cursor, n) }

// flagRow is one selectable manual flag.
type flagRow struct {
	name  string
	desc  string
	count int
}

// flagsPicker toggles the manual flags of the current record.
type flagsPicker struct {
	rows   []flagRow
	cursor int
	sel    map[string]bool
}

// open loads every defined manual flag plus any flag already on the record.
// It reports whether there is anything to show.
func (p *flagsPicker) open(s *review.Session) bool {
	cur := s.Current()
	if cur < 0 {
		return false
	}
	counts := facetCounts(s.ManualFacets())
	seen := make(map[string]bool)
	p.rows = nil
	for _, fd := range s.FlagDefs {
		if seen[fd.Name] {
			continue
		}
		seen[fd.Name] = true
		p.rows = append(p.rows, flagRow{name: fd.Name, desc: fd.Description, count: counts[fd.Name]})
	}
	for _, f := range s.ManualFacets() {
		if seen[f.Value] {
			continue
		}
		seen[f.Value] = true
		p.rows = append(p.rows, flagRow{name: f.Value, count: f.Count})
	}
	p.sel = make(map[string]bool, len(p.rows))
	for _, name := range s.State(cur).ManualFlags {
		p.sel[name] = true
		if seen[name] {
			continue
		}
		seen[name] = true
		p.rows = append(p.rows, flagRow{name: name})
	}
	p.cursor = 0
	return len(p.rows) > 0
}

func (p *flagsPicker) move(delta int) { p.cursor = clampIndex(p.cursor+delta, len(p.rows)) }

func (p *flagsPicker) toggle() {
	r, ok := p.current()
	if !ok {
		return
	}
	if p.sel[r.name] {
		delete(p.sel, r.name)
		return
	}
	p.sel[r.name] = true
}

func (p flagsPicker) current() (flagRow, bool) {
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return flagRow{}, false
	}
	return p.rows[p.cursor], true
}

// selectedNames returns the toggled flag names, sorted.
func (p flagsPicker) selectedNames() []string {
	names := make([]string, 0, len(p.sel))
	for name, on := range p.sel {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// searchPicker searches the corpus live.
type searchPicker struct {
	input   textinput.Model
	results []int
	cursor  int
}

func newSearchPicker() searchPicker {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "text or metadata"
	return searchPicker{input: ti}
}

func (p *searchPicker) open() {
	p.input.SetValue("")
	p.input.Focus()
	p.results = nil
	p.cursor = 0
}

func (p *searchPicker) refresh(s *review.Session) {
	if strings.TrimSpace(p.input.Value()) == "" {
		p.results = nil
	} else {
		p.results = s.Search(p.input.Value(), searchLimit)
	}
	p.cursor = 0
}

func (p *searchPicker) move(delta int) { p.cursor = clampIndex(p.cursor+delta, len(p.results)) }

func (p searchPicker) selected() (int, bool) {
	if p.cursor < 0 || p.cursor >= len(p.results) {
		return 0, false
	}
	return p.results[p.cursor], true
}

// dupPicker lists the duplicate group of the current record.
type dupPicker struct {
	results []int
	cursor  int
}

func (p *dupPicker) open(s *review.Session) {
	cur := s.Current()
	if cur < 0 {
		p.results = nil
	} else {
		p.results = s.Duplicates(cur)
	}
	p.cursor = 0
}

func (p *dupPicker) move(delta int) { p.cursor = clampIndex(p.cursor+delta, len(p.results)) }

func (p dupPicker) selected() (int, bool) {
	if p.cursor < 0 || p.cursor >= len(p.results) {
		return 0, false
	}
	return p.results[p.cursor], true
}

// exportPicker picks an export preset and output path.
type exportPicker struct {
	presets     []export.Preset
	cursor      int
	path        textinput.Model
	pathFocus   bool
	pathTouched bool
	pending     bool
}

func newExportPicker() exportPicker {
	ti := textinput.New()
	ti.Prompt = "path: "
	return exportPicker{path: ti}
}

func (p *exportPicker) open(s *review.Session) {
	p.presets = export.Presets()
	p.cursor = 0
	p.pending = false
	p.pathFocus = false
	p.pathTouched = false
	p.path.Blur()
	p.syncPath(s)
}

func (p exportPicker) current() (export.Preset, bool) {
	if p.cursor < 0 || p.cursor >= len(p.presets) {
		return export.Preset{}, false
	}
	return p.presets[p.cursor], true
}

// movePreset selects another preset and refreshes the suggested path, unless the
// user already typed one.
func (p *exportPicker) movePreset(s *review.Session, delta int) {
	p.cursor = clampIndex(p.cursor+delta, len(p.presets))
	p.syncPath(s)
}

func (p *exportPicker) syncPath(s *review.Session) {
	if p.pathTouched {
		return
	}
	preset, ok := p.current()
	if !ok {
		return
	}
	p.path.SetValue(export.DefaultPath(corpusPath(s), preset.Suffix))
}

func (p exportPicker) outputPath() string { return strings.TrimSpace(p.path.Value()) }

// corpusPath returns the source corpus path of the session.
func corpusPath(s *review.Session) string {
	if s == nil || s.Corpus == nil {
		return ""
	}
	return s.Corpus.Path
}

// palettePicker filters and runs commands.
type palettePicker struct {
	input  textinput.Model
	cursor int
}

func newPalettePicker() palettePicker {
	ti := textinput.New()
	ti.Prompt = ": "
	ti.Placeholder = "command"
	return palettePicker{input: ti}
}

func (p *palettePicker) open() {
	p.input.SetValue("")
	p.input.Focus()
	p.cursor = 0
}

func (p *palettePicker) move(delta, n int) { p.cursor = clampIndex(p.cursor+delta, n) }

// listRows renders the windowed filter list.
func (m model) filterListRows(maxRows, cw int) ([]string, int) {
	return listRows(len(m.facets.rows), m.filter.cursor, maxRows, func(i int) string {
		r := m.facets.rows[i]
		return truncateLine(fmt.Sprintf("%s  %d", r.label, r.count), cw-2)
	})
}

// flagListRows renders the windowed manual flag list.
func (m model) flagListRows(maxRows, cw int) ([]string, int) {
	return listRows(len(m.flags.rows), m.flags.cursor, maxRows, func(i int) string {
		r := m.flags.rows[i]
		mark := "[ ]"
		if m.flags.sel[r.name] {
			mark = "[✓]"
		}
		label := r.name
		if r.count > 0 {
			label = fmt.Sprintf("%s  %d", r.name, r.count)
		}
		return truncateLine(mark+" "+label, cw-2)
	})
}

// recordPreview renders "index  text" for a record.
func (m model) recordPreview(i, cw int) string {
	r := m.sess.Record(i)
	if r == nil {
		return truncateLine(fmt.Sprintf("#%d", i+1), cw-2)
	}
	return truncateLine(fmt.Sprintf("#%-5d %s", i+1, snippet(r.Text, cw-10)), cw-2)
}

// searchListRows renders the windowed search result list.
func (m model) searchListRows(maxRows, cw int) ([]string, int) {
	return listRows(len(m.search.results), m.search.cursor, maxRows, func(i int) string {
		return m.recordPreview(m.search.results[i], cw)
	})
}

// dupListRows renders the windowed duplicate list.
func (m model) dupListRows(maxRows, cw int) ([]string, int) {
	return listRows(len(m.dups.results), m.dups.cursor, maxRows, func(i int) string {
		return m.recordPreview(m.dups.results[i], cw)
	})
}

// exportListRows renders the windowed export preset list.
func (m model) exportListRows(maxRows, cw int) ([]string, int) {
	return listRows(len(m.exp.presets), m.exp.cursor, maxRows, func(i int) string {
		p := m.exp.presets[i]
		return truncateLine(fmt.Sprintf("%s  %s", p.Label, p.Suffix), cw-2)
	})
}
