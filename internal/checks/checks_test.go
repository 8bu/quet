package checks

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDefaultOptions(t *testing.T) {
	opt := DefaultOptions()
	want := Options{MaxChars: 160, RepeatedCharThreshold: 5, WeirdSymbolRatio: 0.35, TemplateMinOccurrences: 8}
	if opt != want {
		t.Fatalf("DefaultOptions() = %+v, want %+v", opt, want)
	}
}

func TestOptionsZeroFieldsUseDefaults(t *testing.T) {
	long := strings.Repeat("a", 200)
	flags := Single(long, Options{})
	detail, ok := flagDetail(flags, TooLong)
	if !ok {
		t.Fatalf("Single(%q, Options{}) = %v, want a too_long flag", long, flags)
	}
	if want := "200 chars, limit 160"; detail != want {
		t.Errorf("too_long detail = %q, want %q", detail, want)
	}
	if _, ok := flagDetail(Single(long, Options{MaxChars: 1000}), TooLong); ok {
		t.Errorf("Single with MaxChars 1000 still flagged too_long: %v", Single(long, Options{MaxChars: 1000}))
	}
	if _, ok := flagDetail(Single("ck Nam 2tr", Options{}), RepeatedChars); ok {
		t.Errorf("Single with zero threshold flagged repeated_chars: %v", Single("ck Nam 2tr", Options{}))
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"case and whitespace", "CK   Nam\t2tr", "ck nam 2tr"},
		{"trim", "  cho Nam vay 2tr  ", "cho nam vay 2tr"},
		{"newlines collapse", "cho\r\nNam\n\nvay 2tr", "cho nam vay 2tr"},
		{"nfc composition", "cafe\u0301 vo\u031b\u0301i", "caf\u00e9 v\u1edbi"},
		{"already normalized", "ck nam 2tr", "ck nam 2tr"},
		{"blank", " \t\n ", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("%s: Normalize(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
	if Normalize("CK Nam 2tr") != Normalize("ck   nam 2tr") {
		t.Errorf("Normalize should equate %q and %q", "CK Nam 2tr", "ck   nam 2tr")
	}
}

func TestHasAmount(t *testing.T) {
	amounts := []string{
		"50k", "2tr", "1tr2", "12tr5", "2 triệu", "2 củ", "5 lít", "1.5tr", "1.2m",
		"500000", "500.000", "500,000", "$20", "20usd", "200k vnd", "+20tr", "-500k",
		"cho Nam vay 2tr", "mượn bà Hoa 500k đóng học phí cho con",
		"nhận lương tháng 7 được 12tr5, trả nợ 3tr", "💰💸🙏 thanks bro 500000",
		"5 l", "2 TRIỆU", "trả 1tr2 tiền điện",
	}
	for _, s := range amounts {
		if !HasAmount(s) {
			t.Errorf("HasAmount(%q) = false, want true", s)
		}
	}
	nonAmounts := []string{
		"", "abc", "04/2026", "ngày 04/2026", "iphone 15", "12", "1.2", "1,5",
		"không có gì", "cho tôi xin lại", "chương trình khuyến mãi",
	}
	for _, s := range nonAmounts {
		if HasAmount(s) {
			t.Errorf("HasAmount(%q) = true, want false", s)
		}
	}
}

func TestHasRepeatedChars(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		threshold int
		want      bool
	}{
		{"stretched word", "ckkkkkkkkk Nam 2tr", 5, true},
		{"one letter", "aaaaaaa", 5, true},
		{"question marks", "????????", 5, true},
		{"case insensitive run", "kKkKk", 5, true},
		{"clean", "ck Nam 2tr", 5, false},
		{"empty", "", 5, false},
		{"short word", "abc", 5, false},
		{"whitespace run does not count", "a     b", 5, false},
		{"exactly at threshold", "aaaaa", 5, true},
		{"just below threshold", "aaaaa", 6, false},
		{"zero threshold means default", "aaaaa", 0, true},
		{"negative threshold means default", "aaaa", -3, false},
	}
	for _, tt := range tests {
		if got := HasRepeatedChars(tt.in, tt.threshold); got != tt.want {
			t.Errorf("%s: HasRepeatedChars(%q, %d) = %v, want %v", tt.name, tt.in, tt.threshold, got, tt.want)
		}
	}
}

func TestSymbolRatio(t *testing.T) {
	limit := DefaultOptions().WeirdSymbolRatio
	clean := []string{
		"", "   ",
		"$20", "+20tr", "-500k", "1.2tr", "2 củ", "500.000",
		"cho Nam vay 2tr", "bắn thg Nam 2 củ tiền hôm nọ",
		"mượn bà Hoa 500k đóng học phí cho con",
	}
	for _, s := range clean {
		if got := SymbolRatio(s); got != 0 {
			t.Errorf("SymbolRatio(%q) = %v, want 0", s, got)
		}
	}
	// Ordinary punctuation (",", ".") is symbolic but stays far below the limit.
	low := []string{
		"nhận lương tháng 7 được 12tr5, trả nợ 3tr",
		"cho vay nóng 5 lít, hẹn mai trả",
	}
	for _, s := range low {
		if got := SymbolRatio(s); got > limit {
			t.Errorf("SymbolRatio(%q) = %v, want <= %v", s, got, limit)
		}
	}
	weird := []string{"💰💸🙏 thanks bro", "????????"}
	for _, s := range weird {
		got := SymbolRatio(s)
		if got <= limit {
			t.Errorf("SymbolRatio(%q) = %v, want > %v", s, got, limit)
		}
		if got > 1 {
			t.Errorf("SymbolRatio(%q) = %v, want <= 1", s, got)
		}
	}
}

func TestTemplatePattern(t *testing.T) {
	tests := []struct{ in, want string }{
		{"cho Nam vay 2tr", "cho <TOKEN> vay <AMOUNT>"},
		{"cho Linh vay 3tr", "cho <TOKEN> vay <AMOUNT>"},
		{"cho Hùng vay 5tr", "cho <TOKEN> vay <AMOUNT>"},
		{"cho Mai vay 1tr", "cho <TOKEN> vay <AMOUNT>"},
		{"Cho Nam vay 2tr", "cho <TOKEN> vay <AMOUNT>"},
		{"cho   Nam   vay   2tr", "cho <TOKEN> vay <AMOUNT>"},
		{"CK Nam 2tr", "ck <TOKEN> <AMOUNT>"},
		{"ck   nam 2tr", "ck nam <AMOUNT>"},
		{"bắn thg Nam 2 củ tiền hôm nọ", "bắn thg <TOKEN> <AMOUNT> tiền hôm nọ"},
		{"mượn bà Hoa 500k đóng học phí cho con", "mượn bà <TOKEN> <AMOUNT> đóng học phí cho con"},
		{"nhận lương tháng 7 được 12tr5, trả nợ 3tr", "nhận lương tháng <NUM> được <AMOUNT> , trả nợ <AMOUNT>"},
		{"ngày 04/2026 trả nợ", "ngày <NUM>/<NUM> trả nợ"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := TemplatePattern(tt.in); got != tt.want {
			t.Errorf("TemplatePattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// longNote is a >160 rune duplicate fixture with no amount and no name token.
const longNote = "chuyển khoản trả nợ thẻ tín dụng số tiền là hai mươi triệu đồng tiền gốc và tiền lãi đã thỏa thuận với nhau từ trước nhưng đến nay vẫn chưa thanh toán đầy đủ nên hẹn cuối tháng này sẽ trả hết một lần cho xong nhé"

func TestAnalyze(t *testing.T) {
	texts := []string{
		"cho Nam vay 2tr",
		"cho Linh vay 3tr",
		"cho Hùng vay 5tr",
		"cho Mai vay 1tr",
		"cho Hoa vay 10tr",
		"cho Tuấn vay 7tr",
		"cho Bình vay 4tr",
		"cho Lan vay 6tr",
		longNote,
		"  " + longNote + "  ",
		strings.ReplaceAll(longNote, " ", "  "),
		"",
	}
	if n := utf8.RuneCountInString(Normalize(longNote)); n <= DefaultOptions().MaxChars {
		t.Fatalf("fixture longNote is %d runes, must exceed MaxChars", n)
	}

	a := Analyze(texts, Options{})
	if len(a.Flags) != len(texts) || len(a.DupGroup) != len(texts) || len(a.Pattern) != len(texts) {
		t.Fatalf("per-record slices = %d/%d/%d, want %d", len(a.Flags), len(a.DupGroup), len(a.Pattern), len(texts))
	}

	if len(a.Groups) != 1 || len(a.Groups[0]) != 3 {
		t.Fatalf("Groups = %v, want one group of 3", a.Groups)
	}
	wantGroup := []int{8, 9, 10}
	for i, idx := range wantGroup {
		if a.Groups[0][i] != idx {
			t.Errorf("Groups[0] = %v, want %v", a.Groups[0], wantGroup)
		}
	}
	for i := range texts {
		want := int32(-1)
		if i >= 8 && i <= 10 {
			want = 0
		}
		if a.DupGroup[i] != want {
			t.Errorf("DupGroup[%d] = %d, want %d", i, a.DupGroup[i], want)
		}
	}

	// Record 8: duplicate + too long + no amount, in AllFlags order.
	got := flagNames(a.Flags[8])
	want := []string{Duplicate, TooLong, MissingAmount}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Flags[8] = %v, want %v", got, want)
	}
	if detail, _ := flagDetail(a.Flags[8], Duplicate); detail != "3 records share this text" {
		t.Errorf("duplicate detail = %q, want %q", detail, "3 records share this text")
	}
	n := utf8.RuneCountInString(Normalize(longNote))
	if detail, _ := flagDetail(a.Flags[8], TooLong); detail != strconv.Itoa(n)+" chars, limit 160" {
		t.Errorf("too_long detail = %q, want %d chars, limit 160", detail, n)
	}
	if a.Pattern[8] != "" {
		t.Errorf("Pattern[8] = %q, want empty (no substitution)", a.Pattern[8])
	}

	// Record 11: empty text only.
	if len(a.Flags[11]) != 1 || a.Flags[11][0] != (Flag{Name: Empty, Detail: "text is empty"}) {
		t.Errorf("Flags[11] = %v, want a single empty flag", a.Flags[11])
	}

	// Template family: 8 identical patterns reach the default threshold.
	pattern := "cho <TOKEN> vay <AMOUNT>"
	wantOccurrences := 8
	if len(a.Templates) != 1 || a.Templates[pattern] != wantOccurrences {
		t.Errorf("Templates = %v, want map[%s:%d]", a.Templates, pattern, wantOccurrences)
	}
	wantDetail := "pattern: cho <TOKEN> vay <AMOUNT> (8 occurrences)"
	for i := range 8 {
		if a.Pattern[i] != pattern {
			t.Errorf("Pattern[%d] = %q, want %q", i, a.Pattern[i], pattern)
		}
		if got := flagNames(a.Flags[i]); strings.Join(got, ",") != PossibleTemplate {
			t.Errorf("Flags[%d] = %v, want only %s", i, got, PossibleTemplate)
		}
		if detail, _ := flagDetail(a.Flags[i], PossibleTemplate); detail != wantDetail {
			t.Errorf("possible_template detail = %q, want %q", detail, wantDetail)
		}
	}
}

func TestAnalyzeTemplateThresholdUsesOption(t *testing.T) {
	texts := []string{"cho Nam vay 2tr", "cho Linh vay 3tr", "cho Mai vay 1tr"}
	a := Analyze(texts, Options{TemplateMinOccurrences: 3})
	if got := a.Templates["cho <TOKEN> vay <AMOUNT>"]; got != 3 {
		t.Fatalf("Templates = %v, want 3 occurrences", a.Templates)
	}
	for i := range texts {
		if _, ok := flagDetail(a.Flags[i], PossibleTemplate); !ok {
			t.Errorf("Flags[%d] = %v, want possible_template", i, a.Flags[i])
		}
	}
}

func TestSingleFlags(t *testing.T) {
	tests := []struct {
		name, text string
		opt        Options
		want       []string
	}{
		{"clean", "cho Nam vay 2tr", Options{}, nil},
		{"empty", "", Options{}, []string{Empty}},
		{"blank text is empty", "   \t ", Options{}, []string{Empty}},
		{"too long", strings.Repeat("cho vay ", 24), Options{}, []string{TooLong, MissingAmount}},
		{"repeated chars", "ckkkkkkkkk Nam 2tr", Options{}, []string{RepeatedChars}},
		{"weird symbols", "💰💸🙏 thanks bro 500000", Options{}, []string{WeirdSymbols}},
		{"digits do not repeat", "500000", Options{}, nil},
		{"missing amount", "cho tôi xin lại tiền", Options{}, []string{MissingAmount}},
		{
			"short max chars",
			"cho Nam vay 2tr và nữa",
			Options{MaxChars: 10},
			[]string{TooLong},
		},
	}
	for _, tt := range tests {
		got := flagNames(Single(tt.text, tt.opt))
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%s: Single(%q, %+v) = %v, want %v", tt.name, tt.text, tt.opt, got, tt.want)
		}
	}
	if detail, ok := flagDetail(Single("ckkkkkkkkk Nam 2tr", Options{}), RepeatedChars); !ok || !strings.Contains(detail, "9 times") {
		t.Errorf("repeated_chars detail = %q, want the offending run length", detail)
	}
	if detail, ok := flagDetail(Single("💰💸🙏 thanks bro 500000", Options{}), WeirdSymbols); !ok || detail != "ratio 0.40" {
		t.Errorf("weird_symbols detail = %q, want %q", detail, "ratio 0.40")
	}
}

func flagNames(flags []Flag) []string {
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		names = append(names, f.Name)
	}
	return names
}

func flagDetail(flags []Flag, name string) (string, bool) {
	for _, f := range flags {
		if f.Name == name {
			return f.Detail, true
		}
	}
	return "", false
}
