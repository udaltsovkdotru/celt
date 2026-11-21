package rules

import (
	"reflect"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtlint"
)

const (
	opRuleName    = "optional-fields"
	opPath        = "test.cel"
	opCmpNickname = `p.nickname == "bob"`
	opCmpAge      = `p.age == 1`
)

// opDescriptorSet builds a descriptor set with a proto3 test.Person (a proto3
// `optional` nickname string, a plain singular int32 age, and an address of
// message type test.Address), a proto3 test.Address (a proto3 `optional` city
// string and a plain singular int32 zip), and a proto2 legacy.Legacy carrying
// an explicit-presence (optional) string note. This exercises every presence
// kind the optional-fields rule understands.
func opDescriptorSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("test.proto"),
				Package: proto.String("test"),
				Syntax:  proto.String("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Person"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:           proto.String("nickname"),
								Number:         proto.Int32(1),
								Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								Proto3Optional: proto.Bool(true),
							},
							{
								Name:   proto.String("age"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
							{
								Name:     proto.String("address"),
								Number:   proto.Int32(3),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
								TypeName: proto.String(".test.Address"),
							},
						},
					},
					{
						Name: proto.String("Address"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:           proto.String("city"),
								Number:         proto.Int32(1),
								Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								Proto3Optional: proto.Bool(true),
							},
							{
								Name:   proto.String("zip"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
						},
					},
				},
			},
			{
				Name:    proto.String("legacy.proto"),
				Package: proto.String("legacy"),
				Syntax:  proto.String("proto2"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Legacy"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:   proto.String("note"),
								Number: proto.Int32(1),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
							{
								Name:   proto.String("count"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
						},
					},
				},
			},
		},
	}
}

// opEnv builds a CEL environment registering the descriptor set and declaring
// Person-, Person-, and Legacy-typed variables.
func opEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.TypeDescs(opDescriptorSet()),
		cel.Variable("p", cel.ObjectType("test.Person")),
		cel.Variable("q", cel.ObjectType("test.Person")),
		cel.Variable("l", cel.ObjectType("legacy.Legacy")),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// opOptionalRule drives optionalFindings through the Rule framework with a
// caller-supplied presence config.
type opOptionalRule struct {
	celtlint.RuleMeta
	cfg OptionalConfig
}

func (r *opOptionalRule) Check(ctx *celtlint.Context) {
	ctx.ReportAll(optionalFindings(ctx, r.cfg))
}

// opAnalyze runs the optional-fields rule over content and returns the
// findings it emits.
func opAnalyze(t *testing.T, cfg OptionalConfig, content string) []celtlint.Finding {
	t.Helper()
	an, err := celtlint.New(
		opEnv(t),
		celtlint.WithDescriptor(opDescriptorSet()),
		celtlint.WithRules(&opOptionalRule{
			RuleMeta: celtlint.RuleMeta{Name: opRuleName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn}},
			cfg:      cfg,
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return an.Analyze(opPath, content)
}

// opCountByCode returns the number of missing-has-check findings.
func opCountByCode(fs []celtlint.Finding) int {
	n := 0
	for i := range fs {
		if fs[i].Code == celtlint.CodeMissingHasCheck {
			n++
		}
	}
	return n
}

// opFindByCode returns the first missing-has-check finding.
func opFindByCode(fs []celtlint.Finding) (celtlint.Finding, bool) {
	for i := range fs {
		if fs[i].Code == celtlint.CodeMissingHasCheck {
			return fs[i], true
		}
	}
	return celtlint.Finding{}, false
}

// TestOptionalMissingHasCheck drives the rule through New+Analyze and asserts
// each enabled presence kind flags an unguarded access with the correct hint.
func TestOptionalMissingHasCheck(t *testing.T) {
	cases := []struct {
		name    string
		cfg     OptionalConfig
		content string
		hint    string
	}{
		{
			name:    "proto3 optional unguarded",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: opCmpNickname,
			hint:    "guard access with has(p.nickname)",
		},
		{
			name:    "proto3 value singular",
			cfg:     OptionalConfig{Proto3Value: true},
			content: opCmpAge,
			hint:    "guard access with has(p.age)",
		},
		{
			name:    "proto2 explicit presence",
			cfg:     OptionalConfig{Proto2Existence: true},
			content: `l.note == "x"`,
			hint:    "guard access with has(l.note)",
		},
		{
			name:    "chained optional receiver",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.address.city == "nyc"`,
			hint:    "guard access with has(p.address.city)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := opAnalyze(t, tc.cfg, tc.content)
			f, ok := opFindByCode(fs)
			if !ok {
				t.Fatalf("no missing-has-check finding in %+v", fs)
			}
			if f.Level != celtlint.LevelWarn {
				t.Errorf("Level = %v, want warn", f.Level)
			}
			if f.Message != "accessing an optional field without a has() check" {
				t.Errorf("Message = %q, want %q", f.Message, "accessing an optional field without a has() check")
			}
			if f.Hint != tc.hint {
				t.Errorf("Hint = %q, want %q", f.Hint, tc.hint)
			}
		})
	}
}

// TestOptionalMissingHasCheckFix asserts each missing-has-check finding carries
// a fix that wraps the offending field access in has(), and that applying it
// yields a source with no remaining findings.
func TestOptionalMissingHasCheckFix(t *testing.T) {
	cases := []struct {
		name    string
		cfg     OptionalConfig
		content string
		fix     string // the wrapped select expression
		want    string // the full source after ApplyFixes
	}{
		{
			name:    "proto3 optional",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: opCmpNickname,
			fix:     `has(p.nickname)`,
			want:    `has(p.nickname) == "bob"`,
		},
		{
			name:    "proto3 value singular",
			cfg:     OptionalConfig{Proto3Value: true},
			content: opCmpAge,
			fix:     `has(p.age)`,
			want:    `has(p.age) == 1`,
		},
		{
			name:    "proto2 explicit presence",
			cfg:     OptionalConfig{Proto2Existence: true},
			content: `l.note == "x"`,
			fix:     `has(l.note)`,
			want:    `has(l.note) == "x"`,
		},
		{
			name:    "chained optional receiver",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.address.city == "nyc"`,
			fix:     `has(p.address.city)`,
			want:    `has(p.address.city) == "nyc"`,
		},
		{
			name:    "whitespace around dot",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.  nickname == "bob"`,
			fix:     `has(p.  nickname)`,
			want:    `has(p.  nickname) == "bob"`,
		},
		{
			name:    "single finding among many accesses",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.nickname == "bob" && p.age == 1`,
			fix:     `has(p.nickname)`,
			want:    `has(p.nickname) == "bob" && p.age == 1`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := opAnalyze(t, tc.cfg, tc.content)
			f, ok := opFindByCode(fs)
			if !ok {
				t.Fatalf("no missing-has-check finding in %+v", fs)
			}
			if !f.IsFix() {
				t.Fatalf("expected the finding to carry a fix: %+v", f)
			}
			if f.Fix != tc.fix {
				t.Errorf("Fix = %q, want %q", f.Fix, tc.fix)
			}
			fixed, kept, changed := celtlint.ApplyFixes(tc.content, fs)
			if !changed {
				t.Fatalf("ApplyFixes = changed=false, want a fix applied")
			}
			if fixed != tc.want {
				t.Errorf("fixed source = %q, want %q", fixed, tc.want)
			}
			for i := range kept {
				if kept[i].IsFix() {
					t.Errorf("kept findings still contain fixable finding %+v", kept[i])
				}
			}
			if after := opAnalyze(t, tc.cfg, fixed); opCountByCode(after) != 0 {
				t.Errorf("re-Analyze(fixed) still reports missing-has-check: %+v", after)
			}
		})
	}
}

// TestOptionalEscapedFieldFixWrapsWholeSelect asserts the fix for an escaped
// field name (e.g. `nick-name`) wraps the full `receiver.field` select, not
// just the field token, so the replacement stays valid CEL.
func TestOptionalEscapedFieldFixWrapsWholeSelect(t *testing.T) {
	const name = "native.Person"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		Nickname *string `cel:"nick-name"`
	}](), celtypes.NativeTypeAlias(name), celtypes.ParseStructTag("cel"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.EnableIdentifierEscapeSyntax(), cel.Types(typ), cel.Variable("a", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithRules(&opOptionalRule{
		RuleMeta: celtlint.RuleMeta{Name: opRuleName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn}},
		cfg:      OptionalConfig{GoNilable: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	const src = "a.`nick-name` == 'x'"
	if _, issues := env.Compile(src); issues.Err() != nil {
		t.Fatalf("invalid fixture: %v", issues.Err())
	}
	fs := a.Analyze(opPath, src)
	f, ok := opFindByCode(fs)
	if !ok {
		t.Fatalf("expected an optional field finding: %+v", fs)
	}
	const want = "has(a.`nick-name`) == 'x'"
	const fix = "has(a.`nick-name`)"
	if f.Fix != fix {
		t.Errorf("Fix = %q, want %q", f.Fix, fix)
	}
	// The fix range must cover the whole select (receiver through field token),
	// even though the diagnostic range only highlights the field name.
	if f.FixStart.Line != 0 || f.FixStart.Char != 0 {
		t.Errorf("FixStart = %+v, want the start of the receiver", f.FixStart)
	}
	if fixed, _, changed := celtlint.ApplyFixes(src, fs); !changed || fixed != want {
		t.Errorf("fixed source = %q (changed=%v), want %q", fixed, changed, want)
	}
}

// TestOptionalNoFalsePositive asserts guarded accesses, non-optional fields,
// and a fully-disabled config produce no findings.
func TestOptionalNoFalsePositive(t *testing.T) {
	cases := []struct {
		name    string
		cfg     OptionalConfig
		content string
	}{
		{
			name:    "has guard on same field",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `has(p.nickname) && p.nickname == "bob"`,
		},
		{
			name:    "non optional field under proto3-optional config",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: opCmpAge,
		},
		{
			name:    "has guard on chained optional field",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `has(p.address.city) && p.address.city == "nyc"`,
		},
		{
			name:    "empty config disables rule",
			cfg:     OptionalConfig{},
			content: opCmpNickname,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := opAnalyze(t, tc.cfg, tc.content)
			if n := opCountByCode(fs); n != 0 {
				t.Errorf("got %d missing-has-check findings, want 0: %+v", n, fs)
			}
		})
	}
}

// TestOptionalBranchSensitive asserts a has() guard only protects accesses on
// the control-flow paths where it holds, not every access in the expression.
func TestOptionalBranchSensitive(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{name: "guarded and", content: `has(p.nickname) && p.nickname == "bob"`, want: 0},
		{name: "explicit true guard", content: `(has(p.nickname) == true) && p.nickname == "bob"`, want: 0},
		{name: "explicit false does not guard", content: `(has(p.nickname) == false) && p.nickname == "bob"`, want: 1},
		{name: "guarded ternary true", content: `has(p.nickname) ? p.nickname == "bob" : true`, want: 0},
		{name: "unguarded ternary false", content: `has(p.nickname) ? true : p.nickname == "bob"`, want: 1},
		{name: "unguarded negated", content: `!has(p.nickname) ? p.nickname == "bob" : true`, want: 1},
		{name: "unguarded or", content: `has(p.nickname) || p.nickname == "bob"`, want: 1},
		{name: "shadowed macro binding", content: `has(p.nickname) && [q].exists(p, p.nickname == "bob")`, want: 1},
		{name: "guarded outer binding in comprehension range", content: `has(p.nickname) && [p.nickname].exists(p, p == "bob")`, want: 0},
		{name: "unguarded outer binding in comprehension range", content: `[p.nickname].exists(p, p == "bob")`, want: 1},
		{name: "different array element", content: `has([p, q][0].nickname) && [p, q][1].nickname == "bob"`, want: 1},
		{name: "same array element", content: `has([p, q][0].nickname) && [p, q][0].nickname == "bob"`, want: 0},
		{name: "different conditional receivers", content: `has((true ? p : q).nickname) && (false ? p : q).nickname == "bob"`, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := opAnalyze(t, OptionalConfig{Proto3Optional: true}, tc.content)
			if n := opCountByCode(fs); n != tc.want {
				t.Errorf("got %d missing-has-check findings, want %d: %+v", n, tc.want, fs)
			}
		})
	}
}

func TestOptionalHintUsesSourceBinding(t *testing.T) {
	const src = `[q].exists(p, p.nickname == "bob")`
	findings := opAnalyze(t, OptionalConfig{Proto3Optional: true}, src)
	finding, ok := opFindByCode(findings)
	if !ok {
		t.Fatalf("expected a missing-has-check finding; got %+v", findings)
	}
	if want := "guard access with has(p.nickname)"; finding.Hint != want {
		t.Errorf("hint = %q, want %q", finding.Hint, want)
	}
}

func TestOptionalMemberCallReceiver(t *testing.T) {
	person := cel.ObjectType("test.Person")
	env, err := opEnv(t).Extend(cel.Function("pick",
		cel.MemberOverload("pick_person_int", []*cel.Type{person, cel.IntType}, person),
		cel.MemberOverload("pick_person_map", []*cel.Type{person, cel.MapType(cel.StringType, cel.IntType)}, person),
		cel.MemberOverload("pick_person_string", []*cel.Type{person, cel.StringType}, person),
		cel.MemberOverload("pick_person_strings", []*cel.Type{person, cel.StringType, cel.StringType}, person),
	), cel.Function("choose", cel.Overload("choose_person", []*cel.Type{cel.StringType}, person)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithDescriptor(opDescriptorSet()), celtlint.WithRules(&opOptionalRule{
		RuleMeta: celtlint.RuleMeta{Name: opRuleName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn}},
		cfg:      OptionalConfig{Proto3Optional: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, guardField, accessField string }{
		{name: "integer arguments", guardField: `p.pick(0).nickname`, accessField: `p.pick(1).nickname`},
		{name: "map arguments", guardField: `p.pick({"mode":0}).nickname`, accessField: `p.pick({"mode":1}).nickname`},
		{name: "string containing key delimiter", guardField: `p.pick("a, string:b").nickname`, accessField: `p.pick("a","b").nickname`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("guard", func(t *testing.T) {
				src := `has(` + tc.guardField + `) && ` + tc.accessField + ` == "bob"`
				findings := a.Analyze(opPath, src)
				if n := opCountByCode(findings); n != 1 {
					t.Errorf("got %d missing-has-check findings, want 1: %+v", n, findings)
				}
			})
			t.Run("hint", func(t *testing.T) {
				findings := a.Analyze(opPath, tc.accessField+` == "bob"`)
				f, ok := opFindByCode(findings)
				if !ok {
					t.Fatalf("expected a missing-has-check finding; got %+v", findings)
				}
				if _, example, ok := strings.Cut(f.Hint, "has("); ok {
					got := strings.Join(strings.Fields(example), "")
					if !strings.HasPrefix(got, ")") && !strings.HasPrefix(got, tc.accessField+")") {
						t.Errorf("hint = %q, want the actual receiver or a generic hint", f.Hint)
					}
				}
			})
		})
	}
	t.Run("global call", func(t *testing.T) {
		const access = `choose("a").nickname == "bob"`
		guarded := a.Analyze(opPath, `has(choose("a").nickname) && `+access)
		if n := opCountByCode(guarded); n != 0 {
			t.Errorf("guarded global call got %d missing-has-check findings: %+v", n, guarded)
		}
		findings := a.Analyze(opPath, access)
		f, ok := opFindByCode(findings)
		if !ok {
			t.Fatalf("unguarded global call has no finding: %+v", findings)
		}
		if _, expr, ok := strings.Cut(f.Hint, "guard access with "); ok {
			if _, issues := env.Compile(expr); issues.Err() != nil {
				t.Errorf("hint is not valid CEL: %q (%v)", f.Hint, issues.Err())
			}
		}
	})
}

func TestOptionalEscapedFieldHasUsableDiagnostic(t *testing.T) {
	const name = "native.Person"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		Nickname *string `cel:"nick-name"`
	}](), celtypes.NativeTypeAlias(name), celtypes.ParseStructTag("cel"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.EnableIdentifierEscapeSyntax(), cel.Types(typ), cel.Variable("a", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithRules(&opOptionalRule{
		RuleMeta: celtlint.RuleMeta{Name: opRuleName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn}},
		cfg:      OptionalConfig{GoNilable: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	const src = "a.`nick-name` == 'x'"
	if _, issues := env.Compile(src); issues.Err() != nil {
		t.Fatalf("invalid fixture: %v", issues.Err())
	}
	findings := a.Analyze(opPath, src)
	f, ok := opFindByCode(findings)
	if !ok {
		t.Fatalf("expected an optional field finding: %+v", findings)
	}
	if _, guard, ok := strings.Cut(f.Hint, "guard access with "); ok {
		if _, issues := env.Compile(guard); issues.Err() != nil {
			t.Errorf("suggested guard %q does not compile: %v", guard, issues.Err())
		}
	}
	start, end := f.Start.Char, f.End.Char
	if f.Start.Line != 0 || f.End.Line != 0 || start < 0 || end < start || end > len(src) || !strings.Contains(src[start:end], "nick-name") {
		t.Errorf("diagnostic range should include the field name: %+v to %+v", f.Start, f.End)
	}
}

func TestOptionalDiagnosticRangeIncludesFieldName(t *testing.T) {
	for _, tc := range []struct{ src, field string }{
		{src: `p.nickname == "a"`, field: "nickname"},
		{src: `p.  nickname == "a"`, field: "nickname"},
		{src: `has(p.address) && p.address.city == "a"`, field: "city"},
	} {
		findings := opAnalyze(t, OptionalConfig{Proto3Optional: true}, tc.src)
		f, ok := opFindByCode(findings)
		if !ok {
			t.Fatalf("expected an optional field finding for %q: %+v", tc.src, findings)
		}
		start, end := f.Start.Char, f.End.Char
		if f.Start.Line != 0 || f.End.Line != 0 || start < 0 || end < start || end > len(tc.src) || !strings.Contains(tc.src[start:end], tc.field) {
			t.Errorf("range %+v to %+v should include %q in %q", f.Start, f.End, tc.field, tc.src)
		}
	}
}

// TestOptionalGuardMustBeOnField asserts that guarding a different optional
// field (or only a non-optional parent) does not suppress the finding.
func TestOptionalGuardMustBeOnField(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{
			name:    "guarding another optional field is not enough",
			content: `has(p.address.city) && p.nickname == "bob"`,
			want:    1,
		},
		{
			name:    "guarding a non-optional parent is not enough",
			content: `has(p.address) && p.address.city == "nyc"`,
			want:    1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := OptionalConfig{Proto3Optional: true}
			fs := opAnalyze(t, cfg, tc.content)
			if n := opCountByCode(fs); n != tc.want {
				t.Errorf("got %d missing-has-check findings, want %d: %+v", n, tc.want, fs)
			}
		})
	}
}

// TestOptionalMultipleFindings asserts one finding per unguarded optional
// field access in the same expression.
func TestOptionalMultipleFindings(t *testing.T) {
	fs := opAnalyze(t, OptionalConfig{Proto3Optional: true},
		`p.nickname == "bob" || p.address.city == "nyc"`)
	if n := opCountByCode(fs); n != 2 {
		t.Errorf("got %d missing-has-check findings, want 2: %+v", n, fs)
	}
}
