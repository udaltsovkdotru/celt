package celtfmt

import (
	"fmt"
	"strconv"
	"strings"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	celtypes "cel.dev/cel-go/common/types"
	celref "cel.dev/cel-go/common/types/ref"

	"github.com/udaltsovkdotru/celt/celtsource"
)

type node interface {
	isNode()
	mustBreak() bool
	setBreak(bool)
}

// baseNode carries the force-break flag derived from BreakRules at build time.
type baseNode struct{ brk bool }

func (n *baseNode) mustBreak() bool { return n.brk }
func (n *baseNode) setBreak(v bool) { n.brk = v }

type ident struct {
	baseNode
	text string
}
type lit struct {
	baseNode
	text string
}
type sel struct {
	baseNode
	obj   node
	field string
	// comments holds `//` comment lines that fall between the selector's dot
	// and its field (e.g. `a.// dot\nb`), so they survive a rewrite.
	comments []elem
}
type idx struct {
	baseNode
	obj      node
	index    node
	optional bool
	// leadComments holds `//` comment lines that fall between the receiver and
	// the opening bracket (e.g. `a // receiver\n [b]`), so they survive a
	// rewrite instead of being dropped.
	leadComments []elem
	// comments holds `//` comment lines that fall between the opening bracket
	// and the index (e.g. `a[ // index\n 0]`), so they survive a rewrite.
	comments []elem
	// afterComments holds `//` comment lines that fall between the index and
	// the closing bracket (e.g. `a[1 // inner\n]`), so they survive a rewrite.
	afterComments []elem
}
type callArg struct {
	value   node
	comment string
	// commentPresent records that a `//` comment was actually seen for this
	// argument, even when its body is empty (e.g. `a, //`). This lets an empty
	// comment be preserved instead of being conflated with "no comment".
	commentPresent bool
	// start is the byte offset of the argument's first token, used to
	// interleave standalone comment lines with arguments in source order.
	start int
	// standalone marks a `//` comment that sits on its own line inside a call,
	// rendered as a comment-only line rather than an argument.
	standalone bool
}
type call struct {
	baseNode
	name node
	args []callArg
	// leadComments holds `//` comment lines that fall between the callee name
	// and its argument-list `(` (e.g. `foo // callee\n (a,b)`), so they survive
	// a rewrite instead of being dropped.
	leadComments []elem
}
type listLit struct {
	baseNode
	elems []elem
}
type mapLit struct {
	baseNode
	elems []elem
}
type bin struct {
	baseNode
	op   string
	l, r node
	// comments holds standalone `//` comment lines that fall between the two
	// operands, so a comment inside a grouped binary expression stays put.
	comments []elem
}
type un struct {
	baseNode
	op string
	x  node
	// comments holds `//` comment lines that fall between the operator and its
	// operand (e.g. `! // unary\n a`), so they survive a rewrite.
	comments []elem
}
type cond struct {
	baseNode
	c, t, f node
	// tComments/fComments hold standalone `//` comment lines that fall between
	// the condition and the true branch / the true branch and the false branch,
	// so a comment inside a conditional keeps its position across a rewrite.
	tComments []elem
	fComments []elem
}
type paren struct {
	baseNode
	x node
	// before/after hold standalone `//` comment lines between the grouping
	// parentheses that precede / follow the expression, so their position
	// relative to it is preserved across a rewrite.
	before []elem
	after  []elem
}

type elem struct {
	key     node
	value   node
	comment string
	// commentPresent records that a `//` comment was actually seen for this
	// element, even when its body is empty (e.g. `[1, //]`). This lets an empty
	// comment be preserved instead of being conflated with "no comment".
	commentPresent bool
	// start is the byte offset of the element's first token, used to interleave
	// standalone comment lines with elements in source order.
	start int
	// standalone marks a `//` comment that sits on its own line inside a
	// container, rendered as a comment-only line rather than an element.
	standalone bool
	// comments holds `//` comment lines that fall between a map key and its
	// value (e.g. `{a // key\n : b}` or `{'a': // first\n 1}`), rendered between
	// the key and the value so they survive a rewrite even when the value also
	// carries a trailing comment of its own.
	comments []elem
}

func (*ident) isNode()   {}
func (*lit) isNode()     {}
func (*sel) isNode()     {}
func (*idx) isNode()     {}
func (*call) isNode()    {}
func (*listLit) isNode() {}
func (*mapLit) isNode()  {}
func (*bin) isNode()     {}
func (*un) isNode()      {}
func (*cond) isNode()    {}
func (*paren) isNode()   {}

const (
	opAnd = "&&"
	opOr  = "||"
)

// leadingDoc extracts the leading comment block(s) preceding the expression,
// preserving blank-line-separated blocks (e.g. `// block one\n\n// block two`).
// It stops at the first non-comment, non-blank line.
func leadingDoc(src string) []string {
	var doc []string
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "//") {
			doc = append(doc, trimmed)
			i++
			continue
		}
		if trimmed == "" {
			// A blank line separates blocks; keep a single blank line only when
			// another comment block follows before the expression.
			j := i
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "//") {
				doc = append(doc, "")
				i = j
				continue
			}
		}
		break
	}
	return doc
}

type celConverter struct {
	src     string
	ast     *celast.AST
	info    *celast.SourceInfo
	spans   map[int64][2]int
	byteOff []int
	breaks  []BreakRule
	// claimedComments tracks `//` comment start offsets already owned by a
	// nested owner (a binary operand gap or a conditional branch gap). Because
	// such constructs have no delimiters, an enclosing container's
	// standaloneComments would otherwise claim the same comment, duplicating
	// it in the output.
	claimedComments map[int]bool
}

// claim records that the comment at start is owned by a nested construct, so
// enclosing containers do not also claim it.
func (b *celConverter) claim(start int) {
	if b.claimedComments != nil {
		b.claimedComments[start] = true
	}
}

// claimed reports whether the comment at start is already owned elsewhere.
func (b *celConverter) claimed(start int) bool {
	return b.claimedComments != nil && b.claimedComments[start]
}

// toByte converts a cel-go source offset (rune-based) to a byte offset.
func (b *celConverter) toByte(ro int) int {
	return celtsource.ByteOffset(b.byteOff, ro)
}

// docTrailingComment returns the `//` comment that trails the root expression
// on the same line, skipping closing parentheses and whitespace first. The AST
// span of a parenthesized root excludes its closing parens, so they are skipped
// to reach a trailing comment such as `(a+b) // tail`. It returns the comment
// body and whether a comment was present (an empty body still counts as
// present).
func (b *celConverter) docTrailingComment(from int) (string, bool) {
	i := from
	for i < len(b.src) {
		switch b.src[i] {
		case ' ', '\t', '\r', ')':
			i++
		case '\n':
			// The trailing comment must be on the expression's line.
			return "", false
		default:
			return b.commentAt(i, len(b.src))
		}
	}
	return "", false
}

// trailingRootComments returns the standalone `//` comment lines that follow
// the root expression on subsequent lines (e.g. `a\n// trailing\n`). They are
// not attached to any element, so without this they would be lost. Every
// comment line after the root is preserved, including ones separated by blank
// lines; the scan stops only at the first non-comment, non-blank line.
func (b *celConverter) trailingRootComments(after int) []string {
	var out []string
	// Begin scanning after the root expression's own line so its same-line
	// trailing comment (handled by docTrailingComment) is not captured again.
	i := after
	for i < len(b.src) && b.src[i] != '\n' {
		i++
	}
	if i < len(b.src) {
		i++
	}
	for i < len(b.src) {
		nl := i
		for nl < len(b.src) && b.src[nl] != '\n' {
			nl++
		}
		trimmed := strings.TrimSpace(b.src[i:nl])
		switch {
		case trimmed == "":
			// Blank line: keep scanning for a following comment.
			i = nl + 1
		case strings.HasPrefix(trimmed, "//"):
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
			out = append(out, "// "+body)
			i = nl + 1
		default:
			// Non-comment, non-blank content: not part of the expression.
			return out
		}
	}
	return out
}

func (b *celConverter) computeSpans(root celast.Expr) {
	b.spans = map[int64][2]int{}
	b.claimedComments = map[int]bool{}
	var walk func(e celast.Expr)
	walk = func(e celast.Expr) {
		s, t := -1, -1
		if o, ok := b.info.GetOffsetRange(e.ID()); ok {
			s, t = b.toByte(int(o.Start)), b.toByte(int(o.Stop))
		}
		ownStop := t
		for _, c := range celast.NavigateExpr(b.ast, e).Children() {
			walk(c)
			cs := b.spans[c.ID()][0]
			ct := b.spans[c.ID()][1]
			if s == -1 || (cs != -1 && cs < s) {
				s = cs
			}
			if ct > t {
				t = ct
			}
		}
		s, t = b.extendSpan(e, s, t, ownStop)
		b.spans[e.ID()] = [2]int{s, t}
	}
	walk(root)
}

func (b *celConverter) extendSpan(e celast.Expr, s, t, _ int) (start, end int) {
	switch e.Kind() {
	case celast.SelectKind:
		// A select's span should include its field name. A `//` comment between
		// the dot and the field (e.g. `a.// dot\nb`) would otherwise make
		// `ownStop + len(field)` land inside the comment, so find the field's
		// true end by scanning forward from the selector dot (token-aware so a
		// `.` inside a preceding comment such as `a // .\n .b` is skipped).
		if field := e.AsSelect().FieldName(); field != "" {
			oe := s
			if op := e.AsSelect().Operand(); op != nil {
				if oes := b.spans[op.ID()][1]; oes > oe {
					oe = oes
				}
			}
			dot := b.nextDot(oe)
			if dot < len(b.src) {
				fs := b.identStartAfter(dot + 1)
				if end := fs + len(field); end > t {
					t = end
				}
			}
		}
	case celast.CallKind:
		fn := e.AsCall().FunctionName()
		if fn == operators.Index || fn == operators.OptIndex {
			// An index access's cel-go span stops before its closing bracket;
			// extend it so a trailing comment (e.g. `a[0] // note`) is reached.
			t = b.extendBracket(s, t, '[', ']')
		} else if _, isOp := operators.FindReverse(fn); !isOp {
			s, t = b.extendCall(s, t)
		}
	case celast.ListKind:
		t = b.extendBracket(s, t, '[', ']')
	case celast.MapKind:
		t = b.extendBracket(s, t, '{', '}')
	case celast.LiteralKind:
		// cel-go's rune-based Stop for a string literal overshoots by one rune,
		// so derive the true end by scanning from the opening quote.
		t = b.extendString(s, t)
	}
	return s, t
}

func (b *celConverter) extendBracket(s, t int, openCh, closeCh byte) int {
	if end := b.matchingBracket(s, openCh, closeCh) + 1; end > t {
		return end
	}
	return t
}

func (b *celConverter) extendString(s, t int) int {
	if s >= 0 && s < len(b.src) && (b.src[s] == '"' || b.src[s] == '\'') {
		return celtsource.SkipString(b.src, s)
	}
	return t
}

func (b *celConverter) extendCall(s, t int) (start, end int) {
	open := s
	for open < len(b.src) && b.src[open] != '(' {
		open++
	}
	if open >= len(b.src) {
		return s, t
	}
	if end := b.matchingBracket(open, '(', ')') + 1; end > t {
		t = end
	}
	j := open - 1
	for j >= 0 && celtsource.IsIdentChar(b.src[j]) {
		j--
	}
	if j+1 < s {
		s = j + 1
	}
	return s, t
}

func (b *celConverter) matchingBracket(open int, openCh, closeCh byte) int {
	depth := 0
	i := open
	for i < len(b.src) {
		c := b.src[i]
		if c == '"' || c == '\'' {
			i = celtsource.SkipString(b.src, i)
			continue
		}
		if c == '/' && i+1 < len(b.src) && b.src[i+1] == '/' {
			for i < len(b.src) && b.src[i] != '\n' {
				i++
			}
			continue
		}
		if c == openCh {
			depth++
		}
		if c == closeCh {
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return i
}

func (b *celConverter) span(e celast.Expr) (start, end int) {
	return b.spans[e.ID()][0], b.spans[e.ID()][1]
}

func (b *celConverter) build(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	n, err := b.buildCore(e, forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	n.setBreak(b.shouldBreak(e))
	s, t := b.span(e)
	// Wrap n in every enclosing grouping parenthesis, innermost first, so
	// nested grouping (e.g. `(( // outer\n ( // inner\n a)))`) preserves the
	// comments at each level instead of dropping the outer ones. After wrapping
	// at (lp, rp), widen the bounds to find the next enclosing paren.
	for {
		lp, lok := b.leftParen(s, forbidOpen)
		if !lok {
			break
		}
		rp, rok := b.rightParen(t, forbidClose)
		if !rok {
			break
		}
		before, after := b.parenComments(lp+1, rp, s, t)
		p := &paren{x: n, before: before, after: after}
		// Preserve a forced break across the paren wrapper so a break rule
		// isn't defeated when its expression is parenthesized.
		p.setBreak(n.mustBreak())
		n = p
		s = lp
		t = rp + 1
	}
	return n, nil
}

// shouldBreak reports whether any enabled BreakRule forces a break on e.
func (b *celConverter) shouldBreak(e celast.Expr) bool {
	for _, br := range b.breaks {
		if br.MustBreak(e) {
			return true
		}
	}
	return false
}

func (b *celConverter) buildCore(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	switch e.Kind() {
	case celast.IdentKind:
		// Emit the AST's identifier value rather than slicing the source text by
		// span. A FormatRule may rewrite an identifier (same or synthetic AST
		// ID); the source span then still points at the original spelling, and a
		// synthetic ID has no source span at all, so slicing would either emit
		// the stale name or panic on a negative bound.
		return &ident{text: e.AsIdent()}, nil
	case celast.LiteralKind:
		return b.buildLiteral(e)
	case celast.SelectKind:
		return b.buildSelect(e, forbidOpen, forbidClose)
	case celast.CallKind:
		return b.buildCall(e, forbidOpen, forbidClose)
	case celast.ListKind:
		return b.buildList(e, forbidOpen, forbidClose)
	case celast.MapKind:
		return b.buildMap(e, forbidOpen, forbidClose)
	case celast.ComprehensionKind:
		return nil, fmt.Errorf("unsupported: CEL comprehensions")
	}
	return nil, fmt.Errorf("unsupported CEL expression kind")
}

// buildLiteral emits a literal, using the AST value when a FormatRule rewrote
// it (the source slice would otherwise be stale).
func (b *celConverter) buildLiteral(e celast.Expr) (node, error) {
	s, t := b.span(e)
	text := ""
	if s >= 0 && t >= 0 && s <= t && t <= len(b.src) {
		text = b.src[s:t]
	}
	// A FormatRule may rewrite a literal to a different value. The source
	// slice (by span) then no longer represents the AST value, so emit the
	// value instead. When the source is already faithful (e.g. 1e3 vs 1000)
	// its original spelling is preserved.
	if v := e.AsLiteral(); v != nil {
		if rendered, ok := literalText(v); ok && !sourceMatches(text, v) {
			text = rendered
		}
	}
	return &lit{text: text}, nil
}

// buildSelect emits a field select, capturing any comments in the selector's
// dot gap.
func (b *celConverter) buildSelect(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	s := e.AsSelect()
	obj, err := b.build(s.Operand(), forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	// A comment in the selector's dot gap (e.g. `a.// dot\nb`,
	// `a. // before\n b`, or `a // .\n .b`) falls between the receiver and
	// its field, so capture every `//` comment between the receiver's end
	// and the field. The dot scan is token-aware so a `.` inside a comment
	// is not mistaken for the real selector dot.
	_, oe := b.span(s.Operand())
	dot := b.nextDot(oe)
	fieldStart := b.identStartAfter(dot + 1)
	var comments []elem
	for _, c := range b.commentsIn(oe, fieldStart) {
		comments = append(comments, c)
		b.claim(c.start)
	}
	return &sel{obj: obj, field: s.FieldName(), comments: comments}, nil
}

func (b *celConverter) buildCall(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	c := e.AsCall()
	fn := c.FunctionName()

	switch fn {
	case operators.Conditional:
		return b.buildConditional(e, forbidOpen, forbidClose)
	case operators.Index, operators.OptIndex:
		return b.buildIndex(e, forbidOpen, forbidClose)
	}
	if display, ok := operators.FindReverse(fn); ok {
		return b.buildOperator(e, display, forbidOpen, forbidClose)
	}
	return b.buildRegularCall(e, forbidOpen, forbidClose)
}

func (b *celConverter) buildConditional(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	args := e.AsCall().Args()
	cc, err := b.build(args[0], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	tt, err := b.build(args[1], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	ff, err := b.build(args[2], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	// Comments between the condition and a branch belong to the conditional
	// (e.g. `a ?\n // note\n b : c` or `a ? // note\n b : c`). Capture them in
	// the gaps and claim them so an enclosing container does not render them
	// twice. commentsIn is used (not just standaloneComments) so a comment
	// trailing the `?`/`:` on the same line is captured too.
	_, ce := b.span(args[0])
	ts, te := b.span(args[1])
	fs, _ := b.span(args[2])
	var tComments, fComments []elem
	for _, c := range b.commentsIn(ce, ts) {
		tComments = append(tComments, c)
		b.claim(c.start)
	}
	for _, c := range b.commentsIn(te, fs) {
		fComments = append(fComments, c)
		b.claim(c.start)
	}
	return &cond{c: cc, t: tt, f: ff, tComments: tComments, fComments: fComments}, nil
}

func (b *celConverter) buildIndex(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	args := e.AsCall().Args()
	obj, err := b.build(args[0], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	ix, err := b.build(args[1], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	// A comment between the opening bracket and the index (e.g.
	// `a[ // index\n 0]`) trails the `[`, not an element, so capture it here.
	_, oe := b.span(args[0])
	bracket := oe
	for bracket < len(b.src) && b.src[bracket] != '[' {
		bracket++
	}
	is, ie := b.span(args[1])
	// A comment between the receiver and the opening bracket (e.g.
	// `a // receiver\n [b]`) trails neither an element nor the `[`, so capture
	// it here (claimed so an enclosing container does not also render it).
	var leadComments []elem
	if oe >= 0 && bracket < len(b.src) {
		for _, c := range b.commentsIn(oe, bracket) {
			leadComments = append(leadComments, c)
			b.claim(c.start)
		}
	}
	var comments []elem
	if bracket < len(b.src) {
		for _, c := range b.commentsIn(bracket+1, is) {
			comments = append(comments, c)
			b.claim(c.start)
		}
	}
	// A comment between the index and the closing bracket (e.g.
	// `a[1 // inner\n]`) trails the operand, so capture it here and render it
	// between the index and the `]`.
	var afterComments []elem
	closeBracket := b.matchingBracket(bracket, '[', ']')
	if closeBracket < len(b.src) {
		for _, c := range b.commentsIn(ie, closeBracket) {
			if b.claimed(c.start) {
				continue
			}
			afterComments = append(afterComments, c)
			b.claim(c.start)
		}
	}
	return &idx{
		obj: obj, index: ix, optional: e.AsCall().FunctionName() == operators.OptIndex,
		leadComments: leadComments, comments: comments, afterComments: afterComments,
	}, nil
}

func (b *celConverter) buildOperator(e celast.Expr, display string, forbidOpen, forbidClose int) (node, error) {
	args := e.AsCall().Args()
	l, err := b.build(args[0], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	if len(args) == 1 {
		// A comment between the operator and its operand (e.g. `! // unary\n a`)
		// trails the operator, not an element, so capture it here.
		us, _ := b.span(e)
		os, _ := b.span(args[0])
		var comments []elem
		for _, c := range b.commentsIn(us, os) {
			comments = append(comments, c)
			b.claim(c.start)
		}
		return &un{op: display, x: l, comments: comments}, nil
	}
	r, err := b.build(args[1], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	// Standalone comments between the two operands (e.g. `(a +\n // note\n b)`)
	// and a comment trailing the operator (e.g. `a + // note\n b`) belong to
	// the binary expression: they must stay between the operands, not drift to
	// after the whole expression or be claimed by an enclosing container.
	// Capture those in the gap between the operands' spans; a parenthesized
	// wrapper around the bin skips them. The bin owns them, so they are claimed
	// to stop an enclosing container from also rendering them (issue 14).
	_, le := b.span(args[0])
	rs, _ := b.span(args[1])
	var between []elem
	for _, c := range b.commentsIn(le, rs) {
		between = append(between, c)
		b.claim(c.start)
	}
	return &bin{op: display, l: l, r: r, comments: between}, nil
}

func (b *celConverter) buildRegularCall(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	c := e.AsCall()
	fn := c.FunctionName()
	s, t := b.span(e)
	args := c.Args()

	var name node
	// calleeStart is the byte offset where the callee's name begins; the
	// argument-list `(` is resolved from it so a receiver's or inner call's
	// parens are not mistaken for this call's.
	var calleeStart int
	if c.IsMemberFunction() {
		target, err := b.build(c.Target(), forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		// A comment between the receiver and the method name (e.g.
		// `a.// method\nsize()`) trails the dot, not an element, so capture it
		// here (same dot-gap logic as a plain selector).
		_, oe := b.span(c.Target())
		dot := b.nextDot(oe)
		fieldStart := b.identStartAfter(dot + 1)
		var comments []elem
		for _, cc := range b.commentsIn(oe, fieldStart) {
			comments = append(comments, cc)
			b.claim(cc.start)
		}
		name = &sel{obj: target, field: fn, comments: comments}
		calleeStart = fieldStart
	} else {
		name = &ident{text: fn}
		calleeStart = s
	}

	// Resolve this call's argument-list `(` and `)`. For a member call the
	// receiver (e.g. `foo()` in `foo().bar(x)`) has its own parens; scanning
	// from the callee's start avoids binding to them.
	open, closeParen := b.callParens(calleeStart, t)

	// For a global call the span start `s` can point at the `(` rather than the
	// name when a comment separates the two (e.g. `foo // callee\n (a,b)`), so
	// recover the true callee start for the lead-comment range below.
	if !c.IsMemberFunction() {
		calleeStart = b.calleeNameStart(open)
	}

	// Comments between the callee name and the argument-list `(` (e.g.
	// `foo // callee\n (a,b)`) trail neither an argument nor the opening paren,
	// so capture them here to keep them on the call (claimed so an enclosing
	// container does not also render them).
	var lead []elem
	for _, cc := range b.commentsIn(calleeStart, open) {
		lead = append(lead, cc)
		b.claim(cc.start)
	}
	var callArgs []callArg
	for _, a := range args {
		v, err := b.build(a, open, closeParen)
		if err != nil {
			return nil, err
		}
		es, et := b.span(a)
		cm, present := b.trailingComment(et, closeParen)
		callArgs = append(callArgs, callArg{value: v, comment: cm, commentPresent: present, start: es})
	}
	return &call{name: name, leadComments: lead, args: b.mergeCallComments(open, closeParen, callArgs)}, nil
}

// mergeCallComments interleaves standalone comment lines inside a call's
// parentheses with its arguments, in source order, so they survive a rewrite.
func (b *celConverter) mergeCallComments(open, closeOff int, args []callArg) []callArg {
	// A comment trailing the opening paren on its own line (`f(// note\n a)`)
	// is not standalone (the paren precedes it); capture it so it survives a
	// rewrite that puts the comment back on the paren's line.
	if c, ok := b.delimTrailingComment(open, closeOff); ok {
		args = append([]callArg{{comment: c.comment, standalone: true, start: c.start}}, args...)
	}
	st := b.standaloneComments(open+1, closeOff)
	if len(st) == 0 {
		return args
	}
	out := make([]callArg, 0, len(args)+len(st))
	i, j := 0, 0
	for i < len(args) && j < len(st) {
		if st[j].start < args[i].start {
			out = append(out, callArg{comment: st[j].comment, standalone: true, start: st[j].start})
			j++
		} else {
			out = append(out, args[i])
			i++
		}
	}
	for ; j < len(st); j++ {
		out = append(out, callArg{comment: st[j].comment, standalone: true, start: st[j].start})
	}
	out = append(out, args[i:]...)
	return out
}

func (b *celConverter) buildList(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	list := e.AsList()
	s, t := b.span(e)
	elems := make([]elem, 0, len(list.Elements()))
	for _, el := range list.Elements() {
		v, err := b.build(el, forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		es, et := b.span(el)
		cm, present := b.trailingComment(et, t)
		elems = append(elems, elem{value: v, comment: cm, commentPresent: present, start: es})
	}
	return &listLit{elems: b.containerBody(s, t, elems)}, nil
}

func (b *celConverter) buildMap(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	m := e.AsMap()
	s, t := b.span(e)
	elems := make([]elem, 0, len(m.Entries()))
	for _, en := range m.Entries() {
		me := en.AsMapEntry()
		k, err := b.build(me.Key(), forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		v, err := b.build(me.Value(), forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		ks, ke := b.span(me.Key())
		vs, ve := b.span(me.Value())
		cm, present := b.trailingComment(ve, t)
		el := elem{key: k, value: v, comment: cm, commentPresent: present, start: ks}
		// A comment in the gap between a map key and its value (e.g.
		// `{a // key\n : b}`, `{a: // colon\n b}`, or `{'a': // first\n 1}`)
		// trails the `:`/key rather than an element. Capture every comment in
		// the gap so it survives a rewrite even when the value also has a
		// trailing comment of its own; each is rendered between key and value.
		for _, c := range b.commentsIn(ke, vs) {
			if b.claimed(c.start) {
				continue
			}
			el.comments = append(el.comments, c)
			b.claim(c.start)
		}
		elems = append(elems, el)
	}
	return &mapLit{elems: b.containerBody(s, t, elems)}, nil
}

// containerBody merges a container's elements with any standalone `//` comment
// lines that appear between its brackets, in source order, so standalone
// comments survive a rewrite instead of being silently dropped. A comment
// trailing the opening delimiter on its own line (`[ // note\n 1]`) is not
// standalone (the delimiter precedes it), so it is captured explicitly.
func (b *celConverter) containerBody(open, closeOff int, elems []elem) []elem {
	if c, ok := b.delimTrailingComment(open, closeOff); ok {
		elems = append([]elem{c}, elems...)
	}
	st := b.standaloneComments(open+1, closeOff)
	if len(st) == 0 {
		return elems
	}
	out := make([]elem, 0, len(elems)+len(st))
	i, j := 0, 0
	for i < len(elems) && j < len(st) {
		if st[j].start < elems[i].start {
			out = append(out, st[j])
			j++
		} else {
			out = append(out, elems[i])
			i++
		}
	}
	out = append(out, st[j:]...)
	out = append(out, elems[i:]...)
	return out
}

// delimTrailingComment returns the `//` comment that trails the opening
// delimiter at open on its own line, e.g. `[ // note\n 1]`. The delimiter is on
// the comment's line, so the standalone scan treats the comment as non-alone;
// capture it here so it is not lost.
func (b *celConverter) delimTrailingComment(open, closeOff int) (elem, bool) {
	i := open + 1
	for i < closeOff && i < len(b.src) && (b.src[i] == ' ' || b.src[i] == '\t' || b.src[i] == '\r') {
		i++
	}
	if i >= closeOff || i >= len(b.src) || b.src[i] == '\n' || b.src[i] != '/' || i+1 >= len(b.src) || b.src[i+1] != '/' {
		return elem{}, false
	}
	j := i + 2
	for j < closeOff && j < len(b.src) && b.src[j] != '\n' {
		j++
	}
	return elem{comment: strings.TrimSpace(b.src[i+2 : j]), commentPresent: true, standalone: true, start: i}, true
}

// standaloneComments returns the `//` comments within [lo, hi) that sit on
// their own line (only whitespace precedes them on their line), as standalone
// elems in source order. Comments that trail an element on the same line are
// captured by trailingComment and are not duplicated here. Only comments at the
// container's own delimiter depth are returned: a comment nested inside a
// sub-container is claimed by that container, not by this one, so each comment
// is assigned to exactly one delimiter level.
func (b *celConverter) standaloneComments(lo, hi int) []elem {
	var out []elem
	i := lo
	depth := 0
	for i < hi && i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '"' || c == '\'':
			i = celtsource.SkipString(b.src, i)
			continue
		case c == '/' && i+1 < len(b.src) && b.src[i+1] == '/':
			e, next := b.scanStandaloneComment(i, hi, depth)
			if e != nil {
				out = append(out, *e)
			}
			i = next
			continue
		case c == '[' || c == '{' || c == '(':
			depth++
		case c == ']' || c == '}' || c == ')':
			depth--
		}
		i++
	}
	return out
}

// scanStandaloneComment examines the `//` comment starting at i and, when it is
// standalone at the container's own depth and unclaimed, returns it as an elem
// along with the byte index just past the comment.
func (b *celConverter) scanStandaloneComment(i, hi, depth int) (e *elem, next int) {
	lineStart := i
	for lineStart > 0 && b.src[lineStart-1] != '\n' {
		lineStart--
	}
	alone := true
	for j := lineStart; j < i; j++ {
		if !isSpaceByte(b.src[j]) {
			alone = false
			break
		}
	}
	j := i + 2
	for j < hi && j < len(b.src) && b.src[j] != '\n' {
		j++
	}
	if depth == 0 && alone && !b.claimed(i) {
		return &elem{comment: strings.TrimSpace(b.src[i+2 : j]), standalone: true, start: i}, j
	}
	return nil, j
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// commentsIn returns every `//` comment in [lo, hi), whether it trails a token
// on its line or sits on its own line. It is used to capture comments in an
// operator gap (e.g. `a + // note` or `a &&\n // note`), which has no
// delimiters to bound a standalone scan.
func (b *celConverter) commentsIn(lo, hi int) []elem {
	var out []elem
	i := lo
	for i < hi && i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '"' || c == '\'':
			i = celtsource.SkipString(b.src, i)
			continue
		case c == '/' && i+1 < len(b.src) && b.src[i+1] == '/':
			j := i + 2
			for j < hi && j < len(b.src) && b.src[j] != '\n' {
				j++
			}
			if !b.claimed(i) {
				out = append(out, elem{comment: strings.TrimSpace(b.src[i+2 : j]), standalone: true, start: i})
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// callParens resolves a call's own argument-list delimiters. The open paren is
// found by scanning forward from openStart (the callee's end), which avoids
// mistaking a receiver's or inner call's `(` for this call's (e.g. the `foo()`
// in `foo().bar(x)`).
func (b *celConverter) callParens(openStart, t int) (open, closeParen int) {
	open = b.openParenAfter(openStart, t)
	closeParen = t - 1
	for closeParen > open && b.src[closeParen] != ')' {
		closeParen--
	}
	return open, closeParen
}

// openParenAfter returns the byte index of the first `(` at or after i, skipping
// string literals and `//` comments so a `(` inside them is not mistaken for a
// call's argument-list paren.
func (b *celConverter) openParenAfter(i, limit int) int {
	for i < limit && i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '"' || c == '\'':
			i = celtsource.SkipString(b.src, i)
			continue
		case c == '/' && i+1 < len(b.src) && b.src[i+1] == '/':
			for i < len(b.src) && b.src[i] != '\n' {
				i++
			}
			continue
		case c == '(':
			return i
		}
		i++
	}
	return i
}

// calleeNameStart returns the byte offset where a call's callee name begins,
// scanning back from the argument-list `(` at open across whitespace and `//`
// comment lines. A comment between the name and `(` (e.g. `foo // callee\n (a,b)`)
// would otherwise be mistaken for the name itself.
func (b *celConverter) calleeNameStart(open int) int {
	i := open - 1
	for i >= 0 {
		for i >= 0 && isSpaceByte(b.src[i]) {
			i--
		}
		if i < 0 {
			return 0
		}
		// Find the start of this run of non-whitespace.
		j := i
		for j > 0 && !isSpaceByte(b.src[j-1]) {
			j--
		}
		// If i sits inside a `//` comment (e.g. the body of `// callee`, whose
		// marker may be separated from the text by a space), skip the whole
		// comment and keep scanning back for the name.
		if b.commentStartOnLine(i) >= 0 {
			i = j - 1
			continue
		}
		// This run is the callee name; return its first ident char.
		for i >= 0 && celtsource.IsIdentChar(b.src[i]) {
			i--
		}
		return i + 1
	}
	return 0
}

// parenComments splits the standalone comments inside grouping parentheses
// [lo, hi) into those preceding and following the child expression, preserving
// their side so a rewrite doesn't move them. A comment inside the child's own
// span (e.g. between a binary expression's operands) is left for the child to
// render rather than treated as following the whole expression. Every comment
// captured here is claimed so an enclosing container or paren does not render
// it a second time.
func (b *celConverter) parenComments(lo, hi, childStart, childEnd int) (before, after []elem) {
	// A comment trailing the opening paren (`( // title\n x)`) is on the same
	// line as the paren, so the standalone scan below misses it; capture it
	// explicitly as a leading comment.
	if c, ok := b.delimTrailingComment(lo-1, hi); ok && !b.claimed(c.start) {
		before = append(before, c)
		b.claim(c.start)
	}
	for _, c := range b.standaloneComments(lo, hi) {
		if b.claimed(c.start) {
			continue
		}
		if c.start < childStart {
			before = append(before, c)
			b.claim(c.start)
		}
	}
	// Comments between the child's end and the closing paren (either trailing
	// the child on the same line, e.g. `(a // inner\n)`, or standalone) belong
	// after the child. Use commentsIn so a non-standalone trailing comment is
	// captured too.
	for _, c := range b.commentsIn(childEnd, hi) {
		if b.claimed(c.start) {
			continue
		}
		after = append(after, c)
		b.claim(c.start)
	}
	return before, after
}

func (b *celConverter) leftParen(s, forbid int) (int, bool) {
	i := s - 1
	for i >= 0 {
		for i >= 0 && celtsource.IsSpace(b.src[i]) {
			i--
		}
		if i < 0 {
			break
		}
		// Position i may sit inside a `//` comment (own-line or one trailing a
		// token such as `( // note\n x)`); skip back past its marker so the
		// comment does not block detecting the enclosing paren.
		if cm := b.commentStartOnLine(i); cm >= 0 {
			i = cm - 1
			continue
		}
		break
	}
	if i >= 0 && b.src[i] == '(' && i != forbid {
		return i, true
	}
	return 0, false
}

// commentStartOnLine returns the index of the `//` marker that starts the
// comment containing byte i, or -1 when i is not inside a comment. String
// literals are skipped so a `//` inside a quoted string (e.g. `'http://'`) is
// not mistaken for a comment delimiter.
func (b *celConverter) commentStartOnLine(i int) int {
	ls := b.lineStart(i)
	for j := ls; j < i; j++ {
		switch b.src[j] {
		case '"', '\'':
			if ns := celtsource.SkipString(b.src, j); ns > j {
				// If the string extends past i, then i is inside the literal
				// and cannot be inside a comment.
				if ns > i {
					return -1
				}
				j = ns - 1
			}
		case '/':
			if j+1 < len(b.src) && b.src[j+1] == '/' {
				return j
			}
		}
	}
	return -1
}

// identStartAfter returns the byte index of the first token starting at or
// after i, skipping whitespace and `//` comment lines. It is used to find a
// selector's field after its dot, across a comment such as `a.// dot\nb`.
func (b *celConverter) identStartAfter(i int) int {
	for i < len(b.src) {
		c := b.src[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		if c == '/' && i+1 < len(b.src) && b.src[i+1] == '/' {
			for i < len(b.src) && b.src[i] != '\n' {
				i++
			}
			continue
		}
		break
	}
	return i
}

// nextDot returns the byte index of the first `.` selector dot at or after i,
// skipping `//` comments and string literals so a `.` inside them (e.g. the
// `// .` in `a // .\n .b`) is not mistaken for the real selection dot.
func (b *celConverter) nextDot(i int) int {
	for i < len(b.src) {
		c := b.src[i]
		switch {
		case c == '"' || c == '\'':
			i = celtsource.SkipString(b.src, i)
			continue
		case c == '/' && i+1 < len(b.src) && b.src[i+1] == '/':
			for i < len(b.src) && b.src[i] != '\n' {
				i++
			}
			continue
		case c == '.':
			return i
		}
		i++
	}
	return i
}

// rootEnd returns the byte offset just past the root expression, extended past
// EVERY enclosing grouping parenthesis (and the comments within them). This
// lets the root's trailing comment be found after the final `)` rather than
// inside a parenthesized root (e.g. `((a) // middle\n) // tail`).
func (b *celConverter) rootEnd(s, t int) int {
	for {
		lp, ok := b.leftParen(s, -1)
		if !ok || lp >= s {
			break
		}
		if rp := b.matchingBracket(lp, '(', ')'); rp+1 > t {
			t = rp + 1
			s = lp
			continue
		}
		break
	}
	return t
}

func (b *celConverter) rightParen(t, forbid int) (int, bool) {
	i := t
	for i < len(b.src) {
		if celtsource.IsSpace(b.src[i]) {
			i++
			continue
		}
		if b.src[i] == '/' && i+1 < len(b.src) && b.src[i+1] == '/' {
			// Skip a `//` comment line between the expression and the closing
			// paren.
			for i < len(b.src) && b.src[i] != '\n' {
				i++
			}
			continue
		}
		break
	}
	if i < len(b.src) && b.src[i] == ')' && i != forbid {
		return i, true
	}
	return 0, false
}

// lineStart returns the byte index of the first character of the line
// containing i.
func (b *celConverter) lineStart(i int) int {
	for i > 0 && b.src[i-1] != '\n' {
		i--
	}
	return i
}

// trailingComment finds a comment trailing an element (after, skipping spaces,
// closing grouping parens, and one comma). It returns the comment body and
// whether a comment was actually present (an empty body still counts as
// present). Closing parens are skipped so a comment after a parenthesized
// operand (e.g. `[(a) // note\n, 2]`) is reached, since a grouping paren's
// span excludes its `)`.
func (b *celConverter) trailingComment(after, limit int) (string, bool) {
	i := after
	seenComma := false
	for i < limit && i < len(b.src) {
		c := b.src[i]
		if c == ' ' || c == '\t' || c == '\r' || c == ')' {
			i++
			continue
		}
		if c == ',' && !seenComma {
			seenComma = true
			i++
			continue
		}
		break
	}
	if i >= limit || i >= len(b.src) {
		return "", false
	}
	// A comment here may already be owned by a nested construct (e.g. a
	// grouped operand's closing paren captured it), so do not double-capture.
	if b.claimed(i) {
		return "", false
	}
	return b.commentAt(i, limit)
}

// commentAt extracts the //-comment starting at i, stopping at the first
// newline or at limit (e.g. a closing paren) so it never swallows a delimiter.
// It returns the comment body (trimmed) and whether a comment was present at i
// (an empty body still counts as present).
func (b *celConverter) commentAt(i, limit int) (string, bool) {
	if b.src[i] != '/' || i+1 >= len(b.src) || b.src[i+1] != '/' {
		return "", false
	}
	j := i + 2
	for j < limit && j < len(b.src) && b.src[j] != '\n' {
		j++
	}
	return strings.TrimSpace(b.src[i+2 : j]), true
}

// literalText renders a literal value as canonical CEL source text.
func literalText(v celref.Val) (string, bool) {
	switch x := v.(type) {
	case celtypes.Bool:
		if bool(x) {
			return "true", true
		}
		return "false", true
	case celtypes.Null:
		return "null", true
	case celtypes.Int:
		return strconv.FormatInt(int64(x), 10), true
	case celtypes.Uint:
		return strconv.FormatUint(uint64(x), 10) + "u", true
	case celtypes.Double:
		s := strconv.FormatFloat(float64(x), 'g', -1, 64)
		// Ensure a CEL double that would otherwise render as an integer literal
		// (e.g. 2 -> "2") keeps its double kind by forcing a decimal point.
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s, true
	case celtypes.String:
		return strconv.Quote(string(x)), true
	case celtypes.Bytes:
		return `b` + strconv.Quote(string(x)), true
	}
	return "", false
}

// sourceMatches reports whether the literal source slice already represents the
// AST value. It first compares against the canonical rendering; if that differs
// (e.g. 1e3 vs 1000), it falls back to parsing the slice and comparing values,
// so only a genuine rewrite (different value) is treated as such. It also
// requires the literal's TYPE to match: a rewrite that changes the kind (e.g.
// int 1 to double 1.0 or uint 1) must not reuse the old source text, even when
// the numeric values compare equal across types.
func sourceMatches(text string, v celref.Val) bool {
	if rt, ok := literalText(v); ok && rt == text {
		return true
	}
	parsed, err := celtsource.Parse(common.NewStringSource(text, ""))
	if err != nil || parsed == nil {
		return false
	}
	e := parsed.Expr()
	if e == nil || e.Kind() != celast.LiteralKind {
		return false
	}
	got := e.AsLiteral()
	if got == nil || v == nil {
		return got == v
	}
	if got.Type() != v.Type() {
		// A cross-type rewrite (int->double, int->uint) changes the literal
		// kind, so the old source text cannot represent the new value.
		return false
	}
	res := got.Equal(v)
	if res == nil {
		return false
	}
	if b, ok := res.Value().(bool); ok {
		return b
	}
	return false
}
