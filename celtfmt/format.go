package celtfmt

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"cel.dev/cel-go/common"

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

func formatExpr(src string, s settings) (string, error) {
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
	return strings.Join(p.out, "\n"), nil
}

type printer struct {
	settings
	out    []string
	col    int
	indent int

	forceBrokenCall bool
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
		p.render(t.obj)
		p.writef(".%s", t.field)
	case *idx:
		p.render(t.obj)
		p.write("[")
		if t.optional {
			p.write("?")
		}
		p.render(t.index)
		p.write("]")
	case *call:
		p.renderCall(t)
	case *listLit:
		p.renderList(t)
	case *mapLit:
		p.renderMap(t)
	case *bin:
		p.renderBin(t)
	case *un:
		p.write(t.op)
		p.render(t.x)
	case *cond:
		p.renderCond(t)
	case *paren:
		inline := "(" + inlineNode(t.x) + ")"
		if canInline(t) && p.inlineFits(inline, t) {
			p.write(inline)
			return
		}
		p.write("(")
		p.indent++
		p.newline()
		p.render(t.x)
		p.indent--
		p.newline()
		p.write(")")
	}
}

func (p *printer) renderCall(t *call) {
	inline := inlineCall(t)
	if !p.forceBrokenCall && canInline(t) && p.inlineFits(inline, t) {
		p.write(inline)
		return
	}
	p.render(t.name)
	p.write("(")
	closeIndent := p.indent
	p.indent++
	freshLine := true
	for i, av := range t.args {
		freshLine = p.renderCallArg(i, av, freshLine)
		if i < len(t.args)-1 {
			p.write(",")
		}
	}
	p.indent = closeIndent
	if lastAttached(t) {
		p.write(")")
	} else {
		p.newline()
		p.write(")")
	}
}

func (p *printer) renderCallArg(i int, av callArg, freshLine bool) bool {
	a := av.value
	switch v := a.(type) {
	case *listLit:
		p.renderCallArgContainer('[', ']', v.elems, av.comment, i)
		return true
	case *mapLit:
		p.renderCallArgContainer('{', '}', v.elems, av.comment, i)
		return true
	default:
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
			if av.comment != "" {
				p.writef(" // %s", av.comment)
			}
			return false
		}
		p.render(a)
		if av.comment != "" {
			p.writef(" // %s", av.comment)
		}
		return true
	}
}

func (p *printer) renderCallArgContainer(open, closing byte, elems []elem, comment string, i int) {
	if i > 0 {
		p.newline()
	}
	p.write(string(open))
	p.newline()
	p.renderElemLines(elems)
	p.indent--
	p.newline()
	p.write(string(closing))
	p.indent++
	if comment != "" {
		p.writef(" // %s", comment)
	}
}

func lastAttached(t *call) bool {
	if len(t.args) == 0 {
		return false
	}
	switch t.args[len(t.args)-1].value.(type) {
	case *listLit, *mapLit:
		return true
	}
	return false
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
		if e.comment == "" {
			continue
		}
		if l := len(elemText(e)) + 1; l > base {
			base = l
		}
	}
	for i, e := range elems {
		if i > 0 {
			p.newline()
		}
		text := elemText(e)
		p.write(text)
		p.write(",")
		if e.comment != "" {
			pad := base - len(text)
			if pad < 1 {
				pad = 1
			}
			p.writef("%s// %s", strings.Repeat(" ", pad), e.comment)
		}
	}
}

func elemText(e elem) string {
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
	p.render(t.t)
	p.newline()
	p.write(": ")
	p.render(t.f)
}

type binItem struct {
	op      string
	operand node
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
	before := len(p.out)
	p.render(it.operand)
	return p.renderedBrokenClose(before)
}

func (p *printer) renderAndItem(i int, it binItem, prevOperand node, hang bool) bool {
	operand := it.operand
	if pp, ok := operand.(*paren); ok {
		operand = pp.x
	}
	if x := hasField(prevOperand); x != nil && contains(operand, x) {
		seg := " " + it.op + " " + inlineNode(operand)
		if canInline(operand) && p.fits(seg) {
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
		before := len(p.out)
		p.render(operand)
		p.indent--
		p.newline()
		p.write(")")
		p.indent -= cont
		return p.renderedBrokenClose(before)
	}
	if hang {
		seg := " " + it.op + " " + inlineNode(operand)
		if canInline(operand) && p.fits(seg) {
			p.write(seg)
			return p.curLineEndsWithClose()
		}
		// Does not fit on the hanging line; break to a fresh indented line.
		p.indent++
		p.newline()
		p.writef("%s ", it.op)
		before := len(p.out)
		p.render(it.operand)
		p.indent--
		return p.renderedBrokenClose(before)
	}
	p.indent++
	p.newline()
	p.writef("%s ", it.op)
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
	var operands []node
	var collect func(x node)
	collect = func(x node) {
		if b, ok := x.(*bin); ok && b.op == op {
			collect(b.l)
			collect(b.r)
			return
		}
		operands = append(operands, x)
	}
	collect(n)
	items := make([]binItem, len(operands))
	if len(operands) == 0 {
		return nil
	}
	items[0] = binItem{operand: operands[0]}
	for i := 1; i < len(operands); i++ {
		items[i] = binItem{op: op, operand: operands[i]}
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

// canInline reports whether n can be rendered on a single line. A node that
// (transitively) contains a trailing `//` comment cannot: CEL has no block
// comments, so joining such a comment onto one line would let it swallow the
// rest of the line (e.g. `["a" // note, "b"]` drops "b"). Such a node must be
// laid out multi-line so each comment stays at the end of its own line.
func canInline(n node) bool {
	switch t := n.(type) {
	case *listLit:
		for _, e := range t.elems {
			if e.comment != "" || !canInline(e.value) {
				return false
			}
		}
		return true
	case *mapLit:
		for _, e := range t.elems {
			if e.comment != "" || !canInline(e.value) || (e.key != nil && !canInline(e.key)) {
				return false
			}
		}
		return true
	case *call:
		for _, a := range t.args {
			if a.comment != "" || !canInline(a.value) {
				return false
			}
		}
		return canInline(t.name)
	case *cond:
		return canInline(t.c) && canInline(t.t) && canInline(t.f)
	case *bin:
		return canInline(t.l) && canInline(t.r)
	case *un:
		return canInline(t.x)
	case *paren:
		return canInline(t.x)
	case *idx:
		return canInline(t.obj) && canInline(t.index)
	default: // ident, lit, sel
		return true
	}
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
