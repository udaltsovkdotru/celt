package rules

import (
	"testing"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtlint"
)

const rlPersonTypeName = "test.Person"
const rlNickname = "nickname"

// Rule names exercised by the tests, shared between the All() list check and
// the per-rule constructors to avoid divergent literals.
const (
	rlRuleOptional         = "optional-fields"
	rlRuleMisspell         = "misspell"
	rlRuleCompile          = "compile"
	rlRuleRawEnumValue     = "raw-enum-value"
	rlRuleEnumComparison   = "enum-comparison"
	rlRuleEnumTypeMismatch = "enum-type-mismatch"
)

// rlDescriptorSet builds a minimal proto3 FileDescriptorSet used to exercise
// the descriptor-backed enum and optional-field rules. test.Person carries two
// enum fields (status, role), a proto3 `optional` string field (nickname), and
// a plain singular int32 field (age).
func rlDescriptorSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("test.proto"),
				Package: proto.String("test"),
				Syntax:  proto.String("proto3"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String("Status"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String("UNKNOWN"), Number: proto.Int32(0)},
							{Name: proto.String("ACTIVE"), Number: proto.Int32(1)},
							{Name: proto.String("INACTIVE"), Number: proto.Int32(2)},
						},
					},
					{
						Name: proto.String("Role"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String("ROLE_UNSPECIFIED"), Number: proto.Int32(0)},
							{Name: proto.String("ADMIN"), Number: proto.Int32(1)},
						},
					},
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Person"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:     proto.String("status"),
								Number:   proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
								TypeName: proto.String(".test.Status"),
							},
							{
								Name:     proto.String("role"),
								Number:   proto.Int32(2),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
								TypeName: proto.String(".test.Role"),
							},
							{
								Name:           proto.String(rlNickname),
								Number:         proto.Int32(3),
								Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								Proto3Optional: proto.Bool(true),
							},
							{
								Name:   proto.String("age"),
								Number: proto.Int32(4),
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

// rlEnv builds a CEL environment whose descriptors register test.Person and
// its enums, and which declares two Person-typed variables (p, q) and one
// string variable (nickname).
func rlEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.TypeDescs(rlDescriptorSet()),
		cel.Variable("p", cel.ObjectType(rlPersonTypeName)),
		cel.Variable("q", cel.ObjectType(rlPersonTypeName)),
		cel.Variable(rlNickname, cel.StringType),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// rlAnalyze drives a single rule through the framework and returns the
// findings it emits for content.
func rlAnalyze(t *testing.T, rule celtlint.Rule, content string) []celtlint.Finding {
	t.Helper()
	an, err := celtlint.New(
		rlEnv(t),
		celtlint.WithDescriptor(rlDescriptorSet()),
		celtlint.WithRules(rule),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return an.Analyze("test.cel", content)
}

// rlFindByCode returns the first finding with the given code, if any.
func rlFindByCode(fs []celtlint.Finding, code string) (celtlint.Finding, bool) {
	for i := range fs {
		if fs[i].Code == code {
			return fs[i], true
		}
	}
	return celtlint.Finding{}, false
}

// rlOptionalRule returns an Optional rule configured to require has() guards
// on proto3 `optional` fields, matching the framework default.
func rlOptionalRule() *Optional {
	return &Optional{RuleMeta: celtlint.RuleMeta{
		Name: rlRuleOptional,
		Defaults: celtlint.RuleParams{
			Level:  celtlint.LevelWarn,
			Config: []byte("proto3-optional: true\n"),
		},
	}}
}

func TestAllRulesListed(t *testing.T) {
	rules := All()
	want := map[string]bool{
		rlRuleOptional:         false,
		rlRuleMisspell:         false,
		rlRuleCompile:          false,
		rlRuleRawEnumValue:     false,
		rlRuleEnumComparison:   false,
		rlRuleEnumTypeMismatch: false,
	}
	if len(rules) != len(want) {
		t.Fatalf("All() = %d rules, want %d", len(rules), len(want))
	}
	for _, r := range rules {
		if _, ok := want[r.Meta().Name]; !ok {
			t.Errorf("All() contains unexpected rule %q", r.Meta().Name)
		}
		want[r.Meta().Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("All() missing rule %q", name)
		}
	}
}

func TestCompileRuleReportsUndeclaredReference(t *testing.T) {
	fs := rlAnalyze(t, Compile{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleCompile,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `missing_var == 1`)

	f, ok := rlFindByCode(fs, celtlint.CodeUndeclaredReference)
	if !ok {
		t.Fatalf("no undeclared-reference finding in %+v", fs)
	}
	if f.Level != celtlint.LevelError {
		t.Errorf("Level = %v, want error", f.Level)
	}
	if f.Message != sgUndeclaredRef {
		t.Errorf("Message = %q, want %q", f.Message, sgUndeclaredRef)
	}
}

func TestCompileRuleNoFindingOnValidInput(t *testing.T) {
	fs := rlAnalyze(t, Compile{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleCompile,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `p.age == 1`)
	if len(fs) != 0 {
		t.Fatalf("expected no compile findings, got %+v", fs)
	}
}

func TestMisspellRuleSuggestsCorrection(t *testing.T) {
	fs := rlAnalyze(t, Misspell{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleMisspell,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `nicknam == "bob"`)

	f, ok := rlFindByCode(fs, celtlint.CodeUnknownIdentifier)
	if !ok {
		t.Fatalf("no unknown-identifier finding in %+v", fs)
	}
	if f.Message != sgUnknownIdent {
		t.Errorf("Message = %q, want %q", f.Message, sgUnknownIdent)
	}
	if f.Fix != rlNickname {
		t.Errorf("Fix = %q, want %q", f.Fix, rlNickname)
	}
	if f.Level != celtlint.LevelError {
		t.Errorf("Level = %v, want error", f.Level)
	}
}

func TestMisspellRuleNoFindingOnValidIdent(t *testing.T) {
	fs := rlAnalyze(t, Misspell{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleMisspell,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `nickname == "bob"`)
	if len(fs) != 0 {
		t.Fatalf("expected no misspell findings, got %+v", fs)
	}
}

func TestRawEnumValueRuleFlagsNumericLiteral(t *testing.T) {
	fs := rlAnalyze(t, RawEnumValue{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleRawEnumValue,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == 1`)

	f, ok := rlFindByCode(fs, celtlint.CodeRawEnumValue)
	if !ok {
		t.Fatalf("no raw-enum-value finding in %+v", fs)
	}
	if f.Message != "use of a raw enum value" {
		t.Errorf("Message = %q, want %q", f.Message, "use of a raw enum value")
	}
	if f.Fix != "test.Status.ACTIVE" {
		t.Errorf("Fix = %q, want %q", f.Fix, "test.Status.ACTIVE")
	}
	if f.Level != celtlint.LevelWarn {
		t.Errorf("Level = %v, want warn", f.Level)
	}
}

func TestRawEnumValueRuleNoFindingOnNamedValue(t *testing.T) {
	fs := rlAnalyze(t, RawEnumValue{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleRawEnumValue,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.age == 1`)
	if len(fs) != 0 {
		t.Fatalf("expected no raw-enum-value findings, got %+v", fs)
	}
}

func TestEnumComparisonRuleFlagsVariableComparison(t *testing.T) {
	fs := rlAnalyze(t, EnumComparison{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleEnumComparison,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == q.status`)

	f, ok := rlFindByCode(fs, celtlint.CodeOddComparison)
	if !ok {
		t.Fatalf("no odd-comparison finding in %+v", fs)
	}
	if f.Message != "odd comparison" {
		t.Errorf("Message = %q, want %q", f.Message, "odd comparison")
	}
	if f.Hint != sgCompareVarsDirect {
		t.Errorf("Hint = %q, want %q", f.Hint, sgCompareVarsDirect)
	}
	if f.Level != celtlint.LevelWarn {
		t.Errorf("Level = %v, want warn", f.Level)
	}
}

func TestEnumTypeMismatchRuleFlagsDifferentEnums(t *testing.T) {
	fs := rlAnalyze(t, EnumTypeMismatch{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleEnumTypeMismatch,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `p.status == p.role`)

	f, ok := rlFindByCode(fs, celtlint.CodeOddComparison)
	if !ok {
		t.Fatalf("no odd-comparison finding in %+v", fs)
	}
	if f.Hint != sgCompareVarsDiffType {
		t.Errorf("Hint = %q, want %q", f.Hint, sgCompareVarsDiffType)
	}
	if f.Level != celtlint.LevelError {
		t.Errorf("Level = %v, want error", f.Level)
	}
}

func TestEnumTypeMismatchRuleNoFindingOnSameEnum(t *testing.T) {
	fs := rlAnalyze(t, EnumTypeMismatch{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleEnumTypeMismatch,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `p.status == q.status`)
	if len(fs) != 0 {
		t.Fatalf("expected no enum-type-mismatch findings, got %+v", fs)
	}
}

func TestOptionalRuleFlagsUnguardedAccess(t *testing.T) {
	fs := rlAnalyze(t, rlOptionalRule(), `p.nickname == "bob"`)

	f, ok := rlFindByCode(fs, celtlint.CodeMissingHasCheck)
	if !ok {
		t.Fatalf("no missing-has-check finding in %+v", fs)
	}
	if f.Message != "accessing an optional field without a has() check" {
		t.Errorf("Message = %q, want %q", f.Message, "accessing an optional field without a has() check")
	}
	if f.Level != celtlint.LevelWarn {
		t.Errorf("Level = %v, want warn", f.Level)
	}
}

func TestOptionalRuleNoFindingWhenGuarded(t *testing.T) {
	fs := rlAnalyze(t, rlOptionalRule(), `has(p.nickname) && p.nickname == "bob"`)
	if len(fs) != 0 {
		t.Fatalf("expected no optional-fields findings, got %+v", fs)
	}
}

func TestOptionalRuleNoFindingOnNonOptionalField(t *testing.T) {
	fs := rlAnalyze(t, rlOptionalRule(), `p.age == 1`)
	if len(fs) != 0 {
		t.Fatalf("expected no optional-fields findings, got %+v", fs)
	}
}

func TestOptionalRuleDisabledWithoutPresenceConfig(t *testing.T) {
	rule := &Optional{RuleMeta: celtlint.RuleMeta{
		Name:     rlRuleOptional,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}
	fs := rlAnalyze(t, rule, `p.nickname == "bob"`)
	if len(fs) != 0 {
		t.Fatalf("expected no optional-fields findings with empty config, got %+v", fs)
	}
}

func TestAllRulesReportNothingOnCorrectInput(t *testing.T) {
	an, err := celtlint.New(
		rlEnv(t),
		celtlint.WithDescriptor(rlDescriptorSet()),
		celtlint.WithRules(All()...),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fs := an.Analyze("test.cel", `p.age == 1`)
	if len(fs) != 0 {
		t.Fatalf("expected no findings from All() on correct input, got %+v", fs)
	}
}
