package celtfmt

import (
	"reflect"
	"testing"

	"cel.dev/cel-go/common"

	"github.com/udaltsovkdotru/celt/celtsource"
)

// fmNote is the comment body used by the trailingComment cases.
const fmNote = "note"

// fmConverter builds a celConverter from source with spans computed, mirroring
// the setup performed by formatExpr. It is the seam through which the parse
// internals (spans, matching brackets, trailing comments) are exercised.
func fmConverter(t *testing.T, src string) *celConverter {
	t.Helper()
	source := common.NewStringSource(src, "")
	parsed, err := celtsource.Parse(source)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	b := &celConverter{
		src:     src,
		ast:     parsed,
		info:    parsed.SourceInfo(),
		byteOff: celtsource.RuneOffsets(src),
	}
	b.computeSpans(parsed.Expr())
	return b
}

func TestLeadingDoc(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{name: "single header", src: "// header\nexpr", want: []string{"// header"}},
		{name: "indented comment trimmed", src: "   // header\nexpr", want: []string{"// header"}},
		{name: "multiple contiguous", src: "// a\n// b\n\nbar", want: []string{"// a", "// b"}},
		{name: "blank after doc stops", src: "// a\n\n\nbar", want: []string{"// a"}},
		{name: "blank then comment stops", src: "//a\n\n// b\nexpr", want: []string{"//a"}},
		{name: "no comment", src: "expr", want: nil},
		{name: "empty input", src: "", want: nil},
		{name: "blank lines only", src: "\n\n", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := leadingDoc(tt.src)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("leadingDoc(%q) = %v, want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestMatchingBracket(t *testing.T) {
	tests := []struct {
		name            string
		src             string
		open            int
		openCh, closeCh byte
		want            int
	}{
		{name: "simple parens", src: "f(a, b)", open: 1, openCh: '(', closeCh: ')', want: 6},
		{name: "nested parens", src: "f((a))", open: 1, openCh: '(', closeCh: ')', want: 5},
		{name: "skips double-quoted string", src: `f(")", g)`, open: 1, openCh: '(', closeCh: ')', want: 8},
		{name: "skips single-quoted string", src: `f(')', g)`, open: 1, openCh: '(', closeCh: ')', want: 8},
		{name: "escaped quote in string", src: `f("a\"b", g)`, open: 1, openCh: '(', closeCh: ')', want: 11},
		{name: "square brackets", src: "[a, [b, c]]", open: 0, openCh: '[', closeCh: ']', want: 10},
		{name: "curly braces", src: "{a: {b: 1}}", open: 0, openCh: '{', closeCh: '}', want: 10},
		{name: "unclosed returns len", src: "f(a, b", open: 1, openCh: '(', closeCh: ')', want: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &celConverter{src: tt.src}
			if got := b.matchingBracket(tt.open, tt.openCh, tt.closeCh); got != tt.want {
				t.Errorf("matchingBracket(%q, %d, %q, %q) = %d, want %d",
					tt.src, tt.open, tt.openCh, tt.closeCh, got, tt.want)
			}
		})
	}
}

func TestSpanCoversExpr(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "ident", src: "xyz", want: "xyz"},
		{name: "int literal", src: "42", want: "42"},
		{name: "string literal", src: `"hi"`, want: `"hi"`},
		{name: "select extends to field", src: "foo.bar", want: "foo.bar"},
		{name: "select chain", src: "a.b.c", want: "a.b.c"},
		{name: "list bracket extends", src: "[1, 2]", want: "[1, 2]"},
		{name: "map brace extends", src: "{a: 1}", want: "{a: 1}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fmConverter(t, tt.src)
			s, e := b.span(b.ast.Expr())
			if s < 0 || e > len(tt.src) {
				t.Fatalf("span %d:%d out of range for %q", s, e, tt.src)
			}
			if got := tt.src[s:e]; got != tt.want {
				t.Errorf("span(%q) text = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestSpanCoversCall(t *testing.T) {
	// For a regular call the span must reach back to the function/member name
	// and forward to the closing paren (extendCall's backward scan over
	// identifier characters plus the matching bracket).
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "plain call", src: "lookup(key)", want: "lookup(key)"},
		{name: "member call", src: "obj.method(x)", want: "obj.method(x)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fmConverter(t, tt.src)
			s, e := b.span(b.ast.Expr())
			if got := tt.src[s:e]; got != tt.want {
				t.Errorf("span(%q) text = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestTrailingComment(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		after, limit int
		want         string
	}{
		{name: "stops at newline", src: "b // note\n", after: 1, limit: 10, want: fmNote},
		{name: "runs to end of input", src: "b // note", after: 1, limit: 9, want: fmNote},
		{name: "comment after comma", src: "b, // note\n", after: 1, limit: 11, want: fmNote},
		{name: "no comment", src: "b)", after: 1, limit: 2, want: ""},
		{name: "non-slash stops scan", src: "b x", after: 1, limit: 4, want: ""},
		{
			name:  "stops at limit before closing paren",
			src:   "b // note)",
			after: 1, limit: 9, want: "note",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &celConverter{src: tt.src}
			if got := b.trailingComment(tt.after, tt.limit); got != tt.want {
				t.Errorf("trailingComment(%q, %d, %d) = %q, want %q",
					tt.src, tt.after, tt.limit, got, tt.want)
			}
		})
	}
}

func TestUnicodeSpansRoundTrip(t *testing.T) {
	// Multibyte content that sits at the very end of the source (or as a
	// trailing operand) round-trips cleanly because the end-of-token byte
	// offset clamps to the source length.
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "string literal with multibyte", src: `"café"`, want: `"café"`},
		{name: "multibyte trailing operand", src: `x + "привет"`, want: `x + "привет"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatExpr(tt.src, nil)
			if err != nil {
				t.Fatalf("FormatExpr(%q): %v", tt.src, err)
			}
			if got != tt.want {
				t.Errorf("FormatExpr(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestUnicodeContainerElements(t *testing.T) {
	// Multibyte string literals as list elements / map values must not pull the
	// following delimiter into their span (cel-go parser offsets are byte-based
	// and must be used without a rune conversion).
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "map value multibyte", src: `{k: "ж"}`, want: `{k: "ж"}`},
		{name: "list element multibyte", src: `["é", "ñ"]`, want: `["é", "ñ"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatExpr(tt.src, nil)
			if err != nil {
				t.Fatalf("FormatExpr(%q): %v", tt.src, err)
			}
			if got != tt.want {
				t.Errorf("FormatExpr(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestPreservesRedundantParens(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "wrapping parens", src: "(foo)", want: "(foo)"},
		{name: "parenthesized bool", src: "(a || b)", want: "(a || b)"},
		{name: "parenthesized call arg", src: "f((x), y)", want: "f((x), y)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatExpr(tt.src, nil)
			if err != nil {
				t.Fatalf("FormatExpr(%q): %v", tt.src, err)
			}
			if got != tt.want {
				t.Errorf("FormatExpr(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}
