package config_test

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"

	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/pkg/config"
)

func TestMacroFixedArityMatchesOverload(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"global", `env:
  macros:
    - name: example
      fixed_arity: 1
      overloads:
        - id: example_two_args
          args: [int, int]
          result: bool
`},
		{"receiver", `env:
  macros:
    - name: example
      receiver: true
      fixed_arity: 1
      overloads:
        - id: example_two_args
          args: [string, int, int]
          result: bool
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.ParseEnv([]byte(tc.yaml))
			if err == nil {
				t.Fatal("ParseEnv accepted an overload whose argument count disagrees with fixed_arity")
			}
			if !strings.Contains(err.Error(), "example") || !strings.Contains(err.Error(), "arity") {
				t.Errorf("error should identify the macro and arity mismatch: %v", err)
			}
		})
	}
}

// The Macro contract permits overloads and/or unconstrained results. If the
// overlap is not representable, validation must explain it before NewEnv.
func TestMacroTypedOverloadWithUnconstrainedResult(t *testing.T) {
	const input = `env:
  macros:
    - name: example
      fixed_arity: 1
      overloads:
        - id: example_string
          args: [string]
          result: bool
      results: [bool]
`
	e, err := config.ParseEnv([]byte(input))
	if err != nil {
		if !strings.Contains(err.Error(), "example") || !strings.Contains(err.Error(), "overload") || !strings.Contains(err.Error(), "result") {
			t.Errorf("validation error should identify the macro and conflicting overload/results: %v", err)
		}
		return
	}
	opts, err := e.Options()
	if err != nil {
		if !strings.Contains(err.Error(), "example") || !strings.Contains(err.Error(), "overload") || !strings.Contains(err.Error(), "result") {
			t.Errorf("options error should identify the macro and conflicting overload/results: %v", err)
		}
		return
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("accepted config must construct a CEL environment: %v", err)
	}
	for _, src := range []string{`example("s")`, `example(1)`} {
		ast, issues := env.Compile(src)
		if issues != nil && issues.Err() != nil {
			t.Errorf("Compile(%s): %v", src, issues.Err())
			continue
		}
		if ast.OutputType() != cel.BoolType {
			t.Errorf("Compile(%s) output = %v, want bool", src, ast.OutputType())
		}
	}
}

func TestBuiltinMacroHoverExpansionIndependentOfCustomMacros(t *testing.T) {
	const src = `[1, 2].exists(x, x > 0)`
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"no custom macro", "env: {}\n"},
		{"unrelated custom macro", "env:\n  macros:\n    - name: custom\n      fixed_arity: 1\n      results: [bool]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := config.ParseEnv([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			opts, err := e.Options()
			if err != nil {
				t.Fatal(err)
			}
			env, err := cel.NewEnv(opts...)
			if err != nil {
				t.Fatal(err)
			}
			a, err := celtlint.New(env)
			if err != nil {
				t.Fatal(err)
			}
			got := a.Hover(src, 0, strings.Index(src, "exists"))
			// Macro tracking must work for builtin macros regardless of any
			// custom macros. A comprehension macro (exists/all/map/filter) is
			// rendered as its own call (as-is), so the hover shows the macro
			// identity and its as-is form, independent of any custom macros.
			if !strings.Contains(got, "exists") {
				t.Errorf("builtin macro hover missing macro identity: %q", got)
			}
			if !strings.Contains(got, "[1, 2].exists(x, x > 0)") {
				t.Errorf("builtin comprehension macro hover missing as-is form: %q", got)
			}
		})
	}
}
