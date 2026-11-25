package celtfmt

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtsource"
)

const defaultFilePerm os.FileMode = 0o644

// WriteFile writes content to path, preserving an existing file's permission
// bits.
func WriteFile(path string, content []byte) error {
	perm := defaultFilePerm
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return os.WriteFile(path, content, perm)
}

const (
	defaultIndent = "  "

	defaultWidth = 100
)

// Config is the set of enabled format rules. Consumers implement it (typically
// backed by their own config file) and pass it to Format or FormatExpr. The
// printer derives indent, width and break behavior from the enabled rules.
type Config interface {
	Rules() []FormatRule
}

// RuleConfig is optionally implemented by Config to supply per-rule
// enablement and raw configuration, mirroring how a config file's fmt section
// maps to rules (the analog of celtlint's ConfigProvider).
type RuleConfig interface {
	// Enabled returns a rule's explicit enabled/disabled override, if the
	// consumer configured it. When not configured, the rule's
	// Defaults.Enabled applies. A Config that does not implement RuleConfig
	// treats every rule returned by Rules() as explicitly enabled.
	Enabled(name string) (bool, bool)
	// Raw returns a rule's raw configuration bytes, if configured. These are
	// passed to a Configurable rule's ApplyConfig instead of Defaults.Config.
	Raw(name string) ([]byte, bool)
}

// activeRules returns the enabled rules from cfg after applying per-rule
// configuration. A rule is active when it is enabled by default or explicitly
// enabled via RuleConfig; a Configurable rule's config is applied before the
// rule runs.
func activeRules(cfg Config) ([]FormatRule, error) {
	var out []FormatRule
	if cfg == nil {
		return out, nil
	}
	rc, hasRC := cfg.(RuleConfig)
	for _, r := range cfg.Rules() {
		if r == nil {
			continue
		}
		meta := r.Meta()
		enabled := meta.Defaults.Enabled
		switch {
		case !hasRC:
			// No per-rule enablement provider: a rule present in Rules() is
			// explicitly enabled.
			enabled = true
		case rc != nil:
			if on, ok := rc.Enabled(meta.Name); ok {
				enabled = on
			}
		}
		if !enabled {
			continue
		}
		if cr, ok := r.(Configurable); ok {
			data := meta.Defaults.Config
			if rc != nil {
				if raw, ok := rc.Raw(meta.Name); ok {
					data = raw
				}
			}
			if err := cr.ApplyConfig(data); err != nil {
				return nil, fmt.Errorf("configure format rule %q: %w", meta.Name, err)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// settings is the printer parameters extracted from the enabled rules.
type settings struct {
	indentUnit string
	lineWidth  int
	breaks     []BreakRule
	rules      []FormatRule
}

// resolveSettings extracts printer parameters from the enabled rules.
func resolveSettings(cfg Config) (settings, error) {
	var s settings
	rules, err := activeRules(cfg)
	if err != nil {
		return s, err
	}
	s.rules = rules
	for _, r := range s.rules {
		if r == nil {
			continue
		}
		if ir, ok := r.(IndentProvider); ok {
			s.indentUnit = ir.Indent()
		}
		if wr, ok := r.(WidthProvider); ok {
			s.lineWidth = wr.Width()
		}
		if br, ok := r.(BreakRule); ok {
			s.breaks = append(s.breaks, br)
		}
	}
	if s.indentUnit == "" {
		s.indentUnit = defaultIndent
	}
	if s.lineWidth <= 0 {
		s.lineWidth = defaultWidth
	}
	return s, nil
}

func Format(src string, cfg Config) (string, error) {
	s, err := resolveSettings(cfg)
	if err != nil {
		return "", err
	}
	doc := leadingDoc(src)
	expr, err := formatExpr(src, s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(doc) > 0 {
		fmt.Fprintf(&b, "%s\n\n", strings.Join(doc, "\n"))
	}
	fmt.Fprintf(&b, "%s\n", expr)
	return b.String(), nil
}

func FormatExpr(src string, cfg Config) (string, error) {
	s, err := resolveSettings(cfg)
	if err != nil {
		return "", err
	}
	return formatExpr(src, s)
}

// rejectUnsupported scans src for CEL constructs the formatter cannot yet
// preserve, and returns an error instead of letting a successful edit silently
// change what the expression means. Until these constructs are fully supported,
// formatting must refuse them rather than emit a corrupted or different result.
func rejectUnsupported(src string) error {
	i := 0
	for i < len(src) {
		c := src[i]
		switch c {
		case '"', '\'':
			// Triple-quoted strings ("...", '...') are truncated by the
			// quote-based span logic; reject them for now.
			if i+2 < len(src) && src[i+1] == c && src[i+2] == c {
				return fmt.Errorf("unsupported: triple-quoted strings")
			}
			i = celtsource.SkipString(src, i)
			continue
		case '/':
			if i+1 < len(src) && src[i+1] == '/' {
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
		}
		i++
	}
	return nil
}

func formatExpr(src string, s settings) (string, error) {
	if err := rejectUnsupported(src); err != nil {
		return "", err
	}
	source := common.NewStringSource(src, "")
	parsed, err := celtsource.Parse(source)
	if err != nil {
		return "", err
	}
	for _, r := range s.rules {
		parsed, err = r.Rewrite(parsed)
		if err != nil {
			return "", err
		}
	}
	b := &celConverter{src: src, ast: parsed, info: parsed.SourceInfo(), byteOff: celtsource.RuneOffsets(src), breaks: s.breaks}
	b.computeSpans(parsed.Expr())
	n, err := b.build(parsed.Expr(), -1, -1)
	if err != nil {
		return "", err
	}
	p := &printer{settings: s}
	p.render(n)
	out := strings.Join(p.out, "\n")
	// Preserve a trailing comment that follows the root expression on the same
	// line (e.g. `] // tail` or `(a+b) // tail`), plus any standalone `//`
	// comment lines after the root on their own lines. They are not attached to
	// any element, so without this they would be lost.
	if rootStart, rootEnd := b.span(parsed.Expr()); rootStart >= 0 {
		rootEnd = b.rootEnd(rootStart, rootEnd)
		if tc, present := b.docTrailingComment(rootEnd); present {
			out += " // " + tc
		}
		if tails := b.trailingRootComments(rootEnd); len(tails) > 0 {
			out += "\n" + strings.Join(tails, "\n")
		}
	}
	// Safety net: refuse to emit an edit that would change the meaning of the
	// expression. Re-parse the output and require structural equivalence with
	// the input; a corruption (dropped optional marker, truncated literal, code
	// swallowed by a comment) shows up here as a difference or an invalid parse.
	if reparsed, err := celtsource.Parse(common.NewStringSource(out, "")); err != nil {
		return "", fmt.Errorf("formatting produced invalid CEL: %w", err)
	} else if !celast.EquivExpr(parsed.Expr(), reparsed.Expr()) {
		return "", fmt.Errorf("formatting would change the meaning of the expression")
	}
	return out, nil
}

type printer struct {
	settings
	out    []string
	col    int
	indent int

	forceBrokenCall bool

	// lastContainerInlined records whether the most recently rendered call
	// argument was a list/map that was kept on a single line, so renderCallClose
	// knows not to glue a closing paren onto its `]`/`}`.
	lastContainerInlined bool
}

func (p *printer) indentStr() string {
	return strings.Repeat(p.indentUnit, p.indent)
}

func (p *printer) newline() {
	p.out = append(p.out, p.indentStr())
	p.col = colWidth(p.indentStr())
}

func (p *printer) write(s string) {
	if len(p.out) == 0 {
		p.newline()
	}
	p.out[len(p.out)-1] += s
	p.col += colWidth(s)
}

func (p *printer) writef(format string, args ...any) {
	p.write(fmt.Sprintf(format, args...))
}

func (p *printer) fits(s string) bool {
	return p.col+colWidth(s) <= p.lineWidth
}

// colWidth measures a string's contribution to line width in runes, not bytes,
// so non-ASCII content does not over-count against the configured width.
func colWidth(s string) int {
	return utf8.RuneCountInString(s)
}

func (p *printer) inlineFits(inline string, n node) bool {
	if n.mustBreak() {
		return false
	}
	return p.fits(inline)
}

func (p *printer) render(n node) {
	switch t := n.(type) {
	case *ident:
		p.write(t.text)
	case *lit:
		p.write(t.text)
	case *sel:
		p.renderSel(t)
	case *idx:
		p.renderIdx(t)
	case *call:
		p.renderCall(t)
	case *listLit:
		p.renderList(t)
	case *mapLit:
		p.renderMap(t)
	case *bin:
		p.renderBin(t)
	case *un:
		p.renderUn(t)
	case *cond:
		p.renderCond(t)
	case *paren:
		p.renderParen(t)
	}
}

// renderSel renders a field select, emitting any comments between the dot and
// the field.
func (p *printer) renderSel(t *sel) {
	p.render(t.obj)
	p.write(".")
	if len(t.comments) > 0 {
		p.renderComments(t.comments)
		p.newline()
	}
	p.write(t.field)
}

// renderIdx renders an index access with its leading, inner and trailing
// comments and optional-select marker.
func (p *printer) renderIdx(t *idx) {
	p.render(t.obj)
	if len(t.leadComments) > 0 {
		for _, c := range t.leadComments {
			p.writef(" // %s", c.comment)
			p.newline()
		}
	}
	p.write("[")
	if t.optional {
		p.write("?")
	}
	if len(t.comments) > 0 {
		p.renderComments(t.comments)
		p.newline()
	}
	p.render(t.index)
	if len(t.afterComments) > 0 {
		p.renderComments(t.afterComments)
		p.newline()
	}
	p.write("]")
}

// renderUn renders a unary expression with any comment after the operator.
func (p *printer) renderUn(t *un) {
	p.write(t.op)
	if len(t.comments) > 0 {
		p.renderComments(t.comments)
		p.newline()
	}
	p.render(t.x)
}

// renderParen renders a parenthesized group, inlining it when it fits and
// otherwise breaking it with its leading/trailing comments.
func (p *printer) renderParen(t *paren) {
	inline := "(" + inlineNode(t.x) + ")"
	if canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	p.write("(")
	p.indent++
	p.newline()
	for _, c := range t.before {
		p.writef("// %s", c.comment)
		p.newline()
	}
	p.render(t.x)
	for _, c := range t.after {
		p.newline()
		p.writef("// %s", c.comment)
	}
	p.indent--
	p.newline()
	p.write(")")
}

func (p *printer) renderCall(t *call) {
	inline := inlineCall(t)
	if !p.forceBrokenCall && canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	p.render(t.name)
	// A comment between the callee and its argument-list `(` is rendered on the
	// name's line and then a newline, so the `(` that follows is not swallowed.
	if len(t.leadComments) > 0 {
		for _, c := range t.leadComments {
			p.writef(" // %s", c.comment)
			p.newline()
		}
	}
	p.write("(")
	closeIndent := p.indent
	p.indent++
	freshLine := true
	for i, av := range t.args {
		freshLine = p.renderCallArg(i, av, freshLine, !hasNextRealArg(t.args, i))
	}
	p.indent = closeIndent
	p.renderCallClose(t)
}

// hasNextRealArg reports whether a later position holds a real argument (i.e.
// not a standalone comment), so the argument at i knows whether to emit a comma.
func hasNextRealArg(args []callArg, i int) bool {
	for j := i + 1; j < len(args); j++ {
		if !args[j].standalone {
			return true
		}
	}
	return false
}

// renderCallClose emits the closing paren. It attaches directly only when the
// last argument is a container with no trailing comment; otherwise the paren is
// placed on its own line so it is not swallowed by a trailing `//` comment.
func (p *printer) renderCallClose(t *call) {
	if len(t.args) == 0 {
		p.write(")")
		return
	}
	last := t.args[len(t.args)-1]
	switch last.value.(type) {
	case *listLit, *mapLit:
		if !last.commentPresent && !p.lastContainerInlined {
			p.write(")")
			return
		}
	}
	p.newline()
	p.write(")")
}

// renderCallArg renders one call argument and returns whether the next argument
// must start on a fresh line. The argument's trailing comment (if any) is the
// last thing written on its line: the delimiter comma is emitted before the
// comment so the comment cannot swallow subsequent arguments.
func (p *printer) renderCallArg(i int, av callArg, freshLine, last bool) bool {
	if av.standalone {
		if i > 0 {
			p.newline()
		}
		p.writef("// %s", av.comment)
		return true
	}
	a := av.value
	switch v := a.(type) {
	case *listLit:
		return p.renderCallArgContainerArg('[', ']', v.elems, av, i, last)
	case *mapLit:
		return p.renderCallArgContainerArg('{', '}', v.elems, av, i, last)
	}

	inline := inlineNode(a)
	fitsInline := canInline(a) && p.inlineFits(inline, a)
	if freshLine {
		p.newline()
	} else if !isSimpleArg(a) {
		p.newline()
	} else if !fitsInline {
		p.newline()
	} else {
		p.write(" ")
	}
	if fitsInline {
		p.write(inline)
	} else {
		p.render(a)
	}
	if !last {
		p.write(",")
	}
	if av.commentPresent {
		p.writef(" // %s", av.comment)
	}
	// A trailing comment is the last thing on its line, so the next argument
	// must start on a fresh line (appending after the comment would swallow it).
	return !fitsInline || av.commentPresent
}

func (p *printer) renderCallArgContainer(open, closing byte, elems []elem, comment string, present bool, i int, last bool) {
	if i > 0 {
		p.newline()
	}
	p.write(string(open))
	// When the container is not the first argument it starts on its own line at
	// the argument-body indent, so its elements are indented one level further
	// and it closes at the argument-body indent. When it is the first argument
	// its `[` sits on the call's `(` line, so elements sit at the argument-body
	// indent and it closes one level shallower.
	if i > 0 {
		p.indent++
	}
	p.newline()
	p.renderElemLines(elems)
	p.indent--
	p.newline()
	p.write(string(closing))
	if i == 0 {
		p.indent++
	}
	if !last {
		p.write(",")
	}
	if present {
		p.writef(" // %s", comment)
	}
}

// renderCallArgContainerArg renders a list/map call argument. A short container
// (no comments, fits on the line) is kept on a single line so a list such as
// ["test"] does not get broken across lines just because the enclosing call is
// broken; longer or commented containers fall through to renderCallArgContainer.
func (p *printer) renderCallArgContainerArg(open, closing byte, elems []elem, av callArg, i int, last bool) bool {
	a := av.value
	inline := inlineNode(a)
	if canInline(a) && p.inlineFits(inline, a) {
		p.lastContainerInlined = true
		p.newline()
		p.write(inline)
		if !last {
			p.write(",")
		}
		if av.commentPresent {
			p.writef(" // %s", av.comment)
		}
		return true
	}
	p.lastContainerInlined = false
	p.renderCallArgContainer(open, closing, elems, av.comment, av.commentPresent, i, last)
	return true
}

func isSimpleArg(n node) bool {
	switch n.(type) {
	case *ident, *lit, *sel, *idx:
		return true
	}
	return false
}

func hasNestedCall(c *call) bool {
	for _, a := range c.args {
		ac, ok := a.value.(*call)
		if !ok {
			continue
		}
		for _, arg := range ac.args {
			if _, ok := arg.value.(*call); ok {
				return true
			}
		}
	}
	return false
}

func (p *printer) curLineEndsWithClose() bool {
	if len(p.out) == 0 {
		return false
	}
	trimmed := strings.TrimRight(p.out[len(p.out)-1], " \t")
	if trimmed == "" {
		return false
	}
	switch trimmed[len(trimmed)-1] {
	case ')', ']', '}':
		return true
	}
	return false
}

func (p *printer) renderedBrokenClose(before int) bool {
	if len(p.out) <= before {
		return false
	}
	return p.curLineEndsWithClose()
}

func (p *printer) renderList(t *listLit) {
	inline := inlineList(t)
	if canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	p.renderElems('[', ']', t.elems)
}

func (p *printer) renderMap(t *mapLit) {
	inline := inlineMap(t)
	if canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	p.renderElems('{', '}', t.elems)
}

func (p *printer) renderElems(open, closing byte, elems []elem) {
	p.write(string(open))
	p.indent++
	p.newline()
	p.renderElemLines(elems)
	p.indent--
	p.newline()
	p.write(string(closing))
}

func (p *printer) renderElemLines(elems []elem) {
	base := 0
	for _, e := range elems {
		if e.standalone || !e.commentPresent {
			continue
		}
		if l := len(elemText(&e)) + 1; l > base {
			base = l
		}
	}
	for i, e := range elems {
		p.renderElem(i, &e, base)
	}
}

// renderElem renders one container element, aligning its trailing comment
// column against base.
func (p *printer) renderElem(i int, e *elem, base int) {
	if i > 0 {
		p.newline()
	}
	if e.standalone {
		p.writef("// %s", e.comment)
		return
	}
	if !canInline(e.value) || len(e.comments) > 0 || (e.key != nil && !canInline(e.key)) {
		p.renderBrokenElem(e)
		return
	}
	p.renderInlineElem(e, base)
}

// renderBrokenElem renders an element whose value cannot be inlined (e.g. it
// contains a comment or forced break) with its proper multi-line layout; the
// comma and any trailing comment attach to its closing line.
func (p *printer) renderBrokenElem(e *elem) {
	// For a map entry the "key: " prefix is emitted first so it isn't lost,
	// rendering a key that itself cannot be inlined (e.g. a grouped key
	// carrying a comment) with its own layout.
	if e.key != nil {
		if canInline(e.key) {
			p.write(inlineNode(e.key) + ": ")
		} else {
			p.render(e.key)
			p.write(": ")
		}
	}
	// Comments between a map key and its value stay on their own lines
	// just before the value.
	if len(e.comments) > 0 {
		for _, c := range e.comments {
			p.newline()
			p.writef("// %s", c.comment)
		}
		p.newline()
	}
	p.render(e.value)
	p.write(",")
	if e.commentPresent {
		p.writef(" // %s", e.comment)
	}
}

// renderInlineElem renders a single-line element, aligning any trailing
// comment against base.
func (p *printer) renderInlineElem(e *elem, base int) {
	text := elemText(e)
	p.write(text)
	p.write(",")
	if e.commentPresent {
		pad := base - len(text)
		if pad < 1 {
			pad = 1
		}
		p.writef("%s// %s", strings.Repeat(" ", pad), e.comment)
	}
}

func elemText(e *elem) string {
	if e.key != nil {
		return inlineNode(e.key) + ": " + inlineNode(e.value)
	}
	return inlineNode(e.value)
}

func (p *printer) renderCond(t *cond) {
	inline := inlineCond(t)
	if canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	p.render(t.c)
	p.newline()
	p.write("? ")
	if len(t.tComments) > 0 {
		p.renderComments(t.tComments)
		p.newline()
	}
	p.render(t.t)
	p.newline()
	p.write(": ")
	if len(t.fComments) > 0 {
		p.renderComments(t.fComments)
		p.newline()
	}
	p.render(t.f)
}

type binItem struct {
	op      string
	operand node
	// comments hold standalone `//` comment lines that fall between the previous
	// operand and this one, carried through chain flattening so a comment in a
	// boolean chain keeps its position across a rewrite.
	comments []elem
}

func (p *printer) renderBin(t *bin) {
	inline := inlineNode(t)
	if canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	if t.op == opAnd || t.op == opOr {
		p.renderChain(t)
		return
	}
	p.render(t.l)
	if isComparisonOp(t.op) {
		p.writef(" %s ", t.op)
	} else {
		p.newline()
		p.writef("%s ", t.op)
	}
	// A standalone comment between the operands stays on its own line just
	// before the right operand.
	for _, c := range t.comments {
		p.newline()
		p.writef("// %s", c.comment)
	}
	p.newline()
	p.render(t.r)
}

func (p *printer) renderChain(t *bin) {
	items := flattenBin(t)
	before := len(p.out)
	if c, ok := items[0].operand.(*call); ok && hasNestedCall(c) {
		p.forceBrokenCall = true
		p.render(c)
		p.forceBrokenCall = false
	} else {
		p.render(items[0].operand)
	}
	hang := p.renderedBrokenClose(before)
	for i := 1; i < len(items); i++ {
		hang = p.renderChainItem(i, items[i], items[i-1].operand, hang)
	}
}

func (p *printer) renderChainItem(i int, it binItem, prevOperand node, hang bool) bool {
	if it.op == opAnd {
		return p.renderAndItem(i, it, prevOperand, hang)
	}
	p.newline()
	p.writef("%s ", it.op)
	if len(it.comments) > 0 {
		p.renderComments(it.comments)
		p.newline()
	}
	before := len(p.out)
	p.render(it.operand)
	return p.renderedBrokenClose(before)
}

// renderComments emits standalone `//` comment lines, each on its own line.
// It ends on the last comment's line, so callers add a newline before the next
// token when comments were emitted.
func (p *printer) renderComments(comments []elem) {
	for _, c := range comments {
		p.newline()
		p.writef("// %s", c.comment)
	}
}

func (p *printer) renderAndItem(i int, it binItem, prevOperand node, hang bool) bool {
	operand := it.operand
	wrapped := false
	var pp *paren
	var isParen bool
	if pp, isParen = operand.(*paren); isParen {
		operand = pp.x
		wrapped = true
	}
	// The has-field heuristic keeps `&& a.b` on the same line as `has(a.b)`,
	// and when the operand does not fit it is grouped as `&& (operand)`. Apply
	// it whether or not the operand is already parenthesized: pass 1 emits a
	// paren-wrapped operand, which pass 2 would otherwise re-parse as an
	// explicit paren and lay out differently (the `&&` moves to the next line).
	// Re-grouping keeps the layout stable across passes. The inline seg still
	// includes the operand's own parentheses, so grouping is never dropped.
	if x := hasField(prevOperand); x != nil && contains(operand, x) {
		return p.renderHasGrouped(it, i, pp, operand, wrapped)
	}
	if hang {
		return p.renderHang(it)
	}
	return p.renderBrokenAnd(it)
}

// renderHasGrouped renders a `&& a.b` item grouped with a preceding has()
// guard, inlining when it fits and otherwise wrapping the operand in parens.
func (p *printer) renderHasGrouped(it binItem, i int, pp *paren, operand node, wrapped bool) bool {
	seg := " " + it.op + " " + inlineNode(it.operand)
	if len(it.comments) == 0 && canInline(it.operand) && p.inlineFits(seg, it.operand) {
		p.write(seg)
		return false
	}
	cont := 0
	if i != 1 {
		cont = 1
	}
	p.writef(" %s (", it.op)
	p.indent = p.indent + 1 + cont
	p.newline()
	if len(it.comments) > 0 {
		p.renderComments(it.comments)
		p.newline()
	}
	before := len(p.out)
	if wrapped {
		// The operand is already parenthesized (from a previous grouping
		// pass); re-emit its inner expression between the grouping parens so
		// the output is identical to the first pass. Preserve any comments
		// the paren carried.
		for _, c := range pp.before {
			p.renderComments([]elem{c})
			p.newline()
		}
		p.render(operand)
		if len(pp.after) > 0 {
			p.renderComments(pp.after)
		}
	} else {
		p.render(operand)
	}
	p.indent--
	p.newline()
	p.write(")")
	p.indent -= cont
	return p.renderedBrokenClose(before)
}

// renderHang renders a chain item on the same (hanging) line, breaking to a
// fresh indented line when it does not fit, preserving the operand's own
// parentheses.
func (p *printer) renderHang(it binItem) bool {
	seg := " " + it.op + " " + inlineNode(it.operand)
	if len(it.comments) == 0 && canInline(it.operand) && p.inlineFits(seg, it.operand) {
		p.write(seg)
		return p.curLineEndsWithClose()
	}
	// Does not fit on the hanging line (or is forced to break); break to a
	// fresh indented line, preserving the operand's own parentheses.
	p.indent++
	p.newline()
	p.writef("%s ", it.op)
	if len(it.comments) > 0 {
		p.renderComments(it.comments)
		p.newline()
	}
	before := len(p.out)
	p.render(it.operand)
	p.indent--
	return p.renderedBrokenClose(before)
}

// renderBrokenAnd renders a chain item that does not fit, breaking the
// operator onto a fresh indented line.
func (p *printer) renderBrokenAnd(it binItem) bool {
	p.indent++
	p.newline()
	p.writef("%s ", it.op)
	if len(it.comments) > 0 {
		p.renderComments(it.comments)
		p.newline()
	}
	before := len(p.out)
	p.render(it.operand)
	p.indent--
	return p.renderedBrokenClose(before)
}

func isComparisonOp(op string) bool {
	switch op {
	case "==", "!=", "<", "<=", ">", ">=":
		return true
	}
	return false
}

func hasField(n node) node {
	c, ok := n.(*call)
	if !ok {
		return nil
	}
	id, ok := c.name.(*ident)
	if !ok || id.text != "has" {
		return nil
	}
	if len(c.args) != 1 {
		return nil
	}
	return c.args[0].value
}

func contains(n, target node) bool {
	if eqNode(n, target) {
		return true
	}
	for _, c := range childNodes(n) {
		if contains(c, target) {
			return true
		}
	}
	return false
}

func childNodes(n node) []node {
	switch t := n.(type) {
	case *sel:
		return []node{t.obj}
	case *idx:
		return []node{t.obj, t.index}
	case *call:
		out := make([]node, 0, len(t.args)+1)
		out = append(out, t.name)
		for _, a := range t.args {
			out = append(out, a.value)
		}
		return out
	case *listLit:
		out := make([]node, 0, len(t.elems))
		for _, e := range t.elems {
			out = append(out, e.value)
		}
		return out
	case *mapLit:
		out := make([]node, 0, len(t.elems))
		for _, e := range t.elems {
			out = append(out, e.value)
		}
		return out
	case *bin:
		return []node{t.l, t.r}
	case *un:
		return []node{t.x}
	case *cond:
		return []node{t.c, t.t, t.f}
	case *paren:
		return []node{t.x}
	}
	return nil
}

func eqNode(a, b node) bool {
	if a == nil || b == nil {
		return a == b
	}
	return inlineNode(a) == inlineNode(b)
}

func flattenBin(n node) []binItem {
	op := ""
	if b, ok := n.(*bin); ok && (b.op == opAnd || b.op == opOr) {
		op = b.op
	}
	var items []binItem
	var collect func(x node, dst []binItem) []binItem
	collect = func(x node, dst []binItem) []binItem {
		if b, ok := x.(*bin); ok && b.op == op {
			dst = collect(b.l, dst)
			before := len(dst)
			dst = collect(b.r, dst)
			// This bin's between-comments belong in the gap before its right
			// side, i.e. before the first item of the flattened right subtree.
			if len(b.comments) > 0 && before < len(dst) {
				dst[before].comments = append(b.comments, dst[before].comments...)
			}
			return dst
		}
		return append(dst, binItem{operand: x})
	}
	items = collect(n, items)
	// Every gap in a flattened same-op chain is joined by the chain operator.
	for i := 1; i < len(items); i++ {
		items[i].op = op
	}
	return items
}

func inlineCall(t *call) string {
	args := make([]string, 0, len(t.args))
	for _, a := range t.args {
		s := inlineNode(a.value)
		if a.comment != "" {
			s += " // " + a.comment
		}
		args = append(args, s)
	}
	return fmt.Sprintf("%s(%s)", inlineNode(t.name), strings.Join(args, ", "))
}

// canInline reports whether n can be rendered on a single line. A node that is
// forced to break (a BreakRule) or that (transitively) contains a trailing `//`
// comment cannot be inlined: the latter would let a `//` comment swallow the
// rest of the line (e.g. `["a" // note, "b"]` drops "b"), and the former must
// keep its operands on separate lines even when nested in a container. Such a
// node must be laid out multi-line.
func canInline(n node) bool {
	if n.mustBreak() {
		return false
	}
	switch t := n.(type) {
	case *listLit:
		return canInlineList(t)
	case *mapLit:
		return canInlineMap(t)
	case *call:
		return canInlineCall(t)
	case *cond:
		return canInlineCond(t)
	case *bin:
		return canInlineBin(t)
	case *un:
		return canInlineUn(t)
	case *sel:
		return canInlineSel(t)
	case *paren:
		return canInlineParen(t)
	case *idx:
		return canInlineIdx(t)
	default: // ident, lit
		return true
	}
}

func canInlineList(t *listLit) bool {
	for _, e := range t.elems {
		if e.standalone || e.commentPresent || len(e.comments) > 0 || !canInline(e.value) {
			return false
		}
	}
	return true
}

func canInlineMap(t *mapLit) bool {
	for _, e := range t.elems {
		if e.standalone || e.commentPresent || len(e.comments) > 0 || !canInline(e.value) || (e.key != nil && !canInline(e.key)) {
			return false
		}
	}
	return true
}

func canInlineCall(t *call) bool {
	if len(t.leadComments) > 0 {
		return false
	}
	for _, a := range t.args {
		if a.standalone || a.commentPresent || !canInline(a.value) {
			return false
		}
	}
	return canInline(t.name)
}

func canInlineCond(t *cond) bool {
	return len(t.tComments) == 0 && len(t.fComments) == 0 && canInline(t.c) && canInline(t.t) && canInline(t.f)
}

func canInlineBin(t *bin) bool {
	return len(t.comments) == 0 && canInline(t.l) && canInline(t.r)
}

func canInlineUn(t *un) bool {
	return len(t.comments) == 0 && canInline(t.x)
}

func canInlineSel(t *sel) bool {
	// A selector's receiver may itself carry a comment or forced break
	// (e.g. `[1 // note\n].size()`); the selector cannot then be inlined or
	// the comment would swallow the rest of the line.
	return len(t.comments) == 0 && canInline(t.obj)
}

func canInlineParen(t *paren) bool {
	// A standalone comment between the parentheses must stay on its own
	// line, so a parenthesized expression carrying one is never inlined.
	return len(t.before) == 0 && len(t.after) == 0 && canInline(t.x)
}

func canInlineIdx(t *idx) bool {
	return len(t.leadComments) == 0 && len(t.comments) == 0 && len(t.afterComments) == 0 && canInline(t.obj) && canInline(t.index)
}

func inlineList(t *listLit) string {
	return inlineElems('[', ']', t.elems)
}

func inlineMap(t *mapLit) string {
	return inlineElems('{', '}', t.elems)
}

func inlineElems(open, closing byte, elems []elem) string {
	parts := make([]string, 0, len(elems))
	for _, e := range elems {
		s := ""
		if e.key != nil {
			s = inlineNode(e.key) + ": " + inlineNode(e.value)
		} else {
			s = inlineNode(e.value)
		}
		if e.comment != "" {
			s += " // " + e.comment
		}
		parts = append(parts, s)
	}
	return fmt.Sprintf("%c%s%c", open, strings.Join(parts, ", "), closing)
}

func inlineCond(t *cond) string {
	return fmt.Sprintf("%s ? %s : %s", inlineNode(t.c), inlineNode(t.t), inlineNode(t.f))
}

func inlineBin(t *bin) string {
	items := flattenBin(t)
	var b strings.Builder
	b.WriteString(inlineNode(items[0].operand))
	for _, it := range items[1:] {
		fmt.Fprintf(&b, " %s %s", it.op, inlineNode(it.operand))
	}
	return b.String()
}

func inlineNode(n node) string {
	switch t := n.(type) {
	case *ident:
		return t.text
	case *lit:
		return t.text
	case *sel:
		return fmt.Sprintf("%s.%s", inlineNode(t.obj), t.field)
	case *idx:
		opt := ""
		if t.optional {
			opt = "?"
		}
		return fmt.Sprintf("%s[%s%s]", inlineNode(t.obj), opt, inlineNode(t.index))
	case *call:
		return inlineCall(t)
	case *listLit:
		return inlineList(t)
	case *mapLit:
		return inlineMap(t)
	case *bin:
		if t.op == opAnd || t.op == opOr {
			return inlineBin(t)
		}
		return fmt.Sprintf("%s %s %s", inlineNode(t.l), t.op, inlineNode(t.r))
	case *un:
		return t.op + inlineNode(t.x)
	case *cond:
		return inlineCond(t)
	case *paren:
		return fmt.Sprintf("(%s)", inlineNode(t.x))
	}
	return ""
}
