package config

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
)

const sampleEnv = `
env:
  container: example.com
  extensions:
    - optional-types
    - lists
    - two-var-comprehensions
    - regex
  variables:
    - name: account
      type: int
      doc: "the account id"
  functions:
    - name: has_intersection
      doc: "shares any element"
      overloads:
        - id: lists_have_intersection
          member: true
          args:
            - "list<string>"
            - "list<string>"
          result: bool
          examples:
            - "xs.has_intersection(ys)"
  macros:
    - name: matches_any
      receiver: false
      vararg: true
      results:
        - bool
      doc: "true when any candidate matches"
`

func TestParseEnv(t *testing.T) {
	e, err := ParseEnv([]byte(sampleEnv))
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	if e.Container != "example.com" {
		t.Errorf("container = %q", e.Container)
	}
	if len(e.Extensions) != 4 {
		t.Errorf("extensions = %d", len(e.Extensions))
	}
	if len(e.Variables) != 1 || e.Variables[0].Name != "account" {
		t.Errorf("variables = %+v", e.Variables)
	}
	if len(e.Functions) != 1 || e.Functions[0].Name != "has_intersection" {
		t.Errorf("functions = %+v", e.Functions)
	}
	if len(e.Macros) != 1 || e.Macros[0].Name != "matches_any" || !e.Macros[0].VarArg {
		t.Errorf("macros = %+v", e.Macros)
	}
}

func TestEnvReflectsInEnv(t *testing.T) {
	e, err := ParseEnv([]byte(sampleEnv))
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

	varNames := map[string]bool{}
	for _, v := range env.Variables() {
		varNames[v.Name()] = true
	}
	if !varNames["account"] {
		t.Errorf("env.Variables() missing account: %v", varNames)
	}

	foundFn := false
	for name := range env.Functions() {
		if name == "has_intersection" {
			foundFn = true
		}
	}
	if !foundFn {
		t.Error("env.Functions() missing has_intersection")
	}

	foundMacro := false
	for _, m := range env.Macros() {
		if m.Function() == "matches_any" {
			foundMacro = true
			if m.IsReceiverStyle() {
				t.Error("matches_any should be global (not receiver)")
			}
		}
	}
	if !foundMacro {
		t.Error("env.Macros() missing matches_any")
	}

	if !strings.Contains(env.Container.Name(), "example.com") {
		t.Errorf("env.Container = %q", env.Container.Name())
	}
}
