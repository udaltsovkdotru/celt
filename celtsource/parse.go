package celtsource

import (
	"fmt"

	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"
)

func Parse(src common.Source) (*celast.AST, error) {
	p, err := parser.NewParser(parser.EnableOptionalSyntax(true))
	if err != nil {
		return nil, err
	}
	parsed, errs := p.Parse(src)
	if errs != nil && len(errs.GetErrors()) > 0 {
		return nil, fmt.Errorf("parse error: %s", errs.ToDisplayString())
	}
	return parsed, nil
}

func Walk(e celast.Expr, visit func(celast.Expr)) {
	if e == nil {
		return
	}
	celast.PreOrderVisit(e, celast.NewExprVisitor(visit))
}
