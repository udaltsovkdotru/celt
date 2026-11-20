// Package celtplugin holds the shared, type-safe building blocks that the fmt,
// lint and LSP plug-in surfaces (celtfmt.FormatRule, celtlint.Rule,
// celtls.Action) have in common: static metadata and a configurable hook.
//
// Each engine aliases its own concrete Meta type onto the generic Meta[P], so
// the framework's public interfaces keep their exact return types (e.g.
// FormatMeta.Meta() returns FormatMeta, RuleMeta.Meta() returns RuleMeta)
// while sharing a single implementation.
package celtplugin

// Meta is the static metadata shared by every plug-in across the fmt, lint and
// LSP surfaces: a name, a description and a default configuration. P is the
// plug-in's defaults type (celtfmt.FormatParams, celtlint.RuleParams, ...).
// Plug-in types embed Meta[P] (as a value) to satisfy their engine's Meta()
// interface.
type Meta[P any] struct {
	Name     string
	Desc     string
	Defaults P
}

// Meta returns the metadata, satisfying an engine's Meta() contract for an
// embedded Meta[P].
func (m Meta[P]) Meta() Meta[P] { return m }

// Configurable is implemented by a plug-in that accepts per-rule
// configuration. Engines apply the plug-in's effective configuration (defaults
// merged with user overrides) via ApplyConfig before the plug-in runs. It is
// shared verbatim by the fmt and lint surfaces; LSP actions currently have no
// config.
type Configurable interface {
	ApplyConfig(data []byte) error
}
