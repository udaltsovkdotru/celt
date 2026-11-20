package celtlint

import (
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtsource"
)

func ParseUnexpanded(src common.Source) *celast.AST {
	parsed, _ := celtsource.Parse(src)
	return parsed
}
