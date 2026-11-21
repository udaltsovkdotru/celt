package celtlint

import (
	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"

	"github.com/udaltsovkdotru/celt/celtsource"
)

// MacroCall describes a single macro invocation recorded in a checked AST. It
// is available only when the env was built with macro call tracking enabled.
type MacroCall struct {
	// ID is the expression id of the node produced by expanding the macro.
	ID int64
	// Name is the macro function name as written in the call.
	Name string
	// Offset and End bound the call's source range (inclusive start, exclusive
	// end) as byte offsets into the source text.
	Offset int
	End    int
	// Expansion is the unparsed expression the macro expanded to.
	Expansion string
	// ResultType is the CEL type of the expanded expression.
	ResultType string
}

// MacroCallsIn returns every macro invocation recorded in a checked AST. ro is
// the rune offsets of the source lines, content the source text, and env the
// environment the AST was checked against (used to re-expand consumed macros).
func MacroCallsIn(checked *celast.AST, ro []int, content string, env *cel.Env) []MacroCall {
	if checked == nil || !checked.IsChecked() {
		return nil
	}
	info := checked.SourceInfo()
	out := make([]MacroCall, 0, len(info.MacroCalls()))
	for id := range info.MacroCalls() {
		out = append(out, *macroCallFor(checked, ro, content, env, id))
	}
	return out
}

// MacroCallAt returns the innermost macro invocation whose source span
// contains the byte offset off, if any. ro is the rune offsets of the source
// lines, content the source text, and env the environment the AST was checked
// against.
func MacroCallAt(checked *celast.AST, ro []int, content string, env *cel.Env, off int) (*MacroCall, bool) {
	if checked == nil || !checked.IsChecked() {
		return nil, false
	}
	var best *MacroCall
	for id := range checked.SourceInfo().MacroCalls() {
		c := macroCallFor(checked, ro, content, env, id)
		// End is exclusive: a cursor exactly at End sits just past the closing
		// paren and is not inside the call.
		if off < c.Offset || off >= c.End {
			continue
		}
		if best == nil || c.End-c.Offset < best.End-best.Offset {
			best = c
		}
	}
	return best, best != nil
}

func macroCallFor(checked *celast.AST, ro []int, content string, env *cel.Env, id int64) *MacroCall {
	info := checked.SourceInfo()
	original, _ := info.GetMacroCall(id)
	name := macroName(original)
	paren := 0
	if r, ok := info.GetOffsetRange(id); ok {
		paren = celtsource.ByteOffset(ro, int(r.Start))
	}
	start, end := macroSpan(content, name, paren)
	expansion, reparsedType := macroExpansion(checked, content, ro, env, id, start, end)
	resultType := celTypeName(checked.GetType(id))
	if resultType == dynTypeName && reparsedType != "" {
		resultType = reparsedType
	}
	return &MacroCall{
		ID:         id,
		Name:       name,
		Offset:     start,
		End:        end,
		Expansion:  expansion,
		ResultType: resultType,
	}
}

// macroSpan bounds a macro invocation's source range. The macro node's own
// offset range points at its argument list, which may be preceded by whitespace
// or comments; the span is opened over the actual function name and closed at
// the balanced parenthesis, ignoring strings and `//` comments (a `)` inside a
// comment is not a delimiter).
func macroSpan(content, name string, paren int) (start, end int) {
	open := macroOpenParen(content, paren)
	if open < 0 {
		return 0, len(content)
	}
	start = macroNameStart(content, open, len(name))
	if start < 0 {
		start = 0
	}
	depth := 0
	for i := open; i < len(content); i++ {
		switch {
		case content[i] == '\'' || content[i] == '"':
			// Skip string literals so parens/braces inside them don't count.
			i = celtsource.SkipString(content, i) - 1
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '/':
			// Skip `//` comments so a `)` inside the comment text does not
			// count as a delimiter.
			for i < len(content) && content[i] != '\n' {
				i++
			}
			i--
		case content[i] == '(':
			depth++
		case content[i] == ')':
			depth--
			if depth == 0 {
				return start, i + 1
			}
		}
	}
	return start, len(content)
}

// macroOpenParen scans forward from from for the argument-list `(`, skipping
// whitespace, strings and comments, and returns its index (-1 if not found).
func macroOpenParen(content string, from int) int {
	i := from
	for i < len(content) {
		c := content[i]
		switch {
		case c == '\'' || c == '"':
			i = celtsource.SkipString(content, i)
		case c == '/' && i+1 < len(content) && content[i+1] == '/':
			for i < len(content) && content[i] != '\n' {
				i++
			}
		case c == '(':
			return i
		default:
			i++
		}
	}
	return -1
}

// macroNameStart scans back from the argument-list `(` over whitespace and `//`
// comment text (whether the comment trails the name or sits between it and the
// `(`) to the end of the function name, then back over nameLen bytes to its
// start.
func macroNameStart(content string, open, nameLen int) int {
	i := open - 1
	for i >= 0 {
		if celtsource.IsSpace(content[i]) {
			i--
			continue
		}
		// If i lies inside a `//` comment, skip back past its start so the name
		// is not located inside the comment text.
		if cstart := commentStartOnLine(content, i); cstart >= 0 {
			i = cstart - 1
			continue
		}
		break
	}
	return i - nameLen + 1
}

// commentStartOnLine returns the offset of the `//` beginning the comment
// containing i, or -1 when i is not inside a comment on its line. String
// literals are skipped so a `//` inside a string (e.g. the URL in
// `"https://x" && f()`) is not mistaken for a comment marker.
func commentStartOnLine(content string, i int) int {
	ls := i
	for ls > 0 && content[ls-1] != '\n' {
		ls--
	}
	j := ls
	for j+1 < len(content) && j <= i {
		c := content[j]
		if c == '\'' || c == '"' {
			j = celtsource.SkipString(content, j)
			continue
		}
		if c == '/' && content[j+1] == '/' {
			return j
		}
		j++
	}
	return -1
}

// macroExpansion renders what a macro call expanded to. When the expansion
// node is still in the AST it is unparsed without the source's macro call map
// (so the actual expansion is rendered, not the original call). Otherwise the
// macro's node was consumed by an enclosing macro, so the call's source text
// is re-parsed with the env to regenerate the expansion. The second return is
// the result type recovered from a re-parse (empty otherwise).
func macroExpansion(checked *celast.AST, content string, _ []int, env *cel.Env, id int64, start, end int) (expansion, reparsedType string) {
	if node, ok := findExprByID(checked.Expr(), id); ok {
		if s, handled := unparseExpansion(node); handled && compiles(env, s) {
			return s, ""
		}
	}
	callText := ""
	if start >= 0 && start <= end && end <= len(content) {
		callText = content[start:end]
	}
	// A receiver macro's span starts at the method name without its receiver
	// (e.g. `all(x, x>0)` from `[1].all(...)`), which is not a valid standalone
	// expression, and the desugared form (a comprehension) cannot be rendered.
	// Omit the expansion rather than publish a fragment or the original call,
	// neither of which is a genuine expansion.
	if env == nil || callText == "" || !compiles(env, callText) {
		return "", ""
	}
	ast, iss := env.Compile(callText)
	if iss != nil && len(iss.Errors()) > 0 {
		return "", ""
	}
	root := ast.NativeRep().Expr()
	if root == nil {
		return "", ""
	}
	if s, handled := unparseExpansion(root); handled {
		return s, celTypeName(ast.OutputType())
	}
	return "", ""
}

// compiles reports whether s is a complete, type-checkable expression.
func compiles(env *cel.Env, s string) bool {
	if env == nil || s == "" {
		return false
	}
	_, iss := env.Compile(s)
	return iss == nil || len(iss.Errors()) == 0
}

// unparseExpansion renders an expression without the source's macro call map
// (so the actual expansion is shown, not the original call). A presence test
// (has(x.f)) has no textual form other than has(...), so it yields no distinct
// expansion. The bool reports whether the node was handled.
func unparseExpansion(node celast.Expr) (string, bool) {
	if node.Kind() == celast.SelectKind && node.AsSelect().IsTestOnly() {
		return "", true
	}
	s, err := parser.Unparse(node, macroFreeInfo())
	if err != nil || s == "" {
		return "", false
	}
	return s, true
}

func macroFreeInfo() *celast.SourceInfo {
	return celast.NewSourceInfo(common.NewStringSource("", ""))
}

func macroName(e celast.Expr) string {
	if e == nil || e.Kind() != celast.CallKind {
		return ""
	}
	return e.AsCall().FunctionName()
}

func findExprByID(root celast.Expr, id int64) (celast.Expr, bool) {
	var found celast.Expr
	celast.PreOrderVisit(root, celast.NewExprVisitor(func(e celast.Expr) {
		if found == nil && e.ID() == id {
			found = e
		}
	}))
	return found, found != nil
}
