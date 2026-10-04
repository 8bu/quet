package annotate

import (
	"reflect"
	"strings"
	"testing"
)

const gidiSchemaPath = "testdata/annotation-v1.yaml"

func loadGidiSchema(t *testing.T) *Schema {
	t.Helper()
	s, err := LoadSchema(gidiSchemaPath)
	if err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	return s
}

func TestLoadSchemaGidiFixture(t *testing.T) {
	s := loadGidiSchema(t)
	if s.Version != "annotation-v1" {
		t.Errorf("Version = %q", s.Version)
	}
	wantTypes := []string{"expense", "income", "borrow", "lend", "repayment_in", "repayment_out", "transfer", "refund"}
	if got := s.typeNames(); !reflect.DeepEqual(got, wantTypes) {
		t.Errorf("types = %v, want %v", got, wantTypes)
	}
	if got := s.Types[3].Description; got != "The user gives loan principal to another party and expects repayment." {
		t.Errorf("lend description = %q", got)
	}
	// Folded (>-) scalars are joined into one line.
	if got := s.Types[6].Description; got != "Movement between the user's own accounts, wallets, cash holdings, or savings. No economic income or expense occurs." {
		t.Errorf("transfer description = %q", got)
	}
	if got := s.statusNames(); !reflect.DeepEqual(got, []string{"complete", "uncertain", "skipped"}) {
		t.Errorf("statuses = %v", got)
	}
	if !strings.HasPrefix(s.Statuses[1].Description, "The text does not establish") {
		t.Errorf("uncertain description = %q", s.Statuses[1].Description)
	}
	if !reflect.DeepEqual(s.Spans, []SpanDef{{Name: "target", NullForTypes: []string{"transfer"}}}) {
		t.Errorf("Spans = %+v, want the implicit target span null for transfer", s.Spans)
	}
	if !s.NullSpan("target", "transfer") || s.NullSpan("target", "lend") || s.NullSpan("target", "") || s.NullSpan("value", "transfer") {
		t.Error("NullSpan: want only target/transfer")
	}
	if s.HasSpanStatuses() {
		t.Error("HasSpanStatuses: the implicit schema declares none")
	}
	if !s.HasType("refund") || s.HasType("other") || !s.HasStatus("skipped") || s.HasStatus("approved") {
		t.Error("HasType/HasStatus mismatch")
	}
}

func TestParseSchemaSequenceForm(t *testing.T) {
	s, err := ParseSchema([]byte("types: [b, a, c]\nstatuses:\n  - complete\n  - later\n"))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	want := &Schema{
		Types:          []TypeDef{{Name: "b"}, {Name: "a"}, {Name: "c"}},
		Statuses:       []StatusDef{{Name: "complete"}, {Name: "later"}},
		Spans:          []SpanDef{{Name: "target"}},
		ImplicitTarget: true,
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("got %+v, want %+v", s, want)
	}
}

func TestParseSchemaErrors(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"empty document", "", "empty"},
		{"not a mapping", "- a\n", "must be a mapping"},
		{"invalid yaml", "types: [a\n", "parse YAML"},
		{"missing types", "statuses: [complete]\n", "types: required"},
		{"null types", "types:\nstatuses: [complete]\n", "types: required"},
		{"empty types mapping", "types: {}\nstatuses: [complete]\n", "types: at least one"},
		{"empty types sequence", "types: []\nstatuses: [complete]\n", "types: at least one"},
		{"missing statuses", "types: [a]\n", "statuses: required"},
		{"empty statuses", "types: [a]\nstatuses: []\n", "statuses: at least one"},
		{"duplicate type in sequence", "types: [a, b, a]\nstatuses: [complete]\n", `types: duplicate "a"`},
		{"duplicate status", "types: [a]\nstatuses: [x, x]\n", `statuses: duplicate "x"`},
		{"types scalar", "types: a\nstatuses: [complete]\n", "types: line 1: expected a mapping or a list"},
		{"type description not scalar", "types:\n  a: [x]\nstatuses: [complete]\n", `description of "a" must be a string`},
		{"type item not scalar", "types: [[a]]\nstatuses: [complete]\n", "types: line 1: expected a name"},
		{"null type item", "types: [a, ~]\nstatuses: [complete]\n", "expected a name"},
		{"null target undeclared", "types: [a]\nstatuses: [complete]\nnull_target_types: [b]\n", `"b" is not a declared type`},
		{"null target not list", "types: [a]\nstatuses: [complete]\nnull_target_types: a\n", "expected a list"},
		{"null label undeclared", "types: [a]\nstatuses: [complete]\nnull_label_statuses: [skipped]\n", `null_label_statuses: line 3: "skipped" is not a declared status`},
		{"null label duplicate", "types: [a]\nstatuses: [skipped]\nnull_label_statuses: [skipped, skipped]\n", `null_label_statuses: line 3: duplicate "skipped"`},
		{"version mapping", "version: {a: 1}\ntypes: [a]\nstatuses: [complete]\n", "version: line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSchema([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseSchemaIgnoresOtherKeysAndDescriptionsMayBeNull(t *testing.T) {
	s, err := ParseSchema([]byte("queue: {size: 3}\ntrainable_statuses: [complete]\ntypes:\n  a:\n  b: B\nstatuses: {complete: done}\nnull_target_types: []\n"))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if !reflect.DeepEqual(s.Types, []TypeDef{{Name: "a"}, {Name: "b", Description: "B"}}) {
		t.Errorf("Types = %+v", s.Types)
	}
	if len(s.Spans) != 1 || len(s.Spans[0].NullForTypes) != 0 {
		t.Errorf("Spans = %+v, want only target with no null types", s.Spans)
	}
}

func TestSchemaValidate(t *testing.T) {
	s := loadGidiSchema(t)
	const text = "cho Nam vay 500k"
	nam := &Target{Text: "Nam", Start: 4, End: 7}
	tests := []struct {
		name  string
		label Label
		want  []string // substrings of the error; nil = valid
	}{
		{"complete lend with target", Label{Status: "complete", Type: new("lend"), Spans: tgt(nam)}, nil},
		{"complete with null target", Label{Status: "complete", Type: new("expense")}, nil},
		{"complete transfer null target", Label{Status: "complete", Type: new("transfer")}, nil},
		{"uncertain all null", Label{Status: "uncertain"}, nil},
		{"skipped all null", Label{Status: "skipped"}, nil},
		{"uncertain carries valid values", Label{Status: "uncertain", Type: new("lend"), Spans: tgt(nam)}, nil},
		{"complete without type", Label{Status: "complete"}, []string{`type: required when annotation_status is "complete"`}},
		{"unknown type", Label{Status: "complete", Type: new("other")}, []string{`type: "other" is not one of [expense, income`}},
		{"empty type string", Label{Status: "uncertain", Type: new("")}, []string{`type: "" is not one of`}},
		{"unknown status", Label{Status: "approved", Type: new("lend")}, []string{`annotation_status: "approved" is not one of [complete, uncertain, skipped]`}},
		{"uncertain carries unknown type", Label{Status: "uncertain", Type: new("gift")}, []string{`type: "gift"`}},
		{"skipped carries bad span", Label{Status: "skipped", Spans: tgt(&Target{Text: "Nam", Start: 3, End: 6})}, []string{`target: text[3:6] is " Na", not "Nam"`}},
		{"transfer with target", Label{Status: "complete", Type: new("transfer"), Spans: tgt(nam)}, []string{`target: must be null for type "transfer"`}},
		{"uncertain transfer with target", Label{Status: "uncertain", Type: new("transfer"), Spans: tgt(nam)}, []string{`must be null for type "transfer"`}},
		{"every problem listed", Label{Status: "nope", Type: new("gift"), Spans: tgt(&Target{Text: "", Start: 0, End: 1})},
			[]string{"annotation_status:", "; type:", "; target.text: expected a non-empty string"}},
		{"padded target text", Label{Status: "complete", Type: new("lend"), Spans: tgt(&Target{Text: " Nam", Start: 3, End: 7})}, []string{"leading or trailing whitespace"}},
		{"target beyond text", Label{Status: "complete", Type: new("lend"), Spans: tgt(&Target{Text: "Nam", Start: 15, End: 18})}, []string{"beyond the text length 16"}},
		{"target start >= end", Label{Status: "complete", Type: new("lend"), Spans: tgt(&Target{Text: "Nam", Start: 7, End: 4})}, []string{"is empty"}},
		{"negative start", Label{Status: "complete", Type: new("lend"), Spans: tgt(&Target{Text: "Nam", Start: -1, End: 2})}, []string{"negative"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Validate(tt.label, text)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate: nil error, want %q", tt.want)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
		})
	}
}
