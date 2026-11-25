// Package celtindex is the public name/member/type index of a CEL environment.
// It records the identifiers the environment makes visible (variables,
// functions, macros, enum values and fields, message members and their
// presence kinds) and answers structural queries used by lint rules and the
// LSP server (completions, hover). The index is a pure data structure: it has
// no rules and no fuzzy/spell logic (that lives in pkg/celtspell).
package celtindex

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	celref "cel.dev/cel-go/common/types/ref"
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
	// ambiguousEnumShort records short enum names that were seen under more
	// than one fully qualified name (e.g. a.Status and b.Status). Such a short
	// name never maps to a single set of members, even if later enums make it
	// look otherwise, so it must not be offered as an unqualified receiver.
	ambiguousEnumShort map[string]bool
	objFields          map[string][]Candidate
	typeMembers        map[string][]Candidate
	fieldTypes         map[string]map[string]string
	varTypes           map[string]string
	listElem           map[string]string
	memberFns          map[string][]string
	receiverMacros     []string
	strFields          map[string]bool
	macroKeywords      []string
	optionalFields     map[string]map[string]PresenceKind

	enumFields map[string]map[string]string
	enumValues map[string]map[int64]string
	enumShort  map[string]string
}

// Build indexes an environment. Native types are resolved via the env's type
// provider, and descriptor-backed proto types via fdset (which may be nil).
func Build(env *cel.Env, fdset *descriptorpb.FileDescriptorSet) *Index {
	ix := &Index{
		valid:              map[string]bool{},
		enumValueCands:     map[string][]Candidate{},
		ambiguousEnumShort: map[string]bool{},
		objFields:          map[string][]Candidate{},
		typeMembers:        map[string][]Candidate{},
		fieldTypes:         map[string]map[string]string{},
		varTypes:           map[string]string{},
		listElem:           map[string]string{},
		memberFns:          map[string][]string{},
		strFields:          map[string]bool{},
		optionalFields:     map[string]map[string]PresenceKind{},
		enumFields:         map[string]map[string]string{},
		enumValues:         map[string]map[int64]string{},
		enumShort:          map[string]string{},
	}

	ix.indexFunctions(env)
	// The `cel` namespace is reserved by cel-go for standard macros such as
	// `cel.bind` (ext.Bindings) and `cel.@inline`. It is a namespace qualifier,
	// not a free identifier, so it is indexed as a known name.
	ix.addValid("cel")
	for _, m := range env.Macros() {
		ix.add(m.Function(), KindMacro)
		ix.macroKeywords = append(ix.macroKeywords, m.Function())
		if m.IsReceiverStyle() {
			ix.receiverMacros = append(ix.receiverMacros, m.Function())
		}
	}
	for _, v := range env.Variables() {
		ix.add(v.Name(), KindVariable)
		if t := v.Type(); t != nil && t.Kind() == celtypes.StructKind {
			ix.varTypes[v.Name()] = t.TypeName()
		}
	}
	for _, c := range celConstants {
		ix.add(c.Name, c.Kind)
	}

	typeFields, objRefs, fieldTypes := ix.indexDescriptor(fdset)
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
		if fields, ok := typeFields[t.TypeName()]; ok {
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
	// Index members by full type name so a dotted receiver path can be
	// resolved to the correct type's fields.
	for typeName, fields := range typeFields {
		ix.typeMembers[typeName] = fields
	}
	ix.fieldTypes = fieldTypes
	return ix
}

// indexDescriptor walks the FileDescriptorSet, indexing top-level enums and
// messages, and returns the accumulated type-field, object-reference and
// field-type maps used to resolve member access.
func (ix *Index) indexDescriptor(fdset *descriptorpb.FileDescriptorSet) (typeFields map[string][]Candidate,
	objRefs map[string]string, fieldTypes map[string]map[string]string) {
	typeFields = map[string][]Candidate{}
	objRefs = map[string]string{}
	fieldTypes = map[string]map[string]string{}
	for _, f := range fdset.GetFile() {
		pkg := f.GetPackage()
		// Every `.`-separated prefix of a fully-qualified name is a namespace
		// qualifier (e.g. `com` and `com.example` in `com.example.Widget`), so
		// a rule may reference them as the receiver of a member access without
		// the misspell rule treating them as unknown free identifiers.
		ix.indexNamespaces(pkg)
		isProto3 := f.GetSyntax() == "proto3"
		for _, e := range f.GetEnumType() {
			full := strings.TrimPrefix(fmt.Sprintf("%s.%s", pkg, e.GetName()), ".")
			ix.indexNamespaces(full)
			vals := make(map[int64]string, len(e.GetValue()))
			for _, v := range e.GetValue() {
				vals[int64(v.GetNumber())] = v.GetName()
			}
			ix.enumValues[full] = vals
			ix.enumShort[full] = e.GetName()
			ix.addEnum(full, e)
		}
		for _, m := range f.GetMessageType() {
			ix.indexMessage(pkg, "", m, typeFields, objRefs, fieldTypes, isProto3)
		}
	}
	return typeFields, objRefs, fieldTypes
}

// indexNamespaces registers every proper `.`-separated prefix of name as a
// namespace qualifier. These are the leading segments of a dotted qualified
// name (a package or enclosing type), which are referenced as receivers but
// resolve through the type system rather than as top-level identifiers.
func (ix *Index) indexNamespaces(name string) {
	for i := range len(name) {
		if name[i] == '.' && i > 0 {
			ix.addValid(name[:i])
		}
	}
}

func (ix *Index) indexMessage(pkg, prefix string, m *descriptorpb.DescriptorProto,
	typeFields map[string][]Candidate, objRefs map[string]string,
	fieldTypes map[string]map[string]string, isProto3 bool) {
	full := strings.TrimPrefix(fmt.Sprintf("%s.%s%s", pkg, prefix, m.GetName()), ".")
	ix.indexNamespaces(full)

	var enumFields map[string]string
	var fields []Candidate
	for _, fd := range m.GetField() {
		name := fd.GetName()
		if enumType := ix.indexField(full, name, fd, isProto3, objRefs, fieldTypes); enumType != "" {
			if enumFields == nil {
				enumFields = map[string]string{}
			}
			enumFields[name] = enumType
		}
		fields = append(fields, Candidate{Name: name, Kind: KindField})
	}
	if len(enumFields) > 0 {
		ix.enumFields[full] = enumFields
	}
	if len(fields) > 0 {
		typeFields[full] = fields
	}
	ix.indexNestedEnums(full, m)
	for _, nested := range m.GetNestedType() {
		ix.indexMessage(pkg, prefix+m.GetName()+".", nested, typeFields, objRefs, fieldTypes, isProto3)
	}
}

// indexField indexes a single message field: its presence kind, member name,
// and (for enum, message and string fields) the type it references. It returns
// the fully-qualified enum type name for an enum field, or "" otherwise.
func (ix *Index) indexField(full, name string, fd *descriptorpb.FieldDescriptorProto,
	isProto3 bool, objRefs map[string]string, fieldTypes map[string]map[string]string) string {
	if fd.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		switch {
		case isProto3:
			ix.addPresence(full, name, PresenceProto3Value)
			if fd.GetProto3Optional() {
				ix.addPresence(full, name, PresenceProto3Optional)
			}
		case fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL:
			ix.addPresence(full, name, PresenceProto2Existence)
		}
	}
	ix.add(name, KindField)
	switch {
	case fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
		// A repeated field is a list; a repeated enum is a list of enum
		// values, not a scalar enum, so it must be classified as such
		// BEFORE the scalar enum case below (otherwise `p.states == []` is
		// mistaken for a scalar-enum comparison).
		ix.listElem[name] = FieldElemType(fd)
	case fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_ENUM:
		return strings.TrimPrefix(fd.GetTypeName(), ".")
	case fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE:
		child := strings.TrimPrefix(fd.GetTypeName(), ".")
		if fieldTypes[full] == nil {
			fieldTypes[full] = map[string]string{}
		}
		fieldTypes[full][name] = child
		objRefs[name] = child
	case fd.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING:
		ix.strFields[name] = true
	}
	return ""
}

// indexNestedEnums registers the enums nested inside a message under the
// message's fully-qualified name.
func (ix *Index) indexNestedEnums(full string, m *descriptorpb.DescriptorProto) {
	for _, e := range m.GetEnumType() {
		enumFull := full + "." + e.GetName()
		vals := make(map[int64]string, len(e.GetValue()))
		for _, v := range e.GetValue() {
			vals[int64(v.GetNumber())] = v.GetName()
		}
		ix.enumValues[enumFull] = vals
		ix.enumShort[enumFull] = e.GetName()
		ix.addEnum(enumFull, e)
	}
}

func (ix *Index) addEnum(fullName string, e *descriptorpb.EnumDescriptorProto) {
	ix.add(e.GetName(), KindEnum)
	var valueCands []Candidate
	for _, v := range e.GetValue() {
		ix.add(v.GetName(), KindValue)
		valueCands = append(valueCands, Candidate{Name: v.GetName(), Kind: KindValue})
	}
	// Key members by the fully qualified name so same-named enums in distinct
	// packages (e.g. a.Status and b.Status) each resolve their own values.
	ix.enumValueCands[fullName] = valueCands
	// A bare (unqualified) receiver still completes when the short name is
	// unambiguous; once two distinct enums share a short name, that name no
	// longer identifies a single set of members, so callers must use the
	// qualified name. The ambiguity is remembered rather than inferred from the
	// absence of an alias, so a third enum with the same short name cannot make
	// the name look unambiguous again.
	short := e.GetName()
	if ix.ambiguousEnumShort[short] {
		return
	}
	if prev, ok := ix.enumValueCands[short]; !ok {
		ix.enumValueCands[short] = valueCands
	} else if !slices.EqualFunc(prev, valueCands, func(a, b Candidate) bool { return a.Name == b.Name && a.Kind == b.Kind }) {
		delete(ix.enumValueCands, short)
		ix.ambiguousEnumShort[short] = true
	}
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
	ix.resolveNativeFields(receiverName, typeName, typeFields, provider, map[string]bool{}, map[string]reflect.Type{})
}

func (ix *Index) resolveNativeFields(receiverName, typeName string,
	typeFields map[string][]Candidate, provider celtypes.Provider, visited map[string]bool, typeCache map[string]reflect.Type) {
	fieldNames, ok := provider.FindStructFieldNames(typeName)
	if !ok {
		return
	}
	// Reuse the type's member list for every receiver of that type (so
	// `r.One.Name` and `r.Two.Name` with the same child type both offer Name),
	// while resolving a given type's members only once to avoid infinite
	// cycles. Members are stored under the type identity too, so typed
	// receivers like `(account)` and `[account][0]` resolve them.
	if fields, seen := ix.typeMembers[typeName]; seen {
		ix.objFields[receiverName] = fields
		return
	}
	if visited[typeName] {
		return
	}
	visited[typeName] = true

	structType, cached := typeCache[typeName]
	if !cached {
		structType = nativeStructType(provider, typeName)
		typeCache[typeName] = structType
	}
	celKinds := nativeFieldKinds(provider, typeName, structType)

	fields := make([]Candidate, 0, len(fieldNames))
	for _, fname := range fieldNames {
		fields = append(fields, Candidate{Name: fname, Kind: KindField})
		ft, ok := provider.FindStructFieldType(typeName, fname)
		if !ok || ft == nil {
			continue
		}
		if isNilableKind(celKinds[fname]) {
			ix.addPresence(typeName, fname, PresenceGoNilable)
		} else if structType == nil && ft.IsSet != nil {
			// Fallback when the Go type can't be recovered: honor the provider's
			// presence callback.
			ix.addPresence(typeName, fname, PresenceGoNilable)
		}
		if ft.Type != nil && ft.Type.Kind() == celtypes.StructKind {
			nested := ft.Type.TypeName()
			if _, isProto := typeFields[nested]; !isProto {
				ix.resolveNativeFields(fname, nested, typeFields, provider, visited, typeCache)
			}
		}
	}
	ix.typeMembers[typeName] = fields
	// The CEL output type of a compiled receiver is the type's short name (e.g.
	// "Account"), while the variable path resolves to the full alias; register
	// both so a typed receiver like `(account)` or `[account][0]` finds the
	// members too.
	if short := shortTypeName(typeName); short != typeName {
		ix.typeMembers[short] = fields
	}
	ix.objFields[receiverName] = fields
}

// shortTypeName returns the final dot-separated component of a type name.
func shortTypeName(typeName string) string {
	if i := strings.LastIndexByte(typeName, '.'); i >= 0 {
		return typeName[i+1:]
	}
	return typeName
}

// nativeStructType recovers the reflect.Type of a registered native struct from
// the type provider by constructing a zero value, or nil when it can't.
func nativeStructType(provider celtypes.Provider, typeName string) reflect.Type {
	v := provider.NewValue(typeName, map[string]celref.Val{})
	rt := reflect.TypeOf(v.Value())
	if rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt == nil || rt.Kind() != reflect.Struct {
		return nil
	}
	return rt
}

// nativeFieldKinds maps each CEL-visible field name (as the type provider
// exposes it) to its Go kind, so presence can be derived from real nilability
// (pointer, map, slice, interface, chan, func) rather than the provider's
// presence callback. The provider may rename fields (e.g. a `cel`/`json`
// struct tag, or a custom field-name handler), so the field each CEL name
// selects is recovered from the provider's own field getter rather than
// guessed from the Go field names — a name-set comparison cannot distinguish a
// permutation of two fields.
func nativeFieldKinds(provider celtypes.Provider, typeName string, structType reflect.Type) map[string]reflect.Kind {
	out := map[string]reflect.Kind{}
	if structType == nil {
		return out
	}
	names, ok := provider.FindStructFieldNames(typeName)
	if !ok {
		return out
	}
	for _, name := range names {
		ft, ok := provider.FindStructFieldType(typeName, name)
		if !ok || ft == nil || ft.GetFrom == nil {
			continue
		}
		if kind := getterFieldKind(ft.GetFrom, structType); kind != reflect.Invalid {
			out[name] = kind
		}
	}
	return out
}

// getterFieldKind determines which Go field a provider field getter reads and
// returns that field's Go kind for nilability. Each top-level field is probed
// with a non-zero value and the one that changes the getter's result is the
// field it reads. For a value-embedded struct that field is the embedding
// field, so the probe descends to the leaf subfield the getter actually reads
// (e.g. a `*string` promoted through an embedded value struct) rather than
// reporting the embedding field's Struct kind.
func getterFieldKind(getter func(any) (any, error), structType reflect.Type) reflect.Kind {
	for i := range structType.NumField() {
		zero := reflect.New(structType)
		zeroVal, zeroErr := getter(zero.Interface())
		probe := reflect.New(structType)
		f := probe.Elem().Field(i)
		// An unexported (private) field cannot be probed: attempting to set it
		// panics. Skip it; the provider does not expose private fields anyway.
		if !f.CanSet() {
			continue
		}
		setProbe(f)
		probeVal, probeErr := getter(probe.Interface())
		if !reflect.DeepEqual(zeroVal, probeVal) || !errors.Is(probeErr, zeroErr) {
			return fieldLeafKind(getter, structType, i)
		}
	}
	return reflect.Invalid
}

// fieldLeafKind returns the nilability kind of the field the getter reads,
// given that it reads top-level field i. A pointer or scalar field's own kind
// is authoritative (a pointer is nilable). A value-embedded struct field is
// descended into: the getter reads one promoted subfield, so each subfield is
// probed in isolation and the first whose fill changes the getter is the leaf.
func fieldLeafKind(getter func(any) (any, error), structType reflect.Type, i int) reflect.Kind {
	ftype := structType.Field(i).Type
	if ftype.Kind() != reflect.Struct {
		return ftype.Kind()
	}
	for j := range ftype.NumField() {
		zero := reflect.New(structType)
		zeroVal, zeroErr := getter(zero.Interface())
		probe := reflect.New(structType)
		sub := probe.Elem().Field(i)
		if !sub.CanSet() {
			continue
		}
		inner := reflect.New(ftype).Elem()
		innerField := inner.Field(j)
		if !innerField.CanSet() {
			continue
		}
		setProbe(innerField)
		sub.Set(inner)
		probeVal, probeErr := getter(probe.Interface())
		if !reflect.DeepEqual(zeroVal, probeVal) || !errors.Is(probeErr, zeroErr) {
			return innerField.Type().Kind()
		}
	}
	return reflect.Invalid
}

// setProbe writes a non-zero, detectable value into a settable field and
// returns the nilable Go kind of the leaf field it filled (or reflect.Invalid
// when nothing nilable was set). Container/pointer fields are made non-nil and
// filled with a distinct value (not just a non-nil-but-empty container, which a
// dereferencing getter cannot tell from a nil one). Structs are descended into
// so a promoted field reached through an embedded struct or pointer (e.g.
// Root embedding *Embedded with Embedded.Nick *string) becomes non-nil and
// detectable; array fields stay zero and are skipped (they are not nilable, so
// omitting them loses nothing). active tracks struct types being expanded so a
// recursive native type (e.g. `Node{ Next *Node }`) is probed shallowly rather
// than expanded forever.
func setProbe(f reflect.Value) {
	setProbeActive(f, map[reflect.Type]bool{})
}

func setProbeActive(f reflect.Value, active map[reflect.Type]bool) reflect.Kind {
	switch f.Kind() {
	case reflect.Pointer:
		return probePointerKind(f, active)
	case reflect.Struct:
		return probeStructKind(f, active)
	case reflect.Slice:
		s := reflect.MakeSlice(f.Type(), 1, 1)
		setProbeActive(s.Index(0), active)
		f.Set(s)
		return reflect.Slice
	case reflect.Map:
		m := reflect.MakeMap(f.Type())
		k := reflect.New(f.Type().Key()).Elem()
		v := reflect.New(f.Type().Elem()).Elem()
		setProbeActive(k, active)
		setProbeActive(v, active)
		m.SetMapIndex(k, v)
		f.Set(m)
		return reflect.Map
	case reflect.Chan:
		f.Set(reflect.MakeChan(f.Type(), 1))
		return reflect.Chan
	case reflect.Func:
		f.Set(reflect.MakeFunc(f.Type(), func(_ []reflect.Value) []reflect.Value { return nil }))
		return reflect.Func
	case reflect.Interface:
		if f.NumMethod() == 0 {
			f.Set(reflect.ValueOf("probe"))
		}
		return reflect.Interface
	case reflect.String:
		f.SetString("probe")
	case reflect.Bool:
		f.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		f.SetUint(1)
	case reflect.Float32, reflect.Float64:
		f.SetFloat(1)
	default:
		return reflect.Invalid
	}
	return reflect.Invalid
}

// probePointerKind fills a pointer field with a fresh value and descends into
// its element, returning the leaf field's nilable kind (or the pointer kind
// when no nilable subfield was reached).
func probePointerKind(f reflect.Value, active map[reflect.Type]bool) reflect.Kind {
	p := reflect.New(f.Type().Elem())
	sub := setProbeActive(p.Elem(), active)
	f.Set(p)
	if sub != reflect.Invalid {
		return sub
	}
	return reflect.Pointer
}

// probeStructKind fills a struct field's nilable subfields (through embedded
// structs/pointers) so a promoted field's getter returns a non-nil value after
// probing the embedding field, letting the probe attribute it to the right
// field. The returned kind is the leaf field's kind, not the embedding field's.
func probeStructKind(f reflect.Value, active map[reflect.Type]bool) reflect.Kind {
	t := f.Type()
	if active[t] {
		// Already expanding this type higher up the probe: a recursive
		// reference (e.g. `Node.Next *Node`). Stop descending; the pointer
		// that led here is still set non-nil by the Pointer case, which is all
		// a nilability probe needs.
		return reflect.Invalid
	}
	active[t] = true
	var leaf reflect.Kind
	for i := range f.NumField() {
		sf := f.Field(i)
		if !sf.CanSet() {
			continue
		}
		if k := setProbeActive(sf, active); k != reflect.Invalid && leaf == reflect.Invalid {
			leaf = k
		}
	}
	delete(active, t)
	return leaf
}

func isNilableKind(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Chan, reflect.Func:
		return true
	}
	return false
}

// FieldPresence returns the presence kinds recorded for a field of the given
// message type (by full type name).
func (ix *Index) FieldPresence(typeName, field string) PresenceKind {
	return ix.optionalFields[typeName][field]
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
		descriptorpb.FieldDescriptorProto_TYPE_SINT64:
		return "int"
	case descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64:
		// Unsigned proto scalars map to CEL's uint type, so repeated fields of
		// them complete list<uint> member overloads rather than list<int>.
		return "uint"
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

// addValid marks name as a known identifier without offering it as a misspell
// or completion candidate. It is used for namespace qualifiers (e.g. `cel` and
// proto package prefixes): they resolve as receivers of member access but are
// not things a typo would be renamed to, so they must not crowd the misspell
// suggestions.
func (ix *Index) addValid(name string) {
	if name == "" || !IsUserIdent(name) {
		return
	}
	ix.valid[name] = true
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

// TypeMembers returns the member candidates of a message type, keyed by its
// full type name (e.g. "Person" or "collision.First"), or nil when the type is
// not indexed.
func (ix *Index) TypeMembers(typeName string) []Candidate {
	if fields, ok := ix.typeMembers[typeName]; ok {
		return fields
	}
	return nil
}

// MemberCandidatesPath resolves the members of a possibly dotted receiver
// path (e.g. "a.child") from the base variable's type, so that two receivers
// that happen to share a bare field name (a.child vs b.child) resolve to their
// own types' fields rather than colliding. It falls back to MemberCandidates
// for bare receivers.
func (ix *Index) MemberCandidatesPath(receiver string) (MemberKind, []Candidate) {
	if kind, cands := ix.MemberCandidates(receiver); kind != MemberNone {
		return kind, cands
	}
	parts := strings.Split(receiver, ".")
	if len(parts) < 2 {
		return MemberNone, nil
	}
	cur, ok := ix.varTypes[parts[0]]
	if !ok {
		// Not a variable-typed chain (e.g. a repeated/string field receiver);
		// fall back to the last segment.
		return ix.MemberCandidates(parts[len(parts)-1])
	}
	for _, field := range parts[1:] {
		child, ok := ix.fieldTypes[cur][field]
		if !ok {
			// The base is a known typed receiver and this field is not a nested
			// message, so it is a scalar/list leaf with no members. Do not
			// borrow object fields from a same-named field of an unrelated type
			// (e.g. `a.child` string must not offer `b.child`'s `name`).
			return MemberNone, nil
		}
		cur = child
	}
	if fields, ok := ix.typeMembers[cur]; ok {
		return MemberObjField, fields
	}
	return ix.MemberCandidates(parts[len(parts)-1])
}

// ReceiverTypeName resolves a possibly dotted receiver path (e.g. "a.child")
// to the full type name of its last element, by walking the field-type index
// from the base variable's type. It returns "" when the path can't be resolved.
func (ix *Index) ReceiverTypeName(receiver string) string {
	parts := strings.Split(receiver, ".")
	cur, ok := ix.varTypes[parts[0]]
	if !ok {
		return ""
	}
	for _, field := range parts[1:] {
		child, ok := ix.fieldTypes[cur][field]
		if !ok {
			return ""
		}
		cur = child
	}
	return cur
}

// ChainTypeName resolves baseType followed by the dotted field chain to the
// final type name by walking the field-type index, so a receiver rooted at a
// locally bound variable (which is not in the global variable index) can be
// typed the same way ReceiverTypeName types one rooted at a global. It returns
// "" when any step of the chain is missing.
func (ix *Index) ChainTypeName(baseType string, chain []string) string {
	cur := baseType
	for _, field := range chain {
		child, ok := ix.fieldTypes[cur][field]
		if !ok {
			return ""
		}
		cur = child
	}
	return cur
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

// MemberFunction reports whether receiver declares a member function named
// name (e.g. whether `w.rename` is a call to a declared member function rather
// than an unknown field of `w`). It resolves the receiver's type and looks up
// its member functions, not just its fields.
func (ix *Index) MemberFunction(receiver, name string) bool {
	if t := ix.ReceiverTypeName(receiver); t != "" {
		return slices.Contains(ix.memberFns[t], name)
	}
	return false
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
