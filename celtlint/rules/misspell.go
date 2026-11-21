package rules

import (
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
	"github.com/udaltsovkdotru/celt/pkg/celtspell"
)

// Misspell flags unknown identifiers and suggests corrections.
type Misspell struct {
	celtlint.RuleMeta
}

func (Misspell) Check(ctx *celtlint.Context) {
	if ctx.Index == nil {
		return
	}
	// Comprehension locals are valid scoped names: they must not be renamed
	// within their own comprehension, but a same-spelled identifier OUTSIDE the
	// comprehension is still a genuine misspelling (e.g. the first `userNme` in
	// `userNme == "" && [1].all(userNme, userNme > 0)`). Derive the lexical
	// scope of each binding and suppress only occurrences inside it. A failed
	// compile leaves no checked AST, so fall back to a parsed AST (parsing does
	// not require type checking and succeeds despite unrelated compile errors).
	var scopes []boundName
	if content := ctx.Source.Content(); content != "" {
		ro := celtsource.RuneOffsets(content)
		if ctx.Ast != nil {
			scopes = comprehensionScopes(ctx.Ast, content, ro)
		} else if ctx.Env != nil {
			if parsed, iss := ctx.Env.Parse(content); parsed != nil && (iss == nil || iss.Err() == nil) {
				scopes = comprehensionScopes(parsed, content, ro)
			}
		}
	}
	ctx.ReportAll(checkMisspellings(ctx.Source, ctx.Index, scopes))
}

// boundName records the source range over which a comprehension binding is in
// scope, so a name is only suppressed where it is actually bound, not across
// the whole document.
type boundName struct {
	name  string
	start int
	end   int
}

// comprehensionScopes collects the names bound by comprehensions (iter, accu
// and second iter variables) together with the byte range of each binding's
// scope. These are valid locals scoped to their comprehension (e.g. the
// argument of `.all(...)`, `.map(...)`, `.filter(...)`), so the misspell
// scanner must not offer to rename them inside that scope, while a same-named
// identifier outside it remains a candidate. It works on both a checked AST
// (where macros are expanded to Comprehension nodes) and a parsed AST (where
// an environment may leave a comprehension macro as an unexpanded Call, whose
// first argument is the iteration variable).
func comprehensionScopes(ast *cel.Ast, content string, ro []int) []boundName {
	var out []boundName
	if ast == nil {
		return out
	}
	native := ast.NativeRep()
	info := native.SourceInfo()
	celtsource.Walk(native.Expr(), func(e celast.Expr) {
		names := comprehensionNames(e)
		if len(names) == 0 {
			return
		}
		start, end := comprehensionScopeRange(info, content, ro, e)
		for _, n := range names {
			out = append(out, boundName{name: n, start: start, end: end})
		}
	})
	return out
}

// comprehensionNames returns the variables a comprehension binds (iter, second
// iter and accumulator), or the first-argument iteration variable of an
// unexpanded comprehension macro call.
func comprehensionNames(e celast.Expr) []string {
	var names []string
	switch e.Kind() {
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		if n := c.IterVar(); n != "" {
			names = append(names, n)
		}
		if c.HasIterVar2() {
			if n := c.IterVar2(); n != "" {
				names = append(names, n)
			}
		}
		if n := c.AccuVar(); n != "" {
			names = append(names, n)
		}
	case celast.CallKind:
		if isComprehensionMacro(e.AsCall().FunctionName()) {
			args := e.AsCall().Args()
			if len(args) > 0 && args[0].Kind() == celast.IdentKind {
				if n := args[0].AsIdent(); n != "" {
					names = append(names, n)
				}
			}
		}
	}
	return names
}

// comprehensionScopeRange returns the byte span [start, end) in which a
// comprehension's bound variables are visible. A comprehension's own offset
// range is degenerate: its start is the argument-list `(` and there is no stop.
// The iter/accu variables are bound within the call's parentheses, so the scope
// runs from that opening paren to the matching close.
func comprehensionScopeRange(info *celast.SourceInfo, content string, ro []int, e celast.Expr) (start, end int) {
	start, end = -1, -1
	if r, ok := info.GetOffsetRange(e.ID()); ok {
		start = celtsource.ByteOffset(ro, int(r.Start))
		if start >= 0 && start < len(content) {
			end = macroScopeEnd(content, start)
		}
	}
	return start, end
}

// macroScopeEnd returns the byte index just past the `)` that balances the `(`
// at open, skipping strings and `//` comments so a paren in either does not
// count as a delimiter.
func macroScopeEnd(content string, open int) int {
	depth := 0
	for i := open; i < len(content); i++ {
		switch {
		case content[i] == '\'' || content[i] == '"':
			i = celtsource.SkipString(content, i) - 1
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '/':
			for i < len(content) && content[i] != '\n' {
				i++
			}
			i--
		case content[i] == '(':
			depth++
		case content[i] == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(content)
}

// isComprehensionMacro reports whether a (possibly qualified) function name is
// one of the standard comprehension macros, whose first argument is the
// iteration variable.
func isComprehensionMacro(name string) bool {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "all", "exists", "exists_one", "filter", "map":
		return true
	}
	return false
}

// checkMisspellings scans the source for identifiers, functions, enum values
// and fields that are unknown and, where a close real name exists, reports a
// finding with a suggested fix. scopes bounds comprehension locals so a name is
// only suppressed inside the comprehension that binds it.
func checkMisspellings(src common.Source, idx *celtindex.Index, scopes []boundName) []celtlint.Finding {
	content := src.Content()
	var out []celtlint.Finding
	i, n := 0, len(content)
	recv := ""
	member := false
	braceDepth := 0
	// condPending[braceDepth] counts ternary `?` at that brace depth not yet
	// matched by a `:`. A `:` that closes a ternary belongs to the conditional,
	// not to a map/constructor field label, so an identifier before it must be
	// treated as a free expression operand rather than a label. The count is
	// per brace depth so a ternary outside a constructor (`true ? Person{Age: 1}
	// : p`) does not turn a field label inside the constructor's braces into an
	// operand: the `?` sits at depth 0 while the constructor's `Age:` sits at
	// depth 1.
	condPending := map[int]int{}
	for i < n {
		c := content[i]
		switch {
		case isComment(content, i):
			i = skipLineComment(content, i)
		case c == '"' || c == '\'':
			i = celtsource.SkipString(content, i)
		case c == '?':
			// A `?` in an optional select (`p.?field`) is not the conditional
			// operator; only a ternary's `?` opens a pending `:`. The two are
			// told apart by whether the `?` follows a `.`.
			if !optionalSelectQ(content, i) {
				condPending[braceDepth]++
			}
			i++
		case c == ':':
			if condPending[braceDepth] > 0 {
				condPending[braceDepth]--
			}
			i++
		case c == '`':
			// An escaped identifier (`...`, the CEL identifier escape syntax) is
			// a single token. Skip it wholesale so the scanner does not treat
			// part of it (e.g. `userNme` in `userNme-other`) as a free
			// identifier and offer to rewrite a valid field name.
			i = skipEscapedIdentifier(content, i)
			recv = ""
			member = false
		case c == '.':
			member = true
			i++
		case c == '{':
			braceDepth++
			i++
		case c == '}':
			if braceDepth > 0 {
				braceDepth--
			}
			i++
		case celtsource.IsIdentStart(c):
			var end int
			var braceInc int
			var identFindings []celtlint.Finding
			end, recv, braceInc, identFindings = scanIdentifier(content, idx, scopes, i, recv, member, braceDepth, condPending)
			out = append(out, identFindings...)
			braceDepth += braceInc
			i = end
		default:
			member = false
			i++
		}
	}
	return out
}

// skipLineComment advances past the remainder of a `//` comment to the end of
// the line.
func skipLineComment(content string, i int) int {
	n := len(content)
	for i < n && content[i] != '\n' {
		i++
	}
	return i
}

// skipEscapedIdentifier advances past a backtick-delimited escaped identifier
// (`...`), including the closing backtick.
func skipEscapedIdentifier(content string, i int) int {
	i++ // consume the opening backtick
	n := len(content)
	for i < n && content[i] != '`' {
		i++
	}
	if i < n {
		i++
	}
	return i
}

// scanIdentifier processes one identifier token starting at start: it advances
// past the token, updates the receiver/member state, and reports any misspell
// findings for it. It returns the byte index just past the token, the updated
// receiver/member state, a brace-depth increment (a message constructor opens a
// brace), and any findings.
func scanIdentifier(content string, idx *celtindex.Index, scopes []boundName,
	start int, recv string, member bool, braceDepth int,
	condPending map[int]int) (end int, newRecv string,
	braceInc int, findings []celtlint.Finding) {
	i := start
	n := len(content)
	for i < n && celtsource.IsIdentByte(content[i]) {
		i++
	}
	tok := content[start:i]
	switch {
	case start > 0 && content[start-1] >= '0' && content[start-1] <= '9':
		// Fragment of a numeric literal (e.g. the `e` in `1e10`, the
		// `x` in `0x1F`), not an identifier.
		return i, "", 0, nil
	case isStringPrefix(tok) && i < n && (content[i] == '"' || content[i] == '\''):
		// A raw/bytes string prefix (r, b, rb, br and their uppercase
		// variants) followed by a quote is part of the string literal,
		// not a misspellable identifier. Skip the whole string.
		i = celtsource.SkipString(content, i)
		return i, "", 0, nil
	case nextSignificantIsBrace(content, i):
		// A message constructor type name (`Foo{...}` or `Foo {...}`).
		// The type and its field labels are names in their own right,
		// not free identifiers to rename (e.g. Foo must not become
		// bool). Keep the type as the receiver so a trailing `.field`
		// resolves.
		for i < n && celtsource.IsSpace(content[i]) {
			i++
		}
		i++ // consume '{'
		return i, tok, 1, nil
	case isFieldLabel(content, i, braceDepth, condPending):
		// A field label inside braces (`Foo{names: ...}`, `{"key": ...}`).
		// It is a member of the message, not a free identifier, so do
		// not offer to rename it. The colon must not close a ternary: an
		// identifier before a conditional's `:` inside braces is an
		// operand, not a label, so it is only suppressed when pendingColon
		// is 0.
		return i, recv, 0, nil
	}
	findings = misspellCandidate(idx, content, scopes, start, i, tok, member, recv)
	return i, appendReceiver(recv, tok, member), 0, findings
}

// isFieldLabel reports whether the identifier at i is a map or message
// constructor field label: it sits inside braces whose `:` does not close a
// pending ternary.
func isFieldLabel(content string, i, braceDepth int, condPending map[int]int) bool {
	return braceDepth > 0 && condPending[braceDepth] == 0 && nextSignificantIsColon(content, i)
}

// misspellCandidate reports the misspell findings for the identifier token
// [start, i), unless it is a comprehension-bound name, a member-function call,
// or a type-constructor chain segment.
func misspellCandidate(idx *celtindex.Index, content string, scopes []boundName,
	start, i int, tok string, member bool, recv string) []celtlint.Finding {
	if isBound(scopes, tok, start, i) {
		return nil
	}
	// A name in call position after a dot is a member-function call;
	// when the receiver declares that function, it is valid even if
	// a same-named (or similar) field exists (e.g. `w.rename()` when
	// `rename` is a member function and `w` has a `name` field). The
	// `(` may be separated from the name by whitespace or a comment
	// (`w.rename ()`, `w.rename // call\n ()`), so skip trivia
	// before classifying the next token.
	if !member || !nextSignificantIsOpenParen(content, i) || !idx.MemberFunction(recv, tok) {
		if !isTypeConstructorChain(content, i) {
			return identFindings(idx, content, start, i, tok, member, recv)
		}
	}
	return nil
}

// appendReceiver extends recv with a newly-scanned member token tok. A member
// segment keeps the accumulated receiver (`a.b.c`); a non-member token starts
// a fresh receiver.
func appendReceiver(recv, tok string, member bool) string {
	if !member {
		return tok
	}
	if recv == "" {
		return tok
	}
	return recv + "." + tok
}

// nextSignificantIsColon reports whether the next non-whitespace character at
// or after i is a ':' (i.e. the identifier is a map/constructor field label).
func nextSignificantIsColon(content string, i int) bool {
	for i < len(content) && celtsource.IsSpace(content[i]) {
		i++
	}
	return i < len(content) && content[i] == ':'
}

// nextSignificantIsBrace reports whether the next non-whitespace character at
// or after i is a '{', which marks a message constructor (the `{` may be
// separated from the type name by whitespace, as in `Foo {x: "a"}`).
func nextSignificantIsBrace(content string, i int) bool {
	for i < len(content) && celtsource.IsSpace(content[i]) {
		i++
	}
	return i < len(content) && content[i] == '{'
}

// nextSignificantIsOpenParen reports whether the next significant character at
// or after i is a '(', skipping whitespace and `//` comments. It is used to
// detect a member call whose argument list is separated from the method name by
// trivia (e.g. `w.rename ()` or `w.rename // call\n ()`).
func nextSignificantIsOpenParen(content string, i int) bool {
	for i < len(content) {
		c := content[i]
		if c == '/' && i+1 < len(content) && content[i+1] == '/' {
			for i < len(content) && content[i] != '\n' {
				i++
			}
			continue
		}
		if celtsource.IsSpace(c) {
			i++
			continue
		}
		return c == '('
	}
	return false
}

// optionalSelectQ reports whether the `?` at i is an optional-select operator
// (`p.?field`) rather than the conditional operator (`cond ? a : b`). An
// optional select's `?` immediately follows a `.`; a ternary's does not.
func optionalSelectQ(content string, i int) bool {
	j := i - 1
	for j >= 0 && celtsource.IsSpace(content[j]) {
		j--
	}
	return j >= 0 && content[j] == '.'
}

// isTypeConstructorChain reports whether the text following the identifier that
// ends at i is a dotted type name ending in `{`, i.e. a message constructor
// such as `one.A{...}`. A leading segment of such a chain (e.g. `one`) is a
// type namespace, not a free identifier, so it must not be flagged as unknown.
func isTypeConstructorChain(content string, i int) bool {
	j := i
	for {
		next, opens := advanceTypeChainSegment(content, j)
		if opens {
			return true
		}
		if next < 0 {
			return false
		}
		j = next
	}
}

// advanceTypeChainSegment consumes one `.name` segment of a type chain starting
// just after the previous name and reports whether the chain terminates in `{`.
// It returns the position after the consumed name (for a further segment) and
// whether that name was followed by `{`.
func advanceTypeChainSegment(content string, j int) (next int, opensBrace bool) {
	j = skipTypeSpace(content, j)
	if j >= len(content) || content[j] != '.' {
		return -1, false
	}
	j++
	if j >= len(content) || !celtsource.IsIdentStart(content[j]) {
		return -1, false
	}
	for j < len(content) && celtsource.IsIdentByte(content[j]) {
		j++
	}
	k := skipTypeSpace(content, j)
	return j, k < len(content) && content[k] == '{'
}

// skipTypeSpace advances j past the horizontal whitespace within a type chain.
func skipTypeSpace(content string, j int) int {
	for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\r') {
		j++
	}
	return j
}

// isBound reports whether the token [start, end) falls inside the scope of a
// comprehension binding of the same name.
func isBound(scopes []boundName, tok string, start, end int) bool {
	for _, s := range scopes {
		if s.name == tok && s.start <= start && end <= s.end {
			return true
		}
	}
	return false
}

func identFindings(idx *celtindex.Index, content string, start, end int, tok string, member bool, receiver string) []celtlint.Finding {
	if idx.Valid(tok) && !member {
		return nil
	}
	if member {
		kind, cands := idx.MemberCandidatesPath(receiver)
		if kind == celtindex.MemberNone || slices.ContainsFunc(cands, func(c celtindex.Candidate) bool { return c.Name == tok }) {
			return nil
		}
		switch kind {
		case celtindex.MemberEnumValue, celtindex.MemberObjField:
			code, msg := celtlint.CodeUnknownEnumVariant, "unknown enum variant"
			if kind == celtindex.MemberObjField {
				code, msg = celtlint.CodeUnknownField, "unknown field"
			}
			repl, hasMatch := match(idx, tok, cands)
			return []celtlint.Finding{unknownError(code, msg, repl, hasMatch, content, start, end)}
		default:
			if repl, hasMatch := match(idx, tok, cands); hasMatch {
				return []celtlint.Finding{unknownError(celtlint.CodeUnknownFunction,
					"unknown function", repl, true, content, start, end)}
			}
		}
		return nil
	}
	if repl, hasMatch := match(idx, tok, nil); hasMatch {
		return []celtlint.Finding{unknownError(celtlint.CodeUnknownIdentifier,
			"unknown identifier", repl, true, content, start, end)}
	}
	return nil
}

func unknownError(code, msg, repl string, hasMatch bool, content string, start, end int) celtlint.Finding {
	f := celtlint.Finding{
		Level:   celtlint.LevelError,
		Code:    code,
		Message: msg,
		Start:   celtsource.OffsetToPos(content, start),
		End:     celtsource.OffsetToPos(content, end),
	}
	if hasMatch {
		f.Fix = repl
	}
	return f
}

// match returns the closest candidate name to token, if any is within the
// spell threshold.
func match(idx *celtindex.Index, token string, cands []celtindex.Candidate) (string, bool) {
	if len(cands) == 0 {
		cands = idx.Candidates()
	}
	lb := strings.ToLower(token)
	if lb == "" {
		return "", false
	}
	if c, ok := celtspell.Closest(lb, cands); ok {
		return c.Name, true
	}
	return "", false
}

func isComment(content string, i int) bool {
	return i+1 < len(content) && content[i] == '/' && content[i+1] == '/'
}

// isStringPrefix reports whether tok is a CEL raw/bytes string prefix: a single
// r/R or b/B, or the two-letter combination of one bytes char and one raw char
// (rb, br and their uppercase forms). Matches the prefixes the CEL lexer accepts
// before a quote.
func isStringPrefix(tok string) bool {
	if len(tok) == 1 {
		return isBytesChar(tok[0]) || isRawChar(tok[0])
	}
	if len(tok) == 2 {
		hasBytes, hasRaw := false, false
		for i := range 2 {
			switch {
			case isBytesChar(tok[i]):
				hasBytes = true
			case isRawChar(tok[i]):
				hasRaw = true
			default:
				return false
			}
		}
		return hasBytes && hasRaw
	}
	return false
}

func isBytesChar(c byte) bool { return c == 'b' || c == 'B' }
func isRawChar(c byte) bool   { return c == 'r' || c == 'R' }

// DropRedundantCompileErrors removes compile errors that are already covered by
// a misspell finding against the same name, or by a macro-call typo.
func DropRedundantCompileErrors(findings []celtlint.Finding, _ string, idx *celtindex.Index, reported map[string]bool) []celtlint.Finding {
	out := findings[:0]
	for i := range findings {
		f := &findings[i]
		if !isCompileError(f.Code) {
			out = append(out, *f)
			continue
		}
		detail := f.Detail
		if detail == "" {
			detail = f.Message
		}
		if name := undeclaredName(detail); name != "" {
			if reported[name] {
				continue
			}
		}
		if name := undefinedField(detail); name != "" && reported[name] {
			continue
		}
		if isCheckMacroError(detail, idx.MacroKeywords()) && reportedMacroTypo(reported, idx.MacroKeywords()) {
			continue
		}
		out = append(out, *f)
	}
	return out
}

func isCheckMacroError(msg string, macros []string) bool {
	for _, m := range macros {
		if strings.Contains(msg, m+"(") {
			return true
		}
	}
	return false
}

func reportedMacroTypo(reported map[string]bool, macros []string) bool {
	for name := range reported {
		lb := strings.ToLower(name)
		if lb == "" {
			continue
		}
		for _, mk := range macros {
			if celtspell.WithinThreshold(celtspell.Levenshtein(lb, strings.ToLower(mk)), len(lb)) {
				return true
			}
		}
	}
	return false
}

// ReportedNames returns the set of tokens already covered by an unknown-name
// finding, used to dedup compile errors.
func ReportedNames(findings []celtlint.Finding, content string) map[string]bool {
	reported := map[string]bool{}
	for i := range findings {
		f := &findings[i]
		if f.Code != celtlint.CodeUnknownField && f.Code != celtlint.CodeUnknownFunction && f.Code != celtlint.CodeUnknownIdentifier {
			continue
		}
		start := clampOffset(celtsource.PosToOffset(content, f.Start), content)
		end := clampOffset(celtsource.PosToOffset(content, f.End), content)
		if end > start && end <= len(content) {
			reported[content[start:end]] = true
		}
	}
	return reported
}

func undeclaredName(msg string) string {
	const marker = "undeclared reference to `"
	if i := strings.Index(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.IndexByte(rest, '`'); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

func undefinedField(msg string) string {
	const marker = "undefined field `"
	if i := strings.Index(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.IndexByte(rest, '`'); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

func clampOffset(off int, content string) int {
	if off < 0 {
		return 0
	}
	if off > len(content) {
		return len(content)
	}
	return off
}
