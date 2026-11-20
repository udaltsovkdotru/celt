package rules

import (
	"github.com/udaltsovkdotru/celt/celtlint"
)

// EnumTypeMismatch flags enum comparisons of mismatched types.
type EnumTypeMismatch struct {
	celtlint.RuleMeta
}

func (EnumTypeMismatch) Check(ctx *celtlint.Context) {
	ctx.ReportAll(enumTypeMismatchFindings(ctx))
}
