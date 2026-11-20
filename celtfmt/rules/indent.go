package rules

import (
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtfmt"
)

// Indent is the built-in indentation rule.
type Indent struct {
	celtfmt.FormatMeta
	indent string
}

func (*Indent) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }
func (r *Indent) Indent() string                           { return r.indent }

// NewIndentRule returns a celtfmt.FormatRule carrying the given indentation
// string, enabled by default.
func NewIndentRule(indent string) celtfmt.FormatRule {
	return &Indent{
		FormatMeta: celtfmt.FormatMeta{
			Name:     "indent",
			Desc:     "indentation string",
			Defaults: celtfmt.FormatParams{Enabled: true},
		},
		indent: indent,
	}
}
