package rules

import (
	"github.com/udaltsovkdotru/celt/celtlint"
)

// EnumComparison flags suspicious comparisons between enum values.
type EnumComparison struct {
	celtlint.RuleMeta
}

func (EnumComparison) Check(ctx *celtlint.Context) {
	ctx.ReportAll(enumCompareFindings(ctx))
}
