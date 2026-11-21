package config

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
)

const (
	macroName = "matches_any"
	macroEnv  = `
env:
  macros:
    - name: ` + macroName + `
      receiver: false
      vararg: true
      results:
        - bool
      doc: "true when any candidate matches"
`
)

func TestNoopMacroParses(t *testing.T) {
	e, err := ParseEnv([]byte(macroEnv))
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	opts, err := e.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	// The macro is registered in the env (recognized by parser/hover/etc.).
	found := false
	for _, m := range env.Macros() {
		if m.Function() == macroName {
			found = true
		}
	}
	if !found {
		t.Fatal(macroName + " macro not registered")
	}
}

// TestMacroCallTypeChecks verifies a vararg macro call is accepted by the
// parser and type-checks against the synthetic function, reporting the
// declared output type.
func TestMacroCallTypeChecks(t *testing.T) {
	e, err := ParseEnv([]byte(macroEnv))
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	opts, err := e.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	for _, src := range []string{
		`matches_any("a")`,
		`matches_any("a", "b", "c")`,
	} {
		ast, iss := env.Compile(src)
		if iss != nil && iss.Err() != nil {
			t.Errorf("Compile(%q): %v", src, iss.Err())
			continue
		}
		if got := ast.OutputType(); got != cel.BoolType {
			t.Errorf("Compile(%q) output type = %v, want %v", src, got, cel.BoolType)
		}
	}
}

const macroTypedEnv = `
env:
  macros:
    - name: includes
      receiver: true
      fixed_arity: 1
      overloads:
        - id: list_includes
          args:
            - "list<string>"
            - "string"
          result: bool
`

// TestMacroTypedOverloads verifies a macro with exact input/output type pairs
// type-checks against the declared overloads.
func TestMacroTypedOverloads(t *testing.T) {
	e, err := ParseEnv([]byte(macroTypedEnv))
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	opts, err := e.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	ast, iss := env.Compile(`["a", "b"].includes("a")`)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile: %v", iss.Err())
	}
	if got := ast.OutputType(); got != cel.BoolType {
		t.Errorf("output type = %v, want %v", got, cel.BoolType)
	}
}

// TestMacroRejectsMultipleResults verifies a macro with several unconstrained
// result types is rejected with a clear validation error (CEL overloads are
// keyed by argument signature, so they would collide at env construction).
func TestMacroRejectsMultipleResults(t *testing.T) {
	_, err := ParseEnv([]byte(`
env:
  macros:
    - name: mm
      vararg: true
      results:
        - bool
        - string
`))
	if err == nil {
		t.Fatal("ParseEnv: expected an error for multiple results, got nil")
	}
}

// TestMacroVarArgAcceptsManyArgs verifies a vararg macro accepts calls well
// beyond the former ten-argument cap (the documented limit is maxMacroArity).
func TestMacroVarArgAcceptsManyArgs(t *testing.T) {
	e, err := ParseEnv([]byte(macroEnv))
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	opts, err := e.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	args := make([]string, 20)
	for i := range args {
		args[i] = "1"
	}
	if _, iss := env.Compile("matches_any(" + strings.Join(args, ", ") + ")"); iss != nil && iss.Err() != nil {
		t.Errorf("Compile(20 args): %v", iss.Err())
	}
}
