package annotate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Item is one queue record to annotate. Queue fields other than id and text are ignored.
type Item struct{ ID, Text string }

// LoadQueue reads the queue JSONL at path: one object per line with string id and text; blank lines are skipped.
// Invalid JSON, missing or non-string id/text, empty and duplicate ids are errors citing the line number.
func LoadQueue(path string) ([]Item, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read queue: %w", err)
	}
	var items []Item
	seen := map[string]int{}
	err = forEachLine(data, func(lineNo int, line []byte) error {
		fields, err := decodeObject(line)
		if err != nil {
			return err
		}
		id, err := requiredString(fields, "id")
		if err != nil {
			return err
		}
		if id == "" {
			return errors.New("id: expected a non-empty string")
		}
		text, err := requiredString(fields, "text")
		if err != nil {
			return err
		}
		if first, dup := seen[id]; dup {
			return fmt.Errorf("duplicate id %q (first on line %d)", id, first)
		}
		seen[id] = lineNo
		items = append(items, Item{ID: id, Text: text})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("queue %s:%w", path, err)
	}
	return items, nil
}

// forEachLine calls fn with the 1-based line number of every non-blank line in data, stopping at the first error,
// which is returned prefixed with the line number.
func forEachLine(data []byte, fn func(lineNo int, line []byte) error) error {
	for i, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if err := fn(i+1, line); err != nil {
			return fmt.Errorf("%d: %w", i+1, err)
		}
	}
	return nil
}

// decodeObject decodes one JSON object into its raw members.
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if fields == nil {
		return nil, errors.New("invalid JSON object: got null")
	}
	return fields, nil
}

// requiredString returns the string member key of fields, erroring when it is missing or not a JSON string.
func requiredString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("missing field %q", key)
	}
	s, err := decodeString(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return s, nil
}

// decodeString decodes raw as a JSON string (null and other kinds are rejected).
func decodeString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", errors.New("expected a string")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errors.New("expected a string")
	}
	return s, nil
}

// isJSONNull reports whether raw is the JSON literal null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
