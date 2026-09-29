package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// KV is an ordered key/value for JSONWith; Value is marshaled with encoding/json.
type KV struct {
	Key   string
	Value any
}

// JSONWith returns the record re-encoded as a single-line JSON object preserving original key order,
// with textField set to text (added if absent) and extra key/values appended in order (replacing same-named keys in place).
// For .txt records or non-object JSON values, produces {"<textField>": text, ...extra}.
func (r *Record) JSONWith(textField, text string, extra []KV) ([]byte, error) {
	var members []member
	if len(r.Raw) > 0 {
		v, err := decodeValue(r.Raw)
		if err != nil {
			return nil, fmt.Errorf("corpus: record %s: malformed raw JSON: %w", r.ID, err)
		}
		if v.kind == kindObject {
			members = v.obj
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	first := true
	writeKey := func(key string) error {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		if err := encodeValue(enc, &buf, key); err != nil {
			return err
		}
		buf.WriteByte(':')
		return nil
	}
	writeMember := func(key string, val any) error {
		if err := writeKey(key); err != nil {
			return err
		}
		return encodeValue(enc, &buf, val)
	}
	writeRaw := func(key string, val *value) error {
		if err := writeKey(key); err != nil {
			return err
		}
		return val.encode(enc, &buf)
	}

	buf.WriteByte('{')
	consumed := make([]bool, len(extra))
	haveText := false
	for _, m := range members {
		if m.key == textField {
			haveText = true
			if err := writeMember(m.key, text); err != nil {
				return nil, err
			}
			continue
		}
		replaced := false
		for i, kv := range extra {
			if kv.Key == m.key {
				if err := writeMember(m.key, kv.Value); err != nil {
					return nil, err
				}
				consumed[i] = true
				replaced = true
				break
			}
		}
		if replaced {
			continue
		}
		if err := writeRaw(m.key, m.val); err != nil {
			return nil, err
		}
	}
	if !haveText {
		if err := writeMember(textField, text); err != nil {
			return nil, err
		}
	}
	for i, kv := range extra {
		if consumed[i] {
			continue
		}
		if err := writeMember(kv.Key, kv.Value); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encodeValue writes v as JSON without a trailing newline, with HTML escaping disabled.
func encodeValue(enc *json.Encoder, buf *bytes.Buffer, v any) error {
	if err := enc.Encode(v); err != nil {
		return err
	}
	if b := buf.Bytes(); len(b) > 0 && b[len(b)-1] == '\n' {
		buf.Truncate(len(b) - 1)
	}
	return nil
}

type kind uint8

const (
	kindScalar kind = iota
	kindObject
	kindArray
)

// value is a decoded JSON value that preserves object key order and number literals.
type value struct {
	kind   kind
	obj    []member
	arr    []*value
	scalar any // string, json.Number, bool, or nil
}

// member is one object key/value pair.
type member struct {
	key string
	val *value
}

// parseValue decodes the next JSON value from dec.
func parseValue(dec *json.Decoder) (*value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return &value{kind: kindScalar, scalar: tok}, nil
	}
	switch delim {
	case '{':
		v := &value{kind: kindObject}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("object key %v is not a string", keyTok)
			}
			child, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			v.obj = append(v.obj, member{key: key, val: child})
		}
		if _, err := dec.Token(); err != nil { // closing '}'
			return nil, err
		}
		return v, nil
	case '[':
		v := &value{kind: kindArray}
		for dec.More() {
			child, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			v.arr = append(v.arr, child)
		}
		if _, err := dec.Token(); err != nil { // closing ']'
			return nil, err
		}
		return v, nil
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
}

// encode writes v as compact JSON, preserving object key order.
func (v *value) encode(enc *json.Encoder, buf *bytes.Buffer) error {
	switch v.kind {
	case kindObject:
		buf.WriteByte('{')
		for i, m := range v.obj {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeValue(enc, buf, m.key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := m.val.encode(enc, buf); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case kindArray:
		buf.WriteByte('[')
		for i, e := range v.arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := e.encode(enc, buf); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	default:
		return encodeValue(enc, buf, v.scalar)
	}
}

// compact renders v as compact JSON with HTML escaping disabled.
func (v *value) compact() (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := v.encode(enc, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// raw renders v as compact JSON bytes.
func (v *value) raw() (json.RawMessage, error) {
	s, err := v.compact()
	if err != nil {
		return nil, err
	}
	return json.RawMessage(s), nil
}

// keyIndex returns the index of the first member whose key matches key case-insensitively, or -1.
func (v *value) keyIndex(key string) int {
	if v.kind != kindObject {
		return -1
	}
	for i, m := range v.obj {
		if strings.EqualFold(m.key, key) {
			return i
		}
	}
	return -1
}

// lookup returns the value of the first member whose key matches key case-insensitively, or nil.
func (v *value) lookup(key string) *value {
	if i := v.keyIndex(key); i >= 0 {
		return v.obj[i].val
	}
	return nil
}

// lookupAny returns the first present value among keys.
func (v *value) lookupAny(keys ...string) *value {
	for _, key := range keys {
		if m := v.lookup(key); m != nil {
			return m
		}
	}
	return nil
}

// stringOrNumber renders a scalar JSON string or number, reporting whether v was one of those.
func stringOrNumber(v *value) (string, bool) {
	if v == nil || v.kind != kindScalar {
		return "", false
	}
	switch s := v.scalar.(type) {
	case string:
		return s, true
	case json.Number:
		return s.String(), true
	}
	return "", false
}

// scalarText renders a decoded value as record text: plain string, number literal, or "" for null.
func scalarText(v *value) string {
	switch s := v.scalar.(type) {
	case string:
		return s
	case json.Number:
		return s.String()
	case bool:
		if s {
			return "true"
		}
		return "false"
	}
	if v.kind == kindObject || v.kind == kindArray {
		s, err := v.compact()
		if err != nil {
			return ""
		}
		return s
	}
	return ""
}
