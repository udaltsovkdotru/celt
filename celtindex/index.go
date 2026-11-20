// Package celtindex is the public name/member/type index of a CEL environment.
// It records the identifiers the environment makes visible (variables,
// functions, macros, enum values and fields, message members and their
// presence kinds) and answers structural queries used by lint rules and the
// LSP server (completions, hover). The index is a pure data structure: it has
// no rules and no fuzzy/spell logic (that lives in pkg/celtspell).
package celtindex

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
)

// Kind classifies an index candidate (the role a name plays in the env).
type Kind string

const (
	KindField    Kind = "field"
	KindFunction Kind = "function"
	KindValue    Kind = "value"
	KindMacro    Kind = "macro"
	KindVariable Kind = "variable"
	KindEnum     Kind = "enum"
)

// Candidate is a name the environment makes visible, together with the role it
// plays. The misspell rule and the LSP completion engine both consume the
// candidate set.
type Candidate struct {
	Name string
	Kind Kind
}

// MemberKind identifies what kind of members an identifier has, driving member
// completion and misspell suggestions.
type MemberKind int

const (
	MemberNone MemberKind = iota
	MemberEnumValue
	MemberObjField
	MemberFunction
)

// PresenceKind identifies why a field may be absent (and thus benefits from a
// has() guard). Multiple kinds can apply to one field; the optional-fields lint
// rule configures which kinds require a guard.
type PresenceKind uint8

const (
	// PresenceProto3Value marks every singular proto3 field: in proto3 any
	// field may be unset, and `optional` is only an explicit-presence marker.
	PresenceProto3Value PresenceKind = 1 << iota
	// PresenceProto3Optional marks fields declared with the proto3 `optional`
	// keyword.
	PresenceProto3Optional
	// PresenceProto2Existence marks proto2 optional fields, which carry
	// explicit presence (existence) tracking.
	PresenceProto2Existence
	// PresenceGoNilable marks fields of a native Go type whose kind is nilable
	// (pointer, map, slice, interface, chan, func), i.e. has() is available.
	PresenceGoNilable
)

var celConstants = []Candidate{
	{Name: "true", Kind: KindValue},
	{Name: "false", Kind: KindValue},
	{Name: "null", Kind: KindValue},
}

// Index is the structural index of an environment: what identifiers exist and
// how they relate (members, enum values, presence). It is built once per
// environment and shared by the analyzer and the LSP server.
type Index struct {
	valid          map[string]bool
	candidates     []Candidate
	enumValueCands map[string][]Candidate
	objFields      map[string][]Candidate
	listElem       map[string]string
	memberFns      map[string][]string
	receiverMacros []string
	strFields      map[string]bool
	macroKeywords  []string
	optionalFields map[string]map[string]PresenceKind

	enumFields map[string]map[string]string
	enumValues map[string]map[int64]string
	enumShort  map[string]string
}

// Build indexes an environment. Native types are resolved via the env's type
// provider, and descriptor-backed proto types via fdset (which may be nil).
//
//nolint:gocyclo // walks the descriptor tree and env; splitting obscures the build order
func Build(env *cel.Env, fdset *descriptorpb.FileDescriptorSet) *Index {
	ix := &Index{
		valid:          map[string]bool{},
		enumValueCands: map[string][]Candidate{},
		objFields:      map[string][]Candidate{},
		listElem:       map[string]string{},
		memberFns:      map[string][]string{},
		strFields:      map[string]bool{},
		optionalFields: map[string]map[string]PresenceKind{},
		enumFields:     map[string]map[string]string{},
		enumValues:     map[string]map[int64]string{},
		enumShort:      map[string]string{},
	}

	ix.indexFunctions(env)
	for _, m := range env.Macros() {
		ix.add(m.Function(), KindMacro)
		ix.macroKeywords = append(ix.macroKeywords, m.Function())
		if m.IsReceiverStyle() {
			ix.receiverMacros = append(ix.receiverMacros, m.Function())
		}
	}
	for _, v := range env.Variables() {
		ix.add(v.Name(), KindVariable)
	}
	for _, c := range celConstants {
		ix.add(c.Name, c.Kind)
	}

	typeFields := map[string][]Candidate{}
	objRefs := map[string]string{}
	for _, f := range fdset.GetFile() {
		pkg := f.GetPackage()
		isProto3 := f.GetSyntax() == "proto3"
		for _, e := range f.GetEnumType() {
			full := fmt.Sprintf("%s.%s", pkg, e.GetName())
			vals := make(map[int64]string, len(e.GetValue()))
			for _, v := range e.GetValue() {
				vals[int64(v.GetNumber())] = v.GetName()
			}
			ix.enumValues[full] = vals
			ix.enumShort[full] = e.GetName()
			ix.addEnum(e)
		}
		for _, m := range f.GetMessageType() {
			ix.indexMessage(pkg, "", m, typeFields, objRefs, isProto3)
		}
	}
	// Any top-level variable typed as a message gets its fields resolved so
	// that member access and has() analysis work for every object-typed
	// variable declared in the environment, not just one fixed variable.
	// Proto messages are keyed off the descriptor; native Go structs are
	// resolved through the env's type provider.
	provider := env.CELTypeProvider()
	for _, v := range env.Variables() {
		t := v.Type()
		if t == nil || t.Kind() != celtypes.StructKind {
			continue
		}
		name := ShortTypeName(t.TypeName())
		if fields, ok := typeFields[name]; ok {
			ix.objFields[v.Name()] = fields
		} else if provider != nil {
			ix.resolveNative(v.Name(), t.TypeName(), typeFields, provider)
		}
	}
	for name, typeName := range objRefs {
		if fields, ok := typeFields[typeName]; ok {
			ix.objFields[name] = fields
		}
	}
	return ix
}

//nolint:gocyclo // recursive descriptor walker handling several field kinds
func (ix *Index) indexMessage(pkg, prefix string, m *descriptorpb.DescriptorProto,
	typeFields map[string][]Candidate, objRefs map[string]string, isProto3 bool) {
	full := fmt.Sprintf("%s.%s%s", pkg, prefix, m.GetName())

	var enumFields map[string]string
	var fields []Candidate
	for _, fd := range m.GetField() {
		name := fd.GetName()
		if fd.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			switch {
			case isProto3:
				ix.addPresence(m.GetName(), name, PresenceProto3Value)
				if fd.GetProto3Optional() {
					ix.addPresence(m.GetName(), name, PresenceProto3Optional)
				}
			case fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL:
				ix.addPresence(m.GetName(), name, PresenceProto2Existence)
			}
		}
		ix.add(name, KindField)
		fields = append(fields, Candidate{Name: name, Kind: KindField})
		switch {
		case fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_ENUM:
			if enumFields == nil {
				enumFields = map[string]string{}
			}
			enumFields[name] = strings.TrimPrefix(fd.GetTypeName(), ".")
		case fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
			ix.listElem[name] = FieldElemType(fd)
		case fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE:
			objRefs[name] = ShortTypeName(fd.GetTypeName())
		case fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING:
			ix.strFields[name] = true
		}
	}
	if len(enumFields) > 0 {
		ix.enumFields[full] = enumFields
	}
	if len(fields) > 0 {
		typeFields[m.GetName()] = fields
	}
	for _, e := range m.GetEnumType() {
		enumFull := full + "." + e.GetName()
		vals := make(map[int64]string, len(e.GetValue()))
		for _, v := range e.GetValue() {
			vals[int64(v.GetNumber())] = v.GetName()
		}
		ix.enumValues[enumFull] = vals
		ix.enumShort[enumFull] = e.GetName()
		ix.addEnum(e)
	}
	for _, nested := range m.GetNestedType() {
		ix.indexMessage(pkg, prefix+m.GetName()+".", nested, typeFields, objRefs, isProto3)
	}
}

func (ix *Index) addEnum(e *descriptorpb.EnumDescriptorProto) {
	ix.add(e.GetName(), KindEnum)
	var valueCands []Candidate
	for _, v := range e.GetValue() {
		ix.add(v.GetName(), KindValue)
		valueCands = append(valueCands, Candidate{Name: v.GetName(), Kind: KindValue})
	}
	ix.enumValueCands[e.GetName()] = valueCands
}

// addPresence ORs a presence kind onto a field of a message type (by short
// name).
func (ix *Index) addPresence(typeName, field string, k PresenceKind) {
	if ix.optionalFields[typeName] == nil {
		ix.optionalFields[typeName] = map[string]PresenceKind{}
	}
	ix.optionalFields[typeName][field] |= k
}

// resolveNative indexes the fields, presence kinds and nested message types of
// a native Go struct reachable from the env's type provider, mirroring what
// indexMessage does for descriptor-backed proto messages.
func (ix *Index) resolveNative(receiverName, typeName string,
	typeFields map[string][]Candidate, provider celtypes.Provider) {
	ix.resolveNativeFields(receiverName, typeName, typeFields, provider, map[string]bool{})
}

func (ix *Index) resolveNativeFields(receiverName, typeName string,
	typeFields map[string][]Candidate, provider celtypes.Provider, visited map[string]bool) {
	short := ShortTypeName(typeName)
	if visited[short] {
		return
	}
	visited[short] = true

	fieldNames, ok := provider.FindStructFieldNames(typeName)
	if !ok {
		return
	}
	fields := make([]Candidate, 0, len(fieldNames))
	for _, fname := range fieldNames {
		fields = append(fields, Candidate{Name: fname, Kind: KindField})
		ft, ok := provider.FindStructFieldType(typeName, fname)
		if !ok || ft == nil {
			continue
		}
		if ft.IsSet != nil {
			ix.addPresence(short, fname, PresenceGoNilable)
		}
		if ft.Type != nil && ft.Type.Kind() == celtypes.StructKind {
			nested := ft.Type.TypeName()
			if _, isProto := typeFields[ShortTypeName(nested)]; !isProto {
				ix.resolveNativeFields(fname, nested, typeFields, provider, visited)
			}
		}
	}
	ix.objFields[receiverName] = fields
}

// FieldPresence returns the presence kinds recorded for a field of the given
// message type (by full type name).
func (ix *Index) FieldPresence(typeName, field string) PresenceKind {
	return ix.optionalFields[ShortTypeName(typeName)][field]
}

// ShortTypeName returns the final path component of a type name
// (e.g. "pkg.Account" -> "Account").
func ShortTypeName(t string) string {
	t = strings.TrimPrefix(t, ".")
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		return t[i+1:]
	}
	return t
}

// FieldElemType returns the element type name of a repeated descriptor field.
func FieldElemType(fd *descriptorpb.FieldDescriptorProto) string {
	switch fd.GetType() {
	case descriptorpb.FieldDescriptorProto_TYPE_MESSAGE,
		descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		return ShortTypeName(fd.GetTypeName())
	case descriptorpb.FieldDescriptorProto_TYPE_STRING:
		return typeString
	case descriptorpb.FieldDescriptorProto_TYPE_BOOL:
		return "bool"
	case descriptorpb.FieldDescriptorProto_TYPE_BYTES:
		return "bytes"
	case descriptorpb.FieldDescriptorProto_TYPE_DOUBLE,
		descriptorpb.FieldDescriptorProto_TYPE_FLOAT:
		return "double"
	case descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64,
		descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64:
		return "int"
	default:
		return strings.ToLower(strings.TrimPrefix(fd.GetType().String(), "TYPE_"))
	}
}

const typeString = "string"

func (ix *Index) add(name string, kind Kind) {
	if name == "" || !IsUserIdent(name) {
		return
	}
	if ix.valid[name] {
		return
	}
	ix.valid[name] = true
	ix.candidates = append(ix.candidates, Candidate{Name: name, Kind: kind})
}

// indexFunctions records every declared function and, for member functions
// whose receiver is a list, maps its element type to the function name. This
// drives member completion without hardcoding any function vocabulary.
func (ix *Index) indexFunctions(env *cel.Env) {
	for name, fd := range env.Functions() {
		ix.add(name, KindFunction)
		for _, o := range fd.OverloadDecls() {
			if !o.IsMemberFunction() {
				continue
			}
			args := o.ArgTypes()
			if len(args) == 0 {
				continue
			}
			ix.addMemberFn(args[0], name)
		}
	}
}

// addMemberFn records a member function under its receiver type. Non-list
// receivers are keyed by type name. List receivers are keyed by element type
// ("list[<elem>]") when the element is concrete, or by the generic "list" key
// when the overload applies to any list (element is a type parameter or dyn).
func (ix *Index) addMemberFn(recv *celtypes.Type, name string) {
	if recv == nil {
		return
	}
	key := ""
	if recv.Kind() == celtypes.ListKind {
		p := recv.Parameters()
		if len(p) == 0 || isGenericElem(p[0]) {
			key = "list"
		} else {
			// Key concrete element receivers by the short type name, matching
			// the element key FnCands derives from FieldElemType for repeated
			// message/enum fields (e.g. "list[Widget.Child]").
			key = "list[" + ShortTypeName(celTypeName(p[0])) + "]"
		}
	} else {
		key = recv.TypeName()
	}
	if key == "" || slicesContains(ix.memberFns[key], name) {
		return
	}
	ix.memberFns[key] = append(ix.memberFns[key], name)
}

func slicesContains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func isGenericElem(t *celtypes.Type) bool {
	return t == nil || t.Kind() == celtypes.DynKind || t.Kind() == celtypes.TypeParamKind
}

func celTypeName(t *celtypes.Type) string {
	return t.TypeName()
}

// Candidates returns the top-level candidate set (variables, functions,
// macros, enum types and values).
func (ix *Index) Candidates() []Candidate { return ix.candidates }

// Valid reports whether name is a known top-level identifier.
func (ix *Index) Valid(name string) bool { return ix.valid[name] }

// MemberCandidates returns the members of an identifier, if it is a known
// receiver (enum type, object field chain, or function-bearing type).
func (ix *Index) MemberCandidates(receiver string) (MemberKind, []Candidate) {
	if variants, ok := ix.enumValueCands[receiver]; ok {
		return MemberEnumValue, variants
	}
	if fields, ok := ix.objFields[receiver]; ok {
		return MemberObjField, fields
	}
	if fn := ix.FnCands(receiver); len(fn) > 0 {
		return MemberFunction, fn
	}
	return MemberNone, nil
}

// ReceiverElem returns the element type of a repeated-field or string-field
// receiver, if any, used to scope member-function completions.
func (ix *Index) ReceiverElem(receiver string) string {
	if elem, ok := ix.listElem[receiver]; ok {
		return elem
	}
	if ix.strFields[receiver] {
		return typeString
	}
	return ""
}

func (ix *Index) memberFnCands(key string, isList bool) []Candidate {
	var names []string
	if isList {
		names = append(names, ix.memberFns["list"]...)
		names = append(names, ix.memberFns[key]...)
		names = append(names, ix.receiverMacros...)
	} else {
		names = append(names, ix.memberFns[key]...)
	}
	return functionCands(names)
}

func functionCands(names ...[]string) []Candidate {
	var out []Candidate
	for _, ns := range names {
		for _, name := range ns {
			out = append(out, Candidate{Name: name, Kind: KindFunction})
		}
	}
	return out
}

// FnCands returns the member functions applicable to a receiver.
func (ix *Index) FnCands(receiver string) []Candidate {
	if elem, ok := ix.listElem[receiver]; ok {
		return ix.memberFnCands("list["+elem+"]", true)
	}
	if ix.strFields[receiver] {
		return ix.memberFnCands(typeString, false)
	}
	return nil
}

// HasEnum reports whether receiver is a known enum type.
func (ix *Index) HasEnum(receiver string) bool {
	_, ok := ix.enumValueCands[receiver]
	return ok
}

// EnumValues returns the number->name map for an enum type (by full name).
func (ix *Index) EnumValues(enumFull string) map[int64]string { return ix.enumValues[enumFull] }

// EnumShort returns the short name of an enum type (by full name).
func (ix *Index) EnumShort(enumFull string) string { return ix.enumShort[enumFull] }

// EnumFields returns the field->enum map for a message type (by full name).
func (ix *Index) EnumFields(typeName string) map[string]string { return ix.enumFields[typeName] }

// EnumValueCands returns the candidate values of an enum type (by short name).
func (ix *Index) EnumValueCands(enumShort string) []Candidate { return ix.enumValueCands[enumShort] }

// MacroKeywords returns the function names of all registered macros, used to
// detect macro typos in compile-error dedup.
func (ix *Index) MacroKeywords() []string { return ix.macroKeywords }

// IsUserIdent reports whether name starts with a letter (a user-facing
// identifier rather than a keyword or builtin).
func IsUserIdent(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
