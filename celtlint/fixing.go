package celtlint

import (
	"sort"

	"github.com/udaltsovkdotru/celt/celtsource"
)

// clampWithinLine clamps an offset derived from a Position so it stays within
// that Position's stated line: a column past the end of its line is clamped to
// the line's end rather than being reinterpreted as pointing into a later line.
func clampWithinLine(src string, p celtsource.Position, off int) int {
	lineStart, lineEnd := celtsource.LineByteRange(src, p.Line)
	if off < lineStart {
		return lineStart
	}
	if off > lineEnd {
		return lineEnd
	}
	return off
}

// ApplyFixes applies the fixable findings to src, returning the new source,
// the findings that remain unfixed, and whether any fix was applied. Findings
// whose fix ranges overlap an already-applied fix are left unfixed (applying
// them against shifted offsets would corrupt the source).
func ApplyFixes(src string, findings []Finding) (string, []Finding, bool) {
	type edit struct {
		off  int
		end  int
		repl string
		fi   int
	}
	var edits []edit
	kept := make([]Finding, 0, len(findings))
	for i := range findings {
		f := &findings[i]
		if !f.IsFix() {
			kept = append(kept, *f)
			continue
		}
		s, e := f.FixRange()
		start := clampOffset(celtsource.PosToOffset(src, s), src)
		end := clampOffset(celtsource.PosToOffset(src, e), src)
		// A column past the end of its stated line must not be reinterpreted as
		// pointing into a later line; clamp the edit within its own line.
		start = clampWithinLine(src, s, start)
		end = clampWithinLine(src, e, end)
		if end < start {
			kept = append(kept, *f)
			continue
		}
		edits = append(edits, edit{off: start, end: end, repl: f.FixText(), fi: i})
	}
	if len(edits) == 0 {
		return src, kept, false
	}
	// Apply from the highest offset downward so earlier offsets stay valid.
	// A stable sort keeps equal-offset edits in their original order.
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].off > edits[j].off })
	out := src
	applied := 0
	floor := len(src) + 1 // lowest offset applied so far
	for _, e := range edits {
		if e.end > floor {
			kept = append(kept, findings[e.fi])
			continue
		}
		out = out[:e.off] + e.repl + out[e.end:]
		floor = e.off
		applied++
	}
	return out, kept, applied > 0
}
