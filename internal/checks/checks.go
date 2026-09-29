// Package checks implements deterministic, explainable quality diagnostics. Diagnostics are informational only and never part of review state.
package checks

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Diagnostic names.
const (
	Duplicate        = "duplicate"
	TooLong          = "too_long"
	Empty            = "empty"
	RepeatedChars    = "repeated_chars"
	WeirdSymbols     = "weird_symbols"
	PossibleTemplate = "possible_template"
)

// All lists diagnostic names in display order.
var All = []string{Duplicate, TooLong, Empty, RepeatedChars, WeirdSymbols, PossibleTemplate}

// Markers substituted by TemplatePattern.
const (
	amountMarker = "<AMOUNT>"
	numMarker    = "<NUM>"
	tokenMarker  = "<TOKEN>"
)

// symbolWeight is what one punctuation/symbol/emoji rune costs in SymbolRatio
// while a letter or digit costs 1. Symbols carry far more signal per character,
// so a short note with a couple of emoji reads as symbol-heavy.
const symbolWeight = 2

// amountRes are the money-amount patterns, matched case-insensitively against a
// NFC-normalized, whitespace-collapsed text (see collapse). Group 1 of every
// match is the amount itself and the trailing group is a boundary that must not
// be a letter or a digit, so "iphone 15" and "04/2026" are not amounts, and a
// bare number only counts from 5 digits or with a thousands separator.
//
// Supported forms:
//
//	50k 2tr 1tr2 12tr5 2 triệu 2 củ 5 lít 1.5tr 1.2m 20usd 5 l
//	200k vnd $20 ₫5000 +20tr -500k
//	500000 500.000 500,000
var amountRes = []*regexp.Regexp{
	// Currency prefix: $20, ₫5.000, €1,50.
	regexp.MustCompile(`(?i)([+-]?[$₫€]\s*\d[\d.,]*)(?:$|[^\p{L}\p{N}])`),
	// Scaled or suffixed: 50k, 2tr, 1tr2, 2 triệu, 2 củ, 5 lít, 1.2m, 20usd, 200k vnd.
	regexp.MustCompile(`(?i)([+-]?\d[\d.,]*\s*(?:triệu|củ|lít|usd|vnd|tr|k|m|l)\d*)(?:$|[^\p{L}\p{N}])`),
	// Bare number of 5+ digits: 500000.
	regexp.MustCompile(`(?i)(\d{5,})(?:$|[^\p{L}\p{N}])`),
	// Thousands separators: 500.000, 500,000.
	regexp.MustCompile(`(?i)(\d{1,3}(?:[.,]\d{3})+)(?:$|[^\p{L}\p{N}])`),
}

// numRe matches residual numeric runs (not part of an amount) inside a word.
var numRe = regexp.MustCompile(`\d+(?:[.,]\d+)*`)

type Options struct {
	MaxChars               int     `yaml:"max_chars"`                // default 160 (runes)
	RepeatedCharThreshold  int     `yaml:"repeated_char_threshold"`  // default 5: same rune repeated >= N times
	WeirdSymbolRatio       float64 `yaml:"weird_symbol_ratio"`       // default 0.35
	TemplateMinOccurrences int     `yaml:"template_min_occurrences"` // default 8
}

// DefaultOptions returns the built-in check thresholds.
func DefaultOptions() Options {
	return Options{
		MaxChars:               160,
		RepeatedCharThreshold:  5,
		WeirdSymbolRatio:       0.35,
		TemplateMinOccurrences: 8,
	}
}

// withDefaults replaces zero and negative fields with their defaults so callers
// may pass partial configs.
func withDefaults(opt Options) Options {
	d := DefaultOptions()
	if opt.MaxChars <= 0 {
		opt.MaxChars = d.MaxChars
	}
	if opt.RepeatedCharThreshold <= 0 {
		opt.RepeatedCharThreshold = d.RepeatedCharThreshold
	}
	if opt.WeirdSymbolRatio <= 0 {
		opt.WeirdSymbolRatio = d.WeirdSymbolRatio
	}
	if opt.TemplateMinOccurrences <= 0 {
		opt.TemplateMinOccurrences = d.TemplateMinOccurrences
	}
	return opt
}

// Diagnostic is one diagnostic with a human-readable explanation, e.g. {PossibleTemplate, "pattern: cho <TOKEN> vay <AMOUNT> (87 occurrences)"}.
type Diagnostic struct {
	Name   string
	Detail string
}

// Analysis is the corpus-wide result, indexed by record index.
type Analysis struct {
	Diagnostics [][]Diagnostic // per record
	DupGroup    []int32        // per record: index into Groups, or -1
	Groups      [][]int        // duplicate groups (record indices, ascending), each len >= 2
	Pattern     []string       // per record template pattern ("" when the text is empty or has no substitution)
	Templates   map[string]int // qualified pattern -> occurrences
}

// Analyze runs all checks over texts (final texts, index-aligned with records). Must handle 100k records quickly.
func Analyze(texts []string, opt Options) *Analysis {
	opt = withDefaults(opt)
	a := &Analysis{
		Diagnostics: make([][]Diagnostic, len(texts)),
		DupGroup:    make([]int32, len(texts)),
		Pattern:     make([]string, len(texts)),
		Templates:   make(map[string]int),
	}

	// Per-record checks and duplicate keys in one pass.
	index := make(map[string]int32, len(texts))
	var groups [][]int
	for i, text := range texts {
		normalized := Normalize(text)
		a.Diagnostics[i] = single(normalized, opt)
		a.DupGroup[i] = -1
		if normalized == "" {
			continue
		}
		if g, ok := index[normalized]; ok {
			groups[g] = append(groups[g], i)
			continue
		}
		index[normalized] = int32(len(groups))
		groups = append(groups, []int{i})
	}
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		g := int32(len(a.Groups))
		a.Groups = append(a.Groups, members)
		detail := fmt.Sprintf("%d records share this text", len(members))
		for _, i := range members {
			a.DupGroup[i] = g
			a.Diagnostics[i] = append([]Diagnostic{{Name: Duplicate, Detail: detail}}, a.Diagnostics[i]...)
		}
	}

	// Template counts. Patterns without <AMOUNT>/<TOKEN> would match every text
	// that merely repeats a number, so only substituted patterns qualify.
	counts := make(map[string]int)
	for i, text := range texts {
		p := TemplatePattern(text)
		if !strings.Contains(p, amountMarker) && !strings.Contains(p, tokenMarker) {
			continue
		}
		a.Pattern[i] = p
		counts[p]++
	}
	min := opt.TemplateMinOccurrences
	if min < 2 {
		min = 2
	}
	for i, p := range a.Pattern {
		c, ok := counts[p]
		if !ok || c < min {
			continue
		}
		a.Templates[p] = c
		a.Diagnostics[i] = append(a.Diagnostics[i], Diagnostic{
			Name:   PossibleTemplate,
			Detail: fmt.Sprintf("pattern: %s (%d occurrences)", p, c),
		})
	}
	return a
}

// Single runs per-record checks only (too_long, empty, repeated_chars, weird_symbols).
func Single(text string, opt Options) []Diagnostic {
	return single(Normalize(text), withDefaults(opt))
}

// single runs the per-record checks over an already normalized text and
// defaults-filled options: rune counts use the normalized text, so trailing
// whitespace cannot make a record too long. Diagnostics come back in All order.
func single(normalized string, opt Options) []Diagnostic {
	var diags []Diagnostic
	if n := utf8RuneCount(normalized); n > opt.MaxChars {
		diags = append(diags, Diagnostic{Name: TooLong, Detail: fmt.Sprintf("%d chars, limit %d", n, opt.MaxChars)})
	}
	if normalized == "" {
		diags = append(diags, Diagnostic{Name: Empty, Detail: "text is empty"})
	}
	if r, n, ok := repeatedRun(normalized, opt.RepeatedCharThreshold); ok {
		diags = append(diags, Diagnostic{Name: RepeatedChars, Detail: fmt.Sprintf("%q repeated %d times", string(r), n)})
	}
	if ratio := symbolRatio(normalized); ratio > opt.WeirdSymbolRatio {
		diags = append(diags, Diagnostic{Name: WeirdSymbols, Detail: fmt.Sprintf("ratio %.2f", ratio)})
	}
	return diags
}

// Normalize: NFC, trim, lowercase, collapse whitespace.
func Normalize(s string) string {
	return strings.ToLower(collapse(s))
}

// collapse applies NFC, trims s, and collapses whitespace runs into one space.
// Case is preserved because TemplatePattern needs it.
func collapse(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// utf8RuneCount is the rune count of s (len([]rune(s)) without the allocation).
func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// HasRepeatedChars reports a run of the same rune (letters or symbols) of length >= threshold. Runs compare case-insensitively; digits and whitespace never form a run.
func HasRepeatedChars(s string, threshold int) bool {
	_, _, ok := repeatedRun(collapse(s), threshold)
	return ok
}

// repeatedRun returns the first rune run of length >= threshold and that length.
// Runes compare case-insensitively; digits and whitespace never form a run, so
// "aaaaaaa" or "????????" form a run but a number like 500000 does not. A
// threshold <= 0 means the default of 5.
func repeatedRun(s string, threshold int) (rune, int, bool) {
	if threshold <= 0 {
		threshold = DefaultOptions().RepeatedCharThreshold
	}
	var (
		prev rune // lowercased rune of the current run
		orig rune // first rune of the current run, as written
		run  int
	)
	for _, r := range s {
		if unicode.IsDigit(r) || unicode.IsSpace(r) {
			if run >= threshold {
				return orig, run, true
			}
			prev, run = 0, 0
			continue
		}
		lower := unicode.ToLower(r)
		if run > 0 && lower == prev {
			run++
			continue
		}
		if run >= threshold {
			return orig, run, true
		}
		prev, orig, run = lower, r, 1
	}
	if run >= threshold {
		return orig, run, true
	}
	return 0, 0, false
}

// SymbolRatio is the fraction of non-space runes that are symbols/punctuation, ignoring amount notation (+20tr, -500k, $20, 1.2tr). Each symbol rune weighs
// symbolWeight while letters and digits weigh 1, so an emoji-heavy short note lands high and "$20", "+20tr", "-500k" or "1.2tr" land at 0.
func SymbolRatio(s string) float64 {
	return symbolRatio(collapse(s))
}

// symbolRatio scores an already collapsed text: amount notation is dropped, then
// every remaining non-space rune weighs symbolWeight when it is punctuation, a
// symbol or an emoji, and 1 otherwise (letters, digits). The result is in [0,1]
// and 0 when nothing non-space is left.
func symbolRatio(n string) float64 {
	if !hasSymbolCandidate(n) {
		return 0
	}
	spans := amountSpans(n)
	symbols, total, next := 0, 0, 0
	for i, r := range n {
		for next < len(spans) && i >= spans[next].end {
			next++
		}
		if next < len(spans) && i >= spans[next].start {
			continue // the amount's own notation is not weird
		}
		if unicode.IsSpace(r) {
			continue
		}
		if isSymbol(r) {
			symbols += symbolWeight
			total += symbolWeight
			continue
		}
		total++
	}
	if total == 0 {
		return 0
	}
	return float64(symbols) / float64(total)
}

// hasSymbolCandidate reports whether n holds any rune that could count as a
// symbol, so ordinary text skips the amount scan.
func hasSymbolCandidate(n string) bool {
	for _, r := range n {
		if isSymbol(r) {
			return true
		}
	}
	return false
}

// isSymbol reports whether r is punctuation, a symbol or an emoji: anything
// that is not a letter, a digit, or whitespace.
func isSymbol(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r)
}

// span is a half-open byte range inside a collapsed string.
type span struct{ start, end int }

// amountSpans returns every amount match in n as merged, ascending spans.
func amountSpans(n string) []span {
	var spans []span
	for _, re := range amountRes {
		for _, m := range re.FindAllStringSubmatchIndex(n, -1) {
			spans = append(spans, span{start: m[2], end: m[3]})
		}
	}
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := make([]span, 0, len(spans))
	for _, sp := range spans {
		if last := len(merged) - 1; last >= 0 && sp.start <= merged[last].end {
			if sp.end > merged[last].end {
				merged[last].end = sp.end
			}
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

// TemplatePattern returns the normalized pattern, e.g. "cho Nam vay 2tr" -> "cho <TOKEN> vay <AMOUNT>".
func TemplatePattern(s string) string {
	n := collapse(s)
	if n == "" {
		return ""
	}
	tokens := make([]string, 0, 16)
	word, prev := 0, 0
	for _, sp := range amountSpans(n) {
		word = appendWords(&tokens, n[prev:sp.start], word)
		tokens = append(tokens, amountMarker)
		word++
		prev = sp.end
	}
	appendWords(&tokens, n[prev:], word)
	return strings.Join(tokens, " ")
}

// appendWords normalizes the whitespace-separated words of seg and appends them
// to tokens, returning the next word index. The first word of the whole text is
// never a name token, so "CK Nam 2tr" and "Cho Nam vay 2tr" share a pattern.
func appendWords(tokens *[]string, seg string, word int) int {
	for _, w := range strings.Fields(seg) {
		*tokens = append(*tokens, patternWord(w, word == 0))
		word++
	}
	return word
}

// patternWord maps one word to its pattern token: a name-like word becomes
// <TOKEN>, other numeric runs become <NUM>, everything else is lowercased.
func patternWord(w string, first bool) string {
	if !first && isNameToken(w) {
		return tokenMarker
	}
	return numRe.ReplaceAllString(strings.ToLower(w), numMarker)
}

// isNameToken reports whether w is name-like: only letters, starting with an
// uppercase one ("Nam", "Hùng", "CK"). Anything else stays literal.
func isNameToken(w string) bool {
	first := true
	for _, r := range w {
		if !unicode.IsLetter(r) {
			return false
		}
		if first {
			if !unicode.IsUpper(r) {
				return false
			}
			first = false
		}
	}
	return !first
}
