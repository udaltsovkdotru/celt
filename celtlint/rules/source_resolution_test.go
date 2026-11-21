package rules

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtlint"
)

func TestMisspellDoesNotFixLiteralTokens(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable("e11", cel.DoubleType),
		cel.Variable("rx", cel.StringType),
		cel.Variable("bx", cel.BytesType),
	)
	if err != nil {
		t.Fatal(err)
	}
	a := msNewAnalyzer(t, env)
	for _, content := range []string{
		`1e10 == 10000000000.0`,
		`r"userNme" == "userNme"`,
		`b"userNme" == b"userNme"`,
	} {
		t.Run(content, func(t *testing.T) {
			if _, issues := env.Compile(content); issues != nil && issues.Err() != nil {
				t.Fatalf("invalid test expression: %v", issues.Err())
			}
			findings := a.Analyze("literal.cel", content)
			fixed, _, changed := celtlint.ApplyFixes(content, findings)
			if len(findings) != 0 || changed || fixed != content {
				t.Errorf("valid literal produced findings %v and fix %q", findings, fixed)
			}
		})
	}
}

func TestMisspellDoesNotRewriteEscapedFieldNames(t *testing.T) {
	env, err := cel.NewEnv(cel.EnableIdentifierEscapeSyntax(), cel.Variable("userName", cel.StringType))
	if err != nil {
		t.Fatal(err)
	}
	const src = "{\"userNme-other\": true}.`userNme-other`"
	if _, issues := env.Compile(src); issues.Err() != nil {
		t.Fatalf("invalid fixture: %v", issues.Err())
	}
	findings := msNewAnalyzer(t, env).Analyze("field.cel", src)
	if fixed, _, changed := celtlint.ApplyFixes(src, findings); changed || len(findings) != 0 {
		t.Errorf("escaped field produced findings %+v and fix %q", findings, fixed)
	}
}

func TestMisspellDoesNotSuppressOccurrencesOutsideComprehension(t *testing.T) {
	fdset := enFDSet()
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("p", cel.ObjectType(enPersonType)), cel.Variable("userName", cel.StringType))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithDescriptor(fdset), celtlint.WithRules(Misspell{RuleMeta: celtlint.RuleMeta{
		Name: rlRuleMisspell, Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ src, want string }{
		{src: `userNme == "" && [1].all(userNme, userNme > 0)`, want: `userName == "" && [1].all(userNme, userNme > 0)`},
		{src: `[1].all(aeg, aeg > 0) && p.aeg > 0`, want: `[1].all(aeg, aeg > 0) && p.age > 0`},
	} {
		findings := a.Analyze("scope.cel", tc.src)
		fixed, _, changed := celtlint.ApplyFixes(tc.src, findings)
		if !changed || fixed != tc.want {
			t.Errorf("fix for %q = %q, want %q; findings=%+v", tc.src, fixed, tc.want, findings)
		}
	}
}

func TestMisspellFindsTernaryOperandInsideMap(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("userName", cel.StringType))
	if err != nil {
		t.Fatal(err)
	}
	a := msNewAnalyzer(t, env)
	for _, tc := range []struct{ src, want string }{
		{src: `true ? userNme : userName`, want: `true ? userName : userName`},
		{src: `{"key": true ? userNme : userName}`, want: `{"key": true ? userName : userName}`},
	} {
		findings := a.Analyze("ternary.cel", tc.src)
		fixed, _, changed := celtlint.ApplyFixes(tc.src, findings)
		if !changed || fixed != tc.want {
			t.Errorf("fix for %q = %q, want %q; findings=%+v", tc.src, fixed, tc.want, findings)
		}
	}
}

func TestMisspellPreservesMessageFieldLabelsAcrossQuestionTokens(t *testing.T) {
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct{ Age int64 }](), celtypes.NativeTypeAlias("Person"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.OptionalTypes(), cel.Types(typ), cel.Variable("p", cel.ObjectType("Person")), cel.Variable("Ages", cel.IntType))
	if err != nil {
		t.Fatal(err)
	}
	a := msNewAnalyzer(t, env)
	for _, src := range []string{`true ? Person{Age: 1} : p`, `p.?Age == optional.none() && Person{Age: 1} == p`} {
		t.Run(src, func(t *testing.T) {
			if _, issues := env.Compile(src); issues.Err() != nil {
				t.Fatalf("invalid message fixture: %v", issues.Err())
			}
			findings := a.Analyze("constructor.cel", src)
			if fixed, _, changed := celtlint.ApplyFixes(src, findings); changed || len(findings) != 0 {
				t.Errorf("valid constructor produced findings %+v and fix %q", findings, fixed)
			}
		})
	}
}

func TestMisspellDoesNotRewriteDeclaredObjectMemberCalls(t *testing.T) {
	fdset := enFDSet()
	person := fdset.File[0].MessageType[0]
	person.Field = append(person.Field, &descriptorpb.FieldDescriptorProto{
		Name: proto.String("name"), Number: proto.Int32(16),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
	})
	typ := cel.ObjectType(enPersonType)
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("w", typ),
		cel.Function("rename", cel.MemberOverload("person_rename", []*cel.Type{typ}, cel.BoolType)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithDescriptor(fdset), celtlint.WithRules(Misspell{RuleMeta: celtlint.RuleMeta{
		Name: rlRuleMisspell, Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"w.rename()", "w.rename ()", "w.rename // call\n ()"} {
		t.Run(src, func(t *testing.T) {
			if _, issues := env.Compile(src); issues.Err() != nil {
				t.Fatalf("invalid member call fixture: %v", issues.Err())
			}
			findings := a.Analyze("method.cel", src)
			if fixed, _, changed := celtlint.ApplyFixes(src, findings); changed || len(findings) != 0 {
				t.Errorf("declared method produced findings %+v and fix %q", findings, fixed)
			}
		})
	}
}

func TestMisspellNestedChildFieldsAreScoped(t *testing.T) {
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	messageField := func(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(number), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(typeName)}
	}
	stringField := func(name string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(1), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}
	}
	fdset := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: proto.String("collision.proto"), Package: proto.String("collision"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("First"), Field: []*descriptorpb.FieldDescriptorProto{messageField("child", 1, ".collision.FirstChild")}},
			{Name: proto.String("FirstChild"), Field: []*descriptorpb.FieldDescriptorProto{stringField("first_name")}},
			{Name: proto.String("Second"), Field: []*descriptorpb.FieldDescriptorProto{messageField("child", 1, ".collision.SecondChild")}},
			{Name: proto.String("SecondChild"), Field: []*descriptorpb.FieldDescriptorProto{stringField("second_name")}},
		},
	}}}
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("a", cel.ObjectType("collision.First")), cel.Variable("b", cel.ObjectType("collision.Second")))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithDescriptor(fdset), celtlint.WithRules(Misspell{RuleMeta: celtlint.RuleMeta{Name: rlRuleMisspell, Defaults: celtlint.RuleParams{Level: celtlint.LevelError}}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ receiver, field, otherField string }{
		{receiver: "a.child", field: "first_name", otherField: "second_name"},
		{receiver: "b.child", field: "second_name", otherField: "first_name"},
	} {
		t.Run(tc.receiver, func(t *testing.T) {
			prefix := tc.receiver + "."
			content := prefix + tc.field + ` == "x"`
			if _, issues := env.Compile(content); issues != nil && issues.Err() != nil {
				t.Fatalf("invalid test expression: %v", issues.Err())
			}
			if findings := a.Analyze("nested.cel", content); len(findings) != 0 {
				t.Errorf("valid expression produced findings: %+v", findings)
			}
			items := a.Completion(prefix, 0, len(prefix))
			contains := func(label string) bool {
				return slices.ContainsFunc(items, func(item celtlint.CompletionItem) bool { return item.Label == label })
			}
			if !contains(tc.field) || contains(tc.otherField) {
				t.Errorf("completion = %+v, want %q but not %q", items, tc.field, tc.otherField)
			}
		})
	}
}

func TestCompileUndeclaredNameRangeMatchesSourceCoordinates(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want celtlint.Position
	}{
		{src: `"é" == "é" && missing`, want: celtlint.Position{Char: len(`"é" == "é" && `)}},
		{src: `"😀" == "😀" && missing`, want: celtlint.Position{Char: len(`"😀" == "😀" && `)}},
		{src: "true\r && missing", want: celtlint.Position{Line: 1, Char: 4}},
	} {
		findings := coCheck(t, coEnv(t), tc.src)
		f, ok := enFindByCode(findings, celtlint.CodeUndeclaredReference)
		if !ok {
			t.Fatalf("expected undeclared-name finding for %q: %+v", tc.src, findings)
		}
		end := tc.want
		end.Char += len("missing")
		if f.Start != tc.want || f.End != end {
			t.Errorf("range for %q = %+v to %+v, want %+v to %+v", tc.src, f.Start, f.End, tc.want, end)
		}
	}
}

func TestCompileTypeMismatchRangeUsesFailingOperand(t *testing.T) {
	const src = `("a" == "b") || ("c" && true)`
	const bad = `"c"`
	findings := coCheck(t, coEnv(t), src)
	f, ok := enFindByCode(findings, celtlint.CodeTypeMismatch)
	if !ok {
		t.Fatalf("expected a type mismatch: %+v", findings)
	}
	want := strings.Index(src, bad)
	if f.Start.Char != want || f.End.Char != want+len(bad) {
		t.Errorf("range [%d,%d) does not cover the failing operand %s at [%d,%d)", f.Start.Char, f.End.Char, bad, want, want+len(bad))
	}
}

func TestCompileRepeatedUndeclaredReferenceSpans(t *testing.T) {
	content := "missing + missing"
	findings := coCheck(t, coEnv(t), content)
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want two undeclared references", findings)
	}
	for i, start := range []int{0, 10} {
		f := findings[i]
		if f.Code != celtlint.CodeUndeclaredReference || f.Start.Line != 0 || f.End.Line != 0 || f.Start.Char != start || f.End.Char != start+len("missing") {
			t.Errorf("finding %d = %+v, want undeclared reference at [0:%d, 0:%d)", i, f, start, start+len("missing"))
		}
	}
}
