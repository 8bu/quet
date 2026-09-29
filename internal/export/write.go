package export

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/8bu/quet/internal/corpus"
	"github.com/8bu/quet/internal/review"
	"github.com/8bu/quet/internal/storage"
)

// quetMeta is the review metadata object embedded as "quet" by Options.WithReview.
// It is marshaled on its own (no HTML escaping) and passed to corpus.JSONWith as raw JSON.
type quetMeta struct {
	ID             string   `json:"id"`
	Status         string   `json:"status"`
	Edited         bool     `json:"edited"`
	OriginalText   *string  `json:"original_text,omitempty"` // only when the record was edited
	ManualFlags    []string `json:"manual_flags"`
	AutoFlags      []string `json:"auto_flags"` // names only
	SuggestedFlags []string `json:"suggested_flags"`
}

// Write streams matching records (in corpus order) to w. Each JSONL line = record.JSONWith(textField, finalText, extra).
// Format "" or "jsonl" writes one JSON object per line; "txt" writes the final text, one record per line.
// Returns the number of records written.
func Write(s *review.Session, w io.Writer, opt Options) (int, error) {
	format := opt.Format
	if format == "" {
		format = "jsonl"
	}
	if format != "jsonl" && format != "txt" {
		return 0, fmt.Errorf("unknown export format %q (want jsonl or txt)", opt.Format)
	}
	statuses := statusSet(opt.Statuses)

	out := bufio.NewWriterSize(w, 1<<16)
	written := 0
	for i := range s.Len() {
		state := s.State(i)
		if statuses != nil && !statuses[state.EffectiveStatus()] {
			continue
		}
		text := s.FinalText(i)
		if format == "txt" {
			if _, err := out.WriteString(text + "\n"); err != nil {
				return written, err
			}
			written++
			continue
		}
		rec := s.Record(i)
		var extra []corpus.KV
		if opt.WithReview {
			value, err := marshalMeta(s, i, rec, state)
			if err != nil {
				return written, err
			}
			extra = []corpus.KV{{Key: "quet", Value: value}}
		}
		line, err := rec.JSONWith(s.Corpus.TextField, text, extra)
		if err != nil {
			return written, fmt.Errorf("encode record %s: %w", rec.ID, err)
		}
		if _, err := out.Write(line); err != nil {
			return written, err
		}
		if err := out.WriteByte('\n'); err != nil {
			return written, err
		}
		written++
	}
	if err := out.Flush(); err != nil {
		return written, err
	}
	return written, nil
}

// marshalMeta builds the "quet" object for record i as compact, HTML-unescaped JSON.
func marshalMeta(s *review.Session, i int, rec *corpus.Record, state review.State) (json.RawMessage, error) {
	meta := quetMeta{
		ID:             rec.ID,
		Status:         string(state.EffectiveStatus()),
		Edited:         state.Edited(),
		ManualFlags:    nonNil(state.ManualFlags),
		AutoFlags:      []string{},
		SuggestedFlags: nonNil(rec.SuggestedFlags),
	}
	if state.Edited() {
		original := rec.Text
		meta.OriginalText = &original
	}
	for _, f := range s.AutoFlags(i) {
		meta.AutoFlags = append(meta.AutoFlags, f.Name)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(meta); err != nil {
		return nil, fmt.Errorf("encode review metadata for %s: %w", rec.ID, err)
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// ToFile writes atomically (temp file in same dir + fsync + rename). Refuses if out resolves to the source corpus or its sidecar;
// refuses to overwrite an existing file unless force.
func ToFile(s *review.Session, out string, opt Options, force bool) (int, error) {
	if err := checkTarget(out, s.Corpus.Path, force); err != nil {
		return 0, err
	}

	dir := filepath.Dir(out)
	tmp, err := os.CreateTemp(dir, filepath.Base(out)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmpName)
		}
	}()

	written, err := Write(s, tmp, opt)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, out); err != nil {
		return 0, fmt.Errorf("write %s: %w", out, err)
	}
	keep = true
	return written, nil
}

// checkTarget rejects writes that would land on the source corpus or its sidecar, and existing files unless force.
func checkTarget(out, corpusPath string, force bool) error {
	outAbs, err := absPath(out)
	if err != nil {
		return err
	}
	corpusAbs, err := absPath(corpusPath)
	if err != nil {
		return err
	}
	if outAbs == corpusAbs {
		return fmt.Errorf("refusing to write over the source corpus %s", corpusPath)
	}
	sidecarAbs, err := absPath(storage.SidecarPath(corpusPath))
	if err != nil {
		return err
	}
	if outAbs == sidecarAbs {
		return fmt.Errorf("refusing to write over the sidecar database %s", storage.SidecarPath(corpusPath))
	}
	if !force {
		if _, err := os.Stat(out); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", out)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("check %s: %w", out, err)
		}
	}
	return nil
}

func absPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return abs, nil
}

func statusSet(statuses []review.ReviewStatus) map[review.ReviewStatus]bool {
	if len(statuses) == 0 {
		return nil // empty = all
	}
	set := make(map[review.ReviewStatus]bool, len(statuses))
	for _, status := range statuses {
		set[status] = true
	}
	return set
}

// nonNil returns an empty (non-nil) slice for nil input so metadata always marshals as [].
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
