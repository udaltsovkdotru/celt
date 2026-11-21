package celtindex_test

import (
	"reflect"
	"testing"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
)

func TestNativePresenceOnlyMarksNilableFields(t *testing.T) {
	const name = "native.Account"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		Name     string  `cel:"name"`
		Nickname *string `cel:"nickname"`
	}](), celtypes.NativeTypeAlias(name), celtypes.ParseStructTag("cel"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("account", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	ix := celtindex.Build(env, nil)
	if got := ix.FieldPresence(name, "name"); got&celtindex.PresenceGoNilable != 0 {
		t.Errorf("non-nilable string field marked nilable: %v", got)
	}
	if got := ix.FieldPresence(name, "nickname"); got&celtindex.PresenceGoNilable == 0 {
		t.Errorf("nilable pointer field not marked nilable: %v", got)
	}
}

func TestNativePresenceUsesUntaggedFieldNames(t *testing.T) {
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
	if _, issues := env.Compile(`account.Name == account.Nickname`); issues.Err() != nil {
		t.Fatalf("fixture must use the provider's field names: %v", issues.Err())
	}
	ix := celtindex.Build(env, nil)
	if got := ix.FieldPresence(name, "Name"); got&celtindex.PresenceGoNilable != 0 {
		t.Errorf("non-nilable Name field marked nilable: %v", got)
	}
	if got := ix.FieldPresence(name, "Nickname"); got&celtindex.PresenceGoNilable == 0 {
		t.Errorf("nilable Nickname field not marked nilable: %v", got)
	}
}

func TestNativePresenceUsesProviderFieldNames(t *testing.T) {
	const name = "native.Account"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		Name     string  `json:"label"`
		Nickname *string `json:"alias,omitempty"`
	}](), celtypes.NativeTypeAlias(name), celtypes.ParseStructTag("json"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("account", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	if _, issues := env.Compile(`account.label == account.alias`); issues.Err() != nil {
		t.Fatalf("fixture must use the provider's field names: %v", issues.Err())
	}
	ix := celtindex.Build(env, nil)
	if got := ix.FieldPresence(name, "label"); got&celtindex.PresenceGoNilable != 0 {
		t.Errorf("non-nilable label field marked nilable: %v", got)
	}
	if got := ix.FieldPresence(name, "alias"); got&celtindex.PresenceGoNilable == 0 {
		t.Errorf("nilable alias field not marked nilable: %v", got)
	}
}

func TestNativePresenceDistinguishesSwappedProviderNames(t *testing.T) {
	const name = "native.Account"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		First  string  `cel:"Second"`
		Second *string `cel:"First"`
	}](), celtypes.NativeTypeAlias(name), celtypes.ParseStructTag("cel"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("account", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	ix := celtindex.Build(env, nil)
	for _, tc := range []struct {
		field string
		want  bool
	}{
		{field: "First", want: true},
		{field: "Second", want: false},
	} {
		if got := ix.FieldPresence(name, tc.field)&celtindex.PresenceGoNilable != 0; got != tc.want {
			t.Errorf("CEL field %s nilable = %v, want %v", tc.field, got, tc.want)
		}
	}
}

func TestNativePresenceSkipsUnexportedFields(t *testing.T) {
	const name = "native.Account"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct {
		secret   string
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
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("building the index for a valid Go type panicked: %v", r)
		}
	}()
	ix := celtindex.Build(env, nil)
	if ix.FieldPresence(name, "Nickname")&celtindex.PresenceGoNilable == 0 {
		t.Error("exported pointer field should still be marked nilable")
	}
}

func TestNativePresenceRecognizesPromotedPointerFields(t *testing.T) {
	type Embedded struct{ Nick *string }
	const name = "native.Root"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct{ *Embedded }](), celtypes.NativeTypeAlias(name))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("r", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	if _, issues := env.Compile(`r.Nick == "x"`); issues.Err() != nil {
		t.Fatalf("CEL must expose the promoted field: %v", issues.Err())
	}
	ix := celtindex.Build(env, nil)
	if got := ix.FieldPresence(name, "Nick"); got&celtindex.PresenceGoNilable == 0 {
		t.Errorf("promoted pointer field should be marked nilable, got %v", got)
	}
}

func TestNativePresenceFindsPointerPromotedThroughValueEmbedding(t *testing.T) {
	type Embedded struct {
		Nick *string
		Age  int64
	}
	const name = "native.Root"
	typ, err := celtypes.NewNativeType(reflect.TypeFor[struct{ Embedded }](), celtypes.NativeTypeAlias(name))
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(cel.Types(typ), cel.Variable("root", cel.ObjectType(name)))
	if err != nil {
		t.Fatal(err)
	}
	if _, issues := env.Compile(`root.Nick == "x" && root.Age == 1`); issues.Err() != nil {
		t.Fatalf("invalid promoted field fixture: %v", issues.Err())
	}
	ix := celtindex.Build(env, nil)
	if got := ix.FieldPresence(name, "Nick"); got&celtindex.PresenceGoNilable == 0 {
		t.Errorf("promoted pointer Nick is missing nilable presence: %v", got)
	}
	if got := ix.FieldPresence(name, "Age"); got&celtindex.PresenceGoNilable != 0 {
		t.Errorf("promoted scalar Age should not be marked nilable: %v", got)
	}
}

func TestRepeatedUnsignedProtoFieldsUseUint(t *testing.T) {
	for _, typ := range []descriptorpb.FieldDescriptorProto_Type{
		descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
	} {
		field := &descriptorpb.FieldDescriptorProto{Type: typ.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()}
		if got := celtindex.FieldElemType(field); got != "uint" {
			t.Errorf("element type for %s = %q, want uint", typ, got)
		}
	}
}

func TestEnumMemberCandidatesUseQualifiedTypeNames(t *testing.T) {
	fdset := &descriptorpb.FileDescriptorSet{}
	for _, tc := range []struct{ pkg, member string }{{pkg: "a", member: "ALPHA"}, {pkg: "b", member: "BETA"}, {pkg: "c", member: "GAMMA"}} {
		fdset.File = append(fdset.File, &descriptorpb.FileDescriptorProto{
			Name: proto.String(tc.pkg + ".proto"), Package: proto.String(tc.pkg), Syntax: proto.String("proto3"),
			EnumType: []*descriptorpb.EnumDescriptorProto{{
				Name: proto.String("Status"), Value: []*descriptorpb.EnumValueDescriptorProto{{Name: proto.String(tc.member), Number: proto.Int32(0)}},
			}},
		})
	}
	env, err := cel.NewEnv(cel.TypeDescs(fdset))
	if err != nil {
		t.Fatal(err)
	}
	ix := celtindex.Build(env, fdset)
	if kind, members := ix.MemberCandidatesPath("Status"); kind == celtindex.MemberEnumValue {
		t.Errorf("unqualified Status is ambiguous and should not resolve to %v", members)
	}
	for _, tc := range []struct{ receiver, member string }{{receiver: "a.Status", member: "ALPHA"}, {receiver: "b.Status", member: "BETA"}, {receiver: "c.Status", member: "GAMMA"}} {
		if _, issues := env.Compile(tc.receiver + "." + tc.member); issues.Err() != nil {
			t.Fatalf("enum fixture must compile: %v", issues.Err())
		}
		kind, members := ix.MemberCandidatesPath(tc.receiver)
		if kind != celtindex.MemberEnumValue || len(members) != 1 || members[0].Name != tc.member {
			t.Errorf("candidates for %s = %v %v, want enum member %s", tc.receiver, kind, members, tc.member)
		}
	}
}

func TestReceiverPathDoesNotBorrowFieldsFromUnrelatedReceiver(t *testing.T) {
	str := func(name string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()}
	}
	fdset := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: proto.String("receivers.proto"), Package: proto.String("example"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("A"), Field: []*descriptorpb.FieldDescriptorProto{str("child")}},
			{Name: proto.String("B"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("child"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".example.C"),
			}}},
			{Name: proto.String("C"), Field: []*descriptorpb.FieldDescriptorProto{str("name")}},
		},
	}}}
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("a", cel.ObjectType("example.A")), cel.Variable("b", cel.ObjectType("example.B")))
	if err != nil {
		t.Fatal(err)
	}
	ix := celtindex.Build(env, fdset)
	kind, members := ix.MemberCandidatesPath("b.child")
	if kind != celtindex.MemberObjField || len(members) != 1 || members[0].Name != "name" {
		t.Fatalf("object receiver control should expose name, got %v %v", kind, members)
	}
	if kind, members := ix.MemberCandidatesPath("a.child"); kind == celtindex.MemberObjField {
		t.Errorf("string receiver got object fields from b.child: %+v", members)
	}
}

func TestPackageLessProto3OptionalPresence(t *testing.T) {
	fdset := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: proto.String("no_package.proto"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:      proto.String("Person"),
			OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_nickname")}},
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("nickname"), Number: proto.Int32(1),
				Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Proto3Optional: proto.Bool(true), OneofIndex: proto.Int32(0),
			}},
		}},
	}}}
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("person", cel.ObjectType("Person")))
	if err != nil {
		t.Fatal(err)
	}
	ix := celtindex.Build(env, fdset)
	want := celtindex.PresenceProto3Optional | celtindex.PresenceProto3Value
	if got := ix.FieldPresence("Person", "nickname"); got&want != want {
		t.Errorf("Person.nickname presence = %v, want flags %v", got, want)
	}
}
