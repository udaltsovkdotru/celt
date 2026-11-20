package celtlint

import (
	"testing"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
)

// spMatchesAnyName is reused in several docblock tests and kept in a constant
// to satisfy the goconst linter.
const spMatchesAnyName = "matches_any"

// TestParseUnexpandedValid verifies ParseUnexpanded returns a parsed AST with
// source info and no recorded macro expansions, and that the root node reflects
// the unexpanded source (a plain call, not its macro expansion).
func TestParseUnexpandedValid(t *testing.T) {
	tests := []struct {
		name    string
		content string
		check   func(t *testing.T, root celast.Expr)
	}{
		{
			name:    spIdentName,
			content: spFooName,
			check: func(t *testing.T, root celast.Expr) {
				t.Helper()
				if root.Kind() != celast.IdentKind || root.AsIdent() != spFooName {
					t.Errorf("root = %v, want ident %q", root, spFooName)
				}
			},
		},
		{
			name:    "plain call stays unexpanded",
			content: spMatchesAnyName + `("a", "b")`,
			check: func(t *testing.T, root celast.Expr) {
				t.Helper()
				if root.Kind() != celast.CallKind {
					t.Fatalf("root kind = %v, want call", root.Kind())
				}
				if got := root.AsCall().FunctionName(); got != spMatchesAnyName {
					t.Errorf("root function = %q, want %q (must not be macro-expanded)", got, spMatchesAnyName)
				}
			},
		},
		{
			name:    "builtin macro stays unexpanded",
			content: `has({"a": 1}.a)`,
			check: func(t *testing.T, root celast.Expr) {
				t.Helper()
				// has() is parsed into a call whose expansion is deferred until
				// checking; parsing must not expand it into a test-only select.
				if root.Kind() != celast.CallKind {
					t.Errorf("root kind = %v, want call (macro not expanded at parse time)", root.Kind())
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ast := ParseUnexpanded(common.NewStringSource(tc.content, "test.cel"))
			if ast == nil {
				t.Fatal("ParseUnexpanded returned nil AST for valid input")
			}
			if ast.Expr() == nil {
				t.Fatal("ParseUnexpanded AST has nil root expr")
			}
			if ast.SourceInfo() == nil {
				t.Fatal("ParseUnexpanded AST has nil source info")
			}
			// A parse-stage AST must not carry macro call records.
			if n := len(ast.SourceInfo().MacroCalls()); n != 0 {
				t.Errorf("parsed AST has %d macro calls, want 0 (not expanded)", n)
			}
			tc.check(t, ast.Expr())
		})
	}
}

// TestParseUnexpandedInvalidInput verifies invalid input yields a nil AST (the
// underlying parse error is discarded, so the caller receives no AST).
func TestParseUnexpandedInvalidInput(t *testing.T) {
	spInvalid := []string{
		"1 +",
		"(1 + 2",
		`"abc`,
		"1 @ 2",
	}
	for _, content := range spInvalid {
		if ast := ParseUnexpanded(common.NewStringSource(content, "test.cel")); ast != nil {
			t.Errorf("ParseUnexpanded(%q) = non-nil AST, want nil", content)
		}
	}
}
