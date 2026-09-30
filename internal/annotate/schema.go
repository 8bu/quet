// Package annotate implements Quet's generic annotation mode: a schema-driven labeling session over a
// JSONL queue whose labels are persisted (and exported) as a JSONL file.
package annotate

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Status roles Quet binds to keys. A schema need not declare them; marking with an undeclared status fails.
const (
	StatusComplete  = "complete"
	StatusUncertain = "uncertain"
	StatusSkipped   = "skipped"
)

// TypeDef is one declared annotation type and its schema description.
type TypeDef struct{ Name, Description string }

// StatusDef is one declared annotation status and its schema description.
type StatusDef struct{ Name, Description string }

// Schema is the annotation contract: declared types and statuses (YAML order), the types whose target must be null
// and the statuses whose type and target must both be null.
type Schema struct {
	Version         string
	Types           []TypeDef   // YAML order
	Statuses        []StatusDef // YAML order
	NullTargetTypes []string
	// NullLabelStatuses are the statuses Mark saves with a null type and target. When the schema has no
	// null_label_statuses key it defaults to [skipped] if skipped is declared (Quet's skip role means "no label").
	// Loading does not enforce it, so older labels that break the rule still open and can be re-marked.
	NullLabelStatuses []string
}

// LoadSchema reads and parses the schema YAML at path.
func LoadSchema(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	s, err := ParseSchema(data)
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", path, err)
	}
	return s, nil
}

// ParseSchema parses schema YAML. types and statuses may each be a mapping name→description or a sequence of
// names; both are required and non-empty. null_target_types, null_label_statuses and version are optional; other
// keys are ignored.
func ParseSchema(data []byte) (*Schema, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("schema is empty")
	}
	root := resolve(doc.Content[0])
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: schema must be a mapping", root.Line)
	}

	fields := map[string]*yaml.Node{}
	for k := 0; k+1 < len(root.Content); k += 2 {
		key, val := root.Content[k], resolve(root.Content[k+1])
		switch key.Value {
		case "version", "types", "statuses", "null_target_types", "null_label_statuses":
			if _, dup := fields[key.Value]; dup {
				return nil, fmt.Errorf("line %d: duplicate key %q", key.Line, key.Value)
			}
			fields[key.Value] = val
		}
	}

	s := &Schema{}
	if v, ok := fields["version"]; ok && !isNull(v) {
		if v.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("version: line %d: expected a string", v.Line)
		}
		s.Version = v.Value
	}

	types, err := parseDefs("types", fields["types"])
	if err != nil {
		return nil, err
	}
	for _, d := range types {
		s.Types = append(s.Types, TypeDef(d))
	}
	statuses, err := parseDefs("statuses", fields["statuses"])
	if err != nil {
		return nil, err
	}
	for _, d := range statuses {
		s.Statuses = append(s.Statuses, StatusDef(d))
	}

	if s.NullTargetTypes, err = parseNames("null_target_types", "type", fields["null_target_types"], s.HasType); err != nil {
		return nil, err
	}
	if s.NullLabelStatuses, err = parseNames("null_label_statuses", "status", fields["null_label_statuses"], s.HasStatus); err != nil {
		return nil, err
	}
	if n, ok := fields["null_label_statuses"]; (!ok || isNull(n)) && s.HasStatus(StatusSkipped) {
		s.NullLabelStatuses = []string{StatusSkipped}
	}
	return s, nil
}

// parseNames parses an optional list of declared names (null or absent = none): every entry a scalar for which
// declared is true, without duplicates. kind names the entry ("type", "status") in errors.
func parseNames(field, kind string, n *yaml.Node, declared func(string) bool) ([]string, error) {
	if n == nil || isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: line %d: expected a list of %s names", field, n.Line, kind)
	}
	var names []string
	seen := map[string]bool{}
	for _, item := range n.Content {
		item = resolve(item)
		if item.Kind != yaml.ScalarNode || isNull(item) {
			return nil, fmt.Errorf("%s: line %d: expected a %s name", field, item.Line, kind)
		}
		if !declared(item.Value) {
			return nil, fmt.Errorf("%s: line %d: %q is not a declared %s", field, item.Line, item.Value, kind)
		}
		if seen[item.Value] {
			return nil, fmt.Errorf("%s: line %d: duplicate %q", field, item.Line, item.Value)
		}
		seen[item.Value] = true
		names = append(names, item.Value)
	}
	return names, nil
}

// def is a parsed name/description pair shared by types and statuses.
type def struct{ Name, Description string }

// parseDefs parses a required, non-empty mapping (name→description) or sequence (names) without duplicates.
func parseDefs(field string, n *yaml.Node) ([]def, error) {
	if n == nil || isNull(n) {
		return nil, fmt.Errorf("%s: required", field)
	}
	var defs []def
	switch n.Kind {
	case yaml.MappingNode:
		for k := 0; k+1 < len(n.Content); k += 2 {
			key, val := resolve(n.Content[k]), resolve(n.Content[k+1])
			if key.Kind != yaml.ScalarNode || isNull(key) {
				return nil, fmt.Errorf("%s: line %d: expected a name", field, key.Line)
			}
			d := def{Name: key.Value}
			switch {
			case isNull(val):
			case val.Kind == yaml.ScalarNode:
				d.Description = val.Value
			default:
				return nil, fmt.Errorf("%s: line %d: description of %q must be a string", field, val.Line, key.Value)
			}
			defs = append(defs, d)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			item = resolve(item)
			if item.Kind != yaml.ScalarNode || isNull(item) {
				return nil, fmt.Errorf("%s: line %d: expected a name", field, item.Line)
			}
			defs = append(defs, def{Name: item.Value})
		}
	default:
		return nil, fmt.Errorf("%s: line %d: expected a mapping or a list", field, n.Line)
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("%s: at least one entry is required", field)
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if strings.TrimSpace(d.Name) == "" {
			return nil, fmt.Errorf("%s: empty name", field)
		}
		if seen[d.Name] {
			return nil, fmt.Errorf("%s: duplicate %q", field, d.Name)
		}
		seen[d.Name] = true
	}
	return defs, nil
}

// resolve follows YAML aliases to the anchored node.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// isNull reports whether n is a YAML null scalar.
func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// HasType reports whether name is a declared type.
func (s *Schema) HasType(name string) bool {
	for _, t := range s.Types {
		if t.Name == name {
			return true
		}
	}
	return false
}

// HasStatus reports whether name is a declared status.
func (s *Schema) HasStatus(name string) bool {
	for _, st := range s.Statuses {
		if st.Name == name {
			return true
		}
	}
	return false
}

// NullTarget reports whether labels of type typ must have a null target.
func (s *Schema) NullTarget(typ string) bool {
	for _, t := range s.NullTargetTypes {
		if t == typ {
			return true
		}
	}
	return false
}

// NullLabel reports whether labels with status must have a null type and a null target.
func (s *Schema) NullLabel(status string) bool {
	for _, st := range s.NullLabelStatuses {
		if st == status {
			return true
		}
	}
	return false
}

// typeNames lists the declared type names in schema order.
func (s *Schema) typeNames() []string {
	names := make([]string, len(s.Types))
	for i, t := range s.Types {
		names[i] = t.Name
	}
	return names
}

// statusNames lists the declared status names in schema order.
func (s *Schema) statusNames() []string {
	names := make([]string, len(s.Statuses))
	for i, st := range s.Statuses {
		names[i] = st.Name
	}
	return names
}
