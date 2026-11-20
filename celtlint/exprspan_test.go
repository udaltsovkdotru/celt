package celtlint

import (
	"strings"
	"testing"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtsource"
)

// spFooName and spAddTwo are reused across several span tests and kept in
// constants to satisfy the goconst linter.
const (
	spIdentName = "ident"
	spFooName   = "foo"
	spAddTwo    = "1 + 2"
	spABField   = "a.b"
)

// spParseAST parses content with celtsource.Parse (macro-unexpanded) and
// returns the parsed AST, failing the test on any parse error.
func spParseAST(t *testing.T, content string) *celast.AST {
	t.Helper()
	ast, err := celtsource.Parse(common.NewStringSource(content, "test.cel"))
	if err != nil {
		t.Fatalf("Parse(%q): %v", content, err)
	}
	return ast
}

// spFirstIdent walks root and returns the first ident with the given name,
// failing the test if none is found.
func spFirstIdent(t *testing.T, root celast.Expr, name string) celast.Expr {
	t.Helper()
	var found celast.Expr
	celtsource.Walk(root, func(e celast.Expr) {
		if found == nil && e.Kind() == celast.IdentKind && e.AsIdent() == name {
			found = e
		}
	})
	if found == nil {
		t.Fatalf("ident %q not found in expression", name)
	}
	return found
}

// TestExprOffsetsRoot verifies ExprOffsets reports byte-exact spans for leaf
// expression nodes (ident and literal tokens), including multibyte (non-ASCII)
// content where the byte and rune offsets diverge.
func TestExprOffsetsRoot(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantSub string
	}{
		{name: spIdentName, content: spFooName, wantSub: spFooName},
		{name: "ident padded", content: "  foo  ", wantSub: spFooName},
		{name: "int literal", content: "42", wantSub: "42"},
		{name: "string literal", content: `"hi"`, wantSub: `"hi"`},
		{name: "multibyte string", content: `"привет"`, wantSub: `"привет"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content
			ast := spParseAST(t, content)
			info := ast.SourceInfo()
			ro := celtsource.RuneOffsets(content)
			start, end := ExprOffsets(info, ro, content, ast.Expr())
			if got := content[start:end]; got != tc.wantSub {
				t.Errorf("ExprOffsets span = content[%d:%d] = %q, want %q", start, end, got, tc.wantSub)
			}
			if want := strings.Index(content, tc.wantSub); start != want {
				t.Errorf("ExprOffsets start = %d, want byte offset %d", start, want)
			}
		})
	}
}

// TestExprOffsetsSubIdentMultibyte verifies ExprOffsets maps rune offsets to
// byte offsets for a sub-expression that follows multibyte text (the ident's
// rune offset differs from its byte offset).
func TestExprOffsetsSubIdentMultibyte(t *testing.T) {
	content := `"héllo" + tail`
	ast := spParseAST(t, content)
	info := ast.SourceInfo()
	ro := celtsource.RuneOffsets(content)
	ident := spFirstIdent(t, ast.Expr(), "tail")
	start, end := ExprOffsets(info, ro, content, ident)
	want := strings.Index(content, "tail")
	if start != want {
		t.Errorf("ExprOffsets(tail) start = %d, want byte offset %d", start, want)
	}
	if got := content[start:end]; got != "tail" {
		t.Errorf("ExprOffsets(tail) span = content[%d:%d] = %q, want %q", start, end, got, "tail")
	}
}

// TestExprOffsetsMultilineSubIdent verifies byte offsets for a sub-expression
// on a later line, after multibyte text and a newline have shifted the byte
// offsets away from the rune offsets.
func TestExprOffsetsMultilineSubIdent(t *testing.T) {
	content := "\"привет\" +\n" + spFooName
	ast := spParseAST(t, content)
	info := ast.SourceInfo()
	ro := celtsource.RuneOffsets(content)
	ident := spFirstIdent(t, ast.Expr(), spFooName)
	start, end := ExprOffsets(info, ro, content, ident)
	want := strings.Index(content, spFooName)
	if start != want {
		t.Errorf("ExprOffsets(foo) start = %d, want byte offset %d", start, want)
	}
	if got := content[start:end]; got != spFooName {
		t.Errorf("ExprOffsets(foo) span = content[%d:%d] = %q, want %q", start, end, got, spFooName)
	}
}

// TestExprOffsetsNoSourceInfo exercises the fallback path taken when the
// expression's id has no offset range recorded: it degrades to a one-byte span
// derived from the (zero-valued) start location.
func TestExprOffsetsNoSourceInfo(t *testing.T) {
	content := spFooName
	ast := spParseAST(t, content)
	empty := celast.NewSourceInfo(common.NewStringSource("", ""))
	ro := celtsource.RuneOffsets(content)
	start, end := ExprOffsets(empty, ro, content, ast.Expr())
	if start != 0 || end != 1 {
		t.Errorf("ExprOffsets(empty info) = (%d,%d), want (0,1) fallback", start, end)
	}
}

// TestExprByteSpan verifies ExprByteSpan spans the expression tree, including
// the select-field-name handling that extends the span over the field
// identifier, and multi-line content.
func TestExprByteSpan(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: spIdentName, content: spFooName, want: spFooName},
		{name: "ident padded", content: "  foo  ", want: spFooName},
		{name: "binary call", content: spAddTwo, want: spAddTwo},
		{name: "multiline call", content: "1 +\n2", want: "1 +\n2"},
		{name: "select field", content: spABField, want: spABField},
		{name: "select nested", content: "a.b.c", want: "a.b.c"},
		{name: "select map target", content: `{"k": 1}.v`, want: `{"k": 1}.v`},
		{name: "select with index target", content: `m["k"].v`, want: `m["k"].v`},
		{name: "multibyte call", content: `"привет" + 1`, want: `"привет" + 1`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.content
			ast := spParseAST(t, content)
			info := ast.SourceInfo()
			ro := celtsource.RuneOffsets(content)
			start, end := ExprByteSpan(info, ro, ast.Expr())
			if got := content[start:end]; got != tc.want {
				t.Errorf("ExprByteSpan = content[%d:%d] = %q, want %q", start, end, got, tc.want)
			}
		})
	}
}

// TestExprByteSpanSelectField verifies the select-field-name handling: the
// span must extend past the target to include the field identifier so the
// whole `a.b` is covered, not just the operand `a`.
func TestExprByteSpanSelectField(t *testing.T) {
	content := "  a.b  "
	ast := spParseAST(t, content)
	info := ast.SourceInfo()
	ro := celtsource.RuneOffsets(content)
	start, end := ExprByteSpan(info, ro, ast.Expr())
	if got := content[start:end]; got != spABField {
		t.Errorf("ExprByteSpan = content[%d:%d] = %q, want %q (field name included)", start, end, got, spABField)
	}
}

// TestExprByteSpanNoSourceInfo verifies the degraded behavior when no node has
// a recorded offset range: the function reports a zero-width span.
func TestExprByteSpanNoSourceInfo(t *testing.T) {
	content := spFooName
	ast := spParseAST(t, content)
	empty := celast.NewSourceInfo(common.NewStringSource("", ""))
	ro := celtsource.RuneOffsets(content)
	start, end := ExprByteSpan(empty, ro, ast.Expr())
	if start != 0 || end != 0 {
		t.Errorf("ExprByteSpan(empty info) = (%d,%d), want (0,0)", start, end)
	}
}

// TestExprByteSpanNilExpr verifies a nil expression yields a zero-width span.
func TestExprByteSpanNilExpr(t *testing.T) {
	info := celast.NewSourceInfo(common.NewStringSource(spFooName, ""))
	ro := celtsource.RuneOffsets(spFooName)
	start, end := ExprByteSpan(info, ro, nil)
	if start != 0 || end != 0 {
		t.Errorf("ExprByteSpan(nil expr) = (%d,%d), want (0,0)", start, end)
	}
}
