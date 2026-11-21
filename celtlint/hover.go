package celtlint

import (
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/decls"
	celtypes "cel.dev/cel-go/common/types"
	"cel.dev/cel-go/parser"

	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtsource"
)

type hoverDoc struct {
	sig  string
	desc string
}

type funcOverload struct {
	sig          string
	receiverElem string
}

type funcDoc struct {
	overloads []funcOverload
	desc      string
}

type HoverDocs struct {
	variables  map[string]hoverDoc
	functions  map[string]funcDoc
	macros     map[string]string
	enums      map[string]hoverDoc
	enumValues map[string]hoverDoc
	fields     map[string]hoverDoc
}

func macroDescription(m parser.Macro) string {
	d, ok := m.(interface{ Documentation() *common.Doc })
	if !ok {
		return ""
	}
	if doc := d.Documentation(); doc != nil {
		return doc.Description
	}
	return ""
}

func LoadHoverDocs(fdset *descriptorpb.FileDescriptorSet, env *cel.Env) *HoverDocs {
	enums, enumValues := enumDocs(fdset)
	functions, macros, variables := envDocs(env)
	return &HoverDocs{
		variables:  variables,
		functions:  functions,
		macros:     macros,
		enums:      enums,
		enumValues: enumValues,
		fields:     fieldDocs(fdset),
	}
}

func envDocs(env *cel.Env) (functions map[string]funcDoc, macros map[string]string, variables map[string]hoverDoc) {
	functions = map[string]funcDoc{}
	variables = map[string]hoverDoc{}
	macros = map[string]string{}
	for name, fd := range env.Functions() {
		if !celtindex.IsUserIdent(name) {
			continue
		}
		if d := funcDocFor(name, fd); len(d.overloads) > 0 {
			functions[name] = d
		}
	}
	for _, m := range env.Macros() {
		if name := m.Function(); celtindex.IsUserIdent(name) {
			macros[name] = macroDescription(m)
		}
	}
	for _, v := range env.Variables() {
		if name := v.Name(); celtindex.IsUserIdent(name) {
			variables[name] = hoverDoc{
				sig:  fmt.Sprintf("var %s %s", name, celTypeName(v.Type())),
				desc: v.Description(),
			}
		}
	}
	return functions, macros, variables
}

func funcDocFor(name string, fd *decls.FunctionDecl) funcDoc {
	d := funcDoc{desc: fd.Description()}
	for _, o := range fd.OverloadDecls() {
		args := o.ArgTypes()
		if o.IsMemberFunction() && len(args) > 0 {
			d.overloads = append(d.overloads, funcOverload{
				sig:          fmt.Sprintf("func (%s) %s(%s) %s", celTypeName(args[0]), name, joinTypeNames(args[1:]), celTypeName(o.ResultType())),
				receiverElem: receiverElemOf(args[0]),
			})
			continue
		}
		d.overloads = append(d.overloads, funcOverload{
			sig: fmt.Sprintf("func %s(%s) %s", name, joinTypeNames(args), celTypeName(o.ResultType())),
		})
	}
	return d
}

func receiverElemOf(t *celtypes.Type) string {
	if t != nil && t.Kind() == celtypes.ListKind {
		if p := t.Parameters(); len(p) > 0 {
			return celTypeName(p[0])
		}
	}
	return ""
}

func joinTypeNames(ts []*celtypes.Type) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = celTypeName(t)
	}
	return strings.Join(parts, ", ")
}

const dynTypeName = "dyn"

func celTypeName(t *celtypes.Type) string {
	if t == nil {
		return dynTypeName
	}
	switch t.Kind() {
	case celtypes.ListKind:
		if p := t.Parameters(); len(p) > 0 {
			return fmt.Sprintf("[]%s", celTypeName(p[0]))
		}
		return "[]any"
	case celtypes.MapKind:
		if p := t.Parameters(); len(p) > 1 {
			return fmt.Sprintf("map[%s]%s", celTypeName(p[0]), celTypeName(p[1]))
		}
		return "map"
	case celtypes.DynKind, celtypes.AnyKind, celtypes.TypeParamKind:
		return t.DeclaredTypeName()
	default:
		return celtindex.ShortTypeName(t.TypeName())
	}
}

func (h *HoverDocs) Lookup(name, receiverElem string) string {
	if d, ok := h.macros[name]; ok {
		if d == "" {
			return fmt.Sprintf("macro %#q", name)
		}
		return fmt.Sprintf("macro %#q\n\n%s", name, d)
	}
	if d, ok := h.functions[name]; ok {
		return renderFuncHover(d, receiverElem)
	}
	if d, ok := h.variables[name]; ok {
		return renderHover(d)
	}
	if d, ok := h.enums[name]; ok {
		return renderHover(d)
	}
	if d, ok := h.enumValues[name]; ok {
		return renderHover(d)
	}
	if d, ok := h.fields[name]; ok {
		return renderHover(d)
	}
	return ""
}

// fieldFor returns the hover doc for a field of a specific receiver type,
// disambiguating fields that share a bare name across different messages.
func (h *HoverDocs) fieldFor(typeName, name string) (hoverDoc, bool) {
	if typeName == "" {
		return hoverDoc{}, false
	}
	d, ok := h.fields[typeName+"."+name]
	return d, ok
}

func renderFuncHover(d funcDoc, receiverElem string) string {
	overloads := filterOverloads(d.overloads, receiverElem)
	sigs := make([]string, 0, len(overloads))
	for _, o := range overloads {
		sigs = append(sigs, o.sig)
	}
	return markdownCode(strings.Join(sigs, "\n"), d.desc)
}

func markdownCode(sig, desc string) string {
	s := fmt.Sprintf("```go\n%s\n```", sig)
	if desc != "" {
		s += "\n\n" + desc
	}
	return s
}

// renderMacroCallHover renders a macro invocation: its result type, the
// macro's description, and (when it has a distinct textual form) its expansion
// formatted with the celtfmt config.
func renderMacroCallHover(mc *MacroCall, desc string, cfg celtfmt.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "macro %#q", mc.Name)
	if mc.ResultType != "" && mc.ResultType != dynTypeName {
		fmt.Fprintf(&b, " → %s", mc.ResultType)
	}
	if desc != "" {
		b.WriteString("\n\n")
		b.WriteString(desc)
	}
	if mc.Expansion != "" {
		b.WriteString("\n\nexpands to:\n")
		b.WriteString("```cel\n")
		b.WriteString(formatMacroExpansion(mc.Expansion, cfg))
		b.WriteString("\n```")
	}
	return b.String()
}

// formatMacroExpansion pretty-prints a macro's desugared CEL with the given
// celtfmt config, falling back to the raw unparsed form when the formatter
// cannot handle it (e.g. a construct it does not yet preserve).
func formatMacroExpansion(expansion string, cfg celtfmt.Config) string {
	if expansion == "" {
		return expansion
	}
	if formatted, err := celtfmt.FormatExpr(expansion, cfg); err == nil && formatted != "" {
		return formatted
	}
	return expansion
}

func filterOverloads(overloads []funcOverload, receiverElem string) []funcOverload {
	if receiverElem == "" {
		return overloads
	}
	var out []funcOverload
	for _, o := range overloads {
		if o.receiverElem == receiverElem {
			out = append(out, o)
		}
	}
	if len(out) > 0 {
		return out
	}
	return overloads
}

func renderHover(d hoverDoc) string {
	return markdownCode(d.sig, d.desc)
}

type enumValue struct {
	name string
	num  int32
}

func enumBlock(name string, values []enumValue) string {
	width := 0
	for _, v := range values {
		if len(v.name) > width {
			width = len(v.name)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "type %s int\n\nconst (\n", name)
	for _, v := range values {
		fmt.Fprintf(&b, "    %-*s %s = %d\n", width, v.name, name, v.num)
	}
	b.WriteString(")")
	return b.String()
}

// Descriptor location path element numbers used in SourceCodeInfo paths:
// FileDescriptorProto.enum_type=5 and message_type=4; DescriptorProto.enum_type=4,
// nested_type=3 and field=2; EnumDescriptorProto.value=2.
const (
	fileEnumType    int32 = 5
	fileMessageType int32 = 4
	msgEnumType     int32 = 4
	msgNestedType   int32 = 3
	enumValueField  int32 = 2
	msgField        int32 = 2
)

func enumDocs(fdset *descriptorpb.FileDescriptorSet) (enums, values map[string]hoverDoc) {
	enums = map[string]hoverDoc{}
	values = map[string]hoverDoc{}
	for _, f := range fdset.GetFile() {
		locations := f.GetSourceCodeInfo().GetLocation()
		if len(locations) == 0 {
			continue
		}
		for ei, e := range f.GetEnumType() {
			indexEnumDocs(enums, values, locations, []int32{fileEnumType, int32(ei)}, e)
		}
		for mi, m := range f.GetMessageType() {
			indexMessageEnums(enums, values, locations, []int32{fileMessageType, int32(mi)}, m)
		}
	}
	return enums, values
}

// indexMessageEnums indexes the enums nested in a message and, recursively, in
// its nested messages, so hover works for enums defined inside messages as well
// as at the top level.
func indexMessageEnums(
	enums, values map[string]hoverDoc,
	locations []*descriptorpb.SourceCodeInfo_Location,
	prefix []int32,
	m *descriptorpb.DescriptorProto,
) {
	for ei, e := range m.GetEnumType() {
		indexEnumDocs(enums, values, locations, appendPath(prefix, msgEnumType, int32(ei)), e)
	}
	for ni, nm := range m.GetNestedType() {
		indexMessageEnums(enums, values, locations, appendPath(prefix, msgNestedType, int32(ni)), nm)
	}
}

func indexEnumDocs(
	enums, values map[string]hoverDoc,
	locations []*descriptorpb.SourceCodeInfo_Location,
	path []int32,
	e *descriptorpb.EnumDescriptorProto,
) {
	var vals []enumValue
	for _, v := range e.GetValue() {
		vals = append(vals, enumValue{name: v.GetName(), num: v.GetNumber()})
	}
	enumDesc := ""
	if loc := matchLocation(locations, path); loc != nil {
		enumDesc = fieldComment(loc)
	}
	enums[e.GetName()] = hoverDoc{sig: enumBlock(e.GetName(), vals), desc: enumDesc}
	for vi, v := range e.GetValue() {
		valDesc := ""
		if loc := matchLocation(locations, appendPath(path, enumValueField, int32(vi))); loc != nil {
			valDesc = fieldComment(loc)
		}
		values[v.GetName()] = hoverDoc{
			sig:  fmt.Sprintf("type %s int\n\nconst %s %s = %d", e.GetName(), v.GetName(), e.GetName(), v.GetNumber()),
			desc: valDesc,
		}
	}
}

// appendPath returns a copy of p with extra elements appended, so recursive
// callers don't share (and later mutate) a single backing array.
func appendPath(p []int32, extra ...int32) []int32 {
	out := make([]int32, 0, len(p)+len(extra))
	out = append(out, p...)
	return append(out, extra...)
}

func fieldDocs(fdset *descriptorpb.FileDescriptorSet) map[string]hoverDoc {
	docs := map[string]hoverDoc{}
	for _, f := range fdset.GetFile() {
		locations := f.GetSourceCodeInfo().GetLocation()
		// Comments are optional metadata: fields are indexed even when the file
		// has no SourceCodeInfo, so the descriptor's field types are always
		// available for a signature. Only the documentation is empty then.
		for mi, msg := range f.GetMessageType() {
			indexMessageFields(docs, locations, []int32{fileMessageType, int32(mi)}, fullMessageName(f.GetPackage(), msg), msg)
		}
	}
	return docs
}

// fullMessageName returns the fully-qualified message name (package plus any
// nesting prefixes), normalized to strip a leading dot, matching how the env's
// type provider names the type.
func fullMessageName(pkg string, m *descriptorpb.DescriptorProto) string {
	return strings.TrimPrefix(pkg+"."+m.GetName(), ".")
}

// indexMessageFields indexes a message's fields and, recursively, the fields
// of its nested messages, so hover works for fields defined inside messages as
// well as at the top level. Each field is keyed by both its full type-qualified
// name ("Type.field") and its bare name, so a receiver type disambiguates
// colliding field names while bare-name lookups still resolve.
func indexMessageFields(
	docs map[string]hoverDoc,
	locations []*descriptorpb.SourceCodeInfo_Location,
	prefix []int32,
	fullName string,
	m *descriptorpb.DescriptorProto,
) {
	fields := m.GetField()
	for fi, fld := range fields {
		desc := ""
		if loc := matchLocation(locations, appendPath(prefix, msgField, int32(fi))); loc != nil {
			desc = fieldComment(loc)
		}
		sig := fmt.Sprintf("type %s struct {\n    %s %s", m.GetName(), fld.GetName(), fieldTypeString(fld))
		if len(fields) > 1 {
			sig += "\n    // ..."
		}
		sig += "\n}"
		d := hoverDoc{sig: sig, desc: desc}
		docs[fullName+"."+fld.GetName()] = d
		docs[fld.GetName()] = d
	}
	for ni, nm := range m.GetNestedType() {
		indexMessageFields(docs, locations, appendPath(prefix, msgNestedType, int32(ni)), fullName+"."+nm.GetName(), nm)
	}
}

func fieldTypeString(fd *descriptorpb.FieldDescriptorProto) string {
	elem := celtindex.FieldElemType(fd)
	if fd.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		return "[]" + elem
	}
	return elem
}

func matchLocation(locations []*descriptorpb.SourceCodeInfo_Location, path []int32) *descriptorpb.SourceCodeInfo_Location {
	for _, loc := range locations {
		p := loc.GetPath()
		if slices.Equal(p, path) {
			return loc
		}
	}
	return nil
}

func fieldComment(loc *descriptorpb.SourceCodeInfo_Location) string {
	if c := strings.TrimSpace(loc.GetLeadingComments()); c != "" {
		return firstLine(c)
	}
	return firstLine(strings.TrimSpace(loc.GetTrailingComments()))
}

func firstLine(s string) string {
	before, _, _ := strings.Cut(s, "\n")
	return before
}

func IdentifierAt(content string, line, char int) (string, bool) {
	off, lineStart, lineEnd, ok := celtsource.LineByteColOffset(content, line, char)
	if !ok {
		return "", false
	}
	if off >= lineEnd || !celtsource.IsIdentByte(content[off]) {
		return "", false
	}
	start, end := off, off
	for start > lineStart && celtsource.IsIdentByte(content[start-1]) {
		start--
	}
	for end < lineEnd && celtsource.IsIdentByte(content[end]) {
		end++
	}
	return content[start:end], true
}
