package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/8bu/quet/internal/config"
	"github.com/8bu/quet/internal/review"
)

// stdin is what `quet edit --text-file -` reads; tests replace it.
var stdin io.Reader = os.Stdin

// recordOut is the JSON shape of one record in the scripting commands' output.
type recordOut struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Edited         bool            `json:"edited"`
	Text           string          `json:"text"`
	OriginalText   *string         `json:"original_text,omitempty"`
	ManualFlags    []string        `json:"manual_flags"`
	SuggestedFlags []string        `json:"suggested_flags"`
	Diagnostics    []diagnosticOut `json:"diagnostics"`
	Source         string          `json:"source,omitempty"`
	Batch          string          `json:"batch,omitempty"`
}

// diagnosticOut is the JSON shape of one diagnostic of a record.
type diagnosticOut struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// newRecordOut describes record i of s as it would be exported now. Arrays are never nil.
func newRecordOut(s *review.Session, i int) recordOut {
	rec := s.Record(i)
	state := s.State(i)
	diags := s.Diagnostics(i)
	out := recordOut{
		ID:             rec.ID,
		Status:         string(state.EffectiveStatus()),
		Edited:         state.Edited(),
		Text:           s.FinalText(i),
		ManualFlags:    append([]string{}, state.ManualFlags...),
		SuggestedFlags: s.SuggestedFlags(i),
		Diagnostics:    make([]diagnosticOut, 0, len(diags)),
		Source:         rec.Source,
		Batch:          rec.Batch,
	}
	if out.Edited {
		original := rec.Text
		out.OriginalText = &original
	}
	for _, d := range diags {
		out.Diagnostics = append(out.Diagnostics, diagnosticOut{Name: d.Name, Detail: d.Detail})
	}
	return out
}

// newEncoder returns a JSON encoder writing one compact object per line without HTML escaping.
func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

// resolveIDs maps record IDs to corpus indices, failing on the first unknown ID.
func resolveIDs(s *review.Session, ids []string) ([]int, error) {
	indices := make([]int, 0, len(ids))
	for _, id := range ids {
		i, ok := s.Index(id)
		if !ok {
			return nil, fmt.Errorf("unknown record id %q", id)
		}
		indices = append(indices, i)
	}
	return indices, nil
}

// runList prints the records matching --filter, in corpus order, up to --limit.
func runList(cmd command, stdout, stderr io.Writer) int {
	var filter review.Filter
	if cmd.hasFilter {
		f, err := review.ParseFilter(cmd.filter)
		if err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 2
		}
		filter = f
	}

	s, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer s.Close()
	if cmd.hasFilter {
		s.SetFilter(filter)
	}

	view := s.View()
	if cmd.hasLimit && cmd.limit > 0 && cmd.limit < len(view) {
		view = view[:cmd.limit]
	}
	out := bufio.NewWriter(stdout)
	enc := newEncoder(out)
	for _, i := range view {
		if cmd.json {
			err = enc.Encode(newRecordOut(s, i))
		} else {
			_, err = fmt.Fprintf(out, "%s\t%s\t%s\n", s.Record(i).ID, s.State(i).EffectiveStatus(), singleLine(s.FinalText(i)))
		}
		if err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
	}
	if err := out.Flush(); err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// singleLine collapses every run of whitespace in text, newlines included, to one space.
func singleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// runShow prints one record in full.
func runShow(cmd command, stdout, stderr io.Writer) int {
	s, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer s.Close()

	indices, err := resolveIDs(s, cmd.ids)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	rec := newRecordOut(s, indices[0])
	if cmd.json {
		err = newEncoder(stdout).Encode(rec)
	} else {
		err = writeShow(stdout, rec)
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// writeShow prints rec as `key: value` lines, the text last.
func writeShow(w io.Writer, rec recordOut) error {
	out := bufio.NewWriter(w)
	showField(out, "id", rec.ID)
	showField(out, "status", rec.Status)
	showField(out, "edited", fmt.Sprint(rec.Edited))
	if rec.Source != "" {
		showField(out, "source", rec.Source)
	}
	if rec.Batch != "" {
		showField(out, "batch", rec.Batch)
	}
	showField(out, "manual_flags", listValue(rec.ManualFlags))
	showField(out, "suggested_flags", listValue(rec.SuggestedFlags))
	for _, d := range rec.Diagnostics {
		showField(out, "diagnostic", d.Name+": "+d.Detail)
	}
	if rec.OriginalText != nil {
		showField(out, "original_text", *rec.OriginalText)
	}
	showField(out, "text", rec.Text)
	return out.Flush()
}

// showField writes one `key: value` line; continuation lines of a multi-line value are indented.
func showField(w *bufio.Writer, key, value string) {
	w.WriteString(key)
	w.WriteString(": ")
	w.WriteString(strings.ReplaceAll(value, "\n", "\n  "))
	w.WriteByte('\n')
}

// listValue joins names for human output, "-" when there are none.
func listValue(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ", ")
}

// recordChange is one scripted write. check validates every target record before
// anything is written, apply changes the current record, summary is its human output line.
type recordChange struct {
	check   func(s *review.Session, indices []int) error
	apply   func(s *review.Session, i int) error
	summary func(s *review.Session, i int) string
}

// runWrite resolves cmd.ids, runs change.check, then applies the change to each record in
// argument order and reports each one as it is written.
func runWrite(cmd command, stdout, stderr io.Writer, change recordChange) int {
	s, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer s.Close()

	indices, err := resolveIDs(s, cmd.ids)
	if err == nil && change.check != nil {
		err = change.check(s, indices)
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}

	enc := newEncoder(stdout)
	for _, i := range indices {
		s.Goto(i)
		if err := change.apply(s, i); err != nil {
			fmt.Fprintf(stderr, "quet: %s: %v\n", s.Record(i).ID, err)
			return 1
		}
		if cmd.json {
			err = enc.Encode(newRecordOut(s, i))
		} else {
			_, err = fmt.Fprintln(stdout, change.summary(s, i))
		}
		if err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
	}
	return 0
}

// runSet sets the review status of each record.
func runSet(cmd command, stdout, stderr io.Writer) int {
	status, err := review.ParseStatus(cmd.status)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 2
	}
	return runWrite(cmd, stdout, stderr, recordChange{
		apply: func(s *review.Session, _ int) error {
			_, err := s.SetStatus(status)
			return err
		},
		summary: func(s *review.Session, i int) string {
			return fmt.Sprintf("%s %s", s.State(i).EffectiveStatus(), s.Record(i).ID)
		},
	})
}

// runFlag adds and removes manual flags (names from flags.yaml) on each record.
func runFlag(cmd command, stdout, stderr io.Writer) int {
	return runWrite(cmd, stdout, stderr, recordChange{
		check: func(s *review.Session, _ []int) error {
			return checkManualFlags(s, slices.Concat(cmd.add, cmd.remove))
		},
		apply: func(s *review.Session, i int) error {
			return s.SetManualFlags(applyDelta(s.State(i).ManualFlags, cmd.add, cmd.remove))
		},
		summary: func(s *review.Session, i int) string {
			return fmt.Sprintf("manual_flags=%s %s", strings.Join(s.State(i).ManualFlags, ","), s.Record(i).ID)
		},
	})
}

// checkManualFlags rejects names that are not defined in the session's flags file.
func checkManualFlags(s *review.Session, names []string) error {
	if s.FlagsPath == "" {
		return fmt.Errorf("no flags file: manual flags come from flags.yaml (run quet init, or pass --flags-file)")
	}
	for _, name := range names {
		if !slices.ContainsFunc(s.FlagDefs, func(def config.FlagDef) bool { return def.Name == name }) {
			return fmt.Errorf("unknown manual flag %q (valid: %s)", name, flagNames(s.FlagDefs))
		}
	}
	return nil
}

// flagNames lists the defined manual flag names, comma separated.
func flagNames(defs []config.FlagDef) string {
	if len(defs) == 0 {
		return "none defined"
	}
	names := make([]string, len(defs))
	for i, def := range defs {
		names[i] = def.Name
	}
	return strings.Join(names, ", ")
}

// runSuggest adds and removes suggested flags (free-form names kept in the sidecar) on each record.
func runSuggest(cmd command, stdout, stderr io.Writer) int {
	return runWrite(cmd, stdout, stderr, recordChange{
		check: func(s *review.Session, indices []int) error {
			for _, i := range indices {
				rec := s.Record(i)
				for _, name := range cmd.remove {
					if slices.Contains(rec.SuggestedFlags, name) {
						return fmt.Errorf("suggested flag %q on %s comes from the corpus metadata and cannot be removed", name, rec.ID)
					}
				}
			}
			return nil
		},
		apply: func(s *review.Session, i int) error {
			return s.SetSuggestedFlags(applyDelta(s.State(i).SuggestedFlags, cmd.add, cmd.remove))
		},
		summary: func(s *review.Session, i int) string {
			return fmt.Sprintf("suggested_flags=%s %s", strings.Join(s.SuggestedFlags(i), ","), s.Record(i).ID)
		},
	})
}

// applyDelta returns (current ∪ add) \ remove; the session sorts and dedupes it.
func applyDelta(current, add, remove []string) []string {
	next := make([]string, 0, len(current)+len(add))
	for _, name := range slices.Concat(current, add) {
		if !slices.Contains(remove, name) {
			next = append(next, name)
		}
	}
	return next
}

// runEdit replaces a record's text (--text, --text-file) or reverts it to the imported text (--revert).
func runEdit(cmd command, stdout, stderr io.Writer) int {
	apply := func(s *review.Session, _ int) error { return s.RevertEdit() }
	if !cmd.revert {
		text, err := editText(cmd)
		if err != nil {
			fmt.Fprintf(stderr, "quet: %v\n", err)
			return 1
		}
		apply = func(s *review.Session, _ int) error { return s.SaveEdit(text) }
	}
	return runWrite(cmd, stdout, stderr, recordChange{
		apply: apply,
		summary: func(s *review.Session, i int) string {
			if s.State(i).Edited() {
				return "edited " + s.Record(i).ID
			}
			return "unedited " + s.Record(i).ID
		},
	})
}

// editText returns the new text for `quet edit`: --text verbatim, or the --text-file
// contents ("-" = stdin) without one trailing line ending.
func editText(cmd command) (string, error) {
	if cmd.hasText {
		return cmd.text, nil
	}
	var data []byte
	var err error
	if cmd.textFile == "-" {
		data, err = io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
	} else if data, err = os.ReadFile(cmd.textFile); err != nil {
		return "", err
	}
	text := string(data)
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
	}
	return text, nil
}

// runUndo reverts the most recent write to the sidecar, whether the TUI or a script made it.
func runUndo(cmd command, stdout, stderr io.Writer) int {
	s, err := openSession(cmd)
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	defer s.Close()

	ok, i, err := s.Undo()
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	if cmd.json {
		var undone *recordOut
		if ok && i >= 0 {
			rec := newRecordOut(s, i)
			undone = &rec
		}
		err = newEncoder(stdout).Encode(struct {
			Undone *recordOut `json:"undone"`
		}{undone})
	} else {
		_, err = fmt.Fprintln(stdout, undoSummary(s, ok, i))
	}
	if err != nil {
		fmt.Fprintf(stderr, "quet: %v\n", err)
		return 1
	}
	return 0
}

// undoSummary is the human output line of `quet undo`.
func undoSummary(s *review.Session, ok bool, i int) string {
	switch {
	case !ok:
		return "nothing to undo"
	case i < 0:
		return "undid a change to a record that is no longer in the corpus"
	default:
		return "undid " + s.Record(i).ID
	}
}
