package celtindex_test

import (
	"testing"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/pkg/celtspell"
)

// spelName constants keep the descriptor builders and the assertions they
// drive in sync without repeating magic string literals.
const (
	spelColorName = "Color"
	spelRedName   = "RED"
	spelGreenName = "GREEN"
	spelNameField = "name"
)

// spelTestEnv builds an env with a handful of real, distinctive names spread
// across variables, functions and member functions so the index has
// non-trivial candidates to match against.
func spelTestEnv(t *testing.T) *cel.Env {
	t.Helper()
	opts := []cel.EnvOption{
		cel.Variable("userName", cel.StringType),
		cel.Variable("items", cel.ListType(cel.StringType)),
		// An object-typed variable whose fields come from the descriptor set.
		cel.Variable("w", cel.ObjectType("test.pkg.Widget")),
		cel.Function("processData",
			cel.Overload("processData_str", []*cel.Type{cel.StringType}, cel.BoolType)),
		cel.Function("joinAll",
			cel.MemberOverload("joinAll_list", []*cel.Type{cel.ListType(cel.StringType), cel.StringType}, cel.StringType)),
		cel.Function("weirdStr",
			cel.MemberOverload("weirdStr_str", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType)),
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// spelTestFDSet returns a descriptor set describing a package with one enum
// (Color: RED/GREEN), a message Widget (scalar, enum, repeated-string,
// string, and message fields) and a nested message Child.
func spelTestFDSet() *descriptorpb.FileDescriptorSet {
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	rep := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("test.proto"),
				Package: proto.String("test.pkg"),
				Syntax:  proto.String("proto3"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String(spelColorName),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String(spelRedName), Number: proto.Int32(0)},
							{Name: proto.String(spelGreenName), Number: proto.Int32(1)},
						},
					},
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Widget"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("id"), Number: proto.Int32(1), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
							{Name: proto.String("color"), Number: proto.Int32(2), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: proto.String(".test.pkg.Color")},
							{Name: proto.String("tags"), Number: proto.Int32(3), Label: rep, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: proto.String(spelNameField), Number: proto.Int32(4), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: proto.String("child"), Number: proto.Int32(5), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".test.pkg.Child")},
						},
					},
					{
						Name: proto.String("Child"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("val"), Number: proto.Int32(1), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
						},
					},
				},
			},
		},
	}
}

func spelBuild(t *testing.T, env *cel.Env, fdset *descriptorpb.FileDescriptorSet) *celtindex.Index {
	t.Helper()
	return celtindex.Build(env, fdset)
}

func spelHasCandidate(ix *celtindex.Index, name string, kind celtindex.Kind) bool {
	for _, c := range ix.Candidates() {
		if c.Name == name && c.Kind == kind {
			return true
		}
	}
	return false
}

// TestSpellBuildIndexesEnumIndex verifies Build populates the enum index:
// full-name value tables, short names and the enum-field mapping of Widget.
func TestSpellBuildIndexesEnumIndex(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	if got := ix.EnumShort("test.pkg.Color"); got != spelColorName {
		t.Errorf("EnumShort(test.pkg.Color) = %q, want Color", got)
	}
	vals := ix.EnumValues("test.pkg.Color")
	if vals[0] != spelRedName || vals[1] != spelGreenName {
		t.Errorf("EnumValues(test.pkg.Color) = %v, want {0:RED 1:GREEN}", vals)
	}
	if got := ix.EnumFields("test.pkg.Widget")["color"]; got != "test.pkg.Color" {
		t.Errorf("EnumFields(test.pkg.Widget).color = %q, want test.pkg.Color", got)
	}
}

// TestSpellBuildIndexesCandidates verifies the index records every kind
// of name: variables, functions, macros, constants, fields, enum types and
// enum values.
func TestSpellBuildIndexesCandidates(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	for _, tc := range []struct {
		name string
		kind celtindex.Kind
	}{
		{"userName", celtindex.KindVariable},
		{"items", celtindex.KindVariable},
		{"w", celtindex.KindVariable},
		{"processData", celtindex.KindFunction},
		{"joinAll", celtindex.KindFunction},
		{"weirdStr", celtindex.KindFunction},
		{"true", celtindex.KindValue},
		{"false", celtindex.KindValue},
		{"null", celtindex.KindValue},
		{spelColorName, celtindex.KindEnum},
		{spelRedName, celtindex.KindValue},
		{spelGreenName, celtindex.KindValue},
		{"id", celtindex.KindField},
		{spelNameField, celtindex.KindField},
	} {
		if !spelHasCandidate(ix, tc.name, tc.kind) {
			t.Errorf("candidates missing %q with kind %q", tc.name, tc.kind)
		}
	}
}

// TestSpellNearestVariable verifies a near-miss variable identifier is
// replaced with the closest real candidate.
func TestSpellNearestVariable(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	repl, kind, ok := celtspell.Nearest(ix, "usrName")
	if !ok {
		t.Fatal("Nearest(usrName): expected a suggestion")
	}
	if repl != "userName" {
		t.Errorf("Nearest(usrName) repl = %q, want userName", repl)
	}
	if kind != celtindex.KindVariable {
		t.Errorf("Nearest(usrName) kind = %q, want variable", kind)
	}
}

// TestSpellNearestFunction verifies a near-miss function name is replaced with
// the closest function candidate.
func TestSpellNearestFunction(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	repl, kind, ok := celtspell.Nearest(ix, "processDaa")
	if !ok {
		t.Fatal("Nearest(processDaa): expected a suggestion")
	}
	if repl != "processData" {
		t.Errorf("Nearest(processDaa) repl = %q, want processData", repl)
	}
	if kind != celtindex.KindFunction {
		t.Errorf("Nearest(processDaa) kind = %q, want function", kind)
	}
}

// TestSpellNearestEnumValue verifies a near-miss enum value is replaced with
// the closest enum-value candidate.
func TestSpellNearestEnumValue(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	repl, kind, ok := celtspell.Nearest(ix, "REEN")
	if !ok {
		t.Fatal("Nearest(REEN): expected a suggestion")
	}
	if repl != spelGreenName {
		t.Errorf("Nearest(REEN) repl = %q, want GREEN", repl)
	}
	if kind != celtindex.KindValue {
		t.Errorf("Nearest(REEN) kind = %q, want value", kind)
	}
}

// TestSpellNearestExact verifies that a correct name yields no suggestion: an
// exact match (including case-insensitive matches) has edit distance zero.
func TestSpellNearestExact(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	if repl, kind, ok := celtspell.Nearest(ix, "userName"); ok {
		t.Errorf("Nearest(userName) = %q/%q/%v, want no suggestion", repl, kind, ok)
	}
	// Merely changing case is still an exact match to the real candidate.
	if repl, kind, ok := celtspell.Nearest(ix, "USERNAME"); ok {
		t.Errorf("Nearest(USERNAME) = %q/%q/%v, want no suggestion", repl, kind, ok)
	}
}

// TestSpellNearestUnknown verifies a token far from every candidate yields no
// suggestion.
func TestSpellNearestUnknown(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	if repl, kind, ok := celtspell.Nearest(ix, "qqqqzz"); ok {
		t.Errorf("Nearest(qqqqzz) = %q/%q/%v, want no suggestion", repl, kind, ok)
	}
}

// TestSpellNearestQualified verifies the namespace prefix of a dotted token is
// preserved in the replacement, only the base segment is matched.
func TestSpellNearestQualified(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	repl, kind, ok := celtspell.Nearest(ix, "foo.userNmae")
	if !ok {
		t.Fatal("Nearest(foo.userNmae): expected a suggestion")
	}
	if repl != "foo.userName" {
		t.Errorf("Nearest(foo.userNmae) repl = %q, want foo.userName", repl)
	}
	if kind != celtindex.KindVariable {
		t.Errorf("Nearest(foo.userNmae) kind = %q, want variable", kind)
	}
}

// TestSpellNearestConstant verifies the built-in constants are candidates.
func TestSpellNearestConstant(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	repl, kind, ok := celtspell.Nearest(ix, "tru")
	if !ok {
		t.Fatal("Nearest(tru): expected a suggestion")
	}
	if repl != "true" || kind != celtindex.KindValue {
		t.Errorf("Nearest(tru) = %q/%q, want true/value", repl, kind)
	}
}

// TestSpellHasEnum verifies the HasEnum guard reflects the indexed enums.
func TestSpellHasEnum(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	if !ix.HasEnum(spelColorName) {
		t.Error("HasEnum(Color) = false, want true")
	}
	if ix.HasEnum("Nope") {
		t.Error("HasEnum(Nope) = true, want false")
	}
}

// TestSpellMemberCandidatesEnum verifies member completion for an enum returns
// the enum's values.
func TestSpellMemberCandidatesEnum(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	kind, cands := ix.MemberCandidates(spelColorName)
	if kind != celtindex.MemberEnumValue {
		t.Fatalf("MemberCandidates(Color) kind = %v, want MemberEnumValue", kind)
	}
	if len(cands) != 2 {
		t.Fatalf("MemberCandidates(Color) = %d candidates, want 2", len(cands))
	}
	for _, want := range []string{spelRedName, spelGreenName} {
		if !spelHasCandidateKind(cands, want, celtindex.KindValue) {
			t.Errorf("enum value candidates missing %q", want)
		}
	}
}

// TestSpellMemberCandidatesObjField verifies member completion for an
// object-typed variable returns its descriptor fields.
func TestSpellMemberCandidatesObjField(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	kind, cands := ix.MemberCandidates("w")
	if kind != celtindex.MemberObjField {
		t.Fatalf("MemberCandidates(w) kind = %v, want MemberObjField", kind)
	}
	for _, want := range []string{"id", "color", "tags", spelNameField, "child"} {
		if !spelHasCandidateKind(cands, want, celtindex.KindField) {
			t.Errorf("obj field candidates missing %q", want)
		}
	}
}

// TestSpellMemberCandidatesMemberFn verifies member completion for a string
// field resolves to the member functions registered on the string receiver.
func TestSpellMemberCandidatesMemberFn(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	kind, cands := ix.MemberCandidates(spelNameField)
	if kind != celtindex.MemberFunction {
		t.Fatalf("MemberCandidates(name) kind = %v, want MemberFunction", kind)
	}
	if !spelHasCandidateKind(cands, "weirdStr", celtindex.KindFunction) {
		t.Errorf("member fn candidates missing weirdStr")
	}
}

// TestSpellMemberCandidatesNone verifies an unknown receiver has no member
// candidates.
func TestSpellMemberCandidatesNone(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	kind, cands := ix.MemberCandidates("zzzz")
	if kind != celtindex.MemberNone {
		t.Errorf("MemberCandidates(zzzz) kind = %v, want MemberNone", kind)
	}
	if cands != nil {
		t.Errorf("MemberCandidates(zzzz) cands = %v, want nil", cands)
	}
}

// TestSpellReceiverElem verifies the receiver-element helper resolves the
// element type for repeated fields and the string marker for string fields.
func TestSpellReceiverElem(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	if got := ix.ReceiverElem("tags"); got != "string" {
		t.Errorf("ReceiverElem(tags) = %q, want string (repeated string field)", got)
	}
	if got := ix.ReceiverElem(spelNameField); got != "string" {
		t.Errorf("ReceiverElem(name) = %q, want string (string field)", got)
	}
	if got := ix.ReceiverElem("id"); got != "" {
		t.Errorf("ReceiverElem(id) = %q, want empty (scalar int field)", got)
	}
}

// TestSpellFnCandsList verifies a repeated string field exposes the member
// functions registered on a list-of-string receiver.
func TestSpellFnCandsList(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	cands := ix.FnCands("tags")
	if !spelHasCandidateKind(cands, "joinAll", celtindex.KindFunction) {
		t.Errorf("FnCands(tags) missing joinAll (list[string] member fn)")
	}
}

// TestSpellFieldPresence verifies proto3 scalar fields record the
// proto3-value presence kind while repeated fields record none.
func TestSpellFieldPresence(t *testing.T) {
	env := spelTestEnv(t)
	ix := spelBuild(t, env, spelTestFDSet())

	if ix.FieldPresence("test.pkg.Widget", "id")&celtindex.PresenceProto3Value == 0 {
		t.Error("Widget.id: expected PresenceProto3Value")
	}
	if ix.FieldPresence("test.pkg.Widget", "tags")&celtindex.PresenceProto3Value != 0 {
		t.Error("Widget.tags: repeated field should not have proto3 presence")
	}
}

// TestSpellBuildIndexesEmpty verifies Build handles an empty env and an
// empty descriptor set without panicking, still indexing built-in constants.
func TestSpellBuildIndexesEmpty(t *testing.T) {
	env, err := cel.NewEnv()
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	ix := celtindex.Build(env, &descriptorpb.FileDescriptorSet{})

	if ix.EnumValues("x") != nil || ix.EnumShort("x") != "" || ix.EnumFields("x") != nil {
		t.Errorf("empty enum index not empty: %+v", ix)
	}
	if !spelHasCandidate(ix, "true", celtindex.KindValue) {
		t.Error("constants not indexed in an empty env")
	}
	// Unknown token still resolves to nothing.
	if _, _, ok := celtspell.Nearest(ix, "qqqqzz"); ok {
		t.Error("Nearest(qqqqzz) unexpectedly matched in an empty env")
	}
}

func spelHasCandidateKind(cands []celtindex.Candidate, name string, kind celtindex.Kind) bool {
	for _, c := range cands {
		if c.Name == name && c.Kind == kind {
			return true
		}
	}
	return false
}

// TestIndexElementSpecificListMemberFn verifies a custom member function whose
// receiver is a list of a concrete message type is indexed under the same
// short-name element key that FnCands derives for a repeated message field, so
// the misspell rule does not report it as an unknown function.
func TestIndexElementSpecificListMemberFn(t *testing.T) {
	fdset := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("m.proto"),
				Package: proto.String("test.pkg"),
				Syntax:  proto.String("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Bag"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("items"), Number: proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
								TypeName: proto.String(".test.pkg.Thing")},
						},
					},
					{
						Name: proto.String("Thing"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("weight"), Number: proto.Int32(1),
								Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:  descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
						},
					},
				},
			},
		},
	}
	env, err := cel.NewEnv(
		cel.TypeDescs(fdset),
		cel.Function("total",
			cel.MemberOverload("total_items",
				[]*cel.Type{cel.ListType(cel.ObjectType("test.pkg.Thing"))}, cel.IntType)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	ix := celtindex.Build(env, fdset)
	cands := ix.FnCands("items")
	if !slicesContainsName(cands, "total") {
		t.Errorf("FnCands(items) missing total, got %v", candNames(cands))
	}
}

func slicesContainsName(cands []celtindex.Candidate, name string) bool {
	for _, c := range cands {
		if c.Name == name {
			return true
		}
	}
	return false
}

func candNames(cands []celtindex.Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Name
	}
	return out
}
