package celtfmt

import (
	"strings"
	"testing"

	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
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
		{
			name: "trailing comment after expression is dropped",
			src:  "foo // trailing",
			want: "foo\n",
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
