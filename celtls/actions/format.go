package actions

import (
	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtls"
)

type formatAction struct {
	celtls.ActionMeta
	cfg celtfmt.Config
}

func newFormatAction(cfg celtfmt.Config) celtls.Action {
	return formatAction{ActionMeta: celtls.ActionMeta{Name: "format", Desc: "format the document"}, cfg: cfg}
}

func (a formatAction) Apply(ctx celtls.ActionContext) []protocol.CodeAction {
	edit, err := celtls.DocFormat(ctx.Content, protocol.FormattingOptions{}, a.cfg)
	if err != nil || edit == nil {
		return nil
	}
	return []protocol.CodeAction{{
		Title: "Format document",
		Kind:  "source.format",
		Edit: &protocol.WorkspaceEdit{
			Changes: map[protocol.DocumentURI][]protocol.TextEdit{
				ctx.URI: {*edit},
			},
		},
	}}
}
