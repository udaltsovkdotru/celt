package rules

import (
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"

	"github.com/udaltsovkdotru/celt/celtfmt"
)

// breakOperatorsRule forces line breaks on boolean operators and conditionals.
type breakOperatorsRule struct {
	celtfmt.FormatMeta
}

func (breakOperatorsRule) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }

func (breakOperatorsRule) MustBreak(e celast.Expr) bool {
	if e.Kind() != celast.CallKind {
		return false
	}
	switch e.AsCall().FunctionName() {
	case operators.LogicalAnd, operators.LogicalOr, operators.Conditional:
		return true
	}
	return false
}

// NewBreakOperatorsRule returns a celtfmt.FormatRule that forces line breaks
// on boolean operators and conditionals. It is disabled by default.
func NewBreakOperatorsRule() celtfmt.FormatRule {
	return breakOperatorsRule{FormatMeta: celtfmt.FormatMeta{
		Name:     "break-operators",
		Desc:     "force line breaks on boolean operators and conditionals",
		Defaults: celtfmt.FormatParams{Enabled: false},
	}}
}
