package celtfmt

import (
	"strings"
	"testing"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	celref "cel.dev/cel-go/common/types/ref"

	"github.com/udaltsovkdotru/celt/celtsource"
)

// Comment literals reused across format test tables.
const (
	cmtTail      = "// tail"
	cmtNote      = "// note"
	cmtOnly      = "//"
	cmtImportant = "// important"
	cmtInner     = "// inner"
)

// ---- test rule implementations -------------------------------------------

// fmtConfig is a minimal celtfmt.Config carrying an explicit rule list.
type fmtConfig struct {
	rules []FormatRule
}

func (c fmtConfig) Rules() []FormatRule { return c.rules }

// fmtInputCall is reused across several tests as a representative simple call.
const fmtInputCall = "foo(bar, baz)"

// fmtConfigOf builds a Config from the given rules.
func fmtConfigOf(rules ...FormatRule) Config {
	return fmtConfig{rules: rules}
}

// fmtIndentRule is a test indentation provider.
type fmtIndentRule struct {
	indent string
}

func (fmtIndentRule) Meta() FormatMeta {
	return FormatMeta{Name: "indent", Defaults: FormatParams{Enabled: true}}
}
func (fmtIndentRule) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }
func (r fmtIndentRule) Indent() string                           { return r.indent }

// fmtWidthRule is a test width provider.
type fmtWidthRule struct {
	width int
}

func (fmtWidthRule) Meta() FormatMeta {
	return FormatMeta{Name: "width", Defaults: FormatParams{Enabled: true}}
}
func (fmtWidthRule) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }
func (r fmtWidthRule) Width() int                               { return r.width }

// fmtBreakRule forces line breaks on boolean operators and conditionals,
// mirroring the production "break-operators" rule.
type fmtBreakRule struct{}

func (fmtBreakRule) Meta() FormatMeta {
	return FormatMeta{Name: "break-operators", Defaults: FormatParams{Enabled: false}}
}
func (fmtBreakRule) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }
func (fmtBreakRule) MustBreak(e celast.Expr) bool {
	if e.Kind() != celast.CallKind {
		return false
	}
	switch e.AsCall().FunctionName() {
	case operators.LogicalAnd, operators.LogicalOr, operators.Conditional:
		return true
	}
	return false
}

func fmtIndent(s string) FormatRule { return fmtIndentRule{indent: s} }
func fmtWidth(n int) FormatRule     { return fmtWidthRule{width: n} }
func fmtBreakOps() FormatRule       { return fmtBreakRule{} }

// ---- Format --------------------------------------------------------------

func TestFormatSimple(t *testing.T) {
	got, err := Format(fmtInputCall, nil)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if got != "foo(bar, baz)\n" {
		t.Errorf("Format = %q, want %q", got, "foo(bar, baz)\n")
	}
}

func TestFormatLeadingDoc(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single comment",
			src:  "// header\n\nfoo",
			want: "// header\n\nfoo\n",
		},
		{
			name: "multiple comments with blank gap",
			src:  "// line one\n// line two\n\nbar(1)",
			want: "// line one\n// line two\n\nbar(1)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Format(tt.src, nil)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if got != tt.want {
				t.Errorf("Format(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestFormatEmptyAndWhitespace(t *testing.T) {
	// Degenerate inputs contain no expression, so formatting must surface a
	// parse error rather than silently returning empty output.
	tests := []struct {
		name string
		src  string
	}{
		{name: "empty", src: ""},
		{name: "spaces", src: "   "},
		{name: "newlines", src: "\n\n\n"},
		{name: "comment only", src: "// nothing else\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Format(tt.src, nil); err == nil {
				t.Errorf("Format(%q) = nil error, want parse error", tt.src)
			}
		})
	}
}

func TestFormatParseError(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "unbalanced paren", src: "foo(bar"},
		{name: "dangling operator", src: "a &&"},
		{name: "unterminated string", src: `"abc`},
		{name: "bare operator", src: "+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Format(tt.src, nil); err == nil {
				t.Errorf("Format(%q) = nil error, want parse error", tt.src)
			}
		})
	}
}

func TestFormatExprNoSuffix(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "simple", src: fmtInputCall, want: fmtInputCall},
		{name: "doc stripped", src: "// header\n\nfoo", want: "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatExpr(tt.src, nil)
			if err != nil {
				t.Fatalf("FormatExpr: %v", err)
			}
			if got != tt.want {
				t.Errorf("FormatExpr(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

// ---- Config and Rules() --------------------------------------------------

func TestConfigDefaults(t *testing.T) {
	// A nil Config and an explicit config with empty/zero-valued indent and
	// width rules must both fall back to the built-in defaults.
	base, err := FormatExpr("a && b && c", nil)
	if err != nil {
		t.Fatalf("FormatExpr(nil): %v", err)
	}
	explicit, err := FormatExpr("a && b && c", fmtConfigOf(fmtIndent(""), fmtWidth(0)))
	if err != nil {
		t.Fatalf("FormatExpr(defaults): %v", err)
	}
	if explicit != base {
		t.Errorf("explicit-defaults = %q, nil = %q", explicit, base)
	}

	// Explicitly supplying the same defaults must also match.
	verbatim, err := FormatExpr("a && b && c", fmtConfigOf(fmtIndent("  "), fmtWidth(100)))
	if err != nil {
		t.Fatalf("FormatExpr(verbatim): %v", err)
	}
	if verbatim != base {
		t.Errorf("verbatim-defaults = %q, nil = %q", verbatim, base)
	}

	// An empty indent falls back to the default even when a BreakRule is also
	// present, so the result must equal the break rule with default indent.
	emptyIndent, err := FormatExpr("a && b && c", fmtConfigOf(fmtIndent(""), fmtWidth(0), fmtBreakOps()))
	if err != nil {
		t.Fatalf("FormatExpr(emptyIndent+break): %v", err)
	}
	breakOnly, err := FormatExpr("a && b && c", fmtConfigOf(fmtBreakOps()))
	if err != nil {
		t.Fatalf("FormatExpr(breakOnly): %v", err)
	}
	if emptyIndent != breakOnly {
		t.Errorf("empty-indent+break = %q, break-only = %q", emptyIndent, breakOnly)
	}
}

func TestConfigLastWins(t *testing.T) {
	t.Run("indent", func(t *testing.T) {
		// The last IndentProvider in the rule list wins.
		got, err := FormatExpr("a && b && c", fmtConfigOf(fmtIndent("    "), fmtIndent("\t"), fmtBreakOps()))
		if err != nil {
			t.Fatalf("FormatExpr: %v", err)
		}
		want := "a\n\t&& b\n\t&& c"
		if got != want {
			t.Errorf("FormatExpr = %q, want %q", got, want)
		}
	})
	t.Run("width", func(t *testing.T) {
		// The last WidthProvider in the rule list wins.
		got, err := FormatExpr("foo(alpha, beta, gamma, delta)", fmtConfigOf(fmtWidth(200), fmtWidth(10)))
		if err != nil {
			t.Fatalf("FormatExpr: %v", err)
		}
		want := "foo(\n  alpha,\n  beta,\n  gamma,\n  delta\n)"
		if got != want {
			t.Errorf("FormatExpr = %q, want %q", got, want)
		}
	})
}

// ---- indentation and width interaction -----------------------------------

func TestIndentAndWidthBreak(t *testing.T) {
	tests := []struct {
		name   string
		indent string
		width  int
		want   string
	}{
		{
			name:   "two space indent",
			indent: "  ",
			width:  10,
			want:   "foo(\n  alpha,\n  beta,\n  gamma,\n  delta\n)",
		},
		{
			name:   "four space indent",
			indent: "    ",
			width:  10,
			want:   "foo(\n    alpha,\n    beta,\n    gamma,\n    delta\n)",
		},
		{
			name:   "tab indent",
			indent: "\t",
			width:  10,
			want:   "foo(\n\talpha,\n\tbeta,\n\tgamma,\n\tdelta\n)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatExpr("foo(alpha, beta, gamma, delta)", fmtConfigOf(fmtIndent(tt.indent), fmtWidth(tt.width)))
			if err != nil {
				t.Fatalf("FormatExpr: %v", err)
			}
			if got != tt.want {
				t.Errorf("FormatExpr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBreakOperatorsIndent(t *testing.T) {
	// A BreakRule forces && chains onto indented lines even at default width.
	tests := []struct {
		name   string
		indent string
		want   string
	}{
		{name: "default indent", indent: "", want: "a\n  && b\n  && c"},
		{name: "four space indent", indent: "    ", want: "a\n    && b\n    && c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fmtConfigOf(fmtBreakOps())
			if tt.indent != "" {
				cfg = fmtConfigOf(fmtIndent(tt.indent), fmtBreakOps())
			}
			got, err := FormatExpr("a && b && c", cfg)
			if err != nil {
				t.Fatalf("FormatExpr: %v", err)
			}
			if got != tt.want {
				t.Errorf("FormatExpr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBreakOperatorsInsideContainers(t *testing.T) {
	for _, src := range []string{
		"f(left_operand && right_operand)",
		"[left_operand && right_operand]",
		`{"key": left_operand && right_operand}`,
		"f((left_operand && right_operand).foo)",
		"[left_operand && right_operand].size()",
		"{(left_operand && right_operand): 1}",
	} {
		t.Run(src, func(t *testing.T) {
			got, err := FormatExpr(src, fmtConfigOf(fmtBreakOps()))
			if err != nil {
				t.Fatal(err)
			}
			left, right := strings.Index(got, "left_operand"), strings.Index(got, "right_operand")
			if left < 0 || right <= left || !strings.Contains(got[left:right], "\n") {
				t.Errorf("break-operators should put the nested operands on different lines: %q", got)
			}
		})
	}
}

func TestBreakOperatorsConditional(t *testing.T) {
	got, err := FormatExpr("cond ? yes : no", fmtConfigOf(fmtBreakOps()))
	if err != nil {
		t.Fatalf("FormatExpr: %v", err)
	}
	want := "cond\n? yes\n: no"
	if got != want {
		t.Errorf("FormatExpr = %q, want %q", got, want)
	}
}

// TestFormatOptionalIndex asserts an optional index access (m[?"k"]) keeps its
// "?"; dropping it would change program semantics (optional -> mandatory).
func TestFormatOptionalIndex(t *testing.T) {
	got, err := FormatExpr(`m[?"k"]`, nil)
	if err != nil {
		t.Fatalf("FormatExpr: %v", err)
	}
	if !strings.Contains(got, "[?") {
		t.Errorf("FormatExpr = %q, want optional index marker", got)
	}
}

// TestBreakOperatorsParen asserts a forced break is preserved when the
// expression is wrapped in parentheses (regression: the paren wrapper used to
// drop the break flag and inline the expression).
func TestBreakOperatorsParen(t *testing.T) {
	got, err := FormatExpr("(a && b)", fmtConfigOf(fmtBreakOps()))
	if err != nil {
		t.Fatalf("FormatExpr: %v", err)
	}
	if strings.Contains(got, "a && b") {
		t.Errorf("FormatExpr = %q, want broken parenthesized chain", got)
	}
}

// ---- idempotence ---------------------------------------------------------

func TestFormatIdempotent(t *testing.T) {
	longCall := "very_long_function_name(argument_one, argument_two, argument_three, " +
		"argument_four, argument_five, argument_six, argument_seven, argument_eight)"
	tests := []struct {
		name string
		src  string
		cfg  Config
	}{
		{name: "simple call", src: fmtInputCall},
		{name: "nested call", src: "foo(bar(baz), qux)"},
		{name: "default width wraps", src: longCall},
		{name: "list literal", src: "foo([1, 2, 3])"},
		{name: "map literal", src: "foo({a: 1, b: 2, c: 3})"},
		{name: "boolean chain", src: "a && b && c && d"},
		{name: "conditional", src: "cond ? yes : no"},
		{name: "leading doc", src: "// header\n\nfoo(bar)"},
		{name: "trailing comment", src: "foo(alpha, beta) // done"},
		{name: "custom indent", src: "a && b && c", cfg: fmtConfigOf(fmtIndent("    "), fmtBreakOps())},
		{name: "narrow width", src: longCall, cfg: fmtConfigOf(fmtWidth(20))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			once, err := Format(tt.src, tt.cfg)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			twice, err := Format(once, tt.cfg)
			if err != nil {
				t.Fatalf("Format(format): %v", err)
			}
			if twice != once {
				t.Errorf("Format(format(src)) = %q, want %q", twice, once)
			}
		})
	}
}

func TestFormatKeepsTrailingCommentBlocks(t *testing.T) {
	// A list/map whose elements carry trailing `//` comments must stay
	// multi-line. CEL has no block comments, so collapsing such a block onto
	// one line (e.g. `["a" // first, "b"]`) would let the comment swallow the
	// rest of the line and silently drop the remaining elements.
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "list",
			src:  "foo([\n  \"a\", // first\n  \"b\", // second\n])",
			want: "foo([\n  \"a\", // first\n  \"b\", // second\n])\n",
		},
		{
			name: "map",
			src:  "foo({\n  a: 1, // first\n  b: 2, // second\n})",
			want: "foo({\n  a: 1, // first\n  b: 2, // second\n})\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Format(tt.src, nil)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if got != tt.want {
				t.Errorf("Format = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDoesNotChangeExpression(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "optional list element", src: "[?optional.of(1)]"},
		{name: "optional map entry", src: "{?a: 1}"},
		{name: "optional select", src: "m.?field"},
		{name: "triple-quoted string", src: `"""hello"""`},
		{name: "comment containing delimiter", src: "f(a, // close ) note\nb)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := mustParseFormatFixture(t, tt.src)
			out, err := FormatExpr(tt.src, nil)
			if err == nil && !celast.EquivExpr(before, mustParseFormatFixture(t, out)) {
				t.Errorf("formatting changed the expression: %q -> %q", tt.src, out)
			}
		})
	}
	// Optional index access is preserved by the formatter and must still format.
	if got, err := FormatExpr(`m[?"k"]`, nil); err != nil {
		t.Errorf("FormatExpr(m[?\"k\"]) = error %v, want success", err)
	} else if got != `m[?"k"]` {
		t.Errorf("FormatExpr(m[?\"k\"]) = %q, want %q", got, `m[?"k"]`)
	}
}

func TestFormatPreservesComments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src      string
		comments []string
	}{
		{
			name:     "standalone and document trailing",
			src:      "[\n  // keep me\n  1\n] // tail",
			comments: []string{"// keep me", cmtTail},
		},
		{
			name:     "between arguments",
			src:      "f(a, // first argument\n  b)",
			comments: []string{"// first argument"},
		},
		{
			name:     "delimiter inside comment",
			src:      "f(a, // close ) note\n  b)",
			comments: []string{"// close ) note"},
		},
		{
			name:     "after final container argument",
			src:      "f([a] // final argument\n)",
			comments: []string{"// final argument"},
		},
		{
			name:     "before first call argument",
			src:      "f(\n  // first argument\n  a,\n  b\n)",
			comments: []string{"// first argument"},
		},
		{
			name:     "after parenthesized root",
			src:      "(a+b) // tail",
			comments: []string{cmtTail},
		},
		{
			name:     "nested list",
			src:      "[[\n  // nested comment\n  1,\n]]",
			comments: []string{"// nested comment"},
		},
		{
			name:     "map with commented list value",
			src:      "{\"x\": [1, // note\n  2]}",
			comments: []string{cmtNote},
		},
		{
			name:     "inside grouped call argument",
			src:      "f((\n  // grouped argument\n  1\n), 2)",
			comments: []string{"// grouped argument"},
		},
		{
			name:     "empty standalone comment before first argument",
			src:      "f(\n  //\n  1)",
			comments: []string{cmtOnly},
		},
		{name: "boolean chain comment", src: "a &&\n  // important\n  b", comments: []string{cmtImportant}},
		{name: "conditional branch comment", src: "a ?\n  // true branch\n  b : c", comments: []string{"// true branch"}},
		{name: "inline binary operator comment", src: "a + // important\n  b", comments: []string{cmtImportant}},
		{name: "binary comment in call argument", src: "f(a +\n  // note\n  b)", comments: []string{cmtNote}},
		{name: "standalone comment after root", src: "a\n// trailing\n", comments: []string{"// trailing"}},
		{name: "trailing comment after index", src: "a[0] // important", comments: []string{cmtImportant}},
		{name: "comment after opening parenthesis", src: "( // title\na)", comments: []string{"// title"}},
		{name: "comment in grouped map key", src: "{(\n// key\na): 1}", comments: []string{"// key"}},
		{name: "comment in member call receiver", src: "[\n// receiver\n1,2].size()", comments: []string{"// receiver"}},
		{name: "multiple trailer comments", src: "a // first\n// second\n", comments: []string{"// first", "// second"}},
		{name: "trailing comment separated by blank line", src: "a\n\n// tail", comments: []string{cmtTail}},
		{name: "map comment before colon", src: "{a // key\n : b}", comments: []string{"// key"}},
		{name: "map comment after colon", src: "{a: // colon\n b}", comments: []string{"// colon"}},
		{name: "inline conditional comment", src: "a ? // true branch\n b : c", comments: []string{"// true branch"}},
		{name: "index operator comment", src: "a[ // index\n 0]", comments: []string{"// index"}},
		{name: "unary operator comment", src: "! // unary\n a", comments: []string{"// unary"}},
		{name: "group inside list and binary expression", src: "[a + ( // group\n b)]", comments: []string{"// group"}},
		{name: "nested grouping comments", src: "(( // outer\n ( // inner\n a)))", comments: []string{"// outer", cmtInner}},
		{name: "grouped operand comment in list", src: "[a + (b // group\n)]", comments: []string{"// group"}},
		{name: "inner and outer trailing comments", src: "(a // inner\n) // outer", comments: []string{cmtInner, "// outer"}},
		{name: "empty element comment", src: "[1, //\n 2]", comments: []string{cmtOnly}},
		{name: "empty root comment", src: "a // ", comments: []string{cmtOnly}},
		{name: "selector dot comment", src: "a.// dot\nb", comments: []string{"// dot"}},
		{name: "trailer after grouped list item", src: "[(a) // note\n, 2]", comments: []string{cmtNote}},
		{name: "trailer after grouped call argument", src: "f((a) // note\n, b)", comments: []string{cmtNote}},
		{name: "trailer after grouped map value", src: "{'a': (b) // note\n, 'c': d}", comments: []string{cmtNote}},
		{name: "slashes in string before grouping", src: "f('http://', (a + b) * c)"},
		{name: "slashes in string before grouped comment", src: "['http://', ( // important\n a)]", comments: []string{cmtImportant}},
		{name: "map comments before and after value", src: "{'a': // first\n 1 // second\n}", comments: []string{"// first", "// second"}},
		{name: "comment before member method name", src: "a.// method\nsize()", comments: []string{"// method"}},
		{name: "comment after index operand", src: "a[1 // inner\n]", comments: []string{cmtInner}},
		{name: "comments in a selector chain", src: "a. // before\n b. // another\n c", comments: []string{"// before", "// another"}},
		{name: "selector dot in a preceding comment", src: "a // .\n .b // tail", comments: []string{"// .", cmtTail}},
		{name: "trailer after nested root grouping", src: "((a) // middle\n) // tail\n// final", comments: []string{"// middle", cmtTail, "// final"}},
		{name: "comment between callee and opening paren", src: "foo // callee\n (a,b)", comments: []string{"// callee"}},
		{name: "comment between receiver and opening bracket", src: "a // receiver\n [b]", comments: []string{"// receiver"}},
		{name: "comment in call receiver should be kept once", src: "foo( // inner call\n ).bar(x)", comments: []string{"// inner call"}},
		{
			name:     "multiple leading comment blocks",
			src:      "// first block\n\n// second block\nx",
			comments: []string{"// first block", "// second block"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := mustParseFormatFixture(t, tc.src)
			out, err := Format(tc.src, nil)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if !celast.EquivExpr(before, mustParseFormatFixture(t, out)) {
				t.Errorf("formatting changed the expression: %q -> %q", tc.src, out)
			}
			for _, comment := range tc.comments {
				if got, want := strings.Count(out, comment), strings.Count(tc.src, comment); got != want {
					t.Errorf("formatted output %q has %d copies of %q, want %d", out, got, comment, want)
				}
			}
			again, err := Format(out, nil)
			if err != nil {
				t.Fatalf("second Format: %v", err)
			}
			if again != out {
				t.Errorf("formatting again changed output: %q -> %q", out, again)
			}
		})
	}
}

func TestFormatKeepsCommentsOnTheirSideOfGroupedExpression(t *testing.T) {
	const src = "(\n  // before\n  policy_value\n  // after\n)"
	out, err := Format(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, expr, after := strings.Index(out, "// before"), strings.Index(out, "policy_value"), strings.Index(out, "// after")
	if before < 0 || expr <= before || after <= expr {
		t.Errorf("formatted output %q should keep the two comments on opposite sides of the expression", out)
	}
}

func TestFormatKeepsCommentBetweenGroupedOperands(t *testing.T) {
	const src = "(left_value +\n  // describes right operand\n  right_value)"
	out, err := Format(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	left, comment, right := strings.Index(out, "left_value"), strings.Index(out, "// describes right operand"), strings.Index(out, "right_value")
	if left < 0 || comment <= left || right <= comment {
		t.Errorf("formatted output %q should keep the comment between the operands", out)
	}
}

func TestFormatOperatorBreaksPreserveGrouping(t *testing.T) {
	const src = `has(a.b) && (a.b || c)`
	out, err := FormatExpr(src, fmtConfigOf(fmtBreakOps()))
	if err != nil {
		t.Fatalf("FormatExpr: %v", err)
	}
	if !strings.Contains(out, "\n") {
		t.Errorf("output %q does not break operators", out)
	}
	if !celast.EquivExpr(mustParseFormatFixture(t, src), mustParseFormatFixture(t, out)) {
		t.Errorf("formatting changed the expression: %q -> %q", src, out)
	}
}

func TestFormatIdempotenceForReceiverCallsAndNarrowGuards(t *testing.T) {
	for _, tc := range []struct {
		src string
		cfg Config
	}{
		{src: "foo().bar(x)"},
		{src: "has(a.b) && a.b.field == 123456789", cfg: fmtConfigOf(fmtWidth(30))},
	} {
		t.Run(tc.src, func(t *testing.T) {
			first, err := FormatExpr(tc.src, tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			second, err := FormatExpr(first, tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if second != first {
				t.Errorf("second format changed %q to %q", first, second)
			}
		})
	}
}

type identRewriteRule struct{ synthetic bool }

func (identRewriteRule) Meta() FormatMeta { return FormatMeta{Name: "rewrite-ident"} }

func (r identRewriteRule) Rewrite(a *celast.AST) (*celast.AST, error) {
	id := a.Expr().ID()
	if r.synthetic {
		id += 1000
	}
	expr := celast.NewExprFactory().NewIdent(id, "renamed")
	return celast.NewAST(expr, a.SourceInfo()), nil
}

func TestFormatUsesRewrittenIdentifierWithOrWithoutSourceSpan(t *testing.T) {
	for _, tc := range []struct {
		name      string
		synthetic bool
	}{{name: "existing ID"}, {name: "synthetic ID", synthetic: true}} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("valid identifier rewrite panicked: %v", p)
				}
			}()
			got, err := FormatExpr("original", fmtConfigOf(identRewriteRule{synthetic: tc.synthetic}))
			if err != nil || got != "renamed" {
				t.Errorf("identifier rewrite = %q, %v; want renamed", got, err)
			}
		})
	}
}

type numericRewriteRule struct{ value celref.Val }

func (numericRewriteRule) Meta() FormatMeta { return FormatMeta{Name: "rewrite-number"} }

func (r numericRewriteRule) Rewrite(a *celast.AST) (*celast.AST, error) {
	expr := celast.NewExprFactory().NewLiteral(a.Expr().ID(), r.value)
	return celast.NewAST(expr, a.SourceInfo()), nil
}

func TestNumericRewritesKeepCELType(t *testing.T) {
	for _, tc := range []struct {
		want  string
		value celref.Val
	}{
		{want: "1.0", value: types.Double(1)},
		{want: "2.0", value: types.Double(2)},
		{want: "1u", value: types.Uint(1)},
	} {
		t.Run(tc.want, func(t *testing.T) {
			got, err := FormatExpr("1", fmtConfigOf(numericRewriteRule{value: tc.value}))
			if err != nil {
				t.Fatal(err)
			}
			if !celast.EquivExpr(mustParseFormatFixture(t, got), mustParseFormatFixture(t, tc.want)) {
				t.Errorf("rewritten CEL %q has a different value or type from %q", got, tc.want)
			}
		})
	}
}

func TestFormatEmitsRewrittenLiteral(t *testing.T) {
	out, err := FormatExpr("false", fmtConfigOf(replaceWithTrueRule{}))
	if err != nil {
		t.Fatalf("FormatExpr with rewrite: %v", err)
	}
	if out != "true" {
		t.Errorf("FormatExpr with rewrite = %q, want true", out)
	}
}

type replaceWithTrueRule struct{}

func (replaceWithTrueRule) Meta() FormatMeta { return FormatMeta{Name: "replace-with-true"} }

func (replaceWithTrueRule) Rewrite(a *celast.AST) (*celast.AST, error) {
	expr := celast.NewExprFactory().NewLiteral(a.Expr().ID(), types.True)
	return celast.NewAST(expr, a.SourceInfo()), nil
}

func mustParseFormatFixture(t *testing.T, src string) celast.Expr {
	t.Helper()
	a, err := celtsource.Parse(common.NewStringSource(src, t.Name()))
	if err != nil {
		t.Fatalf("cannot parse %q: %v", src, err)
	}
	return a.Expr()
}
