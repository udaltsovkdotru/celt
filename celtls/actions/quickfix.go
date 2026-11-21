package actions

import (
	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtls"
)

type quickFixAction struct {
	celtls.ActionMeta
}

func newQuickFixAction() celtls.Action {
	return quickFixAction{celtls.ActionMeta{Name: "quick-fix", Desc: "apply finding fixes"}}
}

func (quickFixAction) Apply(ctx celtls.ActionContext) []protocol.CodeAction {
	var out []protocol.CodeAction
	for i := range ctx.Findings {
		f := &ctx.Findings[i]
		if !f.IsFix() {
			continue
		}
		fRange := celtls.FindingRange(ctx.Content, f)
		// Availability is decided by the diagnostic range the user sees (the
		// cursor inside the reported finding qualifies), while the edit uses
		// the narrower replacement range.
		if !celtls.RangesOverlap(ctx.Range, celtls.DiagnosticRange(ctx.Content, f)) {
			continue
		}
		out = append(out, protocol.CodeAction{
			Title: celtlint.UseInstead(f.HintCallee()),
			Kind:  protocol.QuickFix,
			Edit: &protocol.WorkspaceEdit{
				Changes: map[protocol.DocumentURI][]protocol.TextEdit{
					ctx.URI: {{Range: fRange, NewText: f.FixText()}},
				},
			},
		})
	}
	return out
}
