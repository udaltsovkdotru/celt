package celtlint

import (
	celast "cel.dev/cel-go/common/ast"

	"github.com/udaltsovkdotru/celt/celtsource"
)

func ExprOffsets(sInfo *celast.SourceInfo, ro []int, content string, e celast.Expr) (startOff, endOff int) {
	if r, ok := sInfo.GetOffsetRange(e.ID()); ok {
		return celtsource.ByteOffset(ro, int(r.Start)), celtsource.ByteOffset(ro, int(r.Stop))
	}
	l, c := sInfo.GetStartLocation(e.ID()).Line(), sInfo.GetStartLocation(e.ID()).Column()
	off := celtsource.PosToOffset(content, Position{Line: l - 1, Char: celtsource.ByteOffset(ro, c)})
	return off, off + 1
}

func ExprByteSpan(sInfo *celast.SourceInfo, ro []int, e celast.Expr) (startOff, endOff int) {
	startOff = -1
	celtsource.Walk(e, func(n celast.Expr) {
		r, ok := sInfo.GetOffsetRange(n.ID())
		if !ok {
			return
		}
		start := celtsource.ByteOffset(ro, int(r.Start))
		stop := celtsource.ByteOffset(ro, int(r.Stop))

		if n.Kind() == celast.SelectKind {
			if fname := n.AsSelect().FieldName(); fname != "" {
				stop += len(fname)
			}
		}
		if startOff < 0 || start < startOff {
			startOff = start
		}
		if stop > endOff {
			endOff = stop
		}
	})
	if startOff < 0 {
		return 0, 0
	}
	return startOff, endOff
}
