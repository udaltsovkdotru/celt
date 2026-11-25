package celtlint

import (
	"strconv"

	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	celtypes "cel.dev/cel-go/common/types"
	ref "cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/parser"
)

// renderComprehension renders a Comprehension node back to the macro call that
// produced it, using the tracked macro-call map. This is fully generic — no
// macro name or shape is assumed — so any user macro that expands to a
// comprehension (cel.bind, or a future one) is
// shown under its own name and arguments. Nested macros inside those arguments
// are expanded in turn (a comprehension stays as its own call; other macros
// show their expansion). ok is false when the comprehension has no tracked
// origin (e.g. an unchecked AST), so the caller omits the expansion.
func renderComprehension(e celast.Expr, checked *celast.AST) (string, bool) {
	if checked == nil {
		return "", false
	}
	original, ok := checked.SourceInfo().GetMacroCall(e.ID())
	if !ok {
		return "", false
	}
	return renderMacroCallAsIs(original.AsCall(), checked)
}

// renderMacroCallAsIs renders a macro call (its original name and arguments)
// verbatim, recursing into each argument so nested macros are expanded. An
// argument that is itself a macro call is rendered from its expansion node in
// the checked tree (found by id); any other argument is unparsed as written.
func renderMacroCallAsIs(c celast.CallExpr, checked *celast.AST) (string, bool) {
	args, ok := renderArgsAsIs(c.Args(), checked)
	if !ok {
		return "", false
	}
	if c.IsMemberFunction() {
		target, ok := renderExpr(c.Target(), checked)
		if !ok {
			return "", false
		}
		return target + "." + c.FunctionName() + "(" + args + ")", true
	}
	return c.FunctionName() + "(" + args + ")", true
}

func renderArgsAsIs(args []celast.Expr, checked *celast.AST) (string, bool) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		s, ok := renderArgAsIs(a, checked)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	return joinComma(parts), true
}

// renderArgAsIs renders one argument of an as-is macro call. When the argument
// is a macro call (or contains one), its expansion node is found by id in the
// checked tree and rendered, so nested macros are desugared; otherwise the
// argument is unparsed as written.
func renderArgAsIs(a celast.Expr, checked *celast.AST) (string, bool) {
	if checked != nil {
		if n, ok := findExprByID(checked.Expr(), a.ID()); ok && n != nil {
			if s, ok := renderExpr(n, checked); ok {
				return s, true
			}
		}
	}
	s, err := parser.Unparse(a, macroFreeInfo())
	if err != nil || s == "" {
		return "", false
	}
	return s, true
}

// renderExpr renders an expression, recursing through comprehensions (each
// rendered as its originating macro call) and falling back to parser.Unparse
// for subtrees that contain none.
func renderExpr(e celast.Expr, checked *celast.AST) (string, bool) {
	if e == nil {
		return "", false
	}
	if e.Kind() == celast.ComprehensionKind {
		return renderComprehension(e, checked)
	}
	if !containsComprehension(e) {
		s, err := parser.Unparse(e, macroFreeInfo())
		if err != nil || s == "" {
			return "", false
		}
		return s, true
	}
	return renderNode(e, checked)
}

// renderNode renders a node that contains at least one nested comprehension
// (so parser.Unparse would reject it), by rebuilding it around rendered
// children.
func renderNode(e celast.Expr, checked *celast.AST) (string, bool) {
	switch e.Kind() {
	case celast.IdentKind:
		return e.AsIdent(), true
	case celast.LiteralKind:
		return literalText(e.AsLiteral()), true
	case celast.SelectKind:
		s := e.AsSelect()
		operand, ok := renderExpr(s.Operand(), checked)
		if !ok {
			return "", false
		}
		sel := operand + "." + s.FieldName()
		if s.IsTestOnly() {
			return "has(" + sel + ")", true
		}
		return sel, true
	case celast.CallKind:
		return renderCall(e.AsCall(), checked)
	case celast.ListKind:
		return renderList(e.AsList(), checked)
	case celast.MapKind:
		return renderMap(e.AsMap(), checked)
	case celast.StructKind:
		return renderStruct(e.AsStruct(), checked)
	}
	return "", false
}

func renderCall(c celast.CallExpr, checked *celast.AST) (string, bool) {
	fn := c.FunctionName()
	args := c.Args()
	if op, ok := binaryOp[fn]; ok {
		return renderBinary(c, op, checked)
	}
	switch fn {
	case operators.LogicalNot:
		return renderUnary(args, "!", checked)
	case operators.Negate:
		return renderUnary(args, "-", checked)
	case operators.Conditional:
		return renderConditional(args, checked)
	case operators.Index:
		return renderIndex(args, checked)
	}
	// A named function or member call.
	if c.IsMemberFunction() {
		target, ok := renderExpr(c.Target(), checked)
		if !ok {
			return "", false
		}
		argStrs, ok := renderArgs(args, checked)
		if !ok {
			return "", false
		}
		return target + "." + fn + "(" + argStrs + ")", true
	}
	argStrs, ok := renderArgs(args, checked)
	if !ok {
		return "", false
	}
	return fn + "(" + argStrs + ")", true
}

func renderUnary(args []celast.Expr, op string, checked *celast.AST) (string, bool) {
	if len(args) != 1 {
		return "", false
	}
	a, ok := renderExpr(args[0], checked)
	if !ok {
		return "", false
	}
	return op + a, true
}

func renderConditional(args []celast.Expr, checked *celast.AST) (string, bool) {
	if len(args) != 3 {
		return "", false
	}
	cond, ok := renderExpr(args[0], checked)
	if !ok {
		return "", false
	}
	t, ok := renderExpr(args[1], checked)
	if !ok {
		return "", false
	}
	f, ok := renderExpr(args[2], checked)
	if !ok {
		return "", false
	}
	return cond + " ? " + t + " : " + f, true
}

func renderIndex(args []celast.Expr, checked *celast.AST) (string, bool) {
	if len(args) != 2 {
		return "", false
	}
	operand, ok := renderExpr(args[0], checked)
	if !ok {
		return "", false
	}
	index, ok := renderExpr(args[1], checked)
	if !ok {
		return "", false
	}
	return operand + "[" + index + "]", true
}

// binaryOp maps an operator function name to its infix symbol.
var binaryOp = map[string]string{
	operators.LogicalAnd:    " && ",
	operators.LogicalOr:     " || ",
	operators.Equals:        " == ",
	operators.NotEquals:     " != ",
	operators.Less:          " < ",
	operators.LessEquals:    " <= ",
	operators.Greater:       " > ",
	operators.GreaterEquals: " >= ",
	operators.Add:           " + ",
	operators.Subtract:      " - ",
	operators.Multiply:      " * ",
	operators.Divide:        " / ",
	operators.Modulo:        " % ",
	operators.In:            " in ",
}

func renderBinary(c celast.CallExpr, op string, checked *celast.AST) (string, bool) {
	if len(c.Args()) != 2 {
		return "", false
	}
	l, ok := renderExpr(c.Args()[0], checked)
	if !ok {
		return "", false
	}
	r, ok := renderExpr(c.Args()[1], checked)
	if !ok {
		return "", false
	}
	return l + op + r, true
}

func renderArgs(args []celast.Expr, checked *celast.AST) (string, bool) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		s, ok := renderExpr(a, checked)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	return joinComma(parts), true
}

func renderList(l celast.ListExpr, checked *celast.AST) (string, bool) {
	parts := make([]string, 0, len(l.Elements()))
	for _, el := range l.Elements() {
		s, ok := renderExpr(el, checked)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	return "[" + joinComma(parts) + "]", true
}

func renderMap(m celast.MapExpr, checked *celast.AST) (string, bool) {
	var b []byte
	first := true
	for _, en := range m.Entries() {
		entry := en.AsMapEntry()
		k, ok := renderExpr(entry.Key(), checked)
		if !ok {
			return "", false
		}
		v, ok := renderExpr(entry.Value(), checked)
		if !ok {
			return "", false
		}
		if !first {
			b = append(b, ", "...)
		}
		first = false
		b = append(b, k...)
		b = append(b, ": "...)
		b = append(b, v...)
	}
	return "{" + string(b) + "}", true
}

func renderStruct(s celast.StructExpr, checked *celast.AST) (string, bool) {
	fields := make([]string, 0, len(s.Fields()))
	for _, fe := range s.Fields() {
		f := fe.AsStructField()
		v, ok := renderExpr(f.Value(), checked)
		if !ok {
			return "", false
		}
		fields = append(fields, f.Name()+": "+v)
	}
	return s.TypeName() + "{" + joinComma(fields) + "}", true
}

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

func literalText(v ref.Val) string {
	switch t := v.(type) {
	case celtypes.Bool:
		if bool(t) {
			return "true"
		}
		return "false"
	case celtypes.Int:
		return strconv.FormatInt(int64(t), 10)
	case celtypes.Uint:
		return strconv.FormatUint(uint64(t), 10)
	case celtypes.Double:
		return strconv.FormatFloat(float64(t), 'g', -1, 64)
	case celtypes.String:
		return strconv.Quote(string(t))
	case celtypes.Bytes:
		return strconv.Quote(string(t))
	default:
		// Duration and timestamp literals have a canonical string form via
		// Value(); render them quoted so the output stays a valid expression.
		if val, ok := v.Value().(string); ok && val != "" {
			return strconv.Quote(val)
		}
		return ""
	}
}

// containsComprehension reports whether e contains a comprehension anywhere.
func containsComprehension(e celast.Expr) bool {
	found := false
	celast.PreOrderVisit(e, celast.NewExprVisitor(func(n celast.Expr) {
		if n.Kind() == celast.ComprehensionKind {
			found = true
		}
	}))
	return found
}
