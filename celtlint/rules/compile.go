package rules

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/operators"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
	"github.com/udaltsovkdotru/celt/pkg/celtspell"
)

// Compile reports CEL compiler errors.
type Compile struct {
	celtlint.RuleMeta
}

func (Compile) Check(ctx *celtlint.Context) {
	ctx.ReportAll(checkCompile(ctx))
}

func checkCompile(ctx *celtlint.Context) []celtlint.Finding {
	if !ctx.CompileFailed() {
		return nil
	}
	cf := compileFindings(ctx.Source, ctx.Err, ctx.Index)
	if ctx.Index != nil {
		content := ctx.Source.Content()
		cf = DropRedundantCompileErrors(cf, content, ctx.Index, ReportedNames(ctx.Findings, content))
	}
	return cf
}

func compileFindings(src common.Source, iss *cel.Issues, idx *celtindex.Index) []celtlint.Finding {
	errs := iss.Errors()
	sortErrorsByLocation(errs)
	out := make([]celtlint.Finding, 0, len(errs))
	for _, e := range errs {
		line, col := e.Location.Line(), e.Location.Column()
		if line <= 0 {
			line, col = 1, 0
		}
		msg := celtlint.BacktickQuotes(e.Message)
		start, end := compileErrorSpan(src, e, line, col)
		code, name := compileErrorCategory(msg)
		f := celtlint.Finding{
			Level:   celtlint.LevelError,
			Code:    code,
			Message: name,
			Detail:  msg,
			Start:   start,
			End:     end,
		}
		if m := expectedTypeRx.FindStringSubmatch(msg); m != nil {
			if s, ok := typedLiteralOperand(src, m[2]); ok {
				f.Start, f.End = s.litStart, s.litEnd
				f.SpanMessage = expectedFound(m[1], m[2])
				if s.sibling {
					f.Spans = append(f.Spans, celtlint.Span{
						Start:   s.sibStart,
						End:     s.sibEnd,
						Message: fmt.Sprintf("this is found to be of type %#q", m[1]),
					})
				}
			} else {
				f.SpanMessage = fmt.Sprintf("found %#q", m[2])
				f.Hint = fmt.Sprintf("expected %#q", m[1])
			}
		} else if m := overloadRx.FindStringSubmatch(msg); m != nil {
			parts := strings.Split(m[2], ",")
			if len(parts) == 2 && isComparisonOperator(m[1]) {
				opDisplay, _ := operators.FindReverse(m[1])
				t1, t2 := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
				f.Message = fmt.Sprintf("can't compare %#q with %#q", t1, t2)
				f.SpanMessage = fmt.Sprintf("no implementation for `%s %s %s`", t1, opDisplay, t2)
			}
		}
		if f.Hint == "" && idx != nil && isCompileError(f.Code) {
			f.Hint = suggestFor(src, e, idx)
		}
		out = append(out, f)
	}
	return out
}

func expectedFound(expected, found string) string {
	return fmt.Sprintf("expected %#q, found %#q", expected, found)
}

func compileErrorCategory(msg string) (code, name string) {
	switch {
	case strings.Contains(msg, "expected type"):
		return celtlint.CodeTypeMismatch, "type mismatch"
	case strings.Contains(msg, "found no matching overload"):
		return celtlint.CodeNoMatchingOverload, "no matching overload"
	case strings.Contains(msg, "undeclared reference"):
		return celtlint.CodeUndeclaredReference, "undeclared reference"
	case strings.Contains(msg, "undefined field"):
		return celtlint.CodeUndefinedField, "undefined field"
	case strings.Contains(msg, "token recognition error"):
		return celtlint.CodeUnexpectedToken, "unexpected token"
	default:
		return celtlint.CodeCompileError, "compile error"
	}
}

func isCompileError(code string) bool {
	switch code {
	case celtlint.CodeCompileError, celtlint.CodeTypeMismatch, celtlint.CodeNoMatchingOverload,
		celtlint.CodeUndeclaredReference, celtlint.CodeUndefinedField, celtlint.CodeUnexpectedToken:
		return true
	}
	return false
}

func compileErrorSpan(src common.Source, e *cel.Error, line, col int) (start, end celtlint.Position) {
	content := src.Content()
	msg := celtlint.BacktickQuotes(e.Message)
	if m := expectedTypeRx.FindStringSubmatch(msg); m != nil {
		if s, ok := typedLiteralOperand(src, m[2]); ok {
			return s.litStart, s.litEnd
		}
	}
	if snippet, ok := src.Snippet(line); ok {
		if start, length := errorSpan(snippet, col, msg); length > 0 {
			return celtlint.Position{Line: line - 1, Char: start}, celtlint.Position{Line: line - 1, Char: start + length}
		}
	}
	off := celtsource.PosToOffset(content, celtlint.Position{Line: line - 1, Char: col})
	return celtsource.OffsetToPos(content, off), celtsource.OffsetToPos(content, off+1)
}

var overloadRx = regexp.MustCompile("found no matching overload for `([^`]+)` applied to `\\(([^)]*)\\)`")

var expectedTypeRx = regexp.MustCompile("expected type `([^`]+)` but found `([^`]+)`")

func isComparisonOperator(name string) bool {
	switch name {
	case operators.Equals, operators.NotEquals, operators.Less,
		operators.LessEquals, operators.Greater, operators.GreaterEquals:
		return true
	}
	return false
}

func sortErrorsByLocation(errs []*cel.Error) {
	slices.SortStableFunc(errs, func(a, b *cel.Error) int {
		if a.Location.Line() != b.Location.Line() {
			return a.Location.Line() - b.Location.Line()
		}
		return a.Location.Column() - b.Location.Column()
	})
}

// errorSpan is a rules-local copy of the engine's span computation (it is
// unexported in celtlint). It mirrors celtlint's diag helper so the compile
// rule can compute a source span from a snippet and a message.
func errorSpan(snippet string, col int, msg string) (start, length int) {
	if col == 0 {
		if idx, tok := metadataSpan(snippet, msg); tok != "" {
			return idx, len(tok)
		}
		return len(snippet), 1
	}
	if strings.Contains(msg, "token recognition error") {
		if tok := strings.TrimSpace(celtlint.BtName(celtlint.BacktickQuotes(msg))); tok == "=" {
			return comparisonSpan(snippet, strings.Index(snippet, "="))
		}
	}
	return primarySpan(snippet, col, msg)
}

func metadataSpan(snippet, msg string) (idx int, tok string) {
	if tok := celtlint.BtName(msg); tok != "" {
		if idx := strings.Index(snippet, tok); idx >= 0 {
			return idx, tok
		}
	}
	if tok := celtlint.DqName(msg); tok != "" {
		if idx := strings.Index(snippet, tok); idx >= 0 {
			return idx, tok
		}
	}
	return -1, ""
}

func comparisonSpan(line string, eq int) (start, length int) {
	if eq < 0 || eq >= len(line) {
		return primarySpan(line, eq, "")
	}
	start = eq
	for start > 0 && !isCompBoundary(line[start-1]) {
		start--
	}
	for start < len(line) && (isCompBoundary(line[start]) || line[start] == ' ') {
		start++
	}
	end := eq
	for end < len(line) && !isCompBoundary(line[end]) {
		end++
	}
	for end > start && line[end-1] == ' ' {
		end--
	}
	if end <= start {
		return primarySpan(line, eq, "")
	}
	return start, end - start
}

func isCompBoundary(c byte) bool {
	return c == '&' || c == '|' || c == '(' || c == ')' || c == ','
}

func primarySpan(snippet string, col int, msg string) (start, length int) {
	token := celtlint.BtName(celtlint.BacktickQuotes(msg))
	if token == "" {
		token = celtlint.DqName(msg)
	}
	if token != "" {
		if idx := strings.Index(snippet, token); idx >= 0 {
			return idx, len(token)
		}
		if i := strings.LastIndex(token, "."); i >= 0 {
			if idx := strings.Index(snippet, token[i+1:]); idx >= 0 {
				return idx, len(token) - i - 1
			}
		}
	}
	if col >= len(snippet) {
		col = len(snippet) - 1
	}
	if col < 0 {
		col = 0
	}
	start = col
	for start > 0 && celtsource.IsIdentByte(snippet[start-1]) {
		start--
	}
	end := col
	for end < len(snippet) && celtsource.IsIdentByte(snippet[end]) {
		end++
	}
	if end == start {
		end = start + 1
	}
	return start, end - start
}

func suggestFor(src common.Source, e *cel.Error, idx *celtindex.Index) string {
	if hint := suggestOperator(src, e); hint != "" {
		return hint
	}
	if !isNameError(e.Message) {
		return ""
	}

	line, ok := src.Snippet(e.Location.Line())
	if !ok {
		return ""
	}

	ref := celtlint.BtName(celtlint.BacktickQuotes(e.Message))
	if ref == "" {
		ref = tokenAt(line, e.Location.Column())
	}
	if ref == "" {
		return ""
	}
	if idx.HasEnum(ref) {
		ref = namespaceMember(line, ref)
	}
	if ref == "" {
		return ""
	}

	repl, kind, ok := celtspell.Nearest(idx, ref)
	if !ok {
		return ""
	}
	return renderSuggestion(line, ref, repl, string(kind), e.Location.Line())
}

func suggestOperator(src common.Source, e *cel.Error) string {
	if !strings.Contains(e.Message, "token recognition error") {
		return ""
	}
	tok := strings.TrimSpace(celtlint.BtName(celtlint.BacktickQuotes(e.Message)))
	if tok != "=" {
		return ""
	}

	line, ok := src.Snippet(e.Location.Line())
	if !ok {
		return ""
	}
	idx := strings.Index(line, tok)
	if idx < 0 {
		return ""
	}
	lineNo := e.Location.Line()
	width := len(strconv.Itoa(lineNo))
	newLine := line[:idx] + "==" + line[idx+1:]
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: you might have meant to compare for equality\n", celtlint.Blue("="), celtlint.WhiteBold("help"))
	fmt.Fprintf(&b, "%s\n", gutterLine(width, ""))
	fmt.Fprintf(&b, "%s%s\n", gutterLine(width, strconv.Itoa(lineNo)), newLine)
	fmt.Fprintf(&b, "%s%s%s\n", gutterLine(width, ""), strings.Repeat(" ", idx+1), celtlint.Green("+"))
	fmt.Fprintf(&b, "%s", gutterLine(width, ""))
	return b.String()
}

func isNameError(msg string) bool {
	return strings.Contains(msg, "undeclared reference") ||
		strings.Contains(msg, "undefined field") ||
		strings.Contains(msg, "no such field") ||
		strings.Contains(msg, "does not support field selection") ||
		strings.Contains(msg, "no matching overload") ||
		strings.Contains(msg, "unknown")
}

func namespaceMember(line, ns string) string {
	idx := strings.Index(line, ns+".")
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(ns)+1:]
	end := 0
	for end < len(rest) && celtsource.IsIdentByte(rest[end]) {
		end++
	}
	if end == 0 {
		return ""
	}
	return fmt.Sprintf("%s.%s", ns, rest[:end])
}

func tokenAt(line string, col int) string {
	if col < 0 || col >= len(line) {
		return ""
	}
	start := col
	for start > 0 && celtsource.IsIdentByte(line[start-1]) {
		start--
	}
	end := col
	for end < len(line) && celtsource.IsIdentByte(line[end]) {
		end++
	}
	return line[start:end]
}

func renderSuggestion(line, token, repl, kind string, lineNo int) string {
	newLine := strings.Replace(line, token, repl, 1)
	num := celtlint.Blue(strconv.Itoa(lineNo))
	minus := colorSegment(line, strings.Index(line, token), len(token), celtlint.Red)
	plus := colorSegment(newLine, strings.Index(newLine, repl), len(repl), celtlint.Green)
	return renderDiff(num, minus, plus, "a %s with a similar name exists", kind)
}

func colorSegment(line string, start, length int, fn func(string) string) string {
	if start < 0 || length <= 0 || start+length > len(line) {
		return line
	}
	return line[:start] + fn(line[start:start+length]) + line[start+length:]
}

func renderDiff(num, minus, plus, helpFormat string, helpArgs ...any) string {
	var b strings.Builder
	b.WriteString(celtlint.HintLabel())
	fmt.Fprintf(&b, helpFormat+"\n", helpArgs...)
	fmt.Fprintf(&b, "%s\n", celtlint.Blue("  |"))
	fmt.Fprintf(&b, "%s %s %s\n", num, celtlint.Red("-"), minus)
	fmt.Fprintf(&b, "%s %s %s\n", num, celtlint.Green("+"), plus)
	fmt.Fprintf(&b, "%s", celtlint.Blue("  |"))
	return b.String()
}

func gutter(width int, num string) string {
	return fmt.Sprintf("%*s | ", width, num)
}

func gutterLine(width int, num string) string {
	return celtlint.Blue(gutter(width, num))
}
