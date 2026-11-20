package config

import (
	"testing"

	"cel.dev/cel-go/cel"
)

const nativeTypesEnv = `
env:
  variables:
    - name: account
      type_name: native.Account
  types:
    messages:
      - name: native.Account
        fields:
          - name: id
            type: int
          - name: name
            type: string
`

// TestNativeTypesOnEnv verifies that a YAML-declared native message is defined
// on the env's type provider (no descriptor), and that member access resolves.
func TestNativeTypesOnEnv(t *testing.T) {
	e, err := ParseEnv([]byte(nativeTypesEnv))
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
	// The variable must be present and typed as the native message.
	found := false
	for _, v := range env.Variables() {
		if v.Name() == "account" {
			found = true
		}
	}
	if !found {
		t.Fatal("account variable not declared")
	}
	// Member access on the native type must type-check.
	ast, iss := env.Compile(`account.id`)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("member access on native type failed: %v", iss.Err())
	}
	if ast == nil {
		t.Fatal("member access on native type compiled to nil AST")
	}
}

// TestBuildStructRejectsInvalidFieldNames verifies a message field whose name
// can't be exported (leading underscore, or a case-colliding duplicate)
// produces an error instead of panicking in reflect.StructOf.
func TestBuildStructRejectsInvalidFieldNames(t *testing.T) {
	const fieldTypeInt = "int"
	bad := []Message{
		{Name: "M", Fields: []Field{{Name: "_private", Type: fieldTypeInt}}},
		{Name: "M", Fields: []Field{{Name: "foo", Type: fieldTypeInt}, {Name: "Foo", Type: "string"}}},
	}
	for _, m := range bad {
		if _, err := buildStruct(m); err == nil {
			t.Errorf("buildStruct(%+v) = nil error, want error", m)
		}
	}
	// A normal message still builds.
	if _, err := buildStruct(Message{Name: "M", Fields: []Field{{Name: "id", Type: fieldTypeInt}}}); err != nil {
		t.Errorf("buildStruct(valid) error: %v", err)
	}
}
