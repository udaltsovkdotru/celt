package rules

import (
	"testing"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
)

// Enum-related type names shared by the descriptor builder and the assertions
// that drive it, avoiding divergent magic strings.
const (
	enPersonType  = "test.Person"
	enStatusType  = "test.Status"
	enRoleType    = "test.Role"
	enStatusShort = "Status"
	enRoleShort   = "Role"
	enActiveConst = "test.Status.ACTIVE"
)

// Rule names used by the local test rules that drive the enum checks.
const (
	enRawEnumName  = "en-raw-enum"
	enEnumCmpName  = "en-enum-compare"
	enEnumMismName = "en-enum-mismatch"
)

// enFDSet builds a proto3 FileDescriptorSet with two top-level enums (Status,
// Role), a message Person carrying an enum field per type plus a scalar int32
// field, and a nested enum (Kind) inside Person. It mirrors the shape used to
// drive the descriptor-backed enum rules.
func enFDSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("test.proto"),
				Package: proto.String("test"),
				Syntax:  proto.String("proto3"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String(enStatusShort),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String("UNKNOWN"), Number: proto.Int32(0)},
							{Name: proto.String("ACTIVE"), Number: proto.Int32(1)},
							{Name: proto.String("INACTIVE"), Number: proto.Int32(2)},
						},
					},
					{
						Name: proto.String(enRoleShort),
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
								TypeName: proto.String("." + enStatusType),
							},
							{
								Name:     proto.String("role"),
								Number:   proto.Int32(2),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
								TypeName: proto.String("." + enRoleType),
							},
							{
								Name:   proto.String("age"),
								Number: proto.Int32(3),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
						},
						EnumType: []*descriptorpb.EnumDescriptorProto{
							{
								Name: proto.String("Kind"),
								Value: []*descriptorpb.EnumValueDescriptorProto{
									{Name: proto.String("KIND_A"), Number: proto.Int32(0)},
									{Name: proto.String("KIND_B"), Number: proto.Int32(1)},
								},
							},
						},
					},
				},
			},
		},
	}
}

// enEnv builds a CEL environment registering the descriptor types and two
// Person-typed variables (p, q) so both enum-field and enum-constant
// expressions compile and type-check.
func enEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.TypeDescs(enFDSet()),
		cel.Variable("p", cel.ObjectType(enPersonType)),
		cel.Variable("q", cel.ObjectType(enPersonType)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// enRawEnumRule mirrors rules.RawEnumValue, reporting raw enum literals used
// in comparisons.
type enRawEnumRule struct {
	celtlint.RuleMeta
}

func (enRawEnumRule) Check(ctx *celtlint.Context) { ctx.ReportAll(rawEnumValueFindings(ctx)) }

// enEnumCompareRule mirrors rules.EnumComparison, reporting suspicious but
// type-correct enum comparisons.
type enEnumCompareRule struct {
	celtlint.RuleMeta
}

func (enEnumCompareRule) Check(ctx *celtlint.Context) { ctx.ReportAll(enumCompareFindings(ctx)) }

// enEnumMismatchRule mirrors rules.EnumTypeMismatch, reporting enum
// comparisons of mismatched types.
type enEnumMismatchRule struct {
	celtlint.RuleMeta
}

func (enEnumMismatchRule) Check(ctx *celtlint.Context) { ctx.ReportAll(enumTypeMismatchFindings(ctx)) }

// enAnalyze drives one rule through the framework against the descriptor-backed
// environment and returns the emitted findings for content.
func enAnalyze(t *testing.T, rule celtlint.Rule, content string) []celtlint.Finding {
	t.Helper()
	an, err := celtlint.New(enEnv(t), celtlint.WithDescriptor(enFDSet()), celtlint.WithRules(rule))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return an.Analyze("test.cel", content)
}

// enFindByCode returns the first finding with the given code, if any.
func enFindByCode(fs []celtlint.Finding, code string) (celtlint.Finding, bool) {
	for i := range fs {
		if fs[i].Code == code {
			return fs[i], true
		}
	}
	return celtlint.Finding{}, false
}

// enClassifyCompare compiles content and classifies the first comparison call
// via classifyComparison against the real enum index, exercising
// classifyEnum/enumVariableType with a genuine checked AST.
func enClassifyCompare(t *testing.T, env *cel.Env, content string) (kind compareKind, a, b enumOperand) {
	t.Helper()
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		t.Fatalf("Compile %q: %v", content, iss.Err())
	}
	checked, err := cel.AstToCheckedExpr(ast)
	if err != nil {
		t.Fatalf("AstToCheckedExpr: %v", err)
	}
	native, err := celast.ToAST(checked)
	if err != nil {
		t.Fatalf("ToAST: %v", err)
	}
	refMap := native.ReferenceMap()
	idx := celtindex.Build(env, enFDSet())
	WalkNavigable(celast.NavigateAST(native), func(nav celast.NavigableExpr) bool {
		if nav.Kind() == celast.CallKind && isComparisonOperator(nav.AsCall().FunctionName()) {
			kind, a, b, _ = classifyComparison(nav, idx, refMap)
			return false
		}
		return true
	})
	return kind, a, b
}

// --- Enum index building ---

func TestEnumBuildIndexes(t *testing.T) {
	env := enEnv(t)
	ix := celtindex.Build(env, enFDSet())

	// Top-level enum: short name and value table.
	if got := ix.EnumShort(enStatusType); got != enStatusShort {
		t.Errorf("EnumShort[%s] = %q, want %s", enStatusType, got, enStatusShort)
	}
	if got := ix.EnumShort(enRoleType); got != enRoleShort {
		t.Errorf("EnumShort[%s] = %q, want %s", enRoleType, got, enRoleShort)
	}
	vals := ix.EnumValues(enStatusType)
	wantVals := map[int64]string{0: "UNKNOWN", 1: "ACTIVE", 2: "INACTIVE"}
	for num, name := range wantVals {
		if vals[num] != name {
			t.Errorf("EnumValues[%s][%d] = %q, want %q", enStatusType, num, vals[num], name)
		}
	}
	if len(vals) != len(wantVals) {
		t.Errorf("EnumValues[%s] has %d entries, want %d", enStatusType, len(vals), len(wantVals))
	}

	// Message enum fields map to their full enum type names.
	if got := ix.EnumFields(enPersonType)["status"]; got != enStatusType {
		t.Errorf("EnumFields[%s].status = %q, want %s", enPersonType, got, enStatusType)
	}
	if got := ix.EnumFields(enPersonType)["role"]; got != enRoleType {
		t.Errorf("EnumFields[%s].role = %q, want %s", enPersonType, got, enRoleType)
	}
	// A scalar field is not recorded as an enum field.
	if _, ok := ix.EnumFields(enPersonType)["age"]; ok {
		t.Errorf("EnumFields[%s].age unexpectedly present", enPersonType)
	}

	// Nested enum inside Person is indexed under the full nested type name.
	if got := ix.EnumShort("test.Person.Kind"); got != "Kind" {
		t.Errorf("EnumShort[test.Person.Kind] = %q, want Kind", got)
	}
	if vals := ix.EnumValues("test.Person.Kind"); vals[0] != "KIND_A" || vals[1] != "KIND_B" {
		t.Errorf("EnumValues[test.Person.Kind] = %v, want {0:KIND_A 1:KIND_B}", vals)
	}
}

func TestEnumBuildIndexesEmpty(t *testing.T) {
	env, err := cel.NewEnv()
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	ix := celtindex.Build(env, &descriptorpb.FileDescriptorSet{})
	if len(ix.EnumValues("x")) != 0 || ix.EnumShort("x") != "" || len(ix.EnumFields("x")) != 0 {
		t.Errorf("empty enum index not empty: %+v", ix)
	}
}

// --- classifyComparison / classifyEnum via a real checked AST ---

func TestEnumClassifyComparison(t *testing.T) {
	env := enEnv(t)
	tests := []struct {
		name     string
		content  string
		wantKind compareKind
		wantA    enumOperand
		wantB    enumOperand
	}{
		{
			name:     "int vs int is not an enum comparison",
			content:  `1 == 2`,
			wantKind: compareNone,
		},
		{
			name:     "int vs int on the same field types is not enum",
			content:  `p.age == p.age`,
			wantKind: compareNone,
		},
		{
			name:     "enum variable vs raw int is raw enum",
			content:  `p.status == 1`,
			wantKind: compareRawEnum,
			wantA:    enumOperand{kind: kindVariable, enum: enStatusType},
			wantB:    enumOperand{kind: kindInt, val: 1},
		},
		{
			name:     "raw int on the left is still raw enum",
			content:  `2 == p.status`,
			wantKind: compareRawEnum,
			wantA:    enumOperand{kind: kindInt, val: 2},
			wantB:    enumOperand{kind: kindVariable, enum: enStatusType},
		},
		{
			name:     "variable of same enum compared to variable is a warn",
			content:  `p.status == q.status`,
			wantKind: compareEnumWarn,
			wantA:    enumOperand{kind: kindVariable, enum: enStatusType},
			wantB:    enumOperand{kind: kindVariable, enum: enStatusType},
		},
		{
			name:     "variables of different enums is an error",
			content:  `p.status == p.role`,
			wantKind: compareEnumError,
			wantA:    enumOperand{kind: kindVariable, enum: enStatusType},
			wantB:    enumOperand{kind: kindVariable, enum: enRoleType},
		},
		{
			name:     "variable vs its own named constant is valid",
			content:  `p.status == test.Status.ACTIVE`,
			wantKind: compareNone,
			wantA:    enumOperand{kind: kindVariable, enum: enStatusType},
			wantB:    enumOperand{kind: kindConstant, enum: enStatusType},
		},
		{
			name:     "named constant vs constant of same enum is a warn",
			content:  `test.Status.ACTIVE == test.Status.INACTIVE`,
			wantKind: compareEnumWarn,
			wantA:    enumOperand{kind: kindConstant, enum: enStatusType},
			wantB:    enumOperand{kind: kindConstant, enum: enStatusType},
		},
		{
			name:     "named constant vs variable of a different enum is an error",
			content:  `test.Status.ACTIVE == p.role`,
			wantKind: compareEnumError,
			wantA:    enumOperand{kind: kindConstant, enum: enStatusType},
			wantB:    enumOperand{kind: kindVariable, enum: enRoleType},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, a, b := enClassifyCompare(t, env, tt.content)
			if kind != tt.wantKind {
				t.Fatalf("classifyComparison(%q) kind = %v, want %v", tt.content, kind, tt.wantKind)
			}
			// compareNone short-circuits and returns zeroed operands, so operand
			// identities are only meaningful for non-none classifications.
			if kind == compareNone {
				return
			}
			if a != tt.wantA || b != tt.wantB {
				t.Errorf("classifyComparison(%q) operands = {%+v, %+v}, want {%+v, %+v}",
					tt.content, a, b, tt.wantA, tt.wantB)
			}
		})
	}
}

// --- Raw enum value rule ---

func TestRawEnumValueFlagsNumericLiteral(t *testing.T) {
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == 1`)

	f, ok := enFindByCode(fs, celtlint.CodeRawEnumValue)
	if !ok {
		t.Fatalf("no raw-enum-value finding in %+v", fs)
	}
	if f.Message != "use of a raw enum value" {
		t.Errorf("Message = %q, want %q", f.Message, "use of a raw enum value")
	}
	if f.Hint != "prefer a named enum value over a numeric literal" {
		t.Errorf("Hint = %q, want %q", f.Hint, "prefer a named enum value over a numeric literal")
	}
	if f.Fix != enActiveConst {
		t.Errorf("Fix = %q, want %q", f.Fix, enActiveConst)
	}
	// The fix must target the integer literal (char 12-13), not the enum
	// operand `p.status` (char 0-7).
	if f.FixStart.Line != 0 || f.FixStart.Char != 12 || f.FixEnd.Line != 0 || f.FixEnd.Char != 13 {
		t.Errorf("fix range = %v-%v, want char 12-13 (the literal 1)", f.FixStart, f.FixEnd)
	}
	if f.Level != celtlint.LevelWarn {
		t.Errorf("Level = %v, want warn", f.Level)
	}
	// The fix must resolve in the environment: a bare short enum name
	// (Status.ACTIVE) is an undeclared reference when no container is
	// configured, so the emitted name must be fully qualified.
	if _, iss := enEnv(t).Compile(`p.status == ` + f.Fix); iss != nil && iss.Err() != nil {
		t.Errorf("fix %q does not resolve: %v", f.Fix, iss.Err())
	}
}

func TestRawEnumFixPreservesResolvedConstant(t *testing.T) {
	for _, tc := range []struct {
		name        string
		option      cel.EnvOption
		shadowValue int32
		mustFix     bool
	}{
		{name: "container", option: cel.Container("prod"), shadowValue: 9, mustFix: true},
		{name: "abbreviation with different value", option: cel.Abbrevs("prod.test"), shadowValue: 9},
		{name: "abbreviation with equal value", option: cel.Abbrevs("prod.test"), shadowValue: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enAssertRawEnumFix(t, tc.option, tc.shadowValue, tc.mustFix)
		})
	}
}

func enAssertRawEnumFix(t *testing.T, option cel.EnvOption, shadowValue int32, mustFix bool) {
	t.Helper()
	fdset := enFDSet()
	shadow := proto.CloneOf(fdset.File[0])
	shadow.Name = proto.String("shadow.proto")
	shadow.Package = proto.String("prod.test")
	shadow.MessageType = nil
	shadow.EnumType = shadow.EnumType[:1]
	shadow.EnumType[0].Value[1].Number = proto.Int32(shadowValue)
	fdset.File = append(fdset.File, shadow)

	env, err := cel.NewEnv(cel.TypeDescs(fdset), option, cel.Variable("p", cel.ObjectType(enPersonType)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithDescriptor(fdset), celtlint.WithRules(enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name: enRawEnumName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	const src = `p.status == 1`
	findings := a.Analyze("enum.cel", src)
	if _, ok := enFindByCode(findings, celtlint.CodeRawEnumValue); !ok {
		t.Fatalf("expected a raw enum finding; got %+v", findings)
	}
	fixed, _, changed := celtlint.ApplyFixes(src, findings)
	if !changed {
		if mustFix {
			t.Error("expected an enum fix")
		}
		return
	}
	checked, issues := env.Compile(fixed)
	if err := issues.Err(); err != nil {
		t.Fatalf("fixed expression %q does not compile: %v", fixed, err)
	}
	for _, ref := range checked.NativeRep().ReferenceMap() {
		if ref.Name == enActiveConst && ref.Value != nil && ref.Value.Value() == int64(1) {
			return
		}
	}
	t.Errorf("fixed expression %q does not refer to %s with value 1", fixed, enActiveConst)
}

func TestRawEnumFixesNestedComparisonsInOnePass(t *testing.T) {
	env := enEnv(t)
	a, err := celtlint.New(env, celtlint.WithDescriptor(enFDSet()), celtlint.WithRules(enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name: enRawEnumName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	const src = `(p.status == 1 ? p : q).status == 2`
	if _, issues := env.Compile(src); issues.Err() != nil {
		t.Fatalf("invalid test expression: %v", issues.Err())
	}
	findings := a.Analyze("enum.cel", src)
	if len(findings) != 2 {
		t.Errorf("expected one finding per comparison, got %+v", findings)
	}
	fixed, _, changed := celtlint.ApplyFixes(src, findings)
	if !changed {
		t.Fatal("expected fixes")
	}
	if _, issues := env.Compile(fixed); issues.Err() != nil {
		t.Fatalf("fixed expression %q does not compile: %v", fixed, issues.Err())
	}
	if remaining := a.Analyze("enum.cel", fixed); len(remaining) != 0 {
		t.Errorf("one fix pass left findings in %q: %+v", fixed, remaining)
	}
}

func TestRawEnumValueRightSideNumericLiteral(t *testing.T) {
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `2 == p.status`)

	f, ok := enFindByCode(fs, celtlint.CodeRawEnumValue)
	if !ok {
		t.Fatalf("no raw-enum-value finding in %+v", fs)
	}
	if f.Fix != "test.Status.INACTIVE" {
		t.Errorf("Fix = %q, want %q (value 2 of Status)", f.Fix, "test.Status.INACTIVE")
	}
	// The fix must target the integer literal (char 0-1), not the enum operand.
	if f.FixStart.Line != 0 || f.FixStart.Char != 0 || f.FixEnd.Line != 0 || f.FixEnd.Char != 1 {
		t.Errorf("fix range = %v-%v, want char 0-1 (the literal 2)", f.FixStart, f.FixEnd)
	}
}

func TestRawEnumValueSpanInMacroExpandedExpr(t *testing.T) {
	// The raw enum comparison sits inside a multi-line expression that also
	// contains a macro (`has`). Macro expansion renumbers AST node ids, so the
	// finding's spans must be read from the checked AST's own source info
	// rather than a fresh unexpanded re-parse (which would map ids to wrong
	// positions and corrupt the fix).
	content := "has(p.role)\n  && p.status == 1\n  && p.role == test.Role.ADMIN"
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, content)

	f, ok := enFindByCode(fs, celtlint.CodeRawEnumValue)
	if !ok {
		t.Fatalf("no raw-enum-value finding in %+v", fs)
	}
	if f.Fix != enActiveConst {
		t.Errorf("Fix = %q, want %q (value 1 of Status)", f.Fix, enActiveConst)
	}
	// The literal `1` is on line 1 (0-indexed), chars 17-18 of
	// "  && p.status == 1". The fix must target it, not a node on another line.
	if f.FixStart.Line != 1 || f.FixStart.Char != 17 || f.FixEnd.Line != 1 || f.FixEnd.Char != 18 {
		t.Errorf("fix range = %v-%v, want line 1 chars 17-18 (the literal 1)", f.FixStart, f.FixEnd)
	}
	// The primary span must be the single-line comparison `p.status == 1`
	// (line 1, chars 5-18), not spanning across lines.
	if f.Start.Line != 1 || f.Start.Char != 5 || f.End.Line != 1 || f.End.Char != 18 {
		t.Errorf("finding span = %v-%v, want line 1 chars 5-18", f.Start, f.End)
	}
}

func TestRawEnumValueOutOfRangeHasNoFix(t *testing.T) {
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == 999`)

	f, ok := enFindByCode(fs, celtlint.CodeRawEnumValue)
	if !ok {
		t.Fatalf("no raw-enum-value finding in %+v", fs)
	}
	if f.Fix != "" {
		t.Errorf("Fix = %q, want empty for a value with no enum member", f.Fix)
	}
}

func TestRawEnumValueNoFindingNamedConstant(t *testing.T) {
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == test.Status.ACTIVE`)

	if f, ok := enFindByCode(fs, celtlint.CodeRawEnumValue); ok {
		t.Errorf("unexpected raw-enum-value finding: %+v", f)
	}
}

func TestRawEnumValueNoFindingIntCompare(t *testing.T) {
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.age == 1`)

	if len(fs) != 0 {
		t.Errorf("expected no raw-enum-value findings, got %+v", fs)
	}
}

// --- Enum comparison rule ---

func TestEnumComparisonFlagsVariableVariable(t *testing.T) {
	fs := enAnalyze(t, enEnumCompareRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumCmpName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == q.status`)

	f, ok := enFindByCode(fs, celtlint.CodeOddComparison)
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

func TestEnumComparisonFlagsConstantConstant(t *testing.T) {
	fs := enAnalyze(t, enEnumCompareRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumCmpName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `test.Status.ACTIVE == test.Status.INACTIVE`)

	f, ok := enFindByCode(fs, celtlint.CodeOddComparison)
	if !ok {
		t.Fatalf("no odd-comparison finding in %+v", fs)
	}
	if f.Hint != "comparing enum values" {
		t.Errorf("Hint = %q, want %q", f.Hint, "comparing enum values")
	}
}

func TestEnumComparisonNoFindingValid(t *testing.T) {
	fs := enAnalyze(t, enEnumCompareRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumCmpName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status == test.Status.ACTIVE`)

	if f, ok := enFindByCode(fs, celtlint.CodeOddComparison); ok {
		t.Errorf("unexpected odd-comparison finding: %+v", f)
	}
}

// --- Enum type mismatch rule ---

func TestEnumTypeMismatchFlagsDifferentEnums(t *testing.T) {
	fs := enAnalyze(t, enEnumMismatchRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumMismName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `p.status == p.role`)

	f, ok := enFindByCode(fs, celtlint.CodeOddComparison)
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

func TestEnumTypeMismatchFlagsConstantVsDifferentEnum(t *testing.T) {
	fs := enAnalyze(t, enEnumMismatchRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumMismName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `test.Status.ACTIVE == p.role`)

	f, ok := enFindByCode(fs, celtlint.CodeOddComparison)
	if !ok {
		t.Fatalf("no odd-comparison finding in %+v", fs)
	}
	if f.Hint != "comparing enum values of different types" {
		t.Errorf("Hint = %q, want %q", f.Hint, "comparing enum values of different types")
	}
	if f.Level != celtlint.LevelError {
		t.Errorf("Level = %v, want error", f.Level)
	}
}

func TestRepeatedEnumComparisonsAreListComparisons(t *testing.T) {
	fdset := enFDSet()
	person := fdset.File[0].MessageType[0]
	person.Field = append(person.Field, &descriptorpb.FieldDescriptorProto{
		Name: proto.String("states"), Number: proto.Int32(16), TypeName: proto.String("." + enStatusType),
		Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
	})
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("p", cel.ObjectType(enPersonType)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env, celtlint.WithDescriptor(fdset), celtlint.WithRules(All()...))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"p.states == []", "p.states == [0]", "p.states == p.states"} {
		if _, issues := env.Compile(src); issues.Err() != nil {
			t.Fatalf("invalid list comparison fixture %q: %v", src, issues.Err())
		}
		if f, ok := enFindByCode(a.Analyze("test.cel", src), celtlint.CodeOddComparison); ok {
			t.Errorf("valid list comparison %q received scalar enum finding: %+v", src, f)
		}
	}
}

func TestEnumTypeMismatchFlagsConstantsFromDifferentEnums(t *testing.T) {
	fs := enAnalyze(t, enEnumMismatchRule{RuleMeta: celtlint.RuleMeta{
		Name: enEnumMismName, Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `test.Status.ACTIVE == test.Role.ADMIN`)
	if f, ok := enFindByCode(fs, celtlint.CodeOddComparison); !ok || !f.IsError() {
		t.Errorf("two constants from different enum types should produce a mismatch error: %+v", fs)
	}
}

func TestEnumTypeMismatchNoFindingSameEnum(t *testing.T) {
	fs := enAnalyze(t, enEnumMismatchRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumMismName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `p.status == q.status`)

	if f, ok := enFindByCode(fs, celtlint.CodeOddComparison); ok {
		t.Errorf("unexpected odd-comparison finding: %+v", f)
	}
}

func TestEnumTypeMismatchNoFindingValid(t *testing.T) {
	fs := enAnalyze(t, enEnumMismatchRule{RuleMeta: celtlint.RuleMeta{
		Name:     enEnumMismName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}, `p.status == test.Status.ACTIVE`)

	if len(fs) != 0 {
		t.Errorf("expected no enum-type-mismatch findings, got %+v", fs)
	}
}

// --- Pure helper truth tables ---

func TestEnumOperandIsEnum(t *testing.T) {
	tests := []struct {
		name string
		o    enumOperand
		want bool
	}{
		{name: "variable", o: enumOperand{kind: kindVariable}, want: true},
		{name: "constant", o: enumOperand{kind: kindConstant}, want: true},
		{name: "int", o: enumOperand{kind: kindInt}, want: false},
		{name: "other", o: enumOperand{kind: kindOther}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.o.isEnum(); got != tt.want {
				t.Errorf("enumOperand{kind:%q}.isEnum() = %v, want %v", tt.o.kind, got, tt.want)
			}
		})
	}
}

func TestEnumValidCompare(t *testing.T) {
	enVar := enumOperand{kind: kindVariable, enum: enStatusType}
	enConst := enumOperand{kind: kindConstant, enum: enStatusType}
	roleVar := enumOperand{kind: kindVariable, enum: enRoleType}
	intLit := enumOperand{kind: kindInt, val: 1}

	tests := []struct {
		name string
		a, b enumOperand
		want bool
	}{
		{name: "variable vs its own constant", a: enVar, b: enConst, want: true},
		{name: "constant on the left is not valid directly", a: enConst, b: enVar, want: false},
		{name: "variable vs constant of a different enum", a: roleVar, b: enConst, want: false},
		{name: "variable vs variable", a: enVar, b: enVar, want: false},
		{name: "variable vs int", a: enVar, b: intLit, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validEnumCompare(tt.a, tt.b); got != tt.want {
				t.Errorf("validEnumCompare(%+v, %+v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestEnumCompareWarnMessage(t *testing.T) {
	enVar := enumOperand{kind: kindVariable, enum: enStatusType}
	enConst := enumOperand{kind: kindConstant, enum: enStatusType}
	if got := compareWarnMessage(enVar, enVar); got != "comparing variables directly" {
		t.Errorf("compareWarnMessage(variable, variable) = %q, want %q", got, "comparing variables directly")
	}
	if got := compareWarnMessage(enConst, enConst); got != "comparing enum values" {
		t.Errorf("compareWarnMessage(constant, constant) = %q, want %q", got, "comparing enum values")
	}
}

func TestEnumCompareErrorMessage(t *testing.T) {
	enVar := enumOperand{kind: kindVariable, enum: enStatusType}
	roleVar := enumOperand{kind: kindVariable, enum: enRoleType}
	enConst := enumOperand{kind: kindConstant, enum: enStatusType}
	other := enumOperand{kind: kindOther}

	tests := []struct {
		name string
		a, b enumOperand
		want string
	}{
		{name: "against wrong type", a: enVar, b: other, want: "comparing against a value of the wrong type"},
		{name: "variables of different types", a: enVar, b: roleVar, want: "comparing variables of different types"},
		{name: "constant vs variable different enum", a: enConst, b: roleVar, want: "comparing enum values of different types"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareErrorMessage(tt.a, tt.b); got != tt.want {
				t.Errorf("compareErrorMessage(%+v, %+v) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// --- Guard conditions ---

func TestEnumCheckGuards(t *testing.T) {
	if fs := rawEnumValueFindings(&celtlint.Context{}); fs != nil {
		t.Errorf("rawEnumValueFindings with nil Index = %v, want nil", fs)
	}
	if fs := enumCompareFindings(&celtlint.Context{}); fs != nil {
		t.Errorf("enumCompareFindings with nil Index = %v, want nil", fs)
	}
	if fs := enumTypeMismatchFindings(&celtlint.Context{}); fs != nil {
		t.Errorf("enumTypeMismatchFindings with nil Index = %v, want nil", fs)
	}
}

func TestEnumNoFindingOnCompileFailure(t *testing.T) {
	// A syntactically invalid source fails to compile; the enum checks bail out
	// rather than emit findings from a broken AST.
	fs := enAnalyze(t, enRawEnumRule{RuleMeta: celtlint.RuleMeta{
		Name:     enRawEnumName,
		Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
	}}, `p.status =`)

	if len(fs) != 0 {
		t.Errorf("expected no findings on compile failure, got %+v", fs)
	}
}
