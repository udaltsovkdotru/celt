package celtlint

import (
	"fmt"
	"strings"

	celast "cel.dev/cel-go/common/ast"
	celtypes "cel.dev/cel-go/common/types"
	"cel.dev/cel-go/parser"

	"github.com/udaltsovkdotru/celt/celtsource"
)

// localBinding describes a locally scoped variable (a cel.bind bound variable
// or a comprehension iter/accu variable): its name, the byte span [start, end)
// in which it is in scope, and — when a checked AST is available — its CEL type.
type localBinding struct {
	name     string
	typeName string
	start    int
	end      int
}

// hover renders the binding's documentation (its declared type), or "" when the
// type is unknown (e.g. the AST was parsed but not type-checked).
func (lb localBinding) hover() string {
	if lb.typeName == "" {
		return ""
	}
	return markdownCode(fmt.Sprintf("var %s %s", lb.name, lb.typeName), "")
}

// localBindings returns the locally bound variables of content whose scope
// covers the byte offset off (pass -1 for every binding), together with their
// CEL types where the AST is checked. cel.bind bound variables desugar to
// comprehension iter variables, so they are collected alongside the iter/accu
// variables of genuine comprehension macros. Content is compiled (falling back
// to a parse) so bindings are available even when the document does not
// type-check; in that untyped case bindings still carry names and scopes but no
// types.
func (a *Analyzer) localBindings(content string, off int) []localBinding {
	if a.env == nil {
		return nil
	}
	a.ensureBuilt()
	ast := a.compileContent(content)
	if ast == nil || ast.NativeRep() == nil {
		return nil
	}
	native := ast.NativeRep()
	ro := celtsource.RuneOffsets(content)
	var out []localBinding
	celtsource.Walk(native.Expr(), func(e celast.Expr) {
		switch e.Kind() {
		case celast.ComprehensionKind:
			a.appendComprehensionBinding(&out, native, content, ro, off, e.ID(), e.AsComprehension())
		case celast.CallKind:
			// A parsed (unchecked) AST may leave comprehension and bind macros
			// as unexpanded calls whose first argument is the bound name.
			a.appendCallBinding(&out, native, content, ro, off, e.ID(), e.AsCall())
		}
	})
	return out
}

// appendComprehensionBinding records the variables a comprehension binds. For
// cel.bind the accumulator is the bound variable (with the hidden "#unused"
// iter placeholder), so only the accumulator is recorded, typed from the value
// expression; for iteration comprehensions the iter/second-iter variables are
// recorded, typed from the iterated collection's element type.
func (a *Analyzer) appendComprehensionBinding(
	out *[]localBinding, native *celast.AST, content string, ro []int,
	off int, id int64, c celast.ComprehensionExpr,
) {
	start, end := bindScopeSpan(native.SourceInfo(), content, ro, id)
	if start < 0 || start >= len(content) || end <= start {
		return
	}
	if isBindComprehension(c) {
		typ := ""
		if native.IsChecked() {
			typ = checkedTypeName(native, c.AccuInit())
		} else {
			// The parser expands bind to a comprehension even in an unchecked
			// AST, so recover the type from the value expression when the
			// document does not type-check.
			typ = a.bindingValueType(c.AccuInit())
		}
		appendBinding(out, c.AccuVar(), typ, off, start, end)
		return
	}
	iterType := ""
	if native.IsChecked() {
		iterType = elemTypeName(native, c.IterRange())
	}
	appendBinding(out, c.IterVar(), iterType, off, start, end)
	if c.HasIterVar2() {
		appendBinding(out, c.IterVar2(), iterType, off, start, end)
	}
}

// appendCallBinding records the variable bound by an unexpanded comprehension
// or bind macro call (its first argument). A bind's bound variable is typed
// from its value argument.
func (a *Analyzer) appendCallBinding(out *[]localBinding, native *celast.AST, content string, ro []int, off int, id int64, c celast.CallExpr) {
	fn := c.FunctionName()
	isBind := isBindCall(fn)
	if !isBind && !isComprehensionMacroNameQualified(fn) {
		return
	}
	args := c.Args()
	if len(args) == 0 || args[0].Kind() != celast.IdentKind {
		return
	}
	start, end := bindScopeSpan(native.SourceInfo(), content, ro, id)
	if start < 0 || start >= len(content) || end <= start {
		return
	}
	typ := ""
	if isBind && len(args) > 1 {
		typ = a.bindingValueType(args[1])
	}
	appendBinding(out, args[0].AsIdent(), typ, off, start, end)
}

// appendBinding appends a binding when its name is non-empty and (when a cursor
// offset is given) its scope covers that offset.
func appendBinding(out *[]localBinding, name, typ string, off, start, end int) {
	if name == "" || (off >= 0 && (start > off || off >= end)) {
		return
	}
	*out = append(*out, localBinding{name: name, typeName: typ, start: start, end: end})
}

// bindingValueType resolves the CEL type of a cel.bind value expression from an
// unchecked AST, so a bound variable is still typed when the surrounding
// document does not type-check (as is typical at a completion point).
func (a *Analyzer) bindingValueType(e celast.Expr) string {
	if a.index == nil || e == nil {
		return ""
	}
	s, err := parser.Unparse(e, macroFreeInfo())
	if err != nil || s == "" {
		return ""
	}
	return a.resolveReceiverType(s)
}

// localBindingNamed returns the in-scope local binding named name at byte
// offset off, if any.
func (a *Analyzer) localBindingNamed(content string, off int, name string) (localBinding, bool) {
	for _, lb := range a.localBindings(content, off) {
		if lb.name == name {
			return lb, true
		}
	}
	return localBinding{}, false
}

// bindScopeSpan returns the byte span [start, end) over which a comprehension
// (or unexpanded macro call) binds its variables: from the node's source start
// to the matching close paren.
func bindScopeSpan(info *celast.SourceInfo, content string, ro []int, id int64) (start, end int) {
	r, ok := info.GetOffsetRange(id)
	if !ok {
		return -1, -1
	}
	start = celtsource.ByteOffset(ro, int(r.Start))
	if start < 0 || start >= len(content) {
		return -1, -1
	}
	return start, macroScopeEndAt(content, start)
}

// isBindCall reports whether an unexpanded call is cel.bind (ext.Bindings).
func isBindCall(name string) bool {
	return name == "cel.bind" || name == "bind"
}

// isBindComprehension reports whether a comprehension is the desugaring of
// cel.bind: its loop step is just the accumulator identity (a value is bound,
// not iterated).
func isBindComprehension(c celast.ComprehensionExpr) bool {
	step := c.LoopStep()
	return step != nil && step.Kind() == celast.IdentKind && step.AsIdent() == c.AccuVar()
}

// isComprehensionMacroNameQualified reports whether name (possibly qualified,
// e.g. "_._all_") is a standard comprehension macro, whose first argument is
// the iteration variable.
func isComprehensionMacroNameQualified(name string) bool {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "all", "exists", "exists_one", "filter", "map":
		return true
	}
	return false
}

// checkedTypeName returns the CEL type name of expression e from a checked AST,
// or "" when unavailable.
func checkedTypeName(native *celast.AST, e celast.Expr) string {
	if native == nil || e == nil {
		return ""
	}
	t := native.GetType(e.ID())
	if t == nil {
		return ""
	}
	return celTypeName(t)
}

// elemTypeName returns the element type name of a list-typed expression e, or
// "" when e is not a typed list.
func elemTypeName(native *celast.AST, e celast.Expr) string {
	if native == nil || e == nil {
		return ""
	}
	t := native.GetType(e.ID())
	if t == nil || t.Kind() != celtypes.ListKind || len(t.Parameters()) == 0 {
		return ""
	}
	return celTypeName(t.Parameters()[0])
}
