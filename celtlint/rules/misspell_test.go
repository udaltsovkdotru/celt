//nolint:goconst,misspell // table-driven tests intentionally repeat identifiers and use misspelled tokens to exercise the detector.
package rules

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
)

// msEnv builds a small env with one variable, one object-typed variable and
// one global function, so the index has deterministic candidates.
func msEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable("userName", cel.StringType),
		cel.Variable("w", cel.ObjectType("ms.pkg.Widget")),
		cel.Function("processData",
			cel.Overload("processData_str", []*cel.Type{cel.StringType}, cel.BoolType)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// msFDSet returns a descriptor set describing one enum (Color: RED/GREEN) and
// one message (Widget) with scalar, enum and string fields.
func msFDSet() *descriptorpb.FileDescriptorSet {
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("ms.proto"),
				Package: proto.String("ms.pkg"),
				Syntax:  proto.String("proto3"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String("Color"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String("RED"), Number: proto.Int32(0)},
							{Name: proto.String("GREEN"), Number: proto.Int32(1)},
						},
					},
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Widget"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("id"), Number: proto.Int32(1), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
							{Name: proto.String("color"), Number: proto.Int32(2), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: proto.String(".ms.pkg.Color")},
							{Name: proto.String("name"), Number: proto.Int32(3), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
						},
					},
				},
			},
		},
	}
}

// msNewAnalyzer wires a misspell-only analyzer over env and the descriptor set.
func msNewAnalyzer(t *testing.T, env *cel.Env) *celtlint.Analyzer {
	t.Helper()
	a, err := celtlint.New(env,
		celtlint.WithRules(Misspell{RuleMeta: celtlint.RuleMeta{
			Name:     "misspell",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
		}}),
		celtlint.WithDescriptor(msFDSet()),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// msEmptyIndex builds an index over an empty env and descriptor set.
func msEmptyIndex(t *testing.T) *celtindex.Index {
	t.Helper()
	env, err := cel.NewEnv()
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return celtindex.Build(env, &descriptorpb.FileDescriptorSet{})
}

// msEnumIndex builds an index whose env declares the Color enum.
func msEnumIndex(t *testing.T) *celtindex.Index {
	t.Helper()
	env, err := cel.NewEnv(cel.TypeDescs(msFDSet()))
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return celtindex.Build(env, msFDSet())
}

// msMacroIndex builds an index whose env registers the `has` macro.
func msMacroIndex(t *testing.T) *celtindex.Index {
	t.Helper()
	env, err := cel.NewEnv(cel.Macros(cel.HasMacro))
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return celtindex.Build(env, &descriptorpb.FileDescriptorSet{})
}

// msFinding builds a misspell-style finding spanning a token inside content.
func msFinding(content, token, code string) celtlint.Finding {
	start := strings.Index(content, token)
	return celtlint.Finding{
		Level:   celtlint.LevelError,
		Code:    code,
		Message: "unknown",
		Start:   celtsource.OffsetToPos(content, start),
		End:     celtsource.OffsetToPos(content, start+len(token)),
	}
}

// TestMsCheckMisspellSuggestions verifies that near-miss references to each
// kind of name (identifier, function, enum value, object field) produce a
// suggestion finding with the right code, message and replacement fix.
func TestMsCheckMisspellSuggestions(t *testing.T) {
	env := msEnv(t)
	a := msNewAnalyzer(t, env)
	cases := []struct {
		name    string
		content string
		code    string
		message string
		fix     string
	}{
		{"identifier", "userNme", celtlint.CodeUnknownIdentifier, "unknown identifier", "userName"},
		{"function", `processDaa("x")`, celtlint.CodeUnknownIdentifier, "unknown identifier", "processData"},
		{"enum value", "Color.REEN", celtlint.CodeUnknownEnumVariant, "unknown enum variant", "GREEN"},
		{"object field", "w.nmae", celtlint.CodeUnknownField, "unknown field", "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := a.Analyze("x.cel", tc.content)
			if len(fs) != 1 {
				t.Fatalf("Analyze(%q) = %d findings, want 1: %+v", tc.content, len(fs), fs)
			}
			f := fs[0]
			if f.Code != tc.code {
				t.Errorf("Code = %q, want %q", f.Code, tc.code)
			}
			if f.Message != tc.message {
				t.Errorf("Message = %q, want %q", f.Message, tc.message)
			}
			if f.Fix != tc.fix {
				t.Errorf("Fix = %q, want %q", f.Fix, tc.fix)
			}
			if f.Level != celtlint.LevelError {
				t.Errorf("Level = %v, want LevelError", f.Level)
			}
		})
	}
}

// TestMsCheckMisspellExactNamesNotFlagged verifies exact identifiers,
// functions, enum values and fields are not flagged.
func TestMsCheckMisspellExactNamesNotFlagged(t *testing.T) {
	env := msEnv(t)
	a := msNewAnalyzer(t, env)
	content := `userName + w.name + Color.RED + processData("x")`
	if fs := a.Analyze("x.cel", content); len(fs) != 0 {
		t.Fatalf("Analyze = %d findings, want 0: %+v", len(fs), fs)
	}
}

// TestMsCheckMisspellNoNearMatch verifies a token far from every candidate is
// not flagged.
func TestMsCheckMisspellNoNearMatch(t *testing.T) {
	env := msEnv(t)
	a := msNewAnalyzer(t, env)
	if fs := a.Analyze("x.cel", `qqqqzz`); len(fs) != 0 {
		t.Fatalf("Analyze = %d findings, want 0: %+v", len(fs), fs)
	}
}

// TestMsCheckMisspellSkipsStringsAndComments verifies string literals and //-line
// comments are not scanned for misspellings.
func TestMsCheckMisspellSkipsStringsAndComments(t *testing.T) {
	env := msEnv(t)
	a := msNewAnalyzer(t, env)
	content := "\"userNme\" // userNme\nColor.RED"
	if fs := a.Analyze("x.cel", content); len(fs) != 0 {
		t.Fatalf("Analyze = %d findings, want 0: %+v", len(fs), fs)
	}
}

// TestMsIsCheckMacroError verifies the macro-call matcher used by the compile
// error dedup: a detail mentioning a macro name immediately followed by '('.
func TestMsIsCheckMacroError(t *testing.T) {
	cases := []struct {
		name   string
		msg    string
		macros []string
		want   bool
	}{
		{"macro call present", "use has(x) instead", []string{"has", "exists"}, true},
		{"other macro in list", "call exists(x)", []string{"has", "exists"}, true},
		{"name contains macro but no call parens", "undeclared `hasmap`", []string{"has"}, false},
		{"no macro mention", "undeclared `foo`", []string{"has"}, false},
		{"no macros configured", "call has(x)", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCheckMacroError(tc.msg, tc.macros); got != tc.want {
				t.Errorf("isCheckMacroError(%q, %v) = %v, want %v", tc.msg, tc.macros, got, tc.want)
			}
		})
	}
}

// TestMsReportedMacroTypo verifies the dedup signal that some reported name is
// within edit-distance threshold of a macro keyword.
func TestMsReportedMacroTypo(t *testing.T) {
	cases := []struct {
		name     string
		reported map[string]bool
		macros   []string
		want     bool
	}{
		{"near-miss typo", map[string]bool{"hsa": true}, []string{"has"}, true},
		{"exact macro name", map[string]bool{"has": true}, []string{"has"}, true},
		{"case-insensitive typo", map[string]bool{"HAS": true}, []string{"has"}, true},
		{"unrelated reported name", map[string]bool{"zzzz": true}, []string{"has"}, false},
		{"no reported names", map[string]bool{}, []string{"has"}, false},
		{"no macros", map[string]bool{"hsa": true}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reportedMacroTypo(tc.reported, tc.macros); got != tc.want {
				t.Errorf("reportedMacroTypo(%v, %v) = %v, want %v", tc.reported, tc.macros, got, tc.want)
			}
		})
	}
}

// TestMsDropRedundantDropsReportedUndeclared verifies a compile error whose
// undeclared name was already reported by the misspell rule is dropped.
func TestMsDropRedundantDropsReportedUndeclared(t *testing.T) {
	content := `userNme`
	ms := msFinding(content, "userNme", celtlint.CodeUnknownIdentifier)
	reported := ReportedNames([]celtlint.Finding{ms}, content)
	compile := celtlint.Finding{
		Level:  celtlint.LevelError,
		Code:   celtlint.CodeUndeclaredReference,
		Detail: "undeclared reference to `userNme`",
	}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, content, msEmptyIndex(t), reported)
	if len(out) != 0 {
		t.Fatalf("DropRedundant = %d findings, want 0 (already reported by misspell)", len(out))
	}
}

// TestMsDropRedundantKeepsUnreportedUndeclared verifies an undeclared reference
// not reported by the misspell rule survives dedup.
func TestMsDropRedundantKeepsUnreportedUndeclared(t *testing.T) {
	content := `qqqqzz`
	reported := map[string]bool{"userNme": true}
	compile := celtlint.Finding{
		Level:  celtlint.LevelError,
		Code:   celtlint.CodeUndeclaredReference,
		Detail: "undeclared reference to `qqqqzz`",
	}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, content, msEmptyIndex(t), reported)
	if len(out) != 1 {
		t.Fatalf("DropRedundant = %d findings, want 1 (kept)", len(out))
	}
}

// TestMsDropRedundantDropsReportedUndefinedField verifies an undefined-field
// compile error whose field was already reported is dropped.
func TestMsDropRedundantDropsReportedUndefinedField(t *testing.T) {
	content := `w.nmae`
	ms := msFinding(content, "nmae", celtlint.CodeUnknownField)
	reported := ReportedNames([]celtlint.Finding{ms}, content)
	compile := celtlint.Finding{
		Level:  celtlint.LevelError,
		Code:   celtlint.CodeUndefinedField,
		Detail: "undefined field `nmae`",
	}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, content, msEmptyIndex(t), reported)
	if len(out) != 0 {
		t.Fatalf("DropRedundant = %d findings, want 0 (field already reported)", len(out))
	}
}

// TestMsDropRedundantDropsEnumMemberRef verifies an undeclared enum type used
// as a qualified member (Color.RED) is dropped.
func TestMsDropRedundantDropsEnumMemberRef(t *testing.T) {
	idx := msEnumIndex(t)
	content := `Color.RED`
	compile := celtlint.Finding{Code: celtlint.CodeUndeclaredReference, Detail: "undeclared reference to `Color`"}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, content, idx, map[string]bool{})
	if len(out) != 0 {
		t.Fatalf("DropRedundant = %d findings, want 0 (enum member ref)", len(out))
	}
}

// TestMsDropRedundantKeepsEnumTypeAlone verifies an undeclared enum type used
// without a member selection is kept.
func TestMsDropRedundantKeepsEnumTypeAlone(t *testing.T) {
	idx := msEnumIndex(t)
	content := `Color`
	compile := celtlint.Finding{Code: celtlint.CodeUndeclaredReference, Detail: "undeclared reference to `Color`"}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, content, idx, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("DropRedundant = %d findings, want 1 (not a member ref)", len(out))
	}
}

// TestMsDropRedundantDropsMacroTypo verifies a compile error mentioning a macro
// call is dropped when a reported name is a near-miss of that macro.
func TestMsDropRedundantDropsMacroTypo(t *testing.T) {
	idx := msMacroIndex(t)
	compile := celtlint.Finding{Code: celtlint.CodeCompileError, Detail: "unknown function has() called"}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, "", idx, map[string]bool{"hsa": true})
	if len(out) != 0 {
		t.Fatalf("DropRedundant = %d findings, want 0 (macro typo deduped)", len(out))
	}
}

// TestMsDropRedundantKeepsMacroTypoWithoutNearMacro verifies a macro-looking
// compile error survives dedup when no reported name is near the macro.
func TestMsDropRedundantKeepsMacroTypoWithoutNearMacro(t *testing.T) {
	idx := msMacroIndex(t)
	compile := celtlint.Finding{Code: celtlint.CodeCompileError, Detail: "unknown function has() called"}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, "", idx, map[string]bool{"zzzz": true})
	if len(out) != 1 {
		t.Fatalf("DropRedundant = %d findings, want 1 (kept)", len(out))
	}
}
