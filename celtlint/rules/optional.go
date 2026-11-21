package rules

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	celtypes "cel.dev/cel-go/common/types"
	celref "cel.dev/cel-go/common/types/ref"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
)

// OptionalConfig selects which presence kinds the optional-fields rule treats
// as "optional" and thus requires a has() guard for.
type OptionalConfig struct {
	// Proto3Value requires has() for every singular proto3 field.
	Proto3Value bool `yaml:"proto3-value"`
	// Proto3Optional requires has() for proto3 `optional` fields.
	Proto3Optional bool `yaml:"proto3-optional"`
	// Proto2Existence requires has() for proto2 optional (explicit-presence)
	// fields.
	Proto2Existence bool `yaml:"proto2-existence"`
	// GoNilable requires has() for native Go nilable fields (pointer, map,
	// slice, interface, chan, func).
	GoNilable bool `yaml:"go-nilable"`
}

// Optional requires has() guards around optional field access, per its
// configured presence kinds.
type Optional struct {
	celtlint.RuleMeta
	cfg OptionalConfig
}

// ApplyConfig parses the rule's custom config, which replaces the defaults.
// Unknown keys are rejected so a misspelled option (e.g. `proto3-optoinal`)
// cannot silently disable every check.
func (o *Optional) ApplyConfig(data []byte) error {
	o.cfg = OptionalConfig{}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(&o.cfg)
}

func (o *Optional) Check(ctx *celtlint.Context) {
	ctx.ReportAll(optionalFindings(ctx, o.cfg))
}

// optionalFindings reports optional-field accesses missing a has() guard, per
// the enabled presence kinds in cfg.
func optionalFindings(ctx *celtlint.Context, cfg OptionalConfig) []celtlint.Finding {
	if ctx.Index == nil || ctx.Ast == nil || !ctx.Ast.IsChecked() {
		return nil
	}
	var required celtindex.PresenceKind
	if cfg.Proto3Value {
		required |= celtindex.PresenceProto3Value
	}
	if cfg.Proto3Optional {
		required |= celtindex.PresenceProto3Optional
	}
	if cfg.Proto2Existence {
		required |= celtindex.PresenceProto2Existence
	}
	if cfg.GoNilable {
		required |= celtindex.PresenceGoNilable
	}
	return checkOptionalFields(ctx, required)
}

func checkOptionalFields(ctx *celtlint.Context, required celtindex.PresenceKind) []celtlint.Finding {
	if required == 0 {
		return nil
	}
	ast := ctx.Ast.NativeRep()
	if ast == nil || !ast.IsChecked() {
		return nil
	}
	content := ctx.Source.Content()
	o := &optionalFacts{
		idx:      ctx.Index,
		required: required,
		sInfo:    ast.SourceInfo(),
		ro:       celtsource.RuneOffsets(content),
		content:  content,
	}
	o.walk(celast.NavigateAST(ast), map[string]bool{}, nil)
	return o.out
}

// optionalFacts carries the shared context for branch-aware presence analysis:
// which optional fields are guaranteed present when a boolean expression
// evaluates true/false, driving has() guarding.
type optionalFacts struct {
	idx      *celtindex.Index
	required celtindex.PresenceKind
	sInfo    *celast.SourceInfo
	ro       []int
	content  string
	out      []celtlint.Finding
}

func (*optionalFacts) keyFor(recv, field string) string { return recv + "." + field }

// posWhenTrue reports the optional fields guaranteed present when e evaluates
// true.
func (o *optionalFacts) posWhenTrue(e celast.NavigableExpr, scopes []presenceScope) map[string]bool {
	switch e.Kind() {
	case celast.SelectKind:
		if sel := e.AsSelect(); sel.IsTestOnly() {
			// A guard whose receiver can't be identified (key == "") must
			// not supply a presence fact.
			if key, _, field, isOpt := optionalReceiver(o.idx, o.required, o.sInfo, o.ro, o.content, sel, scopes); isOpt && key != "" {
				return map[string]bool{o.keyFor(key, field): true}
			}
		}
	case celast.CallKind:
		fn := e.AsCall().FunctionName()
		ch := e.Children()
		switch fn {
		case operators.LogicalNot:
			return o.posWhenFalse(ch[0], scopes)
		case operators.LogicalAnd:
			return unionFacts(o.posWhenTrue(ch[0], scopes), o.posWhenTrue(ch[1], scopes))
		case operators.LogicalOr:
			return interFacts(o.posWhenTrue(ch[0], scopes), o.posWhenTrue(ch[1], scopes))
		case operators.Equals, operators.NotEquals:
			return o.posWhenTrueBool(ch, fn, scopes)
		}
	}
	return map[string]bool{}
}

// posWhenFalse reports the optional fields guaranteed present when e evaluates
// false.
func (o *optionalFacts) posWhenFalse(e celast.NavigableExpr, scopes []presenceScope) map[string]bool {
	switch e.Kind() {
	case celast.SelectKind:
		// has(x.f) false -> no field is known present.
		return map[string]bool{}
	case celast.CallKind:
		fn := e.AsCall().FunctionName()
		ch := e.Children()
		switch fn {
		case operators.LogicalNot:
			return o.posWhenTrue(ch[0], scopes)
		case operators.LogicalAnd:
			// a && b is false when a is false, or a is true and b is false.
			return interFacts(o.posWhenFalse(ch[0], scopes), unionFacts(o.posWhenTrue(ch[0], scopes), o.posWhenFalse(ch[1], scopes)))
		case operators.LogicalOr:
			// a || b is false only when both are false.
			return unionFacts(o.posWhenFalse(ch[0], scopes), o.posWhenFalse(ch[1], scopes))
		case operators.Equals, operators.NotEquals:
			if len(ch) >= 2 {
				if isBoolLit(ch[1], true) {
					if fn == operators.NotEquals {
						return o.posWhenTrue(ch[0], scopes)
					}
					return o.posWhenFalse(ch[0], scopes)
				}
				if isBoolLit(ch[1], false) {
					if fn == operators.NotEquals {
						return o.posWhenFalse(ch[0], scopes)
					}
					return o.posWhenTrue(ch[0], scopes)
				}
			}
		}
	}
	return map[string]bool{}
}

// posWhenTrueBool handles a boolean-comparison guard (`a == b` / `a != b`)
// where one side is a bool literal, returning the facts known true when the
// comparison is true. `== true`/`!= false` preserve the operand's truth;
// `== false`/`!= true` invert it (so a guard tested against false does not
// protect the access).
func (o *optionalFacts) posWhenTrueBool(ch []celast.NavigableExpr, fn string, scopes []presenceScope) map[string]bool {
	if len(ch) >= 2 {
		if isBoolLit(ch[1], true) {
			if fn == operators.NotEquals {
				return o.posWhenFalse(ch[0], scopes)
			}
			return o.posWhenTrue(ch[0], scopes)
		}
		if isBoolLit(ch[1], false) {
			if fn == operators.NotEquals {
				return o.posWhenTrue(ch[0], scopes)
			}
			return o.posWhenFalse(ch[0], scopes)
		}
		if isBoolLit(ch[0], true) {
			if fn == operators.NotEquals {
				return o.posWhenFalse(ch[1], scopes)
			}
			return o.posWhenTrue(ch[1], scopes)
		}
		if isBoolLit(ch[0], false) {
			if fn == operators.NotEquals {
				return o.posWhenTrue(ch[1], scopes)
			}
			return o.posWhenFalse(ch[1], scopes)
		}
	}
	return map[string]bool{}
}

// walk descends the expression tree, threading the set of known-present
// optional fields and the comprehension scopes, and reporting any unguarded
// access.
func (o *optionalFacts) walk(e celast.NavigableExpr, present map[string]bool, scopes []presenceScope) {
	switch e.Kind() {
	case celast.ComprehensionKind:
		// A comprehension binds its iter/accu variables in a new scope, so
		// references to them are distinct from any same-named outer
		// variable (e.g. the shadowed `p` in `[q].exists(p, ...)`). The
		// iteration range and accumulator init are evaluated in the
		// enclosing scope (the bindings do not exist yet), so they are
		// walked before the new scope is pushed; only the loop body uses
		// the new bindings.
		c := e.AsComprehension()
		if r, ok := c.IterRange().(celast.NavigableExpr); ok {
			o.walk(r, present, scopes)
		}
		if a, ok := c.AccuInit().(celast.NavigableExpr); ok {
			o.walk(a, present, scopes)
		}
		scopes = append(scopes, presenceScopeFor(e))
		if cond, ok := c.LoopCondition().(celast.NavigableExpr); ok {
			o.walk(cond, present, scopes)
		}
		if step, ok := c.LoopStep().(celast.NavigableExpr); ok {
			o.walk(step, present, scopes)
		}
		if res, ok := c.Result().(celast.NavigableExpr); ok {
			o.walk(res, present, scopes)
		}
		return
	case celast.SelectKind:
		o.reportSelect(e, present, scopes)
		return
	case celast.CallKind:
		fn := e.AsCall().FunctionName()
		ch := e.Children()
		switch fn {
		case operators.Conditional: // c ? a : b
			o.walk(ch[0], present, scopes)
			o.walk(ch[1], unionFacts(present, o.posWhenTrue(ch[0], scopes)), scopes)
			o.walk(ch[2], unionFacts(present, o.posWhenFalse(ch[0], scopes)), scopes)
			return
		case operators.LogicalAnd:
			o.walk(ch[0], present, scopes)
			o.walk(ch[1], unionFacts(present, o.posWhenTrue(ch[0], scopes)), scopes)
			return
		case operators.LogicalOr:
			o.walk(ch[0], present, scopes)
			o.walk(ch[1], unionFacts(present, o.posWhenFalse(ch[0], scopes)), scopes)
			return
		}
	}
	for _, ch := range e.Children() {
		o.walk(ch, present, scopes)
	}
}

// reportSelect reports an unguarded optional-field access on a select.
func (o *optionalFacts) reportSelect(e celast.NavigableExpr, present map[string]bool, scopes []presenceScope) {
	sel := e.AsSelect()
	if sel.IsTestOnly() {
		return
	}
	key, display, field, isOpt := optionalReceiver(o.idx, o.required, o.sInfo, o.ro, o.content, sel, scopes)
	if !isOpt || present[o.keyFor(key, field)] {
		return
	}
	startOff, endOff := celtlint.ExprOffsets(o.sInfo, o.ro, o.content, e)
	// The select's span stop is the position right after the
	// dot; the field name may be separated from it by
	// whitespace (e.g. `p.  nickname`). Locate the actual field
	// token rather than synthesizing a length from the dot, so
	// the range covers the whole field name. An escaped field
	// name (e.g. `nick-name`) also needs backticks to form valid
	// CEL, so its range starts at the field token instead of the
	// receiver. A select's own offset range starts at the dot,
	// so derive the receiver's leftmost offset by walking the
	// whole select expression; the fix wraps the entire select.
	fixStart, _ := celtlint.ExprByteSpan(o.sInfo, o.ro, e)
	tok := endOff
	for tok < len(o.content) && celtsource.IsSpace(o.content[tok]) {
		tok++
	}
	ft := fieldToken(field)
	if ft != field {
		startOff = endOff
		endOff = tok + len(ft)
	} else {
		endOff = tok + len(field)
	}
	hint := "guard this optional field access with a has() check"
	if display != "" {
		hint = fmt.Sprintf("guard access with has(%s.%s)", display, ft)
	}
	o.out = append(o.out, celtlint.Finding{
		Level:    celtlint.LevelWarn,
		Code:     celtlint.CodeMissingHasCheck,
		Message:  "accessing an optional field without a has() check",
		Start:    celtsource.OffsetToPos(o.content, startOff),
		End:      celtsource.OffsetToPos(o.content, endOff),
		Hint:     hint,
		Fix:      "has(" + o.content[fixStart:endOff] + ")",
		FixStart: celtsource.OffsetToPos(o.content, fixStart),
		FixEnd:   celtsource.OffsetToPos(o.content, endOff),
	})
}

// isBoolLit reports whether e is a boolean literal with value val.
func isBoolLit(e celast.NavigableExpr, val bool) bool {
	if e.Kind() != celast.LiteralKind {
		return false
	}
	b, ok := e.AsLiteral().(celtypes.Bool)
	if !ok {
		return false
	}
	return bool(b) == val
}

func unionFacts(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func interFacts(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		if b[k] {
			out[k] = true
		}
	}
	return out
}

// optionalReceiver reports whether sel selects a field whose presence kind
// intersects the rule's required mask. If so it returns a binding-aware,
// structurally-canonical key for the receiver and the field name. The key
// distinguishes shadowed comprehension variables from same-named outer
// variables and includes the full receiver expression (e.g. list indices), so
// two different receivers never share a fact. display is the receiver's source
// text, used in diagnostics (the key may contain internal scope prefixes).
// key may be "" when the receiver cannot be rendered; callers must then not
// carry (or match) a fact, but isOpt is still true so the access is reported.
func optionalReceiver(idx *celtindex.Index, required celtindex.PresenceKind, sInfo *celast.SourceInfo,
	ro []int, content string, sel celast.SelectExpr, scopes []presenceScope) (key, display, field string, isOpt bool) {
	op, isNav := sel.Operand().(celast.NavigableExpr)
	if !isNav {
		return "", "", "", false
	}
	recvType := op.Type()
	if recvType == nil || recvType.Kind() != celtypes.StructKind {
		return "", "", "", false
	}
	field = sel.FieldName()
	if idx.FieldPresence(recvType.TypeName(), field)&required == 0 {
		return "", "", "", false
	}
	return receiverKey(op, scopes), receiverDisplay(sInfo, ro, content, op), field, true
}

// receiverDisplay returns source text for a receiver expression, for use in
// diagnostics so hints show CEL the user can write. Simple identifier/select
// chains, lists, indices and member calls are rebuilt from the AST (source
// offset ranges are unreliable for chained/compound expressions); anything
// else falls back to its source span, and an empty string when no span is
// available so callers fall back to a generic hint.
func receiverDisplay(sInfo *celast.SourceInfo, ro []int, content string, op celast.NavigableExpr) string {
	if p, ok := receiverText(op); ok {
		return p
	}
	if r, ok := sInfo.GetOffsetRange(op.ID()); ok {
		start := celtsource.ByteOffset(ro, int(r.Start))
		stop := celtsource.ByteOffset(ro, int(r.Stop))
		if start >= 0 && stop <= len(content) && start < stop {
			return content[start:stop]
		}
	}
	start, stop := celtlint.ExprByteSpan(sInfo, ro, op)
	if start >= 0 && stop <= len(content) && start < stop {
		return content[start:stop]
	}
	return ""
}

// fieldToken returns a field name as valid CEL source, backtick-quoting it when
// it is not a plain identifier (e.g. a field exposed as `nick-name`).
func fieldToken(field string) string {
	if field == "" || !celtsource.IsIdentStart(field[0]) {
		return "`" + field + "`"
	}
	for i := 1; i < len(field); i++ {
		if !celtsource.IsIdentByte(field[i]) {
			return "`" + field + "`"
		}
	}
	return field
}

// receiverText rebuilds the source text of a receiver expression, returning
// false for any construct it cannot render faithfully. It covers identifiers,
// field selects, lists, indices and calls so compound receivers (e.g.
// `p.pick(1)` or `[p, q][0]`) produce valid CEL for diagnostics and hints.
func receiverText(e celast.NavigableExpr) (string, bool) {
	switch e.Kind() {
	case celast.IdentKind:
		return e.AsIdent(), true
	case celast.SelectKind:
		return receiverTextSelect(e)
	case celast.LiteralKind:
		if text, ok := literalDisplay(e.AsLiteral()); ok {
			return text, true
		}
		return "", false
	case celast.ListKind:
		return receiverTextList(e)
	case celast.MapKind:
		return receiverTextMap(e)
	case celast.CallKind:
		return receiverTextCall(e)
	}
	return "", false
}

// receiverTextSelect renders a field-select receiver as `prefix.field`.
func receiverTextSelect(e celast.NavigableExpr) (string, bool) {
	parent, ok := e.AsSelect().Operand().(celast.NavigableExpr)
	if !ok {
		return "", false
	}
	prefix, ok := receiverText(parent)
	if !ok {
		return "", false
	}
	return prefix + "." + e.AsSelect().FieldName(), true
}

// receiverTextList renders a list-literal receiver as `[elem, ...]`.
func receiverTextList(e celast.NavigableExpr) (string, bool) {
	var parts []string
	for _, el := range e.AsList().Elements() {
		nev, ok := el.(celast.NavigableExpr)
		if !ok {
			return "", false
		}
		s, ok := receiverText(nev)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	return "[" + strings.Join(parts, ", ") + "]", true
}

// receiverTextMap renders a map-literal receiver as `{k: v, ...}`.
func receiverTextMap(e celast.NavigableExpr) (string, bool) {
	var parts []string
	for _, en := range e.AsMap().Entries() {
		me := en.AsMapEntry()
		k, ok1 := me.Key().(celast.NavigableExpr)
		v, ok2 := me.Value().(celast.NavigableExpr)
		if !ok1 || !ok2 {
			return "", false
		}
		ks, ok := receiverText(k)
		if !ok {
			return "", false
		}
		vs, ok := receiverText(v)
		if !ok {
			return "", false
		}
		parts = append(parts, ks+": "+vs)
	}
	return "{" + strings.Join(parts, ", ") + "}", true
}

// receiverTextCall renders a call receiver: an index `recv[idx]`, or a
// (possibly member) function invocation `recv.fn(args)`.
func receiverTextCall(e celast.NavigableExpr) (string, bool) {
	c := e.AsCall()
	if c.FunctionName() == operators.Index {
		// _[_] carries the receiver as args[0] and the index as the last
		// arg; Target() is an unspecified placeholder.
		if len(c.Args()) < 2 {
			return "", false
		}
		recv, ok1 := c.Args()[0].(celast.NavigableExpr)
		idx, ok2 := c.Args()[len(c.Args())-1].(celast.NavigableExpr)
		if !ok1 || !ok2 {
			return "", false
		}
		rs, ok := receiverText(recv)
		if !ok {
			return "", false
		}
		is, ok := receiverText(idx)
		if !ok {
			return "", false
		}
		return rs + "[" + is + "]", true
	}
	fn := c.FunctionName()
	var parts []string
	for _, a := range c.Args() {
		nev, ok := a.(celast.NavigableExpr)
		if !ok {
			return "", false
		}
		s, ok := receiverText(nev)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	call := fn + "(" + strings.Join(parts, ", ") + ")"
	if target, hasTarget := realCallTarget(c); hasTarget {
		ts, ok := receiverText(target)
		if !ok {
			return "", false
		}
		return ts + "." + call, true
	}
	return call, true
}

// presenceScope records the variables a comprehension binds, scoped by the
// comprehension's expression id so shadowed names resolve distinctly.
type presenceScope struct {
	id    int64
	names map[string]bool
}

func presenceScopeFor(e celast.NavigableExpr) presenceScope {
	c := e.AsComprehension()
	names := map[string]bool{}
	if v := c.IterVar(); v != "" {
		names[v] = true
	}
	if c.HasIterVar2() {
		if v := c.IterVar2(); v != "" {
			names[v] = true
		}
	}
	if v := c.AccuVar(); v != "" {
		names[v] = true
	}
	return presenceScope{id: e.ID(), names: names}
}

// receiverKey renders a receiver expression into a binding-aware canonical key:
// the full structure (idents, selects, indices, lists) is preserved so distinct
// receivers differ, and an identifier shadowed by an active comprehension scope
// is prefixed with that scope so it cannot be confused with the outer binding.
func receiverKey(e celast.NavigableExpr, scopes []presenceScope) string {
	switch e.Kind() {
	case celast.IdentKind:
		return qualifiedIdent(e.AsIdent(), scopes)
	case celast.LiteralKind:
		return literalKey(e.AsLiteral())
	case celast.SelectKind:
		return receiverKeySelect(e, scopes)
	case celast.ListKind:
		return receiverKeyList(e, scopes)
	case celast.CallKind:
		return receiverKeyCall(e, scopes)
	}
	return ""
}

// receiverKeySelect renders a field-select receiver key as `prefix.field`.
func receiverKeySelect(e celast.NavigableExpr, scopes []presenceScope) string {
	sel := e.AsSelect()
	op, ok := sel.Operand().(celast.NavigableExpr)
	if !ok {
		return ""
	}
	if p := receiverKey(op, scopes); p != "" {
		return p + "." + sel.FieldName()
	}
	return ""
}

// receiverKeyList renders a list-literal receiver key as `[elem, ...]`.
func receiverKeyList(e celast.NavigableExpr, scopes []presenceScope) string {
	var b strings.Builder
	b.WriteString("[")
	for i, el := range e.AsList().Elements() {
		if i > 0 {
			b.WriteString(", ")
		}
		nev, ok := el.(celast.NavigableExpr)
		if !ok {
			return ""
		}
		if k := receiverKey(nev, scopes); k != "" {
			b.WriteString(k)
		} else {
			return ""
		}
	}
	b.WriteString("]")
	return b.String()
}

// receiverKeyCall renders a call receiver key: an index `recv[idx]`, or a
// (possibly member) function invocation `recv.fn(args)`.
func receiverKeyCall(e celast.NavigableExpr, scopes []presenceScope) string {
	c := e.AsCall()
	if c.FunctionName() == operators.Index {
		// _[_] carries the receiver as args[0] and the index as the last
		// arg; Target() is an unspecified placeholder.
		if len(c.Args()) < 2 {
			return ""
		}
		target, ok1 := c.Args()[0].(celast.NavigableExpr)
		idx, ok2 := c.Args()[len(c.Args())-1].(celast.NavigableExpr)
		if !ok1 || !ok2 {
			return ""
		}
		tk := receiverKey(target, scopes)
		if tk == "" {
			return ""
		}
		ik := receiverKey(idx, scopes)
		if ik == "" {
			return ""
		}
		return tk + "[" + ik + "]"
	}
	// A member-call receiver (e.g. `p.pick(1)`); key by the method and
	// arguments so distinct calls (p.pick(0) vs p.pick(1)) never share
	// a presence fact. Args() excludes the receiver; Target() holds it.
	// If any argument cannot be identified (e.g. a map literal), the
	// whole receiver is unidentified: returning "" makes callers drop
	// the fact rather than conflating calls whose arguments differ.
	fn := c.FunctionName()
	target, hasTarget := realCallTarget(c)
	var tk string
	if hasTarget {
		tk = receiverKey(target, scopes)
		if tk == "" {
			return ""
		}
	}
	var parts []string
	for _, a := range c.Args() {
		nev, ok := a.(celast.NavigableExpr)
		if !ok {
			return ""
		}
		ak := receiverKey(nev, scopes)
		if ak == "" {
			return ""
		}
		parts = append(parts, ak)
	}
	if hasTarget {
		return tk + "." + fn + "(" + strings.Join(parts, ", ") + ")"
	}
	return fn + "(" + strings.Join(parts, ", ") + ")"
}

// qualifiedIdent returns a binding-aware identifier key: the bare name when not
// shadowed, otherwise the name prefixed by the innermost comprehension scope
// that binds it.
func qualifiedIdent(name string, scopes []presenceScope) string {
	for i := len(scopes) - 1; i >= 0; i-- {
		if scopes[i].names[name] {
			return fmt.Sprintf("c%d.%s", scopes[i].id, name)
		}
	}
	return name
}

// literalKey renders a literal value with its type tag so distinct index
// literals (e.g. 0 vs 1) produce distinct receiver keys. String values are
// quoted so their contents cannot be mistaken for the "," separator used when
// joining a call's arguments (e.g. "a, b" must not read as two arguments).
func literalKey(v celref.Val) string {
	if v == nil {
		return "nil"
	}
	if s, ok := v.(celtypes.String); ok {
		return "string:" + strconv.Quote(string(s))
	}
	return fmt.Sprintf("%s:%v", v.Type().TypeName(), v.Value())
}

// realCallTarget returns a call's receiver when it is a real expression (a
// member call such as `p.pick(...)`), reporting false for a global call whose
// target is the AST's unspecified placeholder (`choose(...)`).
func realCallTarget(c celast.CallExpr) (celast.NavigableExpr, bool) {
	t, ok := c.Target().(celast.NavigableExpr)
	if !ok || t.Kind() == celast.UnspecifiedExprKind {
		return nil, false
	}
	return t, true
}

// literalDisplay renders a literal as faithful CEL source so diagnostics and
// hints show what the user wrote (e.g. a string key with quotes). Returns
// false for a literal it cannot render.
func literalDisplay(v celref.Val) (string, bool) {
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
		return strconv.FormatFloat(float64(x), 'g', -1, 64), true
	case celtypes.String:
		return strconv.Quote(string(x)), true
	case celtypes.Bytes:
		return `b` + strconv.Quote(string(x)), true
	}
	return "", false
}
