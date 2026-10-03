package annotate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// multiSchemaYAML declares two span fields: target (null for transfer and void) and value (null for gift and void,
// with the per-span statuses complete and uncertain, complete being the default).
const multiSchemaYAML = `version: annotation-v3
types: [expense, income, transfer, gift, void]
statuses: [complete, uncertain, skipped]
null_label_statuses: [skipped]
spans:
  target:
    description: Counterparty.
    null_for_types: [transfer, void]
  value:
    description: Amount.
    null_for_types: [gift, void]
    statuses: [complete, uncertain]
`

// nfdText holds "café" in NFD: the accent is its own code point (U+0301), so code points and bytes both differ from
// the NFC form.
const nfdText = "cafe\u0301 Nam 50k"

// Queue indices of multiQueue.
const (
	mIdxPizza    = 0 // non-ASCII text, two spans
	mIdxNFD      = 1 // NFD text with a combining mark
	mIdxTransfer = 2
	mIdxSkipped  = 3
	mIdxGift     = 4
)

// multiQueue is the queue of the multi-span tests.
var multiQueue = []Item{
	{ID: "m1", Text: "ăn với Nam ở Pizza 4P 300k"},
	{ID: "m2", Text: nfdText},
	{ID: "m3", Text: "ck 5tr qua tk"},
	{ID: "m4", Text: "Nam gửi tao 500k"},
	{ID: "m5", Text: "t\u1eb7ng Nam qu\u00e0"},
}

// newMultiFixture writes the first n items of multiQueue (all when n <= 0) and schemaYAML into a temp dir.
func newMultiFixture(t *testing.T, schemaYAML string, n int) fixture {
	t.Helper()
	if n <= 0 {
		n = len(multiQueue)
	}
	return newFixtureFor(t, schemaYAML, multiQueue[:n])
}

// newFixtureFor writes items as the queue and schemaYAML as the schema into a temp dir.
func newFixtureFor(t *testing.T, schemaYAML string, items []Item) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		dir:    dir,
		queue:  filepath.Join(dir, "queue.jsonl"),
		schema: filepath.Join(dir, "schema.yaml"),
		out:    filepath.Join(dir, "labels.jsonl"),
	}
	var queue bytes.Buffer
	for _, it := range items {
		line, err := json.Marshal(map[string]string{"id": it.ID, "text": it.Text})
		if err != nil {
			t.Fatal(err)
		}
		queue.Write(line)
		queue.WriteByte('\n')
	}
	for path, data := range map[string][]byte{f.queue: queue.Bytes(), f.schema: []byte(schemaYAML)} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// mustParseSchema parses the multi-span schema.
func mustParseSchema(t *testing.T, yaml string) *Schema {
	t.Helper()
	s, err := ParseSchema([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	return s
}

func mustSetSpan(t *testing.T, s *Session, i int, name string, start, end int) {
	t.Helper()
	if err := s.SetSpan(i, name, start, end); err != nil {
		t.Fatalf("SetSpan(%d,%s,%d,%d): %v", i, name, start, end, err)
	}
}

func mustSetSpanStatus(t *testing.T, s *Session, i int, name, status string) {
	t.Helper()
	if err := s.SetSpanStatus(i, name, status); err != nil {
		t.Fatalf("SetSpanStatus(%d,%s,%s): %v", i, name, status, err)
	}
}

// fileString returns the content of path.
func fileString(t *testing.T, path string) string {
	t.Helper()
	return string(mustRead(t, path))
}

func TestParseSchemaSpans(t *testing.T) {
	s := mustParseSchema(t, multiSchemaYAML)
	want := []SpanDef{
		{Name: "target", Description: "Counterparty.", NullForTypes: []string{"transfer", "void"}},
		{Name: "value", Description: "Amount.", NullForTypes: []string{"gift", "void"}, Statuses: []string{"complete", "uncertain"}},
	}
	if !reflect.DeepEqual(s.Spans, want) {
		t.Errorf("Spans = %+v, want %+v", s.Spans, want)
	}
	if sp, ok := s.Span("value"); !ok || !reflect.DeepEqual(sp, want[1]) {
		t.Errorf("Span(value) = %+v, %v", sp, ok)
	}
	if _, ok := s.Span("nope"); ok {
		t.Error("Span(nope) found")
	}
	nullChecks := []struct {
		span, typ string
		want      bool
	}{
		{"target", "transfer", true}, {"target", "void", true}, {"target", "gift", false}, {"target", "expense", false},
		{"value", "gift", true}, {"value", "void", true}, {"value", "transfer", false},
		{"value", "", false}, {"nope", "gift", false},
	}
	for _, c := range nullChecks {
		if got := s.NullSpan(c.span, c.typ); got != c.want {
			t.Errorf("NullSpan(%q,%q) = %v, want %v", c.span, c.typ, got, c.want)
		}
	}
	if !s.HasSpanStatuses() {
		t.Error("HasSpanStatuses false for a schema with value statuses")
	}
	if got := s.NullLabelStatuses; !reflect.DeepEqual(got, []string{"skipped"}) {
		t.Errorf("NullLabelStatuses = %v", got)
	}
}

func TestParseSchemaSpanForms(t *testing.T) {
	const head = "types: [a, b]\nstatuses: [complete, uncertain]\n"
	tests := []struct {
		name, yaml string
		want       []SpanDef
	}{
		{"sequence of names", head + "spans: [x, y_2, _z]\n",
			[]SpanDef{{Name: "x"}, {Name: "y_2"}, {Name: "_z"}}},
		{"block sequence", head + "spans:\n  - x\n  - y\n",
			[]SpanDef{{Name: "x"}, {Name: "y"}}},
		{"description string, null and mapping", head + "spans:\n  x: The X.\n  y:\n  z:\n    description: The Z.\n    null_for_types: [a]\n    statuses: [uncertain, complete]\n",
			[]SpanDef{{Name: "x", Description: "The X."}, {Name: "y"},
				{Name: "z", Description: "The Z.", NullForTypes: []string{"a"}, Statuses: []string{"uncertain", "complete"}}}},
		{"empty statuses and null options mean none", head + "spans:\n  x:\n    statuses: []\n    null_for_types:\n    description:\n",
			[]SpanDef{{Name: "x"}}},
		{"null spans is absent", head + "spans:\n", []SpanDef{{Name: "target"}}},
		{"explicit null spans keeps null_target_types", head + "null_target_types: [b]\nspans: null\n",
			[]SpanDef{{Name: "target", NullForTypes: []string{"b"}}}},
		{"no spans key", head + "null_target_types: [a, b]\n",
			[]SpanDef{{Name: "target", NullForTypes: []string{"a", "b"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustParseSchema(t, tt.yaml)
			if len(s.Spans) != len(tt.want) {
				t.Fatalf("Spans = %+v, want %+v", s.Spans, tt.want)
			}
			for i, sp := range s.Spans {
				w := tt.want[i]
				if sp.Name != w.Name || sp.Description != w.Description ||
					!sameNames(sp.NullForTypes, w.NullForTypes) || !sameNames(sp.Statuses, w.Statuses) {
					t.Errorf("Spans[%d] = %+v, want %+v", i, sp, w)
				}
			}
		})
	}
	if s := mustParseSchema(t, head+"spans: [x]\n"); s.HasSpanStatuses() {
		t.Error("HasSpanStatuses true for spans without statuses")
	}
}

// sameNames reports whether a and b hold the same names; nil equals empty.
func sameNames(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func TestParseSchemaSpanErrors(t *testing.T) {
	const head = "types: [a, b]\nstatuses: [complete, uncertain]\n"
	tests := []struct {
		name, yaml, want string
	}{
		{"combined with null_target_types", head + "null_target_types: [a]\nspans: [x]\n",
			"spans: cannot be combined with null_target_types; use null_for_types per span"},
		{"combined with empty null_target_types", head + "null_target_types: []\nspans: [x]\n", "cannot be combined with null_target_types"},
		{"empty mapping", head + "spans: {}\n", "spans: line 3: at least one entry is required"},
		{"empty sequence", head + "spans: []\n", "spans: line 3: at least one entry is required"},
		{"scalar", head + "spans: x\n", "spans: line 3: expected a mapping or a list"},
		{"duplicate in sequence", head + "spans: [x, y, x]\n", `duplicate "x"`},
		{"duplicate in mapping", head + "spans:\n  x:\n  y:\n  x:\n", `spans: line 6: duplicate "x"`},
		{"name starts with digit", head + "spans: [1x]\n", `invalid span name "1x"`},
		{"name with dash", head + "spans:\n  a-b:\n", `invalid span name "a-b"`},
		{"name with space", head + "spans:\n  a b:\n", `invalid span name "a b"`},
		{"empty name", head + "spans:\n  \"\":\n", `invalid span name ""`},
		{"non-ASCII name", head + "spans: [giá]\n", `invalid span name "giá"`},
		{"reserved id", head + "spans: [id]\n", `span name "id" is reserved`},
		{"reserved annotation_status", head + "spans: [annotation_status]\n", `span name "annotation_status" is reserved`},
		{"reserved type", head + "spans:\n  type:\n", `span name "type" is reserved`},
		{"reserved note", head + "spans: [note]\n", `span name "note" is reserved`},
		{"reserved span_status", head + "spans: [span_status]\n", `span name "span_status" is reserved`},
		{"null name item", head + "spans: [x, ~]\n", "spans: line 3: expected a span name"},
		{"nested name item", head + "spans: [[x]]\n", "spans: line 3: expected a span name"},
		{"sequence value", head + "spans:\n  x: [a]\n", `spans: line 4: span "x" must be a description, null or a mapping`},
		{"unknown span key", head + "spans:\n  x:\n    foo: 1\n", `spans.x: line 5: unknown key "foo"`},
		{"misspelled key", head + "spans:\n  x:\n    null_for_type: [a]\n", `unknown key "null_for_type"`},
		{"duplicate span key", head + "spans:\n  x:\n    description: a\n    description: b\n", `duplicate key "description"`},
		{"undeclared null_for_types type", head + "spans:\n  x:\n    null_for_types: [zzz]\n", `spans.x.null_for_types: line 5: "zzz" is not a declared type`},
		{"duplicate null_for_types type", head + "spans:\n  x:\n    null_for_types: [a, a]\n", `spans.x.null_for_types: line 5: duplicate "a"`},
		{"null_for_types not a list", head + "spans:\n  x:\n    null_for_types: a\n", "spans.x.null_for_types: line 5: expected a list of type names"},
		{"undeclared span status", head + "spans:\n  x:\n    statuses: [bogus]\n", `spans.x.statuses: line 5: "bogus" is not a declared status`},
		{"duplicate span status", head + "spans:\n  x:\n    statuses: [complete, complete]\n", `spans.x.statuses: line 5: duplicate "complete"`},
		{"statuses not a list", head + "spans:\n  x:\n    statuses: complete\n", "spans.x.statuses: line 5: expected a list of status names"},
		{"description not a string", head + "spans:\n  x:\n    description: [q]\n", "spans.x.description: line 5: expected a string"},
		{"second span reports its own name", head + "spans:\n  x:\n  y:\n    statuses: [nope]\n", "spans.y.statuses"},
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

func TestSchemaValidateSpans(t *testing.T) {
	s := mustParseSchema(t, multiSchemaYAML)
	const text = "ăn với Nam ở Pizza 4P 300k"
	pizza := &Target{Text: "Pizza 4P", Start: 13, End: 21}
	amount := &Target{Text: "300k", Start: 22, End: 26}
	spans := func(target, value *Target) map[string]*Target {
		return map[string]*Target{"target": target, "value": value}
	}
	tests := []struct {
		name  string
		label Label
		want  string // exact error; "" = valid
	}{
		{"both spans", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, amount),
			SpanStatus: map[string]string{"value": "complete"}}, ""},
		{"span status missing is lenient", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, amount)}, ""},
		{"span status on a null span", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, nil),
			SpanStatus: map[string]string{"value": "uncertain"}}, ""},
		{"missing spans map", Label{Status: "uncertain"}, ""},
		{"skipped may carry values", Label{Status: "skipped", Type: new("expense"), Spans: spans(pizza, amount)}, ""},
		{"transfer nulls target only", Label{Status: "complete", Type: new("transfer"), Spans: spans(nil, amount)}, ""},
		{"gift nulls value only", Label{Status: "complete", Type: new("gift"), Spans: spans(pizza, nil)}, ""},
		{"void nulls both", Label{Status: "complete", Type: new("void"), Spans: spans(nil, nil)}, ""},
		{"transfer with target", Label{Status: "complete", Type: new("transfer"), Spans: spans(pizza, amount)},
			`target: must be null for type "transfer"`},
		{"gift with value", Label{Status: "complete", Type: new("gift"), Spans: spans(pizza, amount)},
			`value: must be null for type "gift"`},
		{"void with both lists both in schema order", Label{Status: "complete", Type: new("void"), Spans: spans(pizza, amount)},
			`target: must be null for type "void"; value: must be null for type "void"`},
		{"second span out of range", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, &Target{Text: "300k", Start: 30, End: 34})},
			"value: span end 34 is beyond the text length 26"},
		{"second span wrong slice", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, &Target{Text: "300k", Start: 21, End: 25})},
			`value: text[21:25] is " 300", not "300k"`},
		{"second span empty text", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, &Target{Start: 0, End: 1})},
			"value.text: expected a non-empty string"},
		{"second span padded", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, &Target{Text: " 300k", Start: 21, End: 26})},
			"value.text: has leading or trailing whitespace"},
		{"span status not in list", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, amount),
			SpanStatus: map[string]string{"value": "skipped"}}, `span_status.value: "skipped" is not one of [complete, uncertain]`},
		{"span status on span without statuses", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, amount),
			SpanStatus: map[string]string{"target": "complete"}}, "span_status.target: span declares no statuses"},
		{"span status for unknown span", Label{Status: "complete", Type: new("expense"), Spans: spans(pizza, amount),
			SpanStatus: map[string]string{"ghost": "complete"}}, "span_status.ghost: not a declared span"},
		{"span status on a span null for the type", Label{Status: "complete", Type: new("gift"), Spans: spans(pizza, nil),
			SpanStatus: map[string]string{"value": "complete"}}, `span_status.value: must be absent for type "gift"`},
		{"every problem in order", Label{Status: "nope", Type: new("gift"), Spans: spans(&Target{Start: 0, End: 1}, amount),
			SpanStatus: map[string]string{"ghost": "x", "value": "zzz", "target": "complete"}},
			`annotation_status: "nope" is not one of [complete, uncertain, skipped]; ` +
				"target.text: expected a non-empty string; " +
				`value: must be null for type "gift"; ` +
				"span_status.target: span declares no statuses; " +
				`span_status.value: "zzz" is not one of [complete, uncertain]; ` +
				`span_status.value: must be absent for type "gift"; ` +
				"span_status.ghost: not a declared span"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Validate(tt.label, text)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Fatalf("Validate = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSessionSetTypeNullSpans(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	i := mIdxPizza

	mustSetType(t, s, i, "expense")
	mustSetSpan(t, s, i, "target", 13, 21)
	mustSetSpan(t, s, i, "value", 22, 26)
	mustSetSpanStatus(t, s, i, "value", "uncertain")

	// gift is null for value only: the value and its status go, the target stays.
	cleared, err := s.SetType(i, "gift")
	if err != nil || !reflect.DeepEqual(cleared, []string{"value"}) {
		t.Fatalf("SetType(gift) = %v, %v; want [value]", cleared, err)
	}
	d := s.Draft(i)
	if d.Type != "gift" || d.Spans["target"] == nil || d.Spans["value"] != nil || len(d.SpanStatus) != 0 {
		t.Errorf("draft = %+v", d)
	}
	if st, def := s.SpanStatus(i, "value"); st != "" || def {
		t.Errorf("SpanStatus(value) on gift = %q, %v; want empty", st, def)
	}

	// Switching back does not bring the value back; the status is the default again.
	if cleared, err := s.SetType(i, "expense"); err != nil || len(cleared) != 0 {
		t.Fatalf("SetType(expense) = %v, %v", cleared, err)
	}
	if st, def := s.SpanStatus(i, "value"); st != "complete" || !def {
		t.Errorf("SpanStatus(value) = %q, %v; want complete, default", st, def)
	}

	// void is null for both: cleared lists them in schema order.
	mustSetSpan(t, s, i, "value", 22, 26)
	cleared, err = s.SetType(i, "void")
	if err != nil || !reflect.DeepEqual(cleared, []string{"target", "value"}) {
		t.Fatalf("SetType(void) = %v, %v; want [target value]", cleared, err)
	}
	// transfer is null for target only.
	mustSetType(t, s, i, "expense")
	mustSetSpan(t, s, i, "target", 13, 21)
	mustSetSpan(t, s, i, "value", 22, 26)
	if cleared, _ := s.SetType(i, "transfer"); !reflect.DeepEqual(cleared, []string{"target"}) {
		t.Errorf("SetType(transfer) cleared %v, want [target]", cleared)
	}
	if d := s.Draft(i); d.Spans["target"] != nil || d.Spans["value"] == nil {
		t.Errorf("draft after transfer = %+v", d)
	}
	if _, err := s.SetType(i, "nope"); err == nil {
		t.Error("SetType accepted an undeclared type")
	}
}

func TestSessionSetSpanErrors(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	i := mIdxPizza
	mustSetType(t, s, i, "transfer")
	mustSetType(t, s, mIdxGift, "gift")

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"unknown span", s.SetSpan(i, "price", 0, 1), `span "price" is not declared`},
		{"null for type target", s.SetSpan(i, "target", 7, 10), `type "transfer" must have a null target`},
		{"null for type value", s.SetSpan(mIdxGift, "value", 0, 1), `type "gift" must have a null value`},
		{"padded span", s.SetSpan(mIdxNFD, "value", 9, 13), "leading or trailing whitespace"},
		{"out of range span", s.SetSpan(mIdxNFD, "target", 0, 99), "beyond the text length"},
		{"empty span", s.SetSpan(mIdxNFD, "target", 3, 3), "is empty"},
		{"clear unknown span", s.ClearSpan(i, "price"), `span "price" is not declared`},
		{"status of unknown span", s.SetSpanStatus(i, "price", "complete"), `span "price" is not declared`},
		{"status on span without statuses", s.SetSpanStatus(i, "target", "complete"), "span target declares no statuses"},
		{"status not in list", s.SetSpanStatus(i, "value", "skipped"), `status "skipped" is not one of [complete, uncertain] for span value`},
		{"status on span null for type", s.SetSpanStatus(mIdxGift, "value", "complete"), `type "gift" must have a null value`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", tt.err, tt.want)
			}
		})
	}
	if _, err := s.CycleSpanStatus(i, "target"); err == nil || !strings.Contains(err.Error(), "declares no statuses") {
		t.Errorf("CycleSpanStatus on a span without statuses: %v", err)
	}
	if _, err := s.CycleSpanStatus(i, "price"); err == nil {
		t.Error("CycleSpanStatus accepted an unknown span")
	}
	if _, err := s.CycleSpanStatus(mIdxGift, "value"); err == nil {
		t.Error("CycleSpanStatus accepted a span that is null for the type")
	}
	if s.Dirty(mIdxNFD) {
		t.Errorf("refused edits left a draft: %+v", s.Draft(mIdxNFD))
	}
	if st, def := s.SpanStatus(i, "target"); st != "" || def {
		t.Errorf("SpanStatus of a span without statuses = %q, %v", st, def)
	}
	if st, def := s.SpanStatus(i, "price"); st != "" || def {
		t.Errorf("SpanStatus of an unknown span = %q, %v", st, def)
	}
}

func TestSessionSpanStatusCycleAndDirty(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	i := mIdxPizza

	if s.Dirty(i) {
		t.Fatal("fresh record is dirty")
	}
	if st, def := s.SpanStatus(i, "value"); st != "complete" || !def {
		t.Fatalf("fresh SpanStatus = %q, %v; want complete, default", st, def)
	}
	if got, err := s.CycleSpanStatus(i, "value"); err != nil || got != "uncertain" {
		t.Fatalf("first cycle = %q, %v; want uncertain (default counts as the first)", got, err)
	}
	if st, def := s.SpanStatus(i, "value"); st != "uncertain" || def {
		t.Errorf("SpanStatus = %q, %v; want uncertain, explicit", st, def)
	}
	if !s.Dirty(i) {
		t.Error("changed span status not dirty")
	}
	if got, err := s.CycleSpanStatus(i, "value"); err != nil || got != "complete" {
		t.Fatalf("second cycle = %q, %v; want complete (wrap)", got, err)
	}
	if s.Dirty(i) {
		t.Error("explicit default status over an unlabeled record reported dirty")
	}

	// Saved explicit statuses compare with the draft.
	mustSetType(t, s, i, "expense")
	mustSetSpan(t, s, i, "value", 22, 26)
	mustSetSpanStatus(t, s, i, "value", "uncertain")
	mustMark(t, s, i, StatusComplete)
	if s.Dirty(i) {
		t.Error("draft of a saved label is dirty")
	}
	mustSetSpanStatus(t, s, i, "value", "complete")
	if !s.Dirty(i) {
		t.Error("status change on a saved label not dirty")
	}
	mustSetSpanStatus(t, s, i, "value", "uncertain")
	if s.Dirty(i) {
		t.Error("status restored to the saved value still dirty")
	}

	// A single-status span cycles onto itself.
	one := newMultiFixture(t, strings.Replace(multiSchemaYAML, "statuses: [complete, uncertain]", "statuses: [uncertain]", 1), 0)
	so := one.open(t)
	if got, err := so.CycleSpanStatus(0, "value"); err != nil || got != "uncertain" {
		t.Errorf("single-status cycle = %q, %v", got, err)
	}
}

func TestSessionMarkSpanStatusDefaultsAndNullRules(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)

	// Unset span status → the first listed status; target (no statuses) gets none.
	mustSetType(t, s, mIdxPizza, "expense")
	mustSetSpan(t, s, mIdxPizza, "target", 13, 21)
	mustSetSpan(t, s, mIdxPizza, "value", 22, 26)
	mustMark(t, s, mIdxPizza, StatusComplete)
	l, _ := s.Label(mIdxPizza)
	if !reflect.DeepEqual(l.SpanStatus, map[string]string{"value": "complete"}) {
		t.Errorf("SpanStatus = %v, want value=complete", l.SpanStatus)
	}

	// The span status is kept when the span itself is null.
	mustSetType(t, s, mIdxNFD, "income")
	mustSetSpanStatus(t, s, mIdxNFD, "value", "uncertain")
	mustMark(t, s, mIdxNFD, StatusUncertain)
	l, _ = s.Label(mIdxNFD)
	if l.Spans["value"] != nil || !reflect.DeepEqual(l.SpanStatus, map[string]string{"value": "uncertain"}) {
		t.Errorf("label = %+v, want null value with status uncertain", l)
	}

	// A span null for the type: forced null, no status entry.
	mustSetType(t, s, mIdxGift, "gift")
	mustSetSpan(t, s, mIdxGift, "target", 5, 8)
	mustMark(t, s, mIdxGift, StatusComplete)
	l, _ = s.Label(mIdxGift)
	if l.Spans["value"] != nil || l.Spans["target"] == nil || len(l.SpanStatus) != 0 {
		t.Errorf("gift label = %+v, want target only and no span_status", l)
	}

	// null_label_statuses nulls the type, every span and the span statuses.
	mustSetType(t, s, mIdxSkipped, "expense")
	mustSetSpan(t, s, mIdxSkipped, "target", 0, 3)
	mustSetSpan(t, s, mIdxSkipped, "value", 12, 16)
	mustSetSpanStatus(t, s, mIdxSkipped, "value", "uncertain")
	mustMark(t, s, mIdxSkipped, StatusSkipped)
	l, _ = s.Label(mIdxSkipped)
	if l.Type != nil || l.Spans["target"] != nil || l.Spans["value"] != nil || len(l.SpanStatus) != 0 {
		t.Errorf("skipped label = %+v, want everything null", l)
	}
	if len(l.Spans) != 2 {
		t.Errorf("skipped label Spans = %v, want one entry per declared span", l.Spans)
	}
	want := `{"id":"m4","annotation_status":"skipped","type":null,"target":null,"value":null}`
	if !slices.Contains(f.outLines(t), want) {
		t.Errorf("labels file lacks %q:\n%s", want, fileString(t, f.out))
	}
	if d := s.Draft(mIdxSkipped); !isEmptyDraft(d) || s.Dirty(mIdxSkipped) {
		t.Errorf("draft after skipped mark = %+v", d)
	}

	// uncertain is not a null-label status: it keeps what it carries.
	mustSetType(t, s, mIdxTransfer, "transfer")
	mustSetSpan(t, s, mIdxTransfer, "value", 3, 6)
	mustMark(t, s, mIdxTransfer, StatusUncertain)
	l, _ = s.Label(mIdxTransfer)
	if l.Type == nil || l.Spans["value"] == nil || l.SpanStatus["value"] != "complete" {
		t.Errorf("uncertain label = %+v", l)
	}
}

func TestSessionSpanMapsAreDeepCopies(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	i := mIdxPizza
	mustSetType(t, s, i, "expense")
	mustSetSpan(t, s, i, "target", 13, 21)
	mustSetSpan(t, s, i, "value", 22, 26)
	mustSetSpanStatus(t, s, i, "value", "uncertain")
	mustMark(t, s, i, StatusComplete)

	d := s.Draft(i)
	d.Spans["target"].Text = "mutated"
	d.Spans["value"] = nil
	d.SpanStatus["value"] = "complete"
	l, _ := s.Label(i)
	l.Spans["target"].Text = "mutated"
	l.Spans["value"] = nil
	l.SpanStatus["value"] = "complete"
	*l.Type = "income"

	l2, _ := s.Label(i)
	d2 := s.Draft(i)
	if l2.Spans["target"].Text != "Pizza 4P" || l2.Spans["value"] == nil || l2.SpanStatus["value"] != "uncertain" || *l2.Type != "expense" {
		t.Errorf("label changed through a copy: %+v", l2)
	}
	if d2.Spans["target"].Text != "Pizza 4P" || d2.Spans["value"] == nil || d2.SpanStatus["value"] != "uncertain" || s.Dirty(i) {
		t.Errorf("draft changed through a copy: %+v dirty=%v", d2, s.Dirty(i))
	}

	// Undo keeps its own copy of the replaced label.
	mustSetSpanStatus(t, s, i, "value", "complete")
	mustMark(t, s, i, StatusUncertain)
	if _, _, ok, err := s.Undo(); !ok || err != nil {
		t.Fatalf("Undo = %v, %v", ok, err)
	}
	if l3, _ := s.Label(i); l3.Status != StatusComplete || l3.SpanStatus["value"] != "uncertain" {
		t.Errorf("Undo restored %+v", l3)
	}
}

// TestSessionOffsetsNonASCIIAndNFD: offsets of a second span are code points of the text as typed, without any
// Unicode normalization, for precomposed (Vietnamese) and decomposed (NFD) text alike.
func TestSessionOffsetsNonASCIIAndNFD(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)

	// m1: precomposed text; byte offsets of "300k" would be 27..31, code points are 22..26.
	if b := strings.Index(multiQueue[mIdxPizza].Text, "300k"); b != 27 {
		t.Fatalf("fixture: byte offset of 300k = %d, want 27", b)
	}
	mustSetType(t, s, mIdxPizza, "expense")
	mustSetSpan(t, s, mIdxPizza, "value", 22, 26)
	if got := s.Draft(mIdxPizza).Spans["value"]; got == nil || *got != (Target{Text: "300k", Start: 22, End: 26}) {
		t.Fatalf("value = %+v", got)
	}
	if err := s.SetSpan(mIdxPizza, "value", 27, 31); err == nil {
		t.Error("byte offsets accepted as code points")
	}

	// m2: NFD text. "café" is 5 code points (e + U+0301); "50k" starts at code point 10, byte 11.
	if b := strings.Index(nfdText, "50k"); b != 11 {
		t.Fatalf("fixture: byte offset of 50k = %d, want 11", b)
	}
	mustSetType(t, s, mIdxNFD, "income")
	mustSetSpan(t, s, mIdxNFD, "target", 0, 5)
	mustSetSpan(t, s, mIdxNFD, "value", 10, 13)
	d := s.Draft(mIdxNFD)
	if got := d.Spans["target"]; got == nil || *got != (Target{Text: "cafe\u0301", Start: 0, End: 5}) {
		t.Errorf("target = %+v, want the NFD café as 5 code points", got)
	}
	if got := d.Spans["value"]; got == nil || *got != (Target{Text: "50k", Start: 10, End: 13}) {
		t.Errorf("value = %+v", got)
	}
	// The combining mark is a code point of its own: [0,4) is "cafe" and [4,5) the bare accent.
	mustSetSpan(t, s, mIdxNFD, "target", 0, 4)
	if got := s.Draft(mIdxNFD).Spans["target"]; got == nil || got.Text != "cafe" {
		t.Errorf("target [0,4) = %+v, want cafe", got)
	}
	mustSetSpan(t, s, mIdxNFD, "target", 4, 5)
	if got := s.Draft(mIdxNFD).Spans["target"]; got == nil || got.Text != "\u0301" {
		t.Errorf("target [4,5) = %+v, want the bare accent", got)
	}
	mustSetSpan(t, s, mIdxNFD, "target", 0, 5)
	mustMark(t, s, mIdxNFD, StatusComplete)

	want := `{"id":"m2","annotation_status":"complete","type":"income","target":{"text":"cafe` + "\u0301" +
		`","start":0,"end":5},"value":{"text":"50k","start":10,"end":13},"span_status":{"value":"complete"}}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("labels file = %q, want %q", got, want)
	}

	// The saved NFC form of the same label does not validate against the NFD text (no normalization).
	nfc := mustParseSchema(t, multiSchemaYAML)
	bad := Label{Status: "complete", Type: new("income"), Spans: map[string]*Target{"target": {Text: "caf\u00e9", Start: 0, End: 4}}}
	if err := nfc.Validate(bad, nfdText); err == nil || !strings.Contains(err.Error(), "target: text[0:4] is ") {
		t.Errorf("Validate NFC span over NFD text = %v", err)
	}

	// Reopening validates the stored offsets against the text again.
	r := f.open(t)
	if l, ok := r.Label(mIdxNFD); !ok || *l.Spans["target"] != (Target{Text: "cafe\u0301", Start: 0, End: 5}) {
		t.Errorf("reopened label = %+v, %v", l, ok)
	}
}

// TestSessionExportBytes: the labels file (also the export) holds one line per label in queue order with the keys in
// the contract order: id, annotation_status, type, spans in schema order, span_status, note.
func TestSessionExportBytes(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)

	// Mark out of queue order: the file is rewritten in queue order.
	mustSetType(t, s, mIdxGift, "gift")
	mustSetSpan(t, s, mIdxGift, "target", 5, 8)
	mustMark(t, s, mIdxGift, StatusUncertain)

	mustSetType(t, s, mIdxSkipped, "expense")
	mustSetSpan(t, s, mIdxSkipped, "value", 12, 16)
	mustMark(t, s, mIdxSkipped, StatusSkipped)

	mustSetType(t, s, mIdxTransfer, "transfer")
	if err := s.SetSpan(mIdxTransfer, "target", 0, 2); err == nil { // null for transfer
		t.Fatal("SetSpan accepted a target on transfer")
	}
	mustSetSpan(t, s, mIdxTransfer, "value", 3, 6)
	mustSetSpanStatus(t, s, mIdxTransfer, "value", "uncertain")
	mustMark(t, s, mIdxTransfer, StatusComplete)

	mustSetType(t, s, mIdxNFD, "income")
	mustSetSpan(t, s, mIdxNFD, "target", 6, 9)
	mustSetSpan(t, s, mIdxNFD, "value", 10, 13)
	if got, err := s.CycleSpanStatus(mIdxNFD, "value"); err != nil || got != "uncertain" {
		t.Fatalf("cycle = %q, %v", got, err)
	}
	mustMark(t, s, mIdxNFD, StatusComplete)

	mustSetType(t, s, mIdxPizza, "expense")
	mustSetSpan(t, s, mIdxPizza, "target", 13, 21)
	mustSetSpan(t, s, mIdxPizza, "value", 22, 26)
	mustMark(t, s, mIdxPizza, StatusComplete)

	want := strings.Join([]string{
		`{"id":"m1","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21},"value":{"text":"300k","start":22,"end":26},"span_status":{"value":"complete"}}`,
		`{"id":"m2","annotation_status":"complete","type":"income","target":{"text":"Nam","start":6,"end":9},"value":{"text":"50k","start":10,"end":13},"span_status":{"value":"uncertain"}}`,
		`{"id":"m3","annotation_status":"complete","type":"transfer","target":null,"value":{"text":"5tr","start":3,"end":6},"span_status":{"value":"uncertain"}}`,
		`{"id":"m4","annotation_status":"skipped","type":null,"target":null,"value":null}`,
		`{"id":"m5","annotation_status":"uncertain","type":"gift","target":{"text":"Nam","start":5,"end":8},"value":null}`,
	}, "\n") + "\n"
	if got := fileString(t, f.out); got != want {
		t.Errorf("labels file =\n%s\nwant\n%s", got, want)
	}

	// Writing the labels again through WriteLabels gives the same bytes, and the file reloads to equal labels.
	schema := s.Schema()
	exported := filepath.Join(f.dir, "export.jsonl")
	labels, err := LoadLabels(f.out, schema, multiQueue)
	if err != nil {
		t.Fatalf("LoadLabels: %v", err)
	}
	if err := WriteLabels(exported, schema, multiQueue, labels); err != nil {
		t.Fatalf("WriteLabels: %v", err)
	}
	if got := fileString(t, exported); got != want {
		t.Errorf("export =\n%s\nwant\n%s", got, want)
	}
	for i := range multiQueue {
		l, _ := s.Label(i)
		if !labelEqual(l, labels[multiQueue[i].ID]) {
			t.Errorf("label %d: session %+v, reloaded %+v", i, l, labels[multiQueue[i].ID])
		}
	}
}

// TestSessionResumePartialMultiSpan: a partially labelled file (shuffled, one line without span_status) opens with
// its labels, the cursor on the first unfinished record, and further marks rewrite the file in queue order.
func TestSessionResumePartialMultiSpan(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 4)
	m1 := `{"id":"m1","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21},"value":{"text":"300k","start":22,"end":26}}`
	m2 := `{"id":"m2","annotation_status":"complete","type":"income","target":{"text":"Nam","start":6,"end":9},"value":{"text":"50k","start":10,"end":13},"span_status":{"value":"uncertain"},"note":"ghi chú"}`
	if err := os.WriteFile(f.out, []byte(m2+"\n"+m1+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := f.open(t)
	if s.Cursor() != mIdxTransfer {
		t.Fatalf("cursor = %d, want the first unfinished record %d", s.Cursor(), mIdxTransfer)
	}
	if c := s.Counts(); c.Complete != 2 || c.Remaining != 2 {
		t.Errorf("counts = %+v", c)
	}
	l1, _ := s.Label(mIdxPizza)
	if l1.Spans["value"] == nil || len(l1.SpanStatus) != 0 {
		t.Errorf("m1 = %+v, want the value span and no span status", l1)
	}
	if st, def := s.SpanStatus(mIdxPizza, "value"); st != "complete" || !def || s.Dirty(mIdxPizza) {
		t.Errorf("m1 value status = %q, %v dirty=%v; want the default, clean", st, def, s.Dirty(mIdxPizza))
	}
	l2, _ := s.Label(mIdxNFD)
	if l2.Note != "ghi chú" || l2.SpanStatus["value"] != "uncertain" || *l2.Spans["target"] != (Target{Text: "Nam", Start: 6, End: 9}) {
		t.Errorf("m2 = %+v", l2)
	}
	if got := fileString(t, f.out); got != m2+"\n"+m1+"\n" {
		t.Fatalf("Open rewrote the labels file: %q", got)
	}

	// Mark the rest. The first mark rewrites the whole file in queue order; m1 keeps no span_status.
	mustSetType(t, s, mIdxTransfer, "transfer")
	mustSetSpan(t, s, mIdxTransfer, "value", 3, 6)
	mustMark(t, s, mIdxTransfer, StatusComplete)
	mustMark(t, s, mIdxSkipped, StatusSkipped)
	want := strings.Join([]string{
		m1,
		m2,
		`{"id":"m3","annotation_status":"complete","type":"transfer","target":null,"value":{"text":"5tr","start":3,"end":6},"span_status":{"value":"complete"}}`,
		`{"id":"m4","annotation_status":"skipped","type":null,"target":null,"value":null}`,
	}, "\n") + "\n"
	if got := fileString(t, f.out); got != want {
		t.Errorf("labels file =\n%s\nwant\n%s", got, want)
	}
	if s.Advance() {
		t.Error("Advance found an unfinished record in a finished queue")
	}

	// Reopening a finished file: nothing unfinished, cursor stays on the first record.
	r := f.open(t)
	if r.Cursor() != 0 || r.Counts().Remaining != 0 {
		t.Errorf("reopened cursor %d counts %+v", r.Cursor(), r.Counts())
	}
}

// TestRecheckMultiSpan: a re-check session over a multi-span canonical file keeps every other line byte for byte,
// re-encodes only edited labels and Undo restores the original bytes.
func TestRecheckMultiSpan(t *testing.T) {
	f := newFixtureFor(t, multiSchemaYAML, multiQueue[mIdxNFD:mIdxTransfer+1]) // m2, m3
	outside := `{"id": "m1", "annotation_status": "complete", "type": "expense", "target": {"text": "Pizza 4P", "start": 13, "end": 21}, "value": {"text": "300k", "start": 22, "end": 26}, "span_status": {"value": "complete"}}`
	inQueue := `{"id": "m2", "annotation_status": "complete", "type": "income", "target": {"text": "cafe\u0301", "start": 0, "end": 5}, "value": {"text": "50k", "start": 10, "end": 13}, "span_status": {"value": "uncertain"}}`
	original := outside + "\n" + inQueue + "\n"
	if err := os.WriteFile(f.out, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := OpenRecheck(f.queue, f.schema, f.out)
	if err != nil {
		t.Fatalf("OpenRecheck: %v", err)
	}
	if s.Len() != 2 || s.LabelsTotal() != 2 || s.LabelsInQueue() != 1 {
		t.Fatalf("len %d total %d in queue %d", s.Len(), s.LabelsTotal(), s.LabelsInQueue())
	}
	l, _ := s.Label(0)
	if *l.Spans["target"] != (Target{Text: "cafe\u0301", Start: 0, End: 5}) || l.SpanStatus["value"] != "uncertain" {
		t.Errorf("m2 = %+v", l)
	}

	// Mark m3 (new label) and change m2's value status.
	mustSetType(t, s, 1, "transfer")
	mustSetSpan(t, s, 1, "value", 3, 6)
	mustMark(t, s, 1, StatusComplete)
	m3 := `{"id":"m3","annotation_status":"complete","type":"transfer","target":null,"value":{"text":"5tr","start":3,"end":6},"span_status":{"value":"complete"}}`
	if got := fileString(t, f.out); got != original+m3+"\n" {
		t.Fatalf("after marking m3:\n%s", got)
	}
	// Marking m2 unchanged keeps its hand-formatted bytes.
	mustMark(t, s, 0, StatusComplete)
	if got := fileString(t, f.out); got != original+m3+"\n" {
		t.Fatalf("after re-marking m2 unchanged:\n%s", got)
	}
	if got, err := s.CycleSpanStatus(0, "value"); err != nil || got != "complete" {
		t.Fatalf("cycle = %q, %v", got, err)
	}
	mustMark(t, s, 0, StatusComplete)
	m2 := `{"id":"m2","annotation_status":"complete","type":"income","target":{"text":"cafe` + "\u0301" + `","start":0,"end":5},"value":{"text":"50k","start":10,"end":13},"span_status":{"value":"complete"}}`
	if got := fileString(t, f.out); got != outside+"\n"+m2+"\n"+m3+"\n" {
		t.Fatalf("after changing m2:\n%s", got)
	}

	// Undo twice restores m2's original bytes (the unchanged re-mark is one more undo step), then Undo of m3 empties.
	for range 3 {
		if _, _, ok, err := s.Undo(); !ok || err != nil {
			t.Fatalf("Undo = %v, %v", ok, err)
		}
	}
	if got := fileString(t, f.out); got != original {
		t.Errorf("after undoing everything:\n%s\nwant\n%s", got, original)
	}
}

func TestLoadLabelsMultiSpanDecode(t *testing.T) {
	const prefix = `{"id":"m1","annotation_status":"complete","type":"expense"`
	const target = `"target":{"text":"Pizza 4P","start":13,"end":21}`
	const value = `"value":{"text":"300k","start":22,"end":26}`
	tests := []struct {
		name, content, want string
	}{
		{"missing span key", prefix + "," + target + `}`, `:1: missing field "value"`},
		{"missing first span key", prefix + "," + value + `}`, `:1: missing field "target"`},
		{"unknown key", prefix + "," + target + "," + value + `,"price":null}`, `:1: unknown field(s) ["price"]`},
		{"span not an object", prefix + "," + target + `,"value":"300k"}`, ":1: value: expected an object or null"},
		{"span unknown member", prefix + "," + target + `,"value":{"text":"300k","start":22,"end":26,"x":1}}`, `:1: value: unknown field(s) ["x"]`},
		{"span missing member", prefix + "," + target + `,"value":{"text":"300k","start":22}}`, `:1: value: missing field "end"`},
		{"span text not a string", prefix + "," + target + `,"value":{"text":1,"start":22,"end":26}}`, ":1: value.text: expected a string"},
		{"span fractional offset", prefix + "," + target + `,"value":{"text":"300k","start":22.5,"end":26}}`, ":1: value.start: expected an integer"},
		{"span null offset", prefix + "," + target + `,"value":{"text":"300k","start":22,"end":null}}`, ":1: value.end: expected an integer"},
		{"first span prefixed too", prefix + `,"target":"x",` + value + `}`, ":1: target: expected an object or null"},
		{"span_status not an object", prefix + "," + target + "," + value + `,"span_status":"complete"}`, ":1: span_status: expected an object"},
		{"span_status null", prefix + "," + target + "," + value + `,"span_status":null}`, ":1: span_status: expected an object"},
		{"span_status value not a string", prefix + "," + target + "," + value + `,"span_status":{"value":1}}`, ":1: span_status.value: expected a string"},
		{"span_status invalid status", prefix + "," + target + "," + value + `,"span_status":{"value":"skipped"}}`, `:1: id "m1": span_status.value: "skipped" is not one of [complete, uncertain]`},
		{"span_status on a span without statuses", prefix + "," + target + "," + value + `,"span_status":{"target":"complete"}}`, "span_status.target: span declares no statuses"},
		{"span_status unknown span", prefix + "," + target + "," + value + `,"span_status":{"price":"complete"}}`, "span_status.price: not a declared span"},
		{"span_status on null-for-type span", `{"id":"m5","annotation_status":"complete","type":"gift","target":{"text":"Nam","start":5,"end":8},"value":null,"span_status":{"value":"complete"}}`,
			`id "m5": span_status.value: must be absent for type "gift"`},
		{"span null for type", `{"id":"m3","annotation_status":"complete","type":"transfer","target":{"text":"ck","start":0,"end":2},"value":null}`, `id "m3": target: must be null for type "transfer"`},
		{"second span wrong slice", prefix + "," + target + `,"value":{"text":"300k","start":21,"end":25}}`, `id "m1": value: text[21:25] is " 300", not "300k"`},
		{"second span beyond text", prefix + "," + target + `,"value":{"text":"300k","start":40,"end":44}}`, "value: span end 44 is beyond the text length 26"},
	}
	s := mustParseSchema(t, multiSchemaYAML)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "labels.jsonl", tt.content)
			_, err := LoadLabels(path, s, multiQueue)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("err %q does not cite the file", err)
			}
		})
	}

	// span_status is not a label key unless some span declares statuses.
	noStatuses := mustParseSchema(t, "types: [a]\nstatuses: [complete]\nspans: [x, y]\n")
	path := writeFile(t, "labels.jsonl", `{"id":"m1","annotation_status":"complete","type":"a","x":null,"y":null,"span_status":{}}`)
	if _, err := LoadLabels(path, noStatuses, multiQueue); err == nil || !strings.Contains(err.Error(), `unknown field(s) ["span_status"]`) {
		t.Errorf("span_status without declared statuses: err = %v", err)
	}
	// A schema without spans: the old strict decoding, key "value" is unknown.
	path = writeFile(t, "labels.jsonl", `{"id":"a","annotation_status":"skipped","type":null,"target":null,"value":null}`)
	if _, err := LoadLabels(path, loadGidiSchema(t), labelQueue); err == nil || !strings.Contains(err.Error(), `:1: unknown field(s) ["value"]`) {
		t.Errorf("implicit schema with a value key: err = %v", err)
	}
}

func TestLoadLabelsMultiSpanValid(t *testing.T) {
	s := mustParseSchema(t, multiSchemaYAML)
	path := writeFile(t, "labels.jsonl", strings.Join([]string{
		`{"id":"m3","annotation_status":"complete","type":"transfer","target":null,"value":{"text":"5tr","start":3,"end":6},"span_status":{"value":"uncertain"}}`,
		`{"id":"m4","annotation_status":"skipped","type":null,"target":null,"value":null,"span_status":{}}`,
	}, "\n"))
	got, err := LoadLabels(path, s, multiQueue)
	if err != nil {
		t.Fatalf("LoadLabels: %v", err)
	}
	want := map[string]Label{
		"m3": {ID: "m3", Status: "complete", Type: new("transfer"),
			Spans:      map[string]*Target{"target": nil, "value": {Text: "5tr", Start: 3, End: 6}},
			SpanStatus: map[string]string{"value": "uncertain"}},
		"m4": {ID: "m4", Status: "skipped", Spans: map[string]*Target{"target": nil, "value": nil}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestProposalsMultiSpan: proposals carry one optional key per span and span_status; ProposalProblem, ApplyProposal,
// AcceptProposal and ProposalMatches honour the per-field rules.
func TestProposalsMultiSpan(t *testing.T) {
	f := newMultiFixture(t, multiSchemaYAML, 0)
	s := f.open(t)
	path := writeProposals(t, f.dir,
		`{"id":"m1","annotation_status":"complete","type":"expense","target":{"text":"Nam","start":7,"end":10},"value":{"text":"300k","start":22,"end":26},"span_status":{"value":"uncertain"},"confidence":0.9,"reason":"why","note":"n"}`,
		`{"id":"m2","annotation_status":"complete","type":"income","value":{"text":"50k","start":10,"end":13}}`,
		`{"id":"m3","annotation_status":"complete","type":"transfer","target":{"text":"ck","start":0,"end":2},"value":{"text":"5tr","start":3,"end":6},"span_status":{"value":"complete"}}`,
		`{"id":"m4","annotation_status":"complete","type":"expense","value":null,"span_status":{"target":"complete"}}`,
		`{"id":"m5","annotation_status":"complete","type":"gift","span_status":{"value":"uncertain"}}`,
	)
	if ignored, err := s.LoadProposals(path); err != nil || len(ignored) != 0 {
		t.Fatalf("LoadProposals = %v, %v", ignored, err)
	}

	p, _ := s.Proposal(mIdxPizza)
	conf := 0.9
	want := Proposal{ID: "m1", Status: StatusComplete, Type: new("expense"),
		Spans: map[string]*Target{
			"target": {Text: "Nam", Start: 7, End: 10},
			"value":  {Text: "300k", Start: 22, End: 26},
		},
		SpanStatus: map[string]string{"value": "uncertain"}, Note: "n", Confidence: &conf, Reason: "why"}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("Proposal = %+v, want %+v", p, want)
	}
	p2, _ := s.Proposal(mIdxNFD)
	if p2.Spans["target"] != nil || p2.Spans["value"] == nil || len(p2.SpanStatus) != 0 || len(p2.Spans) != 2 {
		t.Errorf("m2 proposal = %+v, want an absent target as null", p2)
	}
	// Proposal returns deep copies.
	p.Spans["target"].Text = "x"
	p.SpanStatus["value"] = "x"
	if again, _ := s.Proposal(mIdxPizza); !reflect.DeepEqual(again, want) {
		t.Errorf("Proposal after mutating a copy = %+v", again)
	}

	problems := []struct {
		idx  int
		want string
	}{
		{mIdxPizza, ""},
		{mIdxNFD, ""},
		{mIdxTransfer, `target: must be null for type "transfer"`},
		{mIdxSkipped, "span_status.target: span declares no statuses"},
		{mIdxGift, `span_status.value: must be absent for type "gift"`},
	}
	for _, tt := range problems {
		err := s.ProposalProblem(tt.idx)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("ProposalProblem(%d) = %v, want %q", tt.idx, err, tt.want)
		}
	}

	// Apply: null-for-type spans are dropped, the rest loads without saving.
	if err := s.ApplyProposal(mIdxTransfer); err != nil {
		t.Fatalf("ApplyProposal(transfer): %v", err)
	}
	d := s.Draft(mIdxTransfer)
	if d.Type != "transfer" || d.Spans["target"] != nil || d.Spans["value"] == nil || d.SpanStatus["value"] != "complete" || !s.Dirty(mIdxTransfer) {
		t.Errorf("draft = %+v dirty=%v", d, s.Dirty(mIdxTransfer))
	}
	if _, ok := s.Label(mIdxTransfer); ok {
		t.Error("ApplyProposal saved a label")
	}
	// A span status the schema refuses is refused with the draft untouched.
	if err := s.ApplyProposal(mIdxSkipped); err == nil || !strings.Contains(err.Error(), "span_status.target: span declares no statuses") {
		t.Errorf("ApplyProposal(m4) = %v", err)
	}
	if s.Dirty(mIdxSkipped) {
		t.Error("refused ApplyProposal changed the draft")
	}
	// A status on a span that is null for the type is dropped by Apply (the label would be refused by Accept).
	if err := s.ApplyProposal(mIdxGift); err != nil {
		t.Fatalf("ApplyProposal(gift): %v", err)
	}
	if d := s.Draft(mIdxGift); len(d.SpanStatus) != 0 || d.Type != "gift" {
		t.Errorf("gift draft = %+v", d)
	}
	// An invalid span is refused.
	bad := newMultiFixture(t, multiSchemaYAML, 0)
	bs := bad.open(t)
	badPath := writeProposals(t, bad.dir, `{"id":"m1","annotation_status":"complete","type":"expense","value":{"text":"300k","start":21,"end":25}}`)
	if _, err := bs.LoadProposals(badPath); err != nil {
		t.Fatal(err)
	}
	if err := bs.ApplyProposal(mIdxPizza); err == nil || !strings.Contains(err.Error(), `proposal value: text[21:25] is " 300", not "300k"`) {
		t.Errorf("ApplyProposal with a bad value span = %v", err)
	}

	// Accept saves the proposal as Mark would.
	if err := s.AcceptProposal(mIdxTransfer); err == nil {
		t.Error("AcceptProposal accepted a target on a null-for-type span")
	}
	if err := s.AcceptProposal(mIdxSkipped); err == nil {
		t.Error("AcceptProposal accepted a span_status on a span without statuses")
	}
	if _, err := os.Stat(f.out); !os.IsNotExist(err) {
		t.Errorf("refused accepts wrote the labels file: %v", err)
	}
	if err := s.AcceptProposal(mIdxPizza); err != nil {
		t.Fatalf("AcceptProposal: %v", err)
	}
	wantLine := `{"id":"m1","annotation_status":"complete","type":"expense","target":{"text":"Nam","start":7,"end":10},"value":{"text":"300k","start":22,"end":26},"span_status":{"value":"uncertain"},"note":"n"}`
	if got := f.outLines(t); !reflect.DeepEqual(got, []string{wantLine}) {
		t.Errorf("labels file = %q, want %q", got, wantLine)
	}
	if !s.ProposalMatches(mIdxPizza) {
		t.Error("ProposalMatches false right after accepting")
	}

	// An unset proposal span status is the default: accepting stores it and it still matches.
	if err := s.AcceptProposal(mIdxNFD); err != nil {
		t.Fatalf("AcceptProposal(m2): %v", err)
	}
	if l, _ := s.Label(mIdxNFD); l.SpanStatus["value"] != "complete" || l.Spans["target"] != nil {
		t.Errorf("m2 label = %+v", l)
	}
	if !s.ProposalMatches(mIdxNFD) {
		t.Error("ProposalMatches false for a proposal without span_status vs the default")
	}

	// Editing a span status or a span breaks the match.
	mustSetSpanStatus(t, s, mIdxPizza, "value", "complete")
	mustMark(t, s, mIdxPizza, StatusComplete)
	if s.ProposalMatches(mIdxPizza) {
		t.Error("ProposalMatches true for a different span status")
	}
	if _, _, ok, err := s.Undo(); !ok || err != nil {
		t.Fatalf("Undo: %v %v", ok, err)
	}
	if !s.ProposalMatches(mIdxPizza) {
		t.Error("ProposalMatches false after undoing the edit")
	}
	mustSetSpan(t, s, mIdxPizza, "target", 0, 2)
	mustMark(t, s, mIdxPizza, StatusComplete)
	if s.ProposalMatches(mIdxPizza) {
		t.Error("ProposalMatches true for a different target")
	}
}

// TestProposalsSpanStatusIgnoredWithoutSpanStatuses: a schema that declares no span statuses (all schemas without
// spans) ignores span_status in proposals as it ignored every other unknown member.
func TestProposalsSpanStatusIgnoredWithoutSpanStatuses(t *testing.T) {
	f := newFixture(t, "")
	s := f.open(t)
	path := writeProposals(t, f.dir, `{"id":"case-lend","annotation_status":"complete","type":"lend","target":null,"span_status":"whatever"}`)
	if _, err := s.LoadProposals(path); err != nil {
		t.Fatalf("LoadProposals: %v", err)
	}
	if err := s.ProposalProblem(idxLend); err != nil {
		t.Errorf("ProposalProblem = %v", err)
	}
	// span_status of the wrong shape is an error once some span declares statuses.
	m := newMultiFixture(t, multiSchemaYAML, 0)
	ms := m.open(t)
	mpath := writeProposals(t, m.dir, `{"id":"m1","annotation_status":"complete","span_status":"x"}`)
	if _, err := ms.LoadProposals(mpath); err == nil || !strings.Contains(err.Error(), "span_status: expected an object") {
		t.Errorf("LoadProposals = %v", err)
	}
}

// TestImplicitSchemaLabelLinesRoundTripByteIdentical: for a schema without spans the label encoder emits exactly the
// bytes of the former struct encoding (json.Encoder, HTML escaping off), and decoding then encoding a canonical line
// returns it byte for byte.
func TestImplicitSchemaLabelLinesRoundTripByteIdentical(t *testing.T) {
	schema := loadGidiSchema(t)
	lines := []string{
		`{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11}}`,
		`{"id":"a","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":8,"end":11},"note":"với <Nam> & bạn"}`,
		`{"id":"b","annotation_status":"complete","type":"transfer","target":null}`,
		`{"id":"c","annotation_status":"uncertain","type":null,"target":null,"note":"<ambiguous & odd>"}`,
		`{"id":"c","annotation_status":"skipped","type":null,"target":null,"note":"line\u2028sep \"q\" \\ \n tab\t"}`,
		`{"id":"c","annotation_status":"approved","type":"?","target":{"text":"ă","start":0,"end":1}}`,
	}
	type legacyLabel struct {
		ID     string  `json:"id"`
		Status string  `json:"annotation_status"`
		Type   *string `json:"type"`
		Target *Target `json:"target"`
		Note   string  `json:"note,omitempty"`
	}
	for _, line := range lines {
		l, err := decodeLabel(schema, []byte(line))
		if err != nil {
			t.Fatalf("decodeLabel(%s): %v", line, err)
		}
		var got bytes.Buffer
		if err := encodeLabel(&got, schema, l.ID, l); err != nil {
			t.Fatalf("encodeLabel: %v", err)
		}
		if got.String() != line+"\n" {
			t.Errorf("round trip\n got %q\nwant %q", got.String(), line+"\n")
		}
		var legacy bytes.Buffer
		enc := json.NewEncoder(&legacy)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(legacyLabel{ID: l.ID, Status: l.Status, Type: l.Type, Target: l.Spans["target"], Note: l.Note}); err != nil {
			t.Fatal(err)
		}
		if got.String() != legacy.String() {
			t.Errorf("encoding differs from the legacy struct encoding\n got %q\nwant %q", got.String(), legacy.String())
		}
	}

	// The same through a session: an old file's lines survive a rewrite triggered by another record's mark.
	f := newFixture(t, "")
	canonical := strings.Join([]string{
		`{"id":"case-lend","annotation_status":"complete","type":"lend","target":{"text":"Nam","start":4,"end":7},"note":"nợ <&> ă"}`,
		`{"id":"case-expense","annotation_status":"complete","type":"expense","target":{"text":"Pizza 4P","start":13,"end":21}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(f.out, []byte(canonical), 0o644); err != nil {
		t.Fatal(err)
	}
	s := f.open(t)
	mustMark(t, s, idxUncertain, StatusUncertain)
	want := canonical + `{"id":"case-uncertain","annotation_status":"uncertain","type":null,"target":null}` + "\n"
	if got := fileString(t, f.out); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if got := s.Schema().Spans; len(got) != 1 || got[0].Name != "target" {
		t.Errorf("Spans = %+v", got)
	}
}

func TestWriteLabelsStraysAndOrderWithMultiSpan(t *testing.T) {
	schema := mustParseSchema(t, multiSchemaYAML)
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	labels := map[string]Label{
		"m4": {ID: "m4", Status: "skipped"},
		"m1": {ID: "m1", Status: "complete", Type: new("expense"),
			Spans:      map[string]*Target{"value": {Text: "300k", Start: 22, End: 26}, "target": nil},
			SpanStatus: map[string]string{"value": "complete", "ghost": "x"}},
	}
	if err := WriteLabels(path, schema, multiQueue, labels); err != nil {
		t.Fatalf("WriteLabels: %v", err)
	}
	want := `{"id":"m1","annotation_status":"complete","type":"expense","target":null,"value":{"text":"300k","start":22,"end":26},"span_status":{"value":"complete"}}` + "\n" +
		`{"id":"m4","annotation_status":"skipped","type":null,"target":null,"value":null}` + "\n"
	if got := fileString(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	err := WriteLabels(path, schema, multiQueue, map[string]Label{"zz": {ID: "zz", Status: "skipped"}})
	if err == nil || !strings.Contains(err.Error(), `"zz"`) {
		t.Errorf("err = %v, want stray id error", err)
	}
}

func TestLabelEqualSpans(t *testing.T) {
	base := func() Label {
		return Label{ID: "x", Status: "complete", Type: new("expense"),
			Spans:      map[string]*Target{"target": {Text: "a", Start: 0, End: 1}, "value": nil},
			SpanStatus: map[string]string{"value": "complete"}}
	}
	tests := []struct {
		name   string
		change func(*Label)
		equal  bool
	}{
		{"identical", func(*Label) {}, true},
		{"missing span equals null span", func(l *Label) { delete(l.Spans, "value") }, true},
		{"span status removed", func(l *Label) { l.SpanStatus = nil }, false},
		{"span text", func(l *Label) { l.Spans["target"].Text = "b" }, false},
		{"span offsets", func(l *Label) { l.Spans["target"].End = 2 }, false},
		{"span nulled", func(l *Label) { l.Spans["target"] = nil }, false},
		{"span added", func(l *Label) { l.Spans["value"] = &Target{Text: "v", Start: 2, End: 3} }, false},
		{"span status changed", func(l *Label) { l.SpanStatus["value"] = "uncertain" }, false},
		{"span status added", func(l *Label) { l.SpanStatus["target"] = "complete" }, false},
		{"note", func(l *Label) { l.Note = "n" }, false},
		{"type", func(l *Label) { l.Type = new("income") }, false},
		{"type nulled", func(l *Label) { l.Type = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := base(), base()
			tt.change(&b)
			if got := labelEqual(a, b); got != tt.equal {
				t.Errorf("labelEqual = %v, want %v", got, tt.equal)
			}
			if got := labelEqual(b, a); got != tt.equal {
				t.Errorf("labelEqual (swapped) = %v, want %v", got, tt.equal)
			}
		})
	}
	empty := base()
	empty.SpanStatus = map[string]string{}
	none := base()
	none.SpanStatus = nil
	if !labelEqual(empty, none) {
		t.Error("empty and nil span statuses differ")
	}
}
