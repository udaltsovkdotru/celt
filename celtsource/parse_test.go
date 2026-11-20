package celtsource

import (
	"strings"
	"testing"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
)

func TestParseValid(t *testing.T) {
	srcValid := []struct {
		name    string
		content string
	}{
		{name: "literal int", content: "42"},
		{name: "literal string", content: `"hello world"`},
		{name: "arith", content: "1 + 2 * 3"},
		{name: "bool logic", content: "a && !b || c"},
		{name: "select", content: `msg.headers["x"] == "y"`},
		{name: "map literal", content: `{"a": 1, "b": 2}`},
		{name: "list literal", content: "[1, 2, 3]"},
		{name: "call", content: "size(xs) > 0"},
		{name: "multiline", content: "{\n  \"k\": v,\n  \"m\": 2\n}"},
	}
	for _, tc := range srcValid {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := Parse(common.NewTextSource(tc.content))
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tc.content, err)
			}
			if ast == nil {
				t.Fatalf("Parse(%q): nil AST", tc.content)
			}
			if ast.Expr() == nil {
				t.Fatalf("Parse(%q): nil root expr", tc.content)
			}
		})
	}
}

func TestParseInvalid(t *testing.T) {
	srcInvalid := []struct {
		name    string
		content string
	}{
		{name: "trailing operator", content: "1 +"},
		{name: "unclosed paren", content: "(1 + 2"},
		{name: "unclosed string", content: `"abc`},
		{name: "bad token", content: "1 @ 2"},
		{name: "missing operand", content: "a b"},
		{name: "unmatched close", content: "1 + 2)"},
	}
	for _, tc := range srcInvalid {
		t.Run(tc.name, func(t *testing.T) {
			ast, err := Parse(common.NewTextSource(tc.content))
			if err == nil {
				t.Fatalf("Parse(%q): expected error, got AST %v", tc.content, ast)
			}
			if !strings.Contains(err.Error(), "parse error") {
				t.Errorf("Parse(%q) error = %q, want it to mention parse error", tc.content, err)
			}
		})
	}
}

// TestWalkPreOrder verifies Walk visits every node exactly once in pre-order
// (root before children) using a nested expression.
func TestWalkPreOrder(t *testing.T) {
	content := srcArithExpr
	ast, err := Parse(common.NewTextSource(content))
	if err != nil {
		t.Fatalf("Parse(%q): %v", content, err)
	}

	var kinds []celast.ExprKind
	Walk(ast.Expr(), func(e celast.Expr) {
		kinds = append(kinds, e.Kind())
	})

	wantKinds := []celast.ExprKind{
		celast.CallKind,  // root _+_
		celast.IdentKind, // a
		celast.CallKind,  // nested _*_
		celast.IdentKind, // b
		celast.IdentKind, // c
	}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("visited %d nodes, want %d: %v", len(kinds), len(wantKinds), kinds)
	}
	for i, got := range kinds {
		if got != wantKinds[i] {
			t.Errorf("node %d kind = %v, want %v", i, got, wantKinds[i])
		}
	}
}

// TestWalkSingleNode verifies Walk visits a leaf expression exactly once.
func TestWalkSingleNode(t *testing.T) {
	ast, err := Parse(common.NewTextSource("42"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	visited := 0
	Walk(ast.Expr(), func(e celast.Expr) {
		visited++
		if e.Kind() != celast.LiteralKind {
			t.Errorf("kind = %v, want LiteralKind", e.Kind())
		}
	})
	if visited != 1 {
		t.Errorf("visited = %d, want 1", visited)
	}
}

// TestWalkNil verifies Walk(nil, visit) does not call the visitor.
func TestWalkNil(t *testing.T) {
	called := false
	Walk(nil, func(_ celast.Expr) {
		called = true
	})
	if called {
		t.Error("Walk(nil, visit) called the visitor")
	}
}
