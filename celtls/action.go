package celtls

import (
	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtplugin"
)

// ActionParams is the defaults type for LSP actions. Actions currently have no
// per-action configuration, so the field is unused; it exists so ActionMeta
// shares the same celtplugin.Meta shape as the fmt and lint rules.
type ActionParams struct{}

// ActionMeta is the static metadata of an LSP code action rule. Meta() returns
// ActionMeta via the shared celtplugin.Meta.
type ActionMeta = celtplugin.Meta[ActionParams]

// ActionContext carries the inputs an action rule uses to produce code actions
// for a document.
type ActionContext struct {
	URI      protocol.DocumentURI
	Content  string
	Range    protocol.Range
	Findings []celtlint.Finding
}

// Action is a pluggable contributor of LSP code actions, mirroring
// celtlint.Rule and celtfmt.FormatRule. Rules are applied in order and their
// results concatenated.
type Action interface {
	Meta() ActionMeta
	Apply(ctx ActionContext) []protocol.CodeAction
}

// FindingRange returns the fix range of a finding in the given content.
func FindingRange(content string, f *celtlint.Finding) protocol.Range {
	start, end := f.FixRange()
	return protocol.Range{Start: toPosition(content, start), End: toPosition(content, end)}
}

// DiagnosticRange returns the reported span of a finding in the given content.
// This is the range presented to the user as the diagnostic, which may be wider
// than the narrower range a fix replaces.
func DiagnosticRange(content string, f *celtlint.Finding) protocol.Range {
	return protocol.Range{Start: toPosition(content, f.Start), End: toPosition(content, f.End)}
}

// RangesOverlap reports whether two LSP ranges overlap.
func RangesOverlap(a, b protocol.Range) bool {
	return !posLess(a.End, b.Start) && !posLess(b.End, a.Start)
}

func posLess(a, b protocol.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}
