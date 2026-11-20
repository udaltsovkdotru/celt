package rules

import (
	"github.com/udaltsovkdotru/celt/celtlint"
)

// RawEnumValue flags use of raw enum literals instead of named values.
type RawEnumValue struct {
	celtlint.RuleMeta
}

func (RawEnumValue) Check(ctx *celtlint.Context) {
	ctx.ReportAll(rawEnumValueFindings(ctx))
}
