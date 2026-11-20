package celtfmt

import (
	"fmt"
	"strings"

	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"

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
}
type idx struct {
	baseNode
	obj      node
	index    node
	optional bool
}
type callArg struct {
	value   node
	comment string
}
type call struct {
	baseNode
	name node
	args []callArg
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
}
type un struct {
	baseNode
	op string
	x  node
}
type cond struct {
	baseNode
	c, t, f node
}
type paren struct {
	baseNode
	x node
}

type elem struct {
	key     node
	value   node
	comment string
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

func leadingDoc(src string) []string {
	var doc []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			doc = append(doc, trimmed)
		} else if trimmed == "" {
			if len(doc) > 0 {
				break
			}
			continue
		} else {
			break
		}
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
}

// toByte converts a cel-go source offset (rune-based) to a byte offset.
func (b *celConverter) toByte(ro int) int {
	return celtsource.ByteOffset(b.byteOff, ro)
}

func (b *celConverter) computeSpans(root celast.Expr) {
	b.spans = map[int64][2]int{}
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

func (b *celConverter) extendSpan(e celast.Expr, s, t, ownStop int) (start, end int) {
	switch e.Kind() {
	case celast.SelectKind:
		if field := e.AsSelect().FieldName(); field != "" && ownStop >= 0 {
			if end := ownStop + len(field); end > t {
				t = end
			}
		}
	case celast.CallKind:
		if _, isOp := operators.FindReverse(e.AsCall().FunctionName()); !isOp {
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
	if _, lok := b.leftParen(s, forbidOpen); lok {
		if _, rok := b.rightParen(t, forbidClose); rok {
			p := &paren{x: n}
			// Preserve a forced break across the paren wrapper so a break rule
			// isn't defeated when its expression is parenthesized.
			p.setBreak(n.mustBreak())
			n = p
		}
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
		s, t := b.span(e)
		return &ident{text: b.src[s:t]}, nil
	case celast.LiteralKind:
		s, t := b.span(e)
		return &lit{text: b.src[s:t]}, nil
	case celast.SelectKind:
		s := e.AsSelect()
		obj, err := b.build(s.Operand(), forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		return &sel{obj: obj, field: s.FieldName()}, nil
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
	return &cond{c: cc, t: tt, f: ff}, nil
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
	return &idx{obj: obj, index: ix, optional: e.AsCall().FunctionName() == operators.OptIndex}, nil
}

func (b *celConverter) buildOperator(e celast.Expr, display string, forbidOpen, forbidClose int) (node, error) {
	args := e.AsCall().Args()
	l, err := b.build(args[0], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	if len(args) == 1 {
		return &un{op: display, x: l}, nil
	}
	r, err := b.build(args[1], forbidOpen, forbidClose)
	if err != nil {
		return nil, err
	}
	return &bin{op: display, l: l, r: r}, nil
}

func (b *celConverter) buildRegularCall(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	c := e.AsCall()
	fn := c.FunctionName()
	s, t := b.span(e)
	open, closeParen := b.callParens(s, t)
	args := c.Args()

	var name node
	if c.IsMemberFunction() {
		target, err := b.build(c.Target(), forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		name = &sel{obj: target, field: fn}
	} else {
		name = &ident{text: fn}
	}

	var callArgs []callArg
	for _, a := range args {
		v, err := b.build(a, open, closeParen)
		if err != nil {
			return nil, err
		}
		_, et := b.span(a)
		callArgs = append(callArgs, callArg{value: v, comment: b.trailingComment(et, closeParen)})
	}
	return &call{name: name, args: callArgs}, nil
}

func (b *celConverter) buildList(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	list := e.AsList()
	_, t := b.span(e)
	var elems []elem
	for _, el := range list.Elements() {
		v, err := b.build(el, forbidOpen, forbidClose)
		if err != nil {
			return nil, err
		}
		_, et := b.span(el)
		elems = append(elems, elem{value: v, comment: b.trailingComment(et, t)})
	}
	return &listLit{elems: elems}, nil
}

func (b *celConverter) buildMap(e celast.Expr, forbidOpen, forbidClose int) (node, error) {
	m := e.AsMap()
	_, t := b.span(e)
	var elems []elem
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
		_, et := b.span(me.Value())
		elems = append(elems, elem{key: k, value: v, comment: b.trailingComment(et, t)})
	}
	return &mapLit{elems: elems}, nil
}

func (b *celConverter) callParens(s, t int) (open, closeParen int) {
	open = s
	for open < t && b.src[open] != '(' {
		open++
	}
	closeParen = t - 1
	for closeParen > s && b.src[closeParen] != ')' {
		closeParen--
	}
	return open, closeParen
}

func (b *celConverter) leftParen(s, forbid int) (int, bool) {
	i := s - 1
	for i >= 0 && celtsource.IsSpace(b.src[i]) {
		i--
	}
	if i >= 0 && b.src[i] == '(' && i != forbid {
		return i, true
	}
	return 0, false
}

func (b *celConverter) rightParen(t, forbid int) (int, bool) {
	i := t
	for i < len(b.src) && celtsource.IsSpace(b.src[i]) {
		i++
	}
	if i < len(b.src) && b.src[i] == ')' && i != forbid {
		return i, true
	}
	return 0, false
}

func (b *celConverter) trailingComment(after, limit int) string {
	i := after
	seenComma := false
	for i < limit && i < len(b.src) {
		c := b.src[i]
		if c == ' ' || c == '\t' || c == '\r' {
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
		return ""
	}
	return b.commentAt(i, limit)
}

// commentAt extracts the //-comment starting at i, stopping at the first
// newline or at limit (e.g. a closing paren) so it never swallows a delimiter.
func (b *celConverter) commentAt(i, limit int) string {
	if b.src[i] != '/' || i+1 >= len(b.src) || b.src[i+1] != '/' {
		return ""
	}
	j := i + 2
	for j < limit && j < len(b.src) && b.src[j] != '\n' {
		j++
	}
	return strings.TrimSpace(b.src[i+2 : j])
}
