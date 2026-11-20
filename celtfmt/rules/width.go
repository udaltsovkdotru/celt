package rules

import (
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtfmt"
)

// Width is the built-in line-width rule.
type Width struct {
	celtfmt.FormatMeta
	width int
}

func (*Width) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }
func (r *Width) Width() int                               { return r.width }

// NewWidthRule returns a celtfmt.FormatRule carrying the given maximum line
// width, enabled by default.
func NewWidthRule(width int) celtfmt.FormatRule {
	return &Width{
		FormatMeta: celtfmt.FormatMeta{
			Name:     "width",
			Desc:     "maximum line width",
			Defaults: celtfmt.FormatParams{Enabled: true},
		},
		width: width,
	}
}
