package rules

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	celtypes "cel.dev/cel-go/common/types"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
)

// enumWarning produces a Finding for a suspicious comparison, or nil.
type enumWarning func(sp ExprSpans, nav celast.NavigableExpr, idx *celtindex.Index, refMap map[int64]*celast.ReferenceInfo) *celtlint.Finding

func checkEnum(src common.Source, ast *cel.Ast, idx *celtindex.Index, warn enumWarning) []celtlint.Finding {
	// Walk the checked native AST (which carries type info) while reading source
	// positions from the same native AST's source info. Re-parsing or
	// round-tripping through a proto CheckedExpr would either renumber node ids
	// (macro expansion) or collapse offset ranges to start-only, breaking spans.
	native := ast.NativeRep()
	refMap := native.ReferenceMap()

	content := src.Content()
	sp := ExprSpans{info: native.SourceInfo(), ro: celtsource.RuneOffsets(content), content: content}

	var out []celtlint.Finding
	WalkNavigable(celast.NavigateAST(native), func(nav celast.NavigableExpr) bool {
		if d := warn(sp, nav, idx, refMap); d != nil {
			out = append(out, *d)
			return false
		}
		return true
	})
	return out
}

// ExprSpans carries the parsed-AST source info and rune offsets used to render
// a finding's byte span from an expression node.
type ExprSpans struct {
	info    *celast.SourceInfo
	ro      []int
	content string
}

// WalkNavigable walks a navigable AST depth-first, invoking visit on each node.
// The walk stops early for a subtree when visit returns false.
func WalkNavigable(nav celast.NavigableExpr, visit func(celast.NavigableExpr) bool) {
	if nav == nil || !visit(nav) {
		return
	}
	for _, c := range nav.Children() {
		WalkNavigable(c, visit)
	}
}

// compareKind classifies a suspicious enum comparison.
type compareKind int

const (
	compareNone compareKind = iota
	compareRawEnum
	compareEnumWarn
	compareEnumError
)

// classifyComparison inspects a comparison expression and returns its enum
// classification plus the operands.
//
//nolint:gocyclo // exhaustive enum-comparison matrix, hard to flatten without hurting readability
func classifyComparison(nav celast.NavigableExpr, idx *celtindex.Index,
	refMap map[int64]*celast.ReferenceInfo) (kind compareKind, a, b enumOperand, args []celast.Expr) {
	if nav.Kind() != celast.CallKind || !isComparisonOperator(nav.AsCall().FunctionName()) {
		return compareNone, enumOperand{}, enumOperand{}, nil
	}
	args = nav.AsCall().Args()
	if len(args) != 2 {
		return compareNone, enumOperand{}, enumOperand{}, nil
	}
	a = classifyEnum(args[0], idx, refMap)
	b = classifyEnum(args[1], idx, refMap)
	if !a.isEnum() && !b.isEnum() {
		return compareNone, enumOperand{}, enumOperand{}, nil
	}
	if validEnumCompare(a, b) || validEnumCompare(b, a) {
		return compareNone, enumOperand{}, enumOperand{}, nil
	}
	if a.kind == kindInt || b.kind == kindInt {
		return compareRawEnum, a, b, args
	}
	if a.kind == kindOther || b.kind == kindOther {
		return compareEnumError, a, b, args
	}
	if a.kind == kindVariable && b.kind == kindVariable {
		if a.enum == b.enum {
			return compareEnumWarn, a, b, args
		}
		return compareEnumError, a, b, args
	}
	if a.kind == kindVariable || b.kind == kindVariable {
		return compareEnumError, a, b, args
	}
	return compareEnumWarn, a, b, args
}

// rawEnumValueWarning reports a raw enum literal used in a comparison.
func rawEnumValueWarning(sp ExprSpans, nav celast.NavigableExpr, idx *celtindex.Index, refMap map[int64]*celast.ReferenceInfo) *celtlint.Finding {
	kind, a, b, args := classifyComparison(nav, idx, refMap)
	if kind != compareRawEnum {
		return nil
	}
	return rawEnumWarning(sp, nav, a, b, args, idx)
}

// enumCompareWarning reports a suspicious but type-correct enum comparison.
func enumCompareWarning(sp ExprSpans, nav celast.NavigableExpr, idx *celtindex.Index, refMap map[int64]*celast.ReferenceInfo) *celtlint.Finding {
	kind, a, b, args := classifyComparison(nav, idx, refMap)
	if kind != compareEnumWarn {
		return nil
	}
	msg := compareWarnMessage(a, b)
	return renderEnumCompareWarning(sp, nav, args, celtlint.LevelWarn, msg)
}

// enumTypeMismatchWarning reports an enum comparison of mismatched types.
func enumTypeMismatchWarning(sp ExprSpans, nav celast.NavigableExpr, idx *celtindex.Index, refMap map[int64]*celast.ReferenceInfo) *celtlint.Finding {
	kind, a, b, args := classifyComparison(nav, idx, refMap)
	if kind != compareEnumError {
		return nil
	}
	msg := compareErrorMessage(a, b)
	return renderEnumCompareWarning(sp, nav, args, celtlint.LevelError, msg)
}

// rawEnumValueFindings reports use of raw enum literals instead of named values.
func rawEnumValueFindings(ctx *celtlint.Context) []celtlint.Finding {
	if ctx.Index == nil || ctx.Ast == nil || ctx.CompileFailed() {
		return nil
	}
	return checkEnum(ctx.Source, ctx.Ast, ctx.Index, rawEnumValueWarning)
}

// enumCompareFindings reports suspicious but valid enum comparisons.
func enumCompareFindings(ctx *celtlint.Context) []celtlint.Finding {
	if ctx.Index == nil || ctx.Ast == nil || ctx.CompileFailed() {
		return nil
	}
	return checkEnum(ctx.Source, ctx.Ast, ctx.Index, enumCompareWarning)
}

// enumTypeMismatchFindings reports enum comparisons of mismatched types.
func enumTypeMismatchFindings(ctx *celtlint.Context) []celtlint.Finding {
	if ctx.Index == nil || ctx.Ast == nil || ctx.CompileFailed() {
		return nil
	}
	return checkEnum(ctx.Source, ctx.Ast, ctx.Index, enumTypeMismatchWarning)
}

func compareWarnMessage(a, b enumOperand) string {
	if a.kind == kindVariable && b.kind == kindVariable {
		return "comparing variables directly"
	}
	return "comparing enum values"
}

func compareErrorMessage(a, b enumOperand) string {
	if a.kind == kindOther || b.kind == kindOther {
		return "comparing against a value of the wrong type"
	}
	if a.kind == kindVariable && b.kind == kindVariable {
		return "comparing variables of different types"
	}
	return "comparing enum values of different types"
}

func rawEnumWarning(sp ExprSpans, nav celast.NavigableExpr, a, b enumOperand, args []celast.Expr, idx *celtindex.Index) *celtlint.Finding {
	// One operand is a raw integer literal and the other is enum-typed. The fix
	// must replace the integer literal itself (not the enum operand) with the
	// named enum value it corresponds to, so enumSide is the literal's operand.
	enumFull, val := b.enum, a.val
	enumSide := args[0]
	switch {
	case a.kind == kindInt:
		enumFull, val, enumSide = b.enum, a.val, args[0]
	case b.kind == kindInt:
		enumFull, val, enumSide = a.enum, b.val, args[1]
	}

	ns, ne := celtlint.ExprByteSpan(sp.info, sp.ro, nav)
	if ne <= ns {
		return nil
	}
	start, end := celtsource.OffsetToPos(sp.content, ns), celtsource.OffsetToPos(sp.content, ne)
	var fixStart, fixEnd celtlint.Position
	var fix string
	if member, hasMember := idx.EnumValues(enumFull)[val]; hasMember {
		fix = fmt.Sprintf("%s.%s", idx.EnumShort(enumFull), member)
		if s, e := celtlint.ExprOffsets(sp.info, sp.ro, sp.content, enumSide); e > s {
			fixStart = celtsource.OffsetToPos(sp.content, s)
			fixEnd = celtsource.OffsetToPos(sp.content, e)
		}
	}
	return &celtlint.Finding{
		Level:    celtlint.LevelWarn,
		Code:     celtlint.CodeRawEnumValue,
		Message:  "use of a raw enum value",
		Start:    start,
		End:      end,
		Hint:     "prefer a named enum value over a numeric literal",
		Fix:      fix,
		FixStart: fixStart,
		FixEnd:   fixEnd,
	}
}

const (
	kindInt      = "int"
	kindConstant = "constant"
	kindVariable = "variable"
	kindOther    = "other"
)

type enumOperand struct {
	kind string
	enum string
	val  int64
}

func (o enumOperand) isEnum() bool {
	return o.kind == kindVariable || o.kind == kindConstant
}

func classifyEnum(e celast.Expr, idx *celtindex.Index, refMap map[int64]*celast.ReferenceInfo) enumOperand {
	if v, ok := intLiteral(e); ok {
		return enumOperand{kind: kindInt, val: v}
	}
	if full, ok := enumVariableType(e, idx); ok {
		return enumOperand{kind: kindVariable, enum: full}
	}
	if nav, ok := e.(celast.NavigableExpr); ok {
		switch nav.Kind() {
		case celast.SelectKind, celast.IdentKind:
			if info, inRef := refMap[nav.ID()]; inRef {
				if i := strings.LastIndex(info.Name, "."); i >= 0 {
					full := info.Name[:i]
					if idx.EnumValues(full) != nil {
						return enumOperand{kind: kindConstant, enum: full}
					}
				}
			}
		}
	}
	return enumOperand{kind: kindOther}
}

func validEnumCompare(a, b enumOperand) bool {
	return a.kind == kindVariable && b.kind == kindConstant && a.enum == b.enum
}

func enumVariableType(e celast.Expr, idx *celtindex.Index) (string, bool) {
	nav, ok := e.(celast.NavigableExpr)
	if !ok {
		return "", false
	}
	if nav.Kind() == celast.SelectKind {
		sel := nav.AsSelect()
		if op, ok := sel.Operand().(celast.NavigableExpr); ok {
			if enumFull, isField := idx.EnumFields(op.Type().TypeName())[sel.FieldName()]; isField {
				return enumFull, true
			}
		}
	}
	if idx.EnumValues(nav.Type().TypeName()) != nil {
		return nav.Type().TypeName(), true
	}
	return "", false
}

func intLiteral(e celast.Expr) (int64, bool) {
	if e.Kind() != celast.LiteralKind {
		return 0, false
	}
	v, ok := e.AsLiteral().(celtypes.Int)
	if !ok {
		return 0, false
	}
	return int64(v), true
}

func renderEnumCompareWarning(sp ExprSpans, nav celast.NavigableExpr, args []celast.Expr, lvl celtlint.Level, reason string) *celtlint.Finding {
	ns, ne := celtlint.ExprByteSpan(sp.info, sp.ro, nav)
	if ne <= ns {
		return nil
	}
	f := &celtlint.Finding{
		Level:   lvl,
		Code:    celtlint.CodeOddComparison,
		Message: "odd comparison",
		Start:   celtsource.OffsetToPos(sp.content, ns),
		End:     celtsource.OffsetToPos(sp.content, ne),
		Hint:    reason,
	}
	var spans []celtlint.Span
	for _, a := range args {
		as, ae := celtlint.ExprByteSpan(sp.info, sp.ro, a)
		if ae <= as {
			continue
		}
		spans = append(spans, celtlint.Span{
			Start: celtsource.OffsetToPos(sp.content, as),
			End:   celtsource.OffsetToPos(sp.content, ae),
		})
	}
	if len(spans) >= 2 {
		f.Start, f.End = spans[0].Start, spans[0].End
		f.Spans = spans[1:]
	} else {
		f.Spans = spans
	}
	return f
}

func literalTypeName(e celast.Expr) string {
	if e.Kind() != celast.LiteralKind {
		return ""
	}
	switch e.AsLiteral().(type) {
	case celtypes.Bool:
		return "bool"
	case celtypes.Int:
		return "int"
	case celtypes.Uint:
		return "uint"
	case celtypes.Double:
		return "double"
	case celtypes.String:
		return "string"
	case celtypes.Bytes:
		return "bytes"
	}
	return ""
}

type typedLiteralSpan struct {
	litStart, litEnd celtlint.Position
	sibStart, sibEnd celtlint.Position
	sibling          bool
}

func typedLiteralOperand(src common.Source, found string) (typedLiteralSpan, bool) {
	parsed := celtlint.ParseUnexpanded(src)
	if parsed == nil {
		return typedLiteralSpan{}, false
	}
	sInfo := parsed.SourceInfo()
	content := src.Content()
	ro := celtsource.RuneOffsets(content)
	matched := false
	var out typedLiteralSpan
	WalkNavigable(celast.NavigateAST(parsed), func(nav celast.NavigableExpr) bool {
		if matched {
			return false
		}
		if nav.Kind() == celast.CallKind {
			args := nav.AsCall().Args()
			if _, isOp := operators.FindReverse(nav.AsCall().FunctionName()); isOp {
				for i, arg := range args {
					if literalTypeName(arg) != found {
						continue
					}
					s, e := celtlint.ExprOffsets(sInfo, ro, content, arg)
					out.litStart = celtsource.OffsetToPos(content, s)
					out.litEnd = celtsource.OffsetToPos(content, e)
					if len(args) == 2 {
						os, oe := celtlint.ExprOffsets(sInfo, ro, content, args[1-i])
						out.sibStart = celtsource.OffsetToPos(content, os)
						out.sibEnd = celtsource.OffsetToPos(content, oe)
						out.sibling = true
					}
					matched = true
					return false
				}
			}
		}
		return true
	})
	return out, matched
}
