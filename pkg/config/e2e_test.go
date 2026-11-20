package config_test

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtlint/rules"
	"github.com/udaltsovkdotru/celt/pkg/config"
)

// e2eEnvYAML is a `.celt.yml`-shaped declaration exercising the full declarative
// surface: extensions, a typed variable, a native-typed variable, a member and
// a global function overload, a signature-only macro, and a native message type.
const e2eEnvYAML = `
env:
  container: example.com
  extensions:
    - optional-types
    - lists
  variables:
    - name: account
      type: int
      doc: "the account id"
    - name: acct
      type_name: native.Account
      doc: "a native account message"
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
    - name: concat_all
      overloads:
        - id: concat_all_strings
          args:
            - "list<string>"
          result: string
  macros:
    - name: matches_any
      receiver: false
      vararg: true
      results:
        - bool
  types:
    messages:
      - name: native.Account
        fields:
          - name: id
            type: int
          - name: name
            type: string
`

// e2eGoodDoc references only identifiers declared in e2eEnvYAML.
const e2eGoodDoc = `account > 0 &&
  acct.id > 0 &&
  ['a', 'b'].has_intersection(['c']) &&
  concat_all(['a', 'b']) == 'ab' &&
  matches_any('foo', 'bar')`

// e2eBadDoc uses an identifier that is not declared in the env.
const e2eBadDoc = `account > 0 && mystery_thing == 'x'`

// e2eFormatDoc is a small expression whose spacing the formatter normalizes.
const e2eFormatDoc = `a+1`

// e2eBuildEnv parses the declarative config, builds the env options and
// constructs a working cel.Env.
func e2eBuildEnv(t *testing.T) *cel.Env {
	t.Helper()
	e, err := config.ParseEnv([]byte(e2eEnvYAML))
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
	return env
}

// e2eAnalyzer wires the declarative env into a celtlint analyzer with the full
// built-in rule set.
func e2eAnalyzer(t *testing.T, env *cel.Env) *celtlint.Analyzer {
	t.Helper()
	a, err := celtlint.New(env, celtlint.WithRules(rules.All()...))
	if err != nil {
		t.Fatalf("celtlint.New: %v", err)
	}
	return a
}

// e2eHasCode reports whether any finding carries the given code.
func e2eHasCode(findings []celtlint.Finding, code string) bool {
	for i := range findings {
		if findings[i].Code == code {
			return true
		}
	}
	return false
}

// TestE2EConfigToEnv proves the declarative config produces a working env whose
// declared variables, functions and macros are all present.
func TestE2EConfigToEnv(t *testing.T) {
	env := e2eBuildEnv(t)

	varNames := map[string]bool{}
	for _, v := range env.Variables() {
		varNames[v.Name()] = true
	}
	for _, name := range []string{"account", "acct"} {
		if !varNames[name] {
			t.Errorf("env missing declared variable %q", name)
		}
	}

	fnNames := map[string]bool{}
	for name := range env.Functions() {
		fnNames[name] = true
	}
	for _, name := range []string{"has_intersection", "concat_all", "matches_any"} {
		if !fnNames[name] {
			t.Errorf("env missing declared function/macro %q", name)
		}
	}
}

// TestE2ELintValidDocument proves a document that uses the declared macro,
// function, variable and native type compiles with no findings.
func TestE2ELintValidDocument(t *testing.T) {
	env := e2eBuildEnv(t)
	a := e2eAnalyzer(t, env)
	findings := a.Analyze("valid.cel", e2eGoodDoc)
	if e2eHasCode(findings, celtlint.CodeUndeclaredReference) {
		t.Errorf("unexpected undeclared-reference on valid document: %+v", findings)
	}
	if len(findings) != 0 {
		t.Errorf("valid document should lint clean, got %d findings: %+v", len(findings), findings)
	}
}

// TestE2ELintUndeclaredIdentifier proves a document referencing an identifier
// absent from the declarative env is reported by the linter.
func TestE2ELintUndeclaredIdentifier(t *testing.T) {
	env := e2eBuildEnv(t)
	a := e2eAnalyzer(t, env)
	findings := a.Analyze("invalid.cel", e2eBadDoc)
	if !e2eHasCode(findings, celtlint.CodeUndeclaredReference) {
		t.Errorf("expected an undeclared-reference finding, got %+v", findings)
	}
}

// TestE2EFormatDocument proves the declarative env's pipeline also drives the
// formatter, normalizing expression spacing.
func TestE2EFormatDocument(t *testing.T) {
	got, err := celtfmt.Format(e2eFormatDoc, nil)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if want := "a + 1"; strings.TrimSpace(got) != want {
		t.Errorf("Format output = %q, want %q", strings.TrimSpace(got), want)
	}
}
