package celtlint

import (
	"testing"
)

const (
	opSrcAbc   = "abc"
	opSrcHello = "hello"
)

// opFixFinding builds a fixable finding replacing [start, end) with fix. When
// start/end are zero-valued (the zero Position), FixRange falls back to the
// finding's Start/End; tests that rely on the fallback set Start/End instead.
func opFixFinding(fix string, start, end Position) Finding {
	return Finding{Fix: fix, FixStart: start, FixEnd: end}
}

// TestApplyFixesSingle asserts a single fixable finding replaces its range and
// reports that a fix was applied.
func TestApplyFixesSingle(t *testing.T) {
	src := "foo.bar"
	got, kept, changed := ApplyFixes(src, []Finding{opFixFinding("baz", Position{Line: 0, Char: 4}, Position{Line: 0, Char: 7})})
	if got != "foo.baz" {
		t.Errorf("got %q, want %q", got, "foo.baz")
	}
	if len(kept) != 0 {
		t.Errorf("kept = %+v, want none", kept)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}

// TestApplyFixesMultiple asserts non-overlapping fixes are all applied,
// regardless of their order in the input slice.
func TestApplyFixesMultiple(t *testing.T) {
	src := "hello world"
	fs := []Finding{
		opFixFinding("hi", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 5}),
		opFixFinding("earth", Position{Line: 0, Char: 6}, Position{Line: 0, Char: 11}),
	}
	got, kept, changed := ApplyFixes(src, fs)
	if got != "hi earth" {
		t.Errorf("got %q, want %q", got, "hi earth")
	}
	if len(kept) != 0 || !changed {
		t.Errorf("kept = %+v, changed = %v; want none and true", kept, changed)
	}
}

// TestApplyFixesNonFixKept asserts a non-fixable finding is kept and no edit
// is performed.
func TestApplyFixesNonFixKept(t *testing.T) {
	src := opSrcAbc
	f := Finding{Code: CodeMissingHasCheck, Message: "no fix", Start: Position{Line: 0, Char: 1}, End: Position{Line: 0, Char: 2}}
	got, kept, changed := ApplyFixes(src, []Finding{f})
	if got != opSrcAbc {
		t.Errorf("got %q, want %q", got, opSrcAbc)
	}
	if len(kept) != 1 || kept[0].Message != "no fix" {
		t.Errorf("kept = %+v, want the non-fix finding", kept)
	}
	if changed {
		t.Error("changed = true, want false")
	}
}

// TestApplyFixesStartEndFallback asserts that when FixStart/FixEnd are unset,
// the finding's Start/End span is used.
func TestApplyFixesStartEndFallback(t *testing.T) {
	src := opSrcHello
	f := Finding{Fix: "bye", Start: Position{Line: 0, Char: 0}, End: Position{Line: 0, Char: 5}}
	got, _, changed := ApplyFixes(src, []Finding{f})
	if got != "bye" {
		t.Errorf("got %q, want %q", got, "bye")
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}

// TestApplyFixesInsertion asserts a zero-width fix range inserts text.
func TestApplyFixesInsertion(t *testing.T) {
	src := opSrcAbc
	got, _, changed := ApplyFixes(src, []Finding{opFixFinding("-", Position{Line: 0, Char: 1}, Position{Line: 0, Char: 1})})
	if got != "a-bc" {
		t.Errorf("got %q, want %q", got, "a-bc")
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}

// TestApplyFixesInvalidRangeKept asserts a fix whose end precedes its start is
// treated as unfixable and kept.
func TestApplyFixesInvalidRangeKept(t *testing.T) {
	src := opSrcAbc
	f := opFixFinding("x", Position{Line: 0, Char: 5}, Position{Line: 0, Char: 2})
	got, kept, changed := ApplyFixes(src, []Finding{f})
	if got != opSrcAbc {
		t.Errorf("got %q, want %q", got, opSrcAbc)
	}
	if len(kept) != 1 {
		t.Errorf("kept = %+v, want the invalid fix kept", kept)
	}
	if changed {
		t.Error("changed = true, want false")
	}
}

func TestApplyFixesDoesNotCrossLinesFromInvalidColumn(t *testing.T) {
	const src = "ab\ncd"
	f := opFixFinding("X", Position{Line: 0, Char: 4}, Position{Line: 0, Char: 5})
	got, kept, changed := ApplyFixes(src, []Finding{f})
	rejected := got == src && !changed && len(kept) == 1
	clampedToLine := got == "abX\ncd" && changed && len(kept) == 0
	if !rejected && !clampedToLine {
		t.Errorf("out-of-line fix must not edit the next line: source=%q changed=%v kept=%+v", got, changed, kept)
	}
}

// TestApplyFixesClampAtEnd asserts a fix range positioned at the end of the
// source appends text (offsets are clamped to the source length).
func TestApplyFixesClampAtEnd(t *testing.T) {
	src := opSrcAbc
	got, _, changed := ApplyFixes(src, []Finding{opFixFinding("!", Position{Line: 0, Char: 3}, Position{Line: 0, Char: 3})})
	if got != "abc!" {
		t.Errorf("got %q, want %q", got, "abc!")
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}

// TestApplyFixesMixed asserts a fixable and a non-fixable finding are handled
// together: the fix applies and the non-fixable finding is kept.
func TestApplyFixesMixed(t *testing.T) {
	src := "ab"
	fixable := opFixFinding("X", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 1})
	nonFix := Finding{Code: CodeMissingHasCheck, Message: "keep me", Start: Position{Line: 0, Char: 1}, End: Position{Line: 0, Char: 2}}
	got, kept, changed := ApplyFixes(src, []Finding{nonFix, fixable})
	if got != "Xb" {
		t.Errorf("got %q, want %q", got, "Xb")
	}
	if len(kept) != 1 || kept[0].Message != "keep me" {
		t.Errorf("kept = %+v, want the non-fix finding", kept)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}

// TestApplyFixesNone asserts no findings yields the source unchanged and no
// change flag.
func TestApplyFixesNone(t *testing.T) {
	src := opSrcHello
	got, kept, changed := ApplyFixes(src, nil)
	if got != opSrcHello {
		t.Errorf("got %q, want %q", got, opSrcHello)
	}
	if len(kept) != 0 || changed {
		t.Errorf("kept = %+v, changed = %v; want none and false", kept, changed)
	}
}

// TestApplyFixesOverlap asserts overlapping fixes are resolved without
// corrupting offsets: the higher-offset edit is applied and the overlapping
// lower one is kept unfixed, so no edit is applied against shifted source.
func TestApplyFixesOverlap(t *testing.T) {
	src := "abcdef"
	outer := opFixFinding("X", Position{Line: 0, Char: 1}, Position{Line: 0, Char: 5})
	inner := opFixFinding("Y", Position{Line: 0, Char: 2}, Position{Line: 0, Char: 3})
	got, kept, changed := ApplyFixes(src, []Finding{outer, inner})
	// inner (off 2) is higher and applied first; outer (off 1) overlaps it and
	// is kept unfixed rather than being applied against the shifted source.
	if got != "abYdef" {
		t.Errorf("got %q, want %q", got, "abYdef")
	}
	if len(kept) != 1 || kept[0].Fix != "X" {
		t.Errorf("kept = %+v, want the overlapping outer fix", kept)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}

// TestApplyFixesMultilinePadding asserts a multi-line fix is indented to match
// the column of the range it replaces (fixHint padding applied via FixText).
func TestApplyFixesMultilinePadding(t *testing.T) {
	src := "xx\n  foo()"
	got, _, changed := ApplyFixes(src, []Finding{opFixFinding("bar()\nbaz()", Position{Line: 1, Char: 2}, Position{Line: 1, Char: 8})})
	want := "xx\n  bar()\n  baz()"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
}
