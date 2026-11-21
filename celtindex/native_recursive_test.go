package celtindex_test

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"runtime/debug"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"

	"github.com/udaltsovkdotru/celt/celtindex"
)

func TestNativeIndexHandlesRecursivePointer(t *testing.T) {
	const runProbe = "CELT_TEST_RECURSIVE_NATIVE"
	if os.Getenv(runProbe) != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeIndexHandlesRecursivePointer$") // #nosec G204,G702 -- fixed test flag, not tainted input
		cmd.Env = append(os.Environ(), runProbe+"=1", "GOTRACEBACK=none")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("indexing a recursive native type failed: %v\n%s", err, out[:min(len(out), 800)])
		}
		return
	}

	type Node struct {
		Next *Node
		Name string
	}
	const typeName = "native.Node"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[Node](), celtypes.NativeTypeAlias(typeName))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("root", cel.ObjectType(typeName)))
	if err != nil {
		t.Fatal(err)
	}
	// A fatal stack overflow must fail the subprocess, not the whole package.
	debug.SetMaxStack(1 << 20)
	ix := celtindex.Build(env, nil)
	if ix.FieldPresence(typeName, "Next")&celtindex.PresenceGoNilable == 0 {
		t.Error("recursive pointer should be marked nilable")
	}
}
