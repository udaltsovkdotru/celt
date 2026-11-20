//nolint:goconst // test tables intentionally repeat strings
package celtlint

import (
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	celtypes "cel.dev/cel-go/common/types"
	"cel.dev/cel-go/parser"

	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
)

// Test string constants reused across table rows so golangci goconst stays
// quiet about repeated literals.
const (
	hvFooBar     = "foo.bar"
	hvFooCall    = "foo(bar)"
	hvAlphaBeta  = "alpha\nbeta"
	hvFoo        = "foo"
	hvBar        = "bar"
	hvMemberFn   = "hvMember"
	hvMacroFn    = "hv_macro"
	hvMacroName  = "hv_m"
	hvMacroDoc   = "hvDoc"
	hvMacroNoDoc = "hvNoDoc"
	hvVarName    = "hvName"
)

// hvNoopMacro expands a hv_macro call to an unchanged hv_macro call so the
// macro is registered without rewriting the source, mirroring how the config
// package registers signature-only macros.
var hvNoopMacro = func(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
	if len(args) == 0 {
		return eh.NewCall(hvMacroFn), nil
	}
	return eh.NewCall(hvMacroFn, args...), nil
}

// hvTestEnv builds an env containing one variable, two functions (one global
// with a description, one member with two list-receiver overloads) and one
// macro, so hover has real data to render.
func hvTestEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable(hvVarName, cel.StringType),
		cel.Function("hvFn",
			cel.Overload("hvFn_int", []*cel.Type{cel.IntType}, cel.IntType),
			cel.Overload("hvFn_string", []*cel.Type{cel.StringType}, cel.BoolType),
			cel.FunctionDocs("a test function"),
		),
		cel.Function(hvMemberFn,
			cel.MemberOverload("hvMember_list_int", []*cel.Type{cel.ListType(cel.IntType), cel.IntType}, cel.IntType),
			cel.MemberOverload("hvMember_list_string", []*cel.Type{cel.ListType(cel.StringType), cel.StringType}, cel.IntType),
		),
		cel.Macros(cel.GlobalVarArgMacro(hvMacroFn, hvNoopMacro)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// hvFileDescriptorSet builds a minimal proto source with an enum (with a
// value) and a message (with a scalar field and a repeated field) plus
// source-code locations that carry doc comments, so enum/field hover has data.
func hvFileDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name: proto.String("hv.proto"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String("Color"),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String("RED"), Number: proto.Int32(0)},
							{Name: proto.String("BLUE"), Number: proto.Int32(1)},
						},
					},
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Person"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:   proto.String("name"),
								Number: proto.Int32(1),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
							{
								Name:   proto.String("tags"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
						},
					},
				},
				SourceCodeInfo: &descriptorpb.SourceCodeInfo{
					Location: []*descriptorpb.SourceCodeInfo_Location{
						{Path: []int32{5, 0}, LeadingComments: proto.String("the color enum\nsecond line")},
						{Path: []int32{5, 0, 2, 0}, LeadingComments: proto.String("red value")},
						{Path: []int32{4, 0, 2, 0}, LeadingComments: proto.String("person name field")},
					},
				},
			},
		},
	}
}

// TestLoadHoverDocs verifies that loading docs from a real env and fdset
// populates the function, macro, variable, enum, enum-value and field maps.
func TestLoadHoverDocs(t *testing.T) {
	docs := LoadHoverDocs(hvFileDescriptorSet(t), hvTestEnv(t))
	if _, ok := docs.functions["hvFn"]; !ok {
		t.Error("functions missing hvFn")
	}
	if _, ok := docs.functions[hvMemberFn]; !ok {
		t.Error("functions missing hvMember")
	}
	if _, ok := docs.macros[hvMacroFn]; !ok {
		t.Error("macros missing hv_macro")
	}
	v, ok := docs.variables[hvVarName]
	if !ok {
		t.Fatal("variables missing hvName")
	}
	if v.sig != "var hvName string" {
		t.Errorf("variable sig = %q, want %q", v.sig, "var hvName string")
	}
	if _, ok := docs.enums["Color"]; !ok {
		t.Error("enums missing Color")
	}
	if _, ok := docs.enumValues["RED"]; !ok {
		t.Error("enumValues missing RED")
	}
	if _, ok := docs.fields["name"]; !ok {
		t.Error("fields missing name")
	}
}

// hvNestedEnumFileDescriptorSet builds a descriptor with an enum nested inside
// a message, plus source locations so hover has doc comments.
func hvNestedEnumFileDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name: proto.String("hv_nested.proto"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Box"),
						EnumType: []*descriptorpb.EnumDescriptorProto{
							{
								Name: proto.String("State"),
								Value: []*descriptorpb.EnumValueDescriptorProto{
									{Name: proto.String("FULL"), Number: proto.Int32(1)},
								},
							},
						},
						NestedType: []*descriptorpb.DescriptorProto{
							{
								Name: proto.String("Item"),
								Field: []*descriptorpb.FieldDescriptorProto{
									{
										Name:   proto.String("count"),
										Number: proto.Int32(1),
										Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
										Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
									},
								},
							},
						},
					},
				},
				SourceCodeInfo: &descriptorpb.SourceCodeInfo{
					Location: []*descriptorpb.SourceCodeInfo_Location{
						// message_type[0].enum_type[0] = [4,0,4,0], its
						// value[0] = [4,0,4,0,2,0], and
						// message_type[0].nested_type[0].field[0] = [4,0,3,0,2,0].
						{Path: []int32{4, 0, 4, 0}, LeadingComments: proto.String("the state enum")},
						{Path: []int32{4, 0, 4, 0, 2, 0}, LeadingComments: proto.String("full state")},
						{Path: []int32{4, 0, 3, 0, 2, 0}, LeadingComments: proto.String("item count field")},
					},
				},
			},
		},
	}
}

// TestLoadHoverDocsNestedEnum verifies an enum nested inside a message, its
// values, and a field of a nested message are all indexed for hover, like
// top-level ones.
func TestLoadHoverDocsNestedEnum(t *testing.T) {
	fdset := hvNestedEnumFileDescriptorSet(t)
	enums, values := enumDocs(fdset)
	if got, ok := enums["State"]; !ok {
		t.Fatal("enums missing nested State")
	} else if got.desc != "the state enum" {
		t.Errorf("State desc = %q, want %q", got.desc, "the state enum")
	}
	if got, ok := values["FULL"]; !ok {
		t.Fatal("enumValues missing nested FULL")
	} else if got.desc != "full state" {
		t.Errorf("FULL desc = %q, want %q", got.desc, "full state")
	}
	if got, ok := fieldDocs(fdset)["count"]; !ok {
		t.Fatal("fields missing nested-message field count")
	} else if got.desc != "item count field" {
		t.Errorf("count desc = %q, want %q", got.desc, "item count field")
	}
}

// TestHoverDocsLookup verifies Lookup returns the right hover kind and that
// member-function overloads are filtered by receiver element type, with a
// fallback to all overloads when no overload matches the receiver.
func TestHoverDocsLookup(t *testing.T) {
	docs := LoadHoverDocs(hvFileDescriptorSet(t), hvTestEnv(t))
	tests := []struct {
		name         string
		key          string
		receiverElem string
		want         string
	}{
		{
			name: "global function with doc",
			key:  "hvFn",
			want: "```go\nfunc hvFn(int) int\nfunc hvFn(string) bool\n```\n\na test function",
		},
		{
			name:         "member filtered int",
			key:          hvMemberFn,
			receiverElem: "int",
			want:         "```go\nfunc ([]int) hvMember(int) int\n```",
		},
		{
			name:         "member filtered string",
			key:          hvMemberFn,
			receiverElem: "string",
			want:         "```go\nfunc ([]string) hvMember(string) int\n```",
		},
		{
			name:         "member no receiver keeps all",
			key:          hvMemberFn,
			receiverElem: "",
			want:         "```go\nfunc ([]int) hvMember(int) int\nfunc ([]string) hvMember(string) int\n```",
		},
		{
			name:         "member unknown receiver falls back",
			key:          hvMemberFn,
			receiverElem: "unknown",
			want:         "```go\nfunc ([]int) hvMember(int) int\nfunc ([]string) hvMember(string) int\n```",
		},
		{
			name: "variable",
			key:  hvVarName,
			want: "```go\nvar hvName string\n```",
		},
		{
			name: "macro without doc",
			key:  hvMacroFn,
			want: "macro `hv_macro`",
		},
		{
			name: "enum value",
			key:  "RED",
			want: "```go\ntype Color int\n\nconst RED Color = 0\n```\n\nred value",
		},
		{
			name: "enum",
			key:  "Color",
			want: "```go\ntype Color int\n\nconst (\n    RED  Color = 0\n    BLUE Color = 1\n)\n```\n\nthe color enum",
		},
		{
			name: "scalar field with doc",
			key:  "name",
			want: "```go\ntype Person struct {\n    name string\n    // ...\n}\n```\n\nperson name field",
		},
		{
			name: "repeated field",
			key:  "tags",
			want: "```go\ntype Person struct {\n    tags []string\n    // ...\n}\n```",
		},
		{
			name: "unknown key",
			key:  "doesNotExist",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := docs.Lookup(tt.key, tt.receiverElem); got != tt.want {
				t.Errorf("Lookup(%q,%q) =\n%q\nwant\n%q", tt.key, tt.receiverElem, got, tt.want)
			}
		})
	}
}

// TestLookupMacroDescription covers the macro branch with and without a
// description, which a real env macro does not expose.
func TestLookupMacroDescription(t *testing.T) {
	docs := &HoverDocs{
		macros: map[string]string{
			hvMacroDoc:   "expands to something",
			hvMacroNoDoc: "",
		},
	}
	if got := docs.Lookup(hvMacroDoc, ""); got != "macro `hvDoc`\n\nexpands to something" {
		t.Errorf("Lookup macro with desc = %q", got)
	}
	if got := docs.Lookup(hvMacroNoDoc, ""); got != "macro `hvNoDoc`" {
		t.Errorf("Lookup macro without desc = %q", got)
	}
}

// TestRenderMacroCallHover verifies the macro-invocation hover renders the
// result type, description and expansion, omitting dyn results and absent
// fields.
func TestRenderMacroCallHover(t *testing.T) {
	tests := []struct {
		name string
		mc   *MacroCall
		desc string
		want string
	}{
		{
			name: "full",
			mc:   &MacroCall{Name: hvMacroName, ResultType: "int", Expansion: "a ? b : c"},
			desc: "doc",
			want: "macro `hv_m` → int\n\ndoc\n\nexpands to:\n```cel\na ? b : c\n```",
		},
		{
			name: "bare",
			mc:   &MacroCall{Name: hvMacroName},
			want: "macro `hv_m`",
		},
		{
			name: "dyn result omitted",
			mc:   &MacroCall{Name: hvMacroName, ResultType: dynTypeName, Expansion: "x"},
			want: "macro `hv_m`\n\nexpands to:\n```cel\nx\n```",
		},
		{
			name: "no expansion",
			mc:   &MacroCall{Name: hvMacroName, ResultType: "bool"},
			desc: "doc",
			want: "macro `hv_m` → bool\n\ndoc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderMacroCallHover(tt.mc, tt.desc); got != tt.want {
				t.Errorf("renderMacroCallHover() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestCelTypeName verifies how various CEL types render as hover text.
func TestCelTypeName(t *testing.T) {
	tests := []struct {
		name string
		t    *celtypes.Type
		want string
	}{
		{name: "nil", t: nil, want: dynTypeName},
		{name: "int primitive", t: celtypes.IntType, want: "int"},
		{name: "bool primitive", t: celtypes.BoolType, want: "bool"},
		{name: "string primitive", t: celtypes.StringType, want: "string"},
		{name: "list of int", t: celtypes.NewListType(celtypes.IntType), want: "[]int"},
		{name: "list with nil elem", t: celtypes.NewListType(nil), want: "[]dyn"},
		{name: "list of type param", t: celtypes.NewListType(celtypes.NewTypeParamType("T")), want: "[]T"},
		{name: "map string to int", t: celtypes.NewMapType(celtypes.StringType, celtypes.IntType), want: "map[string]int"},
		{name: "dyn", t: celtypes.DynType, want: dynTypeName},
		{name: "any", t: celtypes.AnyType, want: "google.protobuf.Any"},
		{name: "type param", t: celtypes.NewTypeParamType("T"), want: "T"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := celTypeName(tt.t); got != tt.want {
				t.Errorf("celTypeName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIdentifierAt verifies byte-range behavior across line and char positions:
// identifiers spanning start/middle/end, non-identifiers, multi-line content
// and out-of-range positions.
func TestIdentifierAt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		line    int
		char    int
		want    string
		wantOK  bool
	}{
		{name: "single line start", content: hvFooBar, line: 0, char: 0, want: hvFoo, wantOK: true},
		{name: "single line mid", content: hvFooBar, line: 0, char: 1, want: hvFoo, wantOK: true},
		{name: "single line end of ident", content: hvFooBar, line: 0, char: 2, want: hvFoo, wantOK: true},
		{name: "dot is not ident", content: hvFooBar, line: 0, char: 3, wantOK: false},
		{name: "second ident", content: hvFooBar, line: 0, char: 4, want: hvBar, wantOK: true},
		{name: "second ident end", content: hvFooBar, line: 0, char: 6, want: hvBar, wantOK: true},
		{name: "past line end", content: hvFooBar, line: 0, char: 7, wantOK: false},
		{name: "digits are part of ident", content: "x1y2", line: 0, char: 2, want: "x1y2", wantOK: true},
		{name: "underscore start", content: "_private", line: 0, char: 0, want: "_private", wantOK: true},
		{name: "space is not ident", content: "a + b", line: 0, char: 1, wantOK: false},
		{name: "paren is not ident", content: hvFooCall, line: 0, char: 3, wantOK: false},
		{name: "ident after paren", content: hvFooCall, line: 0, char: 4, want: hvBar, wantOK: true},
		{name: "multi line first", content: hvAlphaBeta, line: 0, char: 3, want: "alpha", wantOK: true},
		{name: "multi line second", content: hvAlphaBeta, line: 1, char: 1, want: "beta", wantOK: true},
		{name: "line out of range", content: hvAlphaBeta, line: 2, char: 0, wantOK: false},
		{name: "char at line end", content: hvAlphaBeta, line: 0, char: 5, wantOK: false},
		{name: "char past line end", content: hvAlphaBeta, line: 0, char: 9, wantOK: false},
		{name: "empty first line", content: "\n", line: 0, char: 0, wantOK: false},
		{name: "empty middle line", content: "alpha\n\nbeta", line: 1, char: 0, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := IdentifierAt(tt.content, tt.line, tt.char)
			if ok != tt.wantOK {
				t.Fatalf("IdentifierAt(%q,%d,%d) ok = %v, want %v", tt.content, tt.line, tt.char, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("IdentifierAt(%q,%d,%d) = %q, want %q", tt.content, tt.line, tt.char, got, tt.want)
			}
		})
	}
}
