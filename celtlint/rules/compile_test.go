package rules

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtlint"
)

// lnHasMacro is the built-in CEL presence-test macro name used to assert that
// ParseUnexpanded keeps macro calls in their unexpanded form.
const lnHasMacro = "has"

// coEnv builds a minimal CEL environment with no declarations, suitable for
// producing real compiler diagnostics.
func coEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv()
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// coCheck compiles content and runs the compile rule over it.
func coCheck(t *testing.T, env *cel.Env, content string) []celtlint.Finding {
	t.Helper()
	src := common.NewStringSource(content, "t")
	ctx := &celtlint.Context{Source: src}
	ctx.Ast, ctx.Err = env.CompileSource(src)
	return checkCompile(ctx)
}

// TestParseUnexpandedKeepsMacro verifies ParseUnexpanded returns the parsed,
// unexpanded AST: a macro invocation such as has() is still present as a call
// node rather than being expanded into its desugared form.
func TestParseUnexpandedKeepsMacro(t *testing.T) {
	src := common.NewStringSource(`has({"a": 1}.a)`, "t")
	ast := celtlint.ParseUnexpanded(src)
	if ast == nil {
		t.Fatal("ParseUnexpanded returned nil AST")
	}
	root := ast.Expr()
	if root == nil {
		t.Fatal("ParseUnexpanded returned an AST with no root expr")
	}
	if root.Kind() != celast.CallKind {
		t.Fatalf("root kind = %v, want CallKind (unexpanded macro call)", root.Kind())
	}
	if got := root.AsCall().FunctionName(); got != lnHasMacro {
		t.Errorf("root function = %q, want %q", got, lnHasMacro)
	}
}

// TestParseUnexpandedInvalid verifies ParseUnexpanded returns nil for a source
// that fails to parse (the parse error is discarded, not returned).
func TestParseUnexpandedInvalid(t *testing.T) {
	src := common.NewStringSource("1 +", "t")
	if ast := celtlint.ParseUnexpanded(src); ast != nil {
		t.Errorf("ParseUnexpanded = %v, want nil for invalid source", ast)
	}
}

func TestCheckCompileNoErrors(t *testing.T) {
	env := coEnv(t)
	if fs := coCheck(t, env, `1 + 2`); len(fs) != 0 {
		t.Errorf("checkCompile = %d findings, want 0", len(fs))
	}
}

// TestCompileFindingsCategory checks that compile failures are turned into
// findings with the correct severity and category code.
func TestCompileFindingsCategory(t *testing.T) {
	env := coEnv(t)
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: sgUndeclaredRef, content: "undeclaredRef", want: celtlint.CodeUndeclaredReference},
		{name: "token recognition error", content: "1 @ 2", want: celtlint.CodeUnexpectedToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := coCheck(t, env, tt.content)
			if len(fs) == 0 {
				t.Fatal("checkCompile returned no findings")
			}
			f := fs[0]
			if f.Level != celtlint.LevelError {
				t.Errorf("Level = %v, want LevelError", f.Level)
			}
			if f.Code != tt.want {
				t.Errorf("Code = %q, want %q", f.Code, tt.want)
			}
			if f.Message == "" {
				t.Error("Message is empty")
			}
		})
	}
}

// TestCompileFindingsSortedByLocation verifies multiple compile errors in one
// expression are reported in source order (line, then column), not in the
// checker's internal order.
func TestCompileFindingsSortedByLocation(t *testing.T) {
	env := coEnv(t)
	fs := coCheck(t, env, "undeclaredFoo + undeclaredBar")
	if len(fs) < 2 {
		t.Fatalf("findings = %d, want at least 2", len(fs))
	}
	if fs[0].Start.Line != 0 || fs[1].Start.Line != 0 {
		t.Errorf("findings not on expected line: lines = %d, %d", fs[0].Start.Line, fs[1].Start.Line)
	}
	if fs[0].Start.Char >= fs[1].Start.Char {
		t.Errorf("findings not sorted by column: chars = %d, %d", fs[0].Start.Char, fs[1].Start.Char)
	}
	for _, f := range fs {
		if f.Code != celtlint.CodeUndeclaredReference {
			t.Errorf("Code = %q, want %q", f.Code, celtlint.CodeUndeclaredReference)
		}
	}
}

// TestCompileFindingsPositions verifies each finding carries a source span.
func TestCompileFindingsPositions(t *testing.T) {
	env := coEnv(t)
	fs := coCheck(t, env, "undeclaredRef")
	if len(fs) == 0 {
		t.Fatal("checkCompile returned no findings")
	}
	f := fs[0]
	if f.Start.Line != 0 {
		t.Errorf("Start.Line = %d, want 0", f.Start.Line)
	}
	if f.Start.Char != 0 {
		t.Errorf("Start.Char = %d, want 0", f.Start.Char)
	}
	if f.End.Char <= f.Start.Char {
		t.Errorf("End.Char = %d, want greater than Start.Char %d", f.End.Char, f.Start.Char)
	}
}

// TestCompileFindingsUndeclaredReference verifies an undeclared reference is
// turned into an error finding with the right category, code and span.
func TestCompileFindingsUndeclaredReference(t *testing.T) {
	env := coEnv(t)
	fs := coCheck(t, env, "foo")
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	f := fs[0]
	if f.Level != celtlint.LevelError {
		t.Errorf("Level = %v, want LevelError", f.Level)
	}
	if f.Code != celtlint.CodeUndeclaredReference {
		t.Errorf("Code = %q, want %q", f.Code, celtlint.CodeUndeclaredReference)
	}
	if f.Message != "undeclared reference" {
		t.Errorf("Message = %q, want %q", f.Message, "undeclared reference")
	}
	if f.Start.Line < 0 || f.Start.Char < 0 {
		t.Errorf("invalid Start position: %+v", f.Start)
	}
	if f.End.Line < f.Start.Line {
		t.Errorf("End precedes Start: %+v..%+v", f.Start, f.End)
	}
}

// TestCompileFindingsNoMatchingOverload verifies a no-matching-overload error
// is categorized and named appropriately.
func TestCompileFindingsNoMatchingOverload(t *testing.T) {
	env := coEnv(t)
	fs := coCheck(t, env, `1 + "a"`)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Code != celtlint.CodeNoMatchingOverload {
		t.Errorf("Code = %q, want %q", fs[0].Code, celtlint.CodeNoMatchingOverload)
	}
	if fs[0].Message != "no matching overload" {
		t.Errorf("Message = %q, want %q", fs[0].Message, "no matching overload")
	}
}

// TestCompileFindingsSortedByLocation verifies multiple compile errors are
// reported in source order.
func TestCompileFindingsSortedByLocationMulti(t *testing.T) {
	env := coEnv(t)
	fs := coCheck(t, env, "foo + bar")
	if len(fs) < 2 {
		t.Fatalf("findings = %d, want >= 2", len(fs))
	}
	for i := 1; i < len(fs); i++ {
		prev, cur := fs[i-1].Start, fs[i].Start
		if cur.Line < prev.Line || (cur.Line == prev.Line && cur.Char < prev.Char) {
			t.Errorf("findings not sorted by location: [%d]=%+v then [%d]=%+v",
				i-1, prev, i, cur)
		}
	}
	if fs[0].Start.Line != 0 {
		t.Errorf("expected first error on line 0, got %+v", fs[0].Start)
	}
}

// TestCompileFindingsDetailHasMessage verifies the raw compiler message is
// preserved in each finding's Detail.
func TestCompileFindingsDetailHasMessage(t *testing.T) {
	env := coEnv(t)
	fs := coCheck(t, env, "foo")
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if !strings.Contains(fs[0].Detail, "undeclared reference") {
		t.Errorf("Detail = %q, want it to contain message", fs[0].Detail)
	}
}
