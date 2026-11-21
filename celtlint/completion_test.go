package celtlint

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
)

// String constants reused across the completion env, descriptor builder and
// table rows so golangci goconst stays quiet about repeated literals.
const (
	cpUserName   = "userName"
	cpItemsVar   = "items"
	cpObjVar     = "w"
	cpGlobalFn   = "processData"
	cpMemberFn   = "joinAll"
	cpNoArgFn    = "listSize"
	cpStrFn      = "weirdStr"
	cpMacroName  = "matches_any"
	cpEnumName   = "Color"
	cpEnumRed    = "RED"
	cpEnumGreen  = "GREEN"
	cpFieldID    = "id"
	cpFieldColor = "color"
	cpFieldName  = "name"
	cpFieldTags  = "tags"
	cpConstTrue  = "true"
)

// cpNoopMacro expands a matches_any call to an unchanged matches_any call so
// the macro is registered without rewriting source, mirroring how the config
// package registers signature-only macros.
var cpNoopMacro = func(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
	if len(args) == 0 {
		return eh.NewCall(cpMacroName), nil
	}
	return eh.NewCall(cpMacroName, args...), nil
}

// cpCompletionEnv builds an env with a spread of names across variables,
// global functions, member functions (with and without arguments) and a macro
// so completion has real top-level and member candidates to surface.
func cpCompletionEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable(cpUserName, cel.StringType),
		cel.Variable(cpItemsVar, cel.ListType(cel.StringType)),
		cel.Variable(cpObjVar, cel.ObjectType("test.pkg.Widget")),
		cel.Function(cpGlobalFn,
			cel.Overload("processData_str", []*cel.Type{cel.StringType}, cel.BoolType)),
		cel.Function(cpMemberFn,
			cel.MemberOverload("joinAll_list", []*cel.Type{cel.ListType(cel.StringType), cel.StringType}, cel.StringType)),
		cel.Function(cpNoArgFn,
			cel.MemberOverload("listSize_list", []*cel.Type{cel.ListType(cel.StringType)}, cel.IntType)),
		cel.Function(cpStrFn,
			cel.MemberOverload("weirdStr_str", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType)),
		cel.Macros(cel.GlobalVarArgMacro(cpMacroName, cpNoopMacro)),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// cpTestFDSet returns a descriptor set describing one enum (Color: RED/GREEN)
// and one message (Widget with an int, an enum, a string and a repeated-string
// field) so member completion has fields and enum values to resolve.
func cpTestFDSet() *descriptorpb.FileDescriptorSet {
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	rep := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("cp.proto"),
				Package: proto.String("test.pkg"),
				Syntax:  proto.String("proto3"),
				EnumType: []*descriptorpb.EnumDescriptorProto{
					{
						Name: proto.String(cpEnumName),
						Value: []*descriptorpb.EnumValueDescriptorProto{
							{Name: proto.String(cpEnumRed), Number: proto.Int32(0)},
							{Name: proto.String(cpEnumGreen), Number: proto.Int32(1)},
						},
					},
				},
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Widget"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String(cpFieldID), Number: proto.Int32(1), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
							{Name: proto.String(cpFieldColor), Number: proto.Int32(2), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(), TypeName: proto.String(".test.pkg.Color")},
							{Name: proto.String(cpFieldName), Number: proto.Int32(3), Label: opt, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: proto.String(cpFieldTags), Number: proto.Int32(4), Label: rep, Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
						},
					},
				},
				SourceCodeInfo: &descriptorpb.SourceCodeInfo{
					Location: []*descriptorpb.SourceCodeInfo_Location{
						{Path: []int32{4, 0, 2, 2}, LeadingComments: proto.String("person name field")},
					},
				},
			},
		},
	}
}

// cpAnalyzer builds an Analyzer over the completion env and descriptor set.
func cpAnalyzer(t *testing.T) *Analyzer {
	t.Helper()
	an, err := New(cpCompletionEnv(t), WithDescriptor(cpTestFDSet()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return an
}

// cpWant is a label/kind pair asserted against a set of completion items.
type cpWant struct {
	label string
	kind  string
}

// cpFindItem returns the completion item with the given label, if present.
func cpFindItem(items []CompletionItem, label string) (CompletionItem, bool) {
	for i := range items {
		if items[i].Label == label {
			return items[i], true
		}
	}
	return CompletionItem{}, false
}

func TestCompletionSnippetMatchesReceiverOverload(t *testing.T) {
	fdset := cpTestFDSet()
	file := fdset.File[0]
	file.SourceCodeInfo = nil
	widget := file.MessageType[0]
	widget.Field = append(widget.Field, &descriptorpb.FieldDescriptorProto{
		Name: proto.String("numbers"), Number: proto.Int32(5),
		Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
	})
	env, err := cel.NewEnv(cel.TypeDescs(fdset), cel.Variable("w", cel.ObjectType("test.pkg.Widget")),
		cel.Function("pick",
			cel.MemberOverload("pick_strings", []*cel.Type{cel.ListType(cel.StringType)}, cel.StringType),
			cel.MemberOverload("pick_numbers", []*cel.Type{cel.ListType(cel.IntType), cel.IntType}, cel.IntType),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(env, WithDescriptor(fdset))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		receiver string
		snippet  bool
	}{{receiver: "w.tags"}, {receiver: "w.numbers", snippet: true}} {
		src := tc.receiver + ".pi"
		items := a.Completion(src, 0, len(src))
		item, ok := cpFindItem(items, "pick")
		if !ok || (item.InsertTextFormat == InsertTextFormatSnippet) != tc.snippet {
			t.Errorf("completion for %s: %+v, want argument snippet=%v", tc.receiver, items, tc.snippet)
		}
	}
}

// TestCompletionTopLevel verifies top-level completion surfaces variables,
// macros, enum types and enum values that share the typed prefix, and that a
// too-short prefix or an empty line yields no completion.
func TestCompletionTopLevel(t *testing.T) {
	an := cpAnalyzer(t)

	tests := []struct {
		name    string
		content string
		line    int
		char    int
		want    []cpWant
	}{
		{name: "variable prefix", content: "us", line: 0, char: 2, want: []cpWant{{cpUserName, CompletionKindVariable}}},
		{name: "macro prefix", content: "ma", line: 0, char: 2, want: []cpWant{{cpMacroName, CompletionKindMacro}}},
		{name: "enum type prefix", content: "Col", line: 0, char: 3, want: []cpWant{{cpEnumName, CompletionKindEnum}}},
		{name: "enum value prefix", content: "GR", line: 0, char: 2, want: []cpWant{{cpEnumGreen, CompletionKindValue}}},
		{name: "constant prefix", content: "tr", line: 0, char: 2, want: []cpWant{{cpConstTrue, CompletionKindValue}}},
		{name: "middle of prefix", content: "user", line: 0, char: 3, want: []cpWant{{cpUserName, CompletionKindVariable}}},
		{name: "start of prefix", content: cpUserName, line: 0, char: 2, want: []cpWant{{cpUserName, CompletionKindVariable}}},
		{name: "end of prefix", content: cpUserName, line: 0, char: 8, want: []cpWant{{cpUserName, CompletionKindVariable}}},
		{name: "single char prefix", content: "u", line: 0, char: 1, want: nil},
		{name: "empty line", content: "", line: 0, char: 0, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items := an.Completion(tc.content, tc.line, tc.char)
			if len(tc.want) == 0 {
				if len(items) != 0 {
					t.Fatalf("Completion(%q,%d,%d) = %d items, want none: %v", tc.content, tc.line, tc.char, len(items), items)
				}
				return
			}
			for _, w := range tc.want {
				it, ok := cpFindItem(items, w.label)
				if !ok {
					t.Errorf("Completion(%q) missing %q", tc.content, w.label)
					continue
				}
				if it.Kind != w.kind {
					t.Errorf("Completion(%q) %q kind = %q, want %q", tc.content, w.label, it.Kind, w.kind)
				}
			}
		})
	}
}

// TestCompletionTopLevelExcludesFunctions verifies global functions are not
// offered as top-level completion items; only variables, macros, enums and
// values are.
func TestCompletionTopLevelExcludesFunctions(t *testing.T) {
	an := cpAnalyzer(t)

	items := an.Completion("pro", 0, 3)
	if len(items) != 0 {
		t.Errorf("Completion(pro) = %v, want no items (functions excluded at top level)", items)
	}
	if _, ok := cpFindItem(items, cpGlobalFn); ok {
		t.Errorf("Completion(pro) unexpectedly includes %q", cpGlobalFn)
	}
}

// TestCompletionMemberFields verifies member completion after a dot returns
// the object's descriptor fields, filtered by prefix.
func TestCompletionMemberFields(t *testing.T) {
	an := cpAnalyzer(t)

	tests := []struct {
		name    string
		content string
		line    int
		char    int
		want    []cpWant
	}{
		{name: "all fields", content: "w.", line: 0, char: 2, want: []cpWant{
			{cpFieldID, CompletionKindField},
			{cpFieldColor, CompletionKindField},
			{cpFieldName, CompletionKindField},
			{cpFieldTags, CompletionKindField},
		}},
		{name: "field prefix", content: "w.na", line: 0, char: 4, want: []cpWant{{cpFieldName, CompletionKindField}}},
		{name: "field exact", content: "w.id", line: 0, char: 4, want: []cpWant{{cpFieldID, CompletionKindField}}},
		{name: "no field match", content: "w.zz", line: 0, char: 4, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items := an.Completion(tc.content, tc.line, tc.char)
			if len(items) != len(tc.want) {
				t.Fatalf("Completion(%q) = %d items, want %d: %v", tc.content, len(items), len(tc.want), items)
			}
			for _, w := range tc.want {
				it, ok := cpFindItem(items, w.label)
				if !ok {
					t.Errorf("Completion(%q) missing %q", tc.content, w.label)
					continue
				}
				if it.Kind != CompletionKindField {
					t.Errorf("Completion(%q) %q kind = %q, want field", tc.content, w.label, it.Kind)
				}
			}
		})
	}
}

// TestCompletionMemberFieldDocumentation verifies a member field completion
// carries non-empty hover documentation describing the field.
func TestCompletionMemberFieldDocumentation(t *testing.T) {
	an := cpAnalyzer(t)

	items := an.Completion("w.name", 0, 6)
	it, ok := cpFindItem(items, cpFieldName)
	if !ok {
		t.Fatalf("Completion(w.name) missing name: %v", items)
	}
	if it.Documentation == "" {
		t.Error("field completion has empty documentation")
	}
	if !strings.Contains(it.Documentation, cpFieldName) {
		t.Errorf("field documentation = %q, want it to mention %q", it.Documentation, cpFieldName)
	}
}

// TestCompletionMemberEnum verifies member completion on an enum type name
// returns the enum's values.
func TestCompletionMemberEnum(t *testing.T) {
	an := cpAnalyzer(t)

	tests := []struct {
		name    string
		content string
		line    int
		char    int
		want    []cpWant
	}{
		{name: "all values", content: "Color.", line: 0, char: 6, want: []cpWant{
			{cpEnumRed, CompletionKindValue},
			{cpEnumGreen, CompletionKindValue},
		}},
		{name: "value prefix", content: "Color.R", line: 0, char: 7, want: []cpWant{{cpEnumRed, CompletionKindValue}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items := an.Completion(tc.content, tc.line, tc.char)
			if len(items) != len(tc.want) {
				t.Fatalf("Completion(%q) = %d items, want %d: %v", tc.content, len(items), len(tc.want), items)
			}
			for _, w := range tc.want {
				it, ok := cpFindItem(items, w.label)
				if !ok {
					t.Errorf("Completion(%q) missing %q", tc.content, w.label)
					continue
				}
				if it.Kind != CompletionKindValue {
					t.Errorf("Completion(%q) %q kind = %q, want value", tc.content, w.label, it.Kind)
				}
			}
		})
	}
}

// TestCompletionMemberFunction verifies member completion on list and string
// receivers returns member functions, rendering argument-taking functions as
// snippets and argument-less functions as plain call text.
func TestCompletionMemberFunction(t *testing.T) {
	an := cpAnalyzer(t)

	tests := []struct {
		name       string
		content    string
		line       int
		char       int
		wantLabel  string
		wantInsert string
		wantFormat int
	}{
		{name: "member with args", content: "w.tags.jo", line: 0, char: 9, wantLabel: cpMemberFn, wantInsert: "joinAll($0)", wantFormat: InsertTextFormatSnippet},
		{name: "string member with args", content: "w.name.we", line: 0, char: 9, wantLabel: cpStrFn, wantInsert: "weirdStr($0)", wantFormat: InsertTextFormatSnippet},
		{name: "member without args", content: "w.tags.lis", line: 0, char: 9, wantLabel: cpNoArgFn, wantInsert: "listSize()", wantFormat: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items := an.Completion(tc.content, tc.line, tc.char)
			it, ok := cpFindItem(items, tc.wantLabel)
			if !ok {
				t.Fatalf("Completion(%q) missing %q: %v", tc.content, tc.wantLabel, items)
			}
			if it.Kind != CompletionKindFunction {
				t.Errorf("Completion(%q) %q kind = %q, want function", tc.content, tc.wantLabel, it.Kind)
			}
			if it.InsertText != tc.wantInsert {
				t.Errorf("Completion(%q) %q InsertText = %q, want %q", tc.content, tc.wantLabel, it.InsertText, tc.wantInsert)
			}
			if it.InsertTextFormat != tc.wantFormat {
				t.Errorf("Completion(%q) %q InsertTextFormat = %d, want %d", tc.content, tc.wantLabel, it.InsertTextFormat, tc.wantFormat)
			}
			if it.Documentation == "" {
				t.Errorf("Completion(%q) %q has empty documentation", tc.content, tc.wantLabel)
			}
		})
	}
}

// TestCompletionMemberNoCandidates verifies member completion yields nothing
// for an unknown receiver and for a receiver whose type has no member mapping
// (a plain string variable is not indexed for member functions).
func TestCompletionMemberNoCandidates(t *testing.T) {
	an := cpAnalyzer(t)

	if items := an.Completion("xyz.", 0, 4); len(items) != 0 {
		t.Errorf("Completion(xyz.) = %v, want none for unknown receiver", items)
	}
	if items := an.Completion(cpUserName+".", 0, len(cpUserName)+1); len(items) != 0 {
		t.Errorf("Completion(userName.) = %v, want none (string variable not member-indexed)", items)
	}
}
