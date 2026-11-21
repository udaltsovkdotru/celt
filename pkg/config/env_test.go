package config

import (
	"fmt"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
)

const envKeyType = "type"

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

func TestEnvRejectsMalformedTypeExpressions(t *testing.T) {
	for _, tc := range []struct {
		name, key, typ string
	}{
		{name: "unclosed list", key: envKeyType, typ: "list<int"},
		{name: "extra map argument", key: envKeyType, typ: "map<string,int, bool>"},
		{name: "empty qualified name", key: envKeyType, typ: "."},
		{name: "extra closing bracket", key: envKeyType, typ: "list<int>>"},
		{name: "empty name component", key: envKeyType, typ: "pkg..Person"},
		{name: "empty type_name reference", key: "type_name", typ: "."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Appendf(nil, "env:\n  variables:\n    - name: subject\n      %s: %q\n", tc.key, tc.typ)
			if err := envLoadError(src); err == nil {
				t.Errorf("environment loaded with malformed type %q", tc.typ)
			} else if !strings.Contains(err.Error(), "subject") {
				t.Errorf("error should identify the variable subject: %v", err)
			}
		})
	}
}

func TestEnvRejectsDuplicateNativeMessageNames(t *testing.T) {
	const src = `
env:
  types:
    messages:
      - name: native.Account
        fields:
          - name: id
            type: int
      - name: %q
        fields:
          - name: email
            type: string
`
	for _, alias := range []string{"native.Account", ".native.Account"} {
		t.Run(alias, func(t *testing.T) {
			err := envLoadError(fmt.Appendf(nil, src, alias))
			if err == nil {
				t.Fatalf("environment loaded with conflicting declarations native.Account and %s", alias)
			}
			if !strings.Contains(err.Error(), "native.Account") {
				t.Errorf("error should identify the duplicate message native.Account: %v", err)
			}
		})
	}
}

func TestEnvRejectsReceiverMacroOverloadWithoutReceiverArgument(t *testing.T) {
	const src = `env:
  macros:
    - name: test
      receiver: true
      vararg: true
      overloads:
        - args: []
          result: bool
`
	if err := envLoadError([]byte(src)); err == nil {
		t.Error("environment accepted a receiver macro overload without a receiver parameter")
	}
}

func envLoadError(src []byte) error {
	e, err := ParseEnv(src)
	if err != nil {
		return err
	}
	opts, err := e.Options()
	if err != nil {
		return err
	}
	_, err = cel.NewEnv(opts...)
	return err
}
