package celtlint

import (
	"reflect"
	"slices"
	"testing"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
)

func TestCompletionIncludesBothNativeSiblingsOfSameType(t *testing.T) {
	type child struct{ Name string }
	const rootName = "native.Root"
	rootType, err := celtypes.NewNativeType(reflect.TypeFor[struct{ One, Two *child }](), celtypes.NativeTypeAlias(rootName))
	if err != nil {
		t.Fatal(err)
	}
	childType, err := celtypes.NewNativeType(reflect.TypeFor[child]())
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(rootType, childType), cel.Variable("r", cel.ObjectType(rootName)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"r.One.", "r.Two."} {
		if _, issues := env.Compile(src + "Name"); issues.Err() != nil {
			t.Fatalf("invalid receiver fixture %q: %v", src, issues.Err())
		}
		items := a.Completion(src, 0, len(src))
		if !slices.ContainsFunc(items, func(it CompletionItem) bool { return it.Label == "Name" && it.Kind == CompletionKindField }) {
			t.Errorf("completion for %q is missing the Name field: %+v", src, items)
		}
	}
}

func TestCompletionUsesNativeReceiverType(t *testing.T) {
	const name = "native.Account"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		Name     string
		Nickname *string
	}](), celtypes.NativeTypeAlias(name))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("account", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"account.", "(account).", "[account][0]."} {
		t.Run(src, func(t *testing.T) {
			if _, issues := env.Compile(src + "Name"); issues.Err() != nil {
				t.Fatalf("invalid receiver fixture: %v", issues.Err())
			}
			items := a.Completion(src, 0, len(src))
			for _, field := range []string{"Name", "Nickname"} {
				if !slices.ContainsFunc(items, func(it CompletionItem) bool { return it.Label == field && it.Kind == CompletionKindField }) {
					t.Errorf("completion missing native field %q: %+v", field, items)
				}
			}
		})
	}
}
