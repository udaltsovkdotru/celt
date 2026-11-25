package celtlint

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
)

// bindAnalyzer builds an analyzer over ext.Bindings (cel.bind) plus the
// completion descriptor set (a Widget message) and the two completion
// variables, so a locally bound variable's type and members can be resolved.
func bindAnalyzer(t *testing.T) *Analyzer {
	t.Helper()
	env, err := cel.NewEnv(
		ext.Bindings(),
		cel.Variable(cpObjVar, cel.ObjectType("test.pkg.Widget")),
		cel.Variable(cpUserName, cel.StringType),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	a, err := New(env, WithDescriptor(cpTestFDSet()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// TestBindVariableHoverShowsType verifies hovering a cel.bind bound variable
// surfaces its type (the value expression's type) instead of the global
// documentation.
func TestBindVariableHoverShowsType(t *testing.T) {
	a := bindAnalyzer(t)
	src := bindRenderSrc
	// Hover the `secret` reference inside the third argument (the body).
	off := strings.Index(src, "secret ==")
	if got := a.Hover(src, 0, off); !strings.Contains(got, "var secret string") {
		t.Errorf("Hover = %q, want the bound variable's string type", got)
	}
}

// TestBindVariableCompletionInScope verifies a cel.bind bound variable is
// offered as a completion inside its scope (the third argument).
func TestBindVariableCompletionInScope(t *testing.T) {
	a := bindAnalyzer(t)
	src := `cel.bind(secret, "hello", secret)`
	// Cursor just before the closing paren, i.e. after the body's `secret`.
	items := a.Completion(src, 0, len(src)-1)
	if _, ok := cpFindItem(items, "secret"); !ok {
		t.Errorf("Completion = %+v, want the in-scope bound variable secret", items)
	}
}

// TestBindVariableMemberCompletion verifies member completion on a cel.bind
// bound variable resolves the bound value's type and offers its fields.
func TestBindVariableMemberCompletion(t *testing.T) {
	a := bindAnalyzer(t)
	// `item` is bound to the Widget `w`; complete a member prefix on it inside
	// the body. The document stays parseable so the binding can be resolved.
	src := `cel.bind(item, w, item.na) && true`
	off := strings.Index(src, "item.na") + len("item.na")
	items := a.Completion(src, 0, off)
	if _, ok := cpFindItem(items, cpFieldName); !ok {
		t.Errorf("Completion = %+v, want Widget member %q", items, cpFieldName)
	}
}
