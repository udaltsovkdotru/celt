package celtlint

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	celtypes "cel.dev/cel-go/common/types"
	"cel.dev/cel-go/ext"
	"cel.dev/cel-go/parser"

	"github.com/udaltsovkdotru/celt/celtsource"
)

const boolTypeName = "bool"

// bindRenderSrc is a cel.bind expression reused by the bind-rendering tests so
// the literal is not repeated (goconst).
const bindRenderSrc = `cel.bind(secret, "hello", secret == userName)`

// testMacroEnv builds an env with a passthrough signature-only macro and macro
// call tracking enabled, mirroring how the config package registers macros.
func testMacroEnv(t *testing.T) *cel.Env {
	t.Helper()
	noop := func(eh parser.ExprHelper, target celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		if target != nil {
			return eh.NewMemberCall("matches_any", target, args...), nil
		}
		return eh.NewCall("matches_any", args...), nil
	}
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		cel.Macros(cel.GlobalVarArgMacro("matches_any", noop)),
		cel.Function("matches_any",
			cel.Overload("matches_any_1", []*cel.Type{cel.DynType}, cel.BoolType),
			cel.Overload("matches_any_2", []*cel.Type{cel.DynType, cel.DynType}, cel.BoolType),
			cel.Overload("matches_any_3", []*cel.Type{cel.DynType, cel.DynType, cel.DynType}, cel.BoolType),
		),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

func TestMacroCallAt(t *testing.T) {
	env := testMacroEnv(t)
	content := `matches_any("a", "b")`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	ro := celtsource.RuneOffsets(content)
	mc, ok := MacroCallAt(ast.NativeRep(), ro, content, env, 1)
	if !ok {
		t.Fatal("MacroCallAt: macro call not found at offset 1")
	}
	if mc.Name != "matches_any" {
		t.Errorf("Name = %q, want matches_any", mc.Name)
	}
	if mc.ResultType != boolTypeName {
		t.Errorf("ResultType = %q, want bool", mc.ResultType)
	}
	if mc.Offset != 0 {
		t.Errorf("Offset = %d, want 0", mc.Offset)
	}
	if mc.Expansion == "" {
		t.Error("Expansion is empty")
	}
}

func TestMacroCallAtNotMacro(t *testing.T) {
	env := testMacroEnv(t)
	content := `1 + 2`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	if mc, ok := MacroCallAt(ast.NativeRep(), celtsource.RuneOffsets(content), content, env, 0); ok {
		t.Fatalf("MacroCallAt: unexpected macro call %+v", mc)
	}
}

func TestMacroCallsIn(t *testing.T) {
	env := testMacroEnv(t)
	content := `matches_any("a") && matches_any("b", "c")`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	calls := MacroCallsIn(ast.NativeRep(), celtsource.RuneOffsets(content), content, env)
	if len(calls) != 2 {
		t.Fatalf("MacroCallsIn = %d calls, want 2", len(calls))
	}
	for _, c := range calls {
		if c.Name != "matches_any" || c.ResultType != boolTypeName {
			t.Errorf("unexpected macro call: %+v", c)
		}
	}
}

// TestMacroSpanSkipsStringLiteral verifies a paren inside a string argument
// does not terminate the macro span early (regression: macroSpan counted raw
// parens without skipping string literals).
func TestMacroSpanSkipsStringLiteral(t *testing.T) {
	env := testMacroEnv(t)
	content := `matches_any("a)b")`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	ro := celtsource.RuneOffsets(content)
	mc, ok := MacroCallAt(ast.NativeRep(), ro, content, env, 1)
	if !ok {
		t.Fatal("MacroCallAt: macro not found")
	}
	if mc.End != len(content) {
		t.Errorf("End = %d, want %d (paren inside string literal must be ignored)", mc.End, len(content))
	}
}

func TestMacroCallSpanIncludesNameAndSkipsComments(t *testing.T) {
	env := testMacroEnv(t)
	for _, content := range []string{
		`matches_any  ("a")`,
		"matches_any(\"a\", // ) is comment text\n \"b\")",
		"has // comment before arguments\n ({'a': 1}.a)",
	} {
		t.Run(content, func(t *testing.T) {
			ast, issues := env.Compile(content)
			if err := issues.Err(); err != nil {
				t.Fatalf("invalid macro call fixture: %v", err)
			}
			calls := MacroCallsIn(ast.NativeRep(), celtsource.RuneOffsets(content), content, env)
			if len(calls) != 1 || calls[0].Offset != 0 || calls[0].End != len(content) {
				t.Errorf("macro call span should cover bytes [0,%d): %+v", len(content), calls)
			}
		})
	}
}

func TestMacroCallSpanIgnoresSlashesInsidePrecedingString(t *testing.T) {
	env := testMacroEnv(t)
	const content = `"https://x" == "x" && matches_any("a")`
	ast, issues := env.Compile(content)
	if err := issues.Err(); err != nil {
		t.Fatal(err)
	}
	native := ast.NativeRep()
	ro := celtsource.RuneOffsets(content)
	calls := MacroCallsIn(native, ro, content, env)
	want := strings.Index(content, "matches_any")
	if len(calls) != 1 || calls[0].Offset != want || calls[0].End != len(content) {
		t.Errorf("expected exactly one macro span [%d,%d): %+v", want, len(content), calls)
	}
	if call, ok := MacroCallAt(native, ro, content, env, 0); ok {
		t.Errorf("preceding string was mistaken for part of the macro call: %+v", call)
	}
}

func TestReceiverMacroShowsAsIsExpansion(t *testing.T) {
	env, err := cel.NewEnv(cel.EnableMacroCallTracking())
	if err != nil {
		t.Fatal(err)
	}
	const content = "[1,2].all(x,x>0)"
	ast, issues := env.Compile(content)
	if err := issues.Err(); err != nil {
		t.Fatal(err)
	}
	calls := MacroCallsIn(ast.NativeRep(), celtsource.RuneOffsets(content), content, env)
	if len(calls) != 1 || calls[0].Name != "all" {
		t.Fatalf("expected tracked all call: %+v", calls)
	}
	// A comprehension macro is rendered as its own call (as-is), so hovering it
	// surfaces the macro's own form.
	if expansion := strings.Join(strings.Fields(calls[0].Expansion), ""); expansion != "[1,2].all(x,x>0)" {
		t.Errorf("Expansion = %q, want the as-is macro form %q", calls[0].Expansion, "[1,2].all(x,x>0)")
	}
}

func TestMacroCallAtEndExclusive(t *testing.T) {
	env := testMacroEnv(t)
	content := `matches_any("a", "b")`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	ro := celtsource.RuneOffsets(content)
	native := ast.NativeRep()
	mc, ok := MacroCallAt(native, ro, content, env, 1)
	if !ok {
		t.Fatal("MacroCallAt: macro not found")
	}
	if _, ok := MacroCallAt(native, ro, content, env, mc.End); ok {
		t.Error("MacroCallAt: matched at exclusive End boundary")
	}
	if _, ok := MacroCallAt(native, ro, content, env, mc.End-1); !ok {
		t.Error("MacroCallAt: missed the position just inside End")
	}
}

// nestedMacroEnv mimics a macro whose expansion node is consumed by an
// enclosing macro: pick(cond, val) expands to a conditional, and best(...)
// consumes pick expansions (rebuilding new conditionals from their cond/val),
// so the pick expansion nodes are dropped from the checked AST.
func nestedMacroEnv(t *testing.T) *cel.Env {
	t.Helper()
	pick := func(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		zero := eh.NewLiteral(celtypes.Int(0))
		return eh.NewCall(operators.Conditional, args[0], args[1], zero), nil
	}
	best := func(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		expr := eh.NewLiteral(celtypes.Int(0))
		for i := len(args) - 1; i >= 0; i-- {
			a := args[i].AsCall()
			cond, val := a.Args()[0], a.Args()[1]
			expr = eh.NewCall(operators.Conditional, cond, val, expr)
		}
		return expr, nil
	}
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		cel.Macros(
			parser.NewGlobalMacro("pick", 2, pick),
			parser.NewGlobalVarArgMacro("best", best),
		),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// TestMacroCallAtNested verifies hovering a macro nested inside a consuming
// macro resolves to the inner macro, not the enclosing one.
func TestMacroCallAtNested(t *testing.T) {
	env := nestedMacroEnv(t)
	content := `best(pick(true, 1), pick(false, 2))`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	ro := celtsource.RuneOffsets(content)
	native := ast.NativeRep()
	// offset 6 is inside the first `pick` (which starts at offset 5).
	mc, ok := MacroCallAt(native, ro, content, env, 6)
	if !ok {
		t.Fatal("MacroCallAt: no macro found at offset 6")
	}
	if mc.Name != "pick" {
		t.Errorf("Name = %q, want pick (got enclosing macro)", mc.Name)
	}
}

// TestMacroExpansionRendered verifies the Expansion field carries the real
// expanded expression, not the original macro call text.
func TestMacroExpansionRendered(t *testing.T) {
	pick := func(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		zero := eh.NewLiteral(celtypes.Int(0))
		return eh.NewCall(operators.Conditional, args[0], args[1], zero), nil
	}
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		cel.Macros(parser.NewGlobalMacro("pick", 2, pick)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	content := `pick(true, 1)`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	calls := MacroCallsIn(ast.NativeRep(), celtsource.RuneOffsets(content), content, env)
	if len(calls) != 1 {
		t.Fatalf("MacroCallsIn = %d, want 1", len(calls))
	}
	if got := calls[0].Expansion; got != "true ? 1 : 0" {
		t.Errorf("Expansion = %q, want %q", got, "true ? 1 : 0")
	}
}

// TestMacroHas verifies the built-in has() macro is resolved: it is tracked and
// reported as bool, with no expansion text (a presence test has no textual form
// other than has(...), so echoing it would be redundant).
func TestMacroHas(t *testing.T) {
	env, err := cel.NewEnv(cel.EnableMacroCallTracking())
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	content := `has({"a": 1}.a)`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	ro := celtsource.RuneOffsets(content)
	mc, ok := MacroCallAt(ast.NativeRep(), ro, content, env, 1)
	if !ok {
		t.Fatal("MacroCallAt: has not found")
	}
	if mc.Name != "has" {
		t.Errorf("Name = %q, want has", mc.Name)
	}
	if mc.ResultType != boolTypeName {
		t.Errorf("ResultType = %q, want bool", mc.ResultType)
	}
	if mc.Expansion != "" {
		t.Errorf("Expansion = %q, want empty (presence test has no textual expansion)", mc.Expansion)
	}
}

// listContainsMacro expands <recv>.list_contains(values) into a single
// comprehension over the receiver list:
//
//	<recv>.exists(x, x in values)
func listContainsMacro(eh parser.ExprHelper, target celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
	accu := eh.AccuIdentName()
	item := eh.NewIdent("x")
	return eh.NewComprehension(
		target,
		"x",
		accu,
		eh.NewLiteral(celtypes.False),
		eh.NewCall(operators.NotStrictlyFalse, eh.NewCall(operators.LogicalNot, eh.NewAccuIdent())),
		eh.NewCall(operators.LogicalOr, eh.NewAccuIdent(), eh.NewCall(operators.In, item, args[0])),
		eh.NewAccuIdent(),
	), nil
}

// TestMacroReceiverComprehensionExpansion verifies that a receiver macro whose
// expansion is a comprehension is shown as-is (its own call form), without
// being desugared into a guessed shape.
func TestMacroReceiverComprehensionExpansion(t *testing.T) {
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		cel.Macros(parser.NewReceiverMacro("list_contains", 1, listContainsMacro)),
		cel.Variable("xs", cel.ListType(cel.StringType)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	content := `xs.list_contains(["a"])`
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	ro := celtsource.RuneOffsets(content)
	mc, ok := MacroCallAt(ast.NativeRep(), ro, content, env, 3)
	if !ok {
		t.Fatal("MacroCallAt: list_contains not found")
	}
	// The expansion is the macro's own call form, shown as-is.
	if want := `xs.list_contains(["a"])`; mc.Expansion != want {
		t.Errorf("Expansion = %q, want %q", mc.Expansion, want)
	}
}

// TestRenderStandardComprehensions verifies the standard comprehension shapes
// are rendered back into their macro form by the renderer.
func TestRenderStandardComprehensions(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"exists", `[1, 2].exists(x, x > 1)`, `[1, 2].exists(x, x > 1)`},
		{"all", `[1, 2].all(x, x > 0)`, `[1, 2].all(x, x > 0)`},
		{"map", `[1, 2].map(x, x * 2)`, `[1, 2].map(x, x * 2)`},
		{"filter", `[1, 2].filter(x, x > 1)`, `[1, 2].filter(x, x > 1)`},
		{"exists_one", `[1, 2].exists_one(x, x == 1)`, `[1, 2].exists_one(x, x == 1)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(cel.EnableMacroCallTracking())
			if err != nil {
				t.Fatal(err)
			}
			ast, iss := env.Compile(tt.src)
			if iss != nil && iss.Err() != nil {
				t.Fatalf("Compile: %v", iss.Err())
			}
			native := ast.NativeRep()
			ro := celtsource.RuneOffsets(tt.src)
			calls := MacroCallsIn(native, ro, tt.src, env)
			if len(calls) != 1 {
				t.Fatalf("MacroCallsIn = %d, want 1", len(calls))
			}
			node, ok := findExprByID(native.Expr(), calls[0].ID)
			if !ok || node.Kind() != celast.ComprehensionKind {
				t.Fatalf("expansion node not found or not a comprehension")
			}
			got, ok := renderComprehension(node, native)
			if !ok || got != tt.want {
				t.Errorf("renderComprehension = %q (ok=%v), want %q", got, ok, tt.want)
			}
		})
	}
}

// TestRenderBindComprehension verifies a cel.bind comprehension (whose loop
// step is the accumulator identity) renders back to its `cel.bind` form.
func TestRenderBindComprehension(t *testing.T) {
	env, err := cel.NewEnv(cel.EnableMacroCallTracking(), ext.Bindings(),
		cel.Variable("userName", cel.StringType))
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	src := bindRenderSrc
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	native := ast.NativeRep()
	ro := celtsource.RuneOffsets(src)
	calls := MacroCallsIn(native, ro, src, env)
	if len(calls) != 1 {
		t.Fatalf("MacroCallsIn = %d, want 1", len(calls))
	}
	node, ok := findExprByID(native.Expr(), calls[0].ID)
	if !ok || node.Kind() != celast.ComprehensionKind {
		t.Fatalf("expansion node not found or not a comprehension")
	}
	got, ok := renderComprehension(node, native)
	if !ok {
		t.Fatalf("renderComprehension failed for cel.bind")
	}
	if want := bindRenderSrc; got != want {
		t.Errorf("renderComprehension = %q, want %q", got, want)
	}
}

// wrapMacro expands wrap(cond, val) to `cond ? val : 0`, so its expansion is a
// non-comprehension node that can contain a comprehension in its first argument
// (mirroring a macro wrapping a comprehension).
func wrapMacro(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
	zero := eh.NewLiteral(celtypes.Int(0))
	return eh.NewCall(operators.Conditional, args[0], args[1], zero), nil
}

// TestMacroWrappingComprehensionExpansion verifies a macro whose expansion is
// not itself a comprehension but contains one (a macro wrapping a cel.bind) is
// rendered with the nested comprehension desugared, not dropped.
func TestMacroWrappingComprehensionExpansion(t *testing.T) {
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		ext.Bindings(),
		cel.Macros(parser.NewGlobalMacro("wrap", 2, wrapMacro)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	const src = `wrap(cel.bind(secret, "hello", secret == "hello"), 1)`
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	native := ast.NativeRep()
	calls := MacroCallsIn(native, celtsource.RuneOffsets(src), src, env)
	var wrapExp string
	for _, mc := range calls {
		if mc.Name == "wrap" {
			wrapExp = mc.Expansion
		}
	}
	if want := `cel.bind(secret, "hello", secret == "hello") ? 1 : 0`; wrapExp != want {
		t.Errorf("wrap expansion = %q, want %q", wrapExp, want)
	}
}

// pickMacro expands pick(cond, val) to `cond ? val : 0`, a non-comprehension
// macro used to check that such macros appearing inside a comprehension are
// themselves expanded when the comprehension is rendered.
func pickMacro(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
	zero := eh.NewLiteral(celtypes.Int(0))
	return eh.NewCall(operators.Conditional, args[0], args[1], zero), nil
}

// TestComprehensionExpandsNonComprehensionMacroArgs verifies that when a
// comprehension's sub-expression (range or predicate) is itself a
// non-comprehension macro, that macro is expanded too — the recursion continues
// until the sub-expression has no macros left (or hits a comprehension macro,
// which a direct-only renderer cannot reconstruct).
func TestComprehensionExpandsNonComprehensionMacroArgs(t *testing.T) {
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		cel.Macros(parser.NewGlobalMacro("pick", 2, pickMacro)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	// The `map` macro's predicate is a `pick` call that desugars to a
	// conditional; the rendered comprehension must show that expansion, not the
	// raw `pick(...)`.
	const src = `[1,2].map(x, pick(x > 0, x))`
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	native := ast.NativeRep()
	calls := MacroCallsIn(native, celtsource.RuneOffsets(src), src, env)
	var mapExp string
	for _, mc := range calls {
		if mc.Name == "map" {
			mapExp = mc.Expansion
		}
	}
	if want := `[1, 2].map(x, (x > 0) ? x : 0)`; mapExp != want {
		t.Errorf("map expansion = %q, want %q", mapExp, want)
	}
}

// TestComprehensionNestedMacroArgRendersAsIs verifies that a comprehension
// whose argument is itself a comprehension macro is rendered as-is: the outer
// macro keeps its own call form and the nested macro is shown as its own call.
func TestComprehensionNestedMacroArgRendersAsIs(t *testing.T) {
	env, err := cel.NewEnv(cel.EnableMacroCallTracking())
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	const src = `[1,2].exists(x, [3,4].all(y, y > x))`
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	native := ast.NativeRep()
	ro := celtsource.RuneOffsets(src)
	calls := MacroCallsIn(native, ro, src, env)
	var existsExp string
	for _, mc := range calls {
		if mc.Name == "exists" {
			existsExp = mc.Expansion
		}
	}
	if want := `[1, 2].exists(x, [3, 4].all(y, y > x))`; existsExp != want {
		t.Errorf("exists expansion = %q, want %q", existsExp, want)
	}
}
