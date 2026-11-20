package rules

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

	celast "cel.dev/cel-go/common/ast"
	celtypes "cel.dev/cel-go/common/types"

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
func (o *Optional) ApplyConfig(data []byte) error {
	o.cfg = OptionalConfig{}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return yaml.Unmarshal(data, &o.cfg)
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
	idx := ctx.Index
	ast := ctx.Ast.NativeRep()
	if ast == nil || !ast.IsChecked() {
		return nil
	}
	content := ctx.Source.Content()
	ro := celtsource.RuneOffsets(content)
	sInfo := ast.SourceInfo()

	root := celast.NavigateAST(ast)

	// Collect optional-field accesses guarded by has(). In the checked AST a
	// has(x.f) call expands to a presence-test (test-only) select, so a
	// guarded access is exactly a test-only select on an optional field.
	guarded := map[string]bool{}
	celast.PostOrderVisit(root, celast.NewExprVisitor(func(e celast.Expr) {
		n, ok := e.(celast.NavigableExpr)
		if !ok || n.Kind() != celast.SelectKind {
			return
		}
		sel := n.AsSelect()
		if !sel.IsTestOnly() {
			return
		}
		if recv, field, isOptional := optionalReceiver(idx, required, sInfo, ro, content, sel); isOptional {
			guarded[recv+"."+field] = true
		}
	}))

	var out []celtlint.Finding
	celast.PostOrderVisit(root, celast.NewExprVisitor(func(e celast.Expr) {
		n, ok := e.(celast.NavigableExpr)
		if !ok || n.Kind() != celast.SelectKind {
			return
		}
		sel := n.AsSelect()
		if sel.IsTestOnly() {
			return
		}
		recv, field, isOptional := optionalReceiver(idx, required, sInfo, ro, content, sel)
		if !isOptional || guarded[recv+"."+field] {
			return
		}
		startOff, endOff := celtlint.ExprOffsets(sInfo, ro, content, n)
		out = append(out, celtlint.Finding{
			Level:   celtlint.LevelWarn,
			Code:    celtlint.CodeMissingHasCheck,
			Message: "accessing an optional field without a has() check",
			Start:   celtsource.OffsetToPos(content, startOff),
			End:     celtsource.OffsetToPos(content, endOff),
			Hint:    fmt.Sprintf("guard access with has(%s.%s)", recv, field),
		})
	}))
	return out
}

// optionalReceiver reports whether sel selects a field whose presence kind
// intersects the rule's required mask. If so it returns the receiver's source
// text and the field name.
func optionalReceiver(idx *celtindex.Index, required celtindex.PresenceKind, sInfo *celast.SourceInfo,
	ro []int, content string, sel celast.SelectExpr) (recv, field string, ok bool) {
	op, isNav := sel.Operand().(celast.NavigableExpr)
	if !isNav {
		return "", "", false
	}
	recvType := op.Type()
	if recvType == nil || recvType.Kind() != celtypes.StructKind {
		return "", "", false
	}
	field = sel.FieldName()
	if idx.FieldPresence(recvType.TypeName(), field)&required == 0 {
		return "", "", false
	}
	recv, ok = receiverPath(op)
	if !ok {
		recv = receiverText(sInfo, ro, content, op)
	}
	return recv, field, true
}

// receiverPath reconstructs the dotted identifier path of a receiver
// expression (e.g. `user.address` for the operand of `.city`). Source
// offset ranges are unreliable for chained selects, so the path is rebuilt
// from the AST. Returns false for receivers that aren't a simple field chain.
func receiverPath(e celast.NavigableExpr) (string, bool) {
	switch e.Kind() {
	case celast.IdentKind:
		return e.AsIdent(), true
	case celast.SelectKind:
		parent, ok := e.AsSelect().Operand().(celast.NavigableExpr)
		if !ok {
			return "", false
		}
		prefix, ok := receiverPath(parent)
		if !ok {
			return "", false
		}
		return prefix + "." + e.AsSelect().FieldName(), true
	}
	return "", false
}

func receiverText(sInfo *celast.SourceInfo, ro []int, content string, e celast.NavigableExpr) string {
	if r, ok := sInfo.GetOffsetRange(e.ID()); ok {
		start := celtsource.ByteOffset(ro, int(r.Start))
		stop := celtsource.ByteOffset(ro, int(r.Stop))
		if start >= 0 && stop <= len(content) && start < stop {
			return content[start:stop]
		}
	}
	// Intermediate select operands may lack a recorded offset range; fall back
	// to the full byte span of the expression.
	start, stop := celtlint.ExprByteSpan(sInfo, ro, e)
	if start >= 0 && stop <= len(content) && start < stop {
		return content[start:stop]
	}
	return ""
}
