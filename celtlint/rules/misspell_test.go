//nolint:misspell // tests intentionally use misspelled tokens to exercise the detector
package rules

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
)

// Misspelled tokens and macro names repeated across the table-driven tests.
const (
	msUserNme = "userNme"
	msHsa     = "hsa"
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
		{"identifier", msUserNme, celtlint.CodeUnknownIdentifier, "unknown identifier", "userName"},
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

// TestMsCheckMisspellSkipsComprehensionVars verifies comprehension-bound locals
// (the iteration variable of .all/.map/.filter/...) are not offered for rename,
// even when a close global name exists.
func TestMsCheckMisspellSkipsComprehensionVars(t *testing.T) {
	env := msEnv(t)
	a := msNewAnalyzer(t, env)
	for _, content := range []string{
		`[1].all(userNme, userNme > 0)`,
		`[1].map(userNme, userNme)`,
	} {
		if fs := a.Analyze("x.cel", content); len(fs) != 0 {
			t.Errorf("Analyze(%q) = %d findings, want 0 (comprehension-bound local): %+v", content, len(fs), fs)
		}
	}
}

// TestMsCheckMisspellSkipsNamespaceQualifiers verifies that a bare identifier
// starting a dotted member access is treated as a namespace/type/package
// qualifier, not a free identifier to rename: the `cel` in `cel.bind(...)`
// (ext.Bindings) and a qualified type prefix.
func TestMsCheckMisspellSkipsNamespaceQualifiers(t *testing.T) {
	env, err := cel.NewEnv(ext.Bindings(), cel.Variable("x", cel.StringType))
	if err != nil {
		t.Fatal(err)
	}
	a := msNewAnalyzer(t, env)
	for _, content := range []string{
		`cel.bind(y, "b", y == "b")`,
		`cel.bind(y, "b", y == "b") && true`,
	} {
		if _, issues := env.Compile(content); issues.Err() != nil {
			t.Fatalf("fixture %q invalid: %v", content, issues.Err())
		}
		for _, f := range a.Analyze("x.cel", content) {
			t.Errorf("Analyze(%q) produced %s (%q) on namespace qualifier", content, f.Code, f.Message)
		}
	}
}

// TestMsCheckMisspellSkipsQualifiedProtoPrefix verifies that a fully-qualified
// proto type reference (`pkg.Type.field`) does not flag the package/type
// segments (`pkg`, `Type`) as unknown identifiers: they are namespace
// qualifiers resolved through the type system.
func TestMsCheckMisspellSkipsQualifiedProtoPrefix(t *testing.T) {
	fdset := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: proto.String("com.example.proto"), Package: proto.String("com.example"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Widget"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("name"), Number: proto.Int32(1),
				Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			}},
		}},
	}}}
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("w", cel.ObjectType("com.example.Widget")))
	if err != nil {
		t.Fatal(err)
	}
	a, err := celtlint.New(env,
		celtlint.WithDescriptor(fdset),
		celtlint.WithRules(Misspell{RuleMeta: celtlint.RuleMeta{
			Name: "misspell", Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
		}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	content := `com.example.Widget{name: "x"}.name == "x"`
	if _, issues := env.Compile(content); issues.Err() != nil {
		t.Fatalf("fixture invalid: %v", issues.Err())
	}
	if fs := a.Analyze("x.cel", content); len(fs) != 0 {
		t.Fatalf("qualified proto reference produced findings: %+v", fs)
	}
}

// TestMsCheckMisspellFlagsReceiverVariableTypo verifies that a namespace is
// only treated as such when it is actually a known qualifier: a near-miss of a
// declared variable used as a receiver (`reul.field` for `rule.field`) is
// still reported, because the namespace index does not swallow genuine typos.
func TestMsCheckMisspellFlagsReceiverVariableTypo(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("rule", cel.ObjectType("com.example.Widget")))
	if err != nil {
		t.Fatal(err)
	}
	a := msNewAnalyzer(t, env)
	fs := a.Analyze("x.cel", `reul.name == "x"`)
	f, ok := rlFindByCode(fs, celtlint.CodeUnknownIdentifier)
	if !ok {
		t.Fatalf("receiver typo of a variable was not reported: %+v", fs)
	}
	if f.Fix != "rule" {
		t.Errorf("Fix = %q, want %q", f.Fix, "rule")
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
		{"macro call present", "use has(x) instead", []string{lnHasMacro, "exists"}, true},
		{"other macro in list", "call exists(x)", []string{lnHasMacro, "exists"}, true},
		{"name contains macro but no call parens", "undeclared `hasmap`", []string{lnHasMacro}, false},
		{"no macro mention", "undeclared `foo`", []string{lnHasMacro}, false},
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
		{"near-miss typo", map[string]bool{msHsa: true}, []string{lnHasMacro}, true},
		{"exact macro name", map[string]bool{lnHasMacro: true}, []string{lnHasMacro}, true},
		{"case-insensitive typo", map[string]bool{"HAS": true}, []string{lnHasMacro}, true},
		{"unrelated reported name", map[string]bool{"zzzz": true}, []string{lnHasMacro}, false},
		{"no reported names", map[string]bool{}, []string{lnHasMacro}, false},
		{"no macros", map[string]bool{msHsa: true}, nil, false},
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
	ms := msFinding(content, msUserNme, celtlint.CodeUnknownIdentifier)
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
	reported := map[string]bool{msUserNme: true}
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

// TestMsDropRedundantKeepsUncoveredEnumMemberRef verifies an undeclared enum
// type used as a qualified member (Color.RED) is kept when no finding covers
// that occurrence: the compiler error is only suppressed when another finding
// actually reports the same name (a bare short enum name without a container
// does not resolve, so its error must not be hidden).
func TestMsDropRedundantKeepsUncoveredEnumMemberRef(t *testing.T) {
	idx := msEnumIndex(t)
	content := `Color.RED`
	compile := celtlint.Finding{Code: celtlint.CodeUndeclaredReference, Detail: "undeclared reference to `Color`"}
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, content, idx, map[string]bool{})
	if len(out) != 1 {
		t.Fatalf("DropRedundant = %d findings, want 1 (kept; no finding covers Color)", len(out))
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
	out := DropRedundantCompileErrors([]celtlint.Finding{compile}, "", idx, map[string]bool{msHsa: true})
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
