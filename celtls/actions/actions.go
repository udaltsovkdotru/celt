// Package actions holds the built-in LSP code actions, one per file.
package actions

import (
	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtls"
)

// All returns the built-in LSP code actions. The format action is included
// when cfg is non-nil.
func All(cfg celtfmt.Config) []celtls.Action {
	acts := []celtls.Action{newQuickFixAction()}
	if cfg != nil {
		acts = append(acts, newFormatAction(cfg))
	}
	return acts
}
