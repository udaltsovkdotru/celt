package celtfmt

import (
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtplugin"
)

// FormatParams is the default configuration of a format rule.
type FormatParams struct {
	// Enabled reports whether the rule is active when not explicitly
	// configured. Some rules (e.g. break operators) are off by default.
	Enabled bool
	// Config is the rule's default configuration as raw bytes, applied before
	// any user-supplied config. May be nil when the rule has no config.
	Config []byte
}

// FormatMeta is the static metadata of a format rule: its name, description
// and default configuration. Rules embed it (as a value) to satisfy
// FormatRule. Meta() returns FormatMeta via the shared celtplugin.Meta.
type FormatMeta = celtplugin.Meta[FormatParams]

// FormatRule is a named formatting rule. A rule may be a source rewrite, a
// printer parameter (indent/width), or a printer behavior (break operators).
// Rules are applied in order during formatting.
type FormatRule interface {
	Meta() FormatMeta
	Rewrite(ast *celast.AST) (*celast.AST, error)
}

// Configurable is implemented by a FormatRule that accepts per-rule
// configuration. The framework passes the rule's default config (from
// Defaults), then its raw config bytes (from under fmt[<name>]), to ApplyConfig
// before the rule runs.
type Configurable interface {
	FormatRule
	celtplugin.Configurable
}

// BreakRule is implemented by a FormatRule that forces line breaks on certain
// expressions during printing.
type BreakRule interface {
	FormatRule
	MustBreak(e celast.Expr) bool
}

// IndentProvider is implemented by a FormatRule that supplies the indentation
// string. When multiple are present the last one wins.
type IndentProvider interface {
	FormatRule
	Indent() string
}

// WidthProvider is implemented by a FormatRule that supplies the maximum line
// width. When multiple are present the last one wins.
type WidthProvider interface {
	FormatRule
	Width() int
}
