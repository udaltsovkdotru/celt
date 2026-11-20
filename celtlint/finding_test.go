package celtlint

import (
	"strings"
	"testing"

	"cel.dev/cel-go/common"
)

// Shared string literals reused across the celtlint tests so golangci-lint's
// goconst check stays satisfied within these files.
const (
	fdSrcOddComp = "x == 1"
	fdFooDotBar  = "foo.bar"
	fdFoo        = "foo"
	fdFooCall    = "foo(bar)"
	fdFooBar     = "foo bar"
	fdAbc        = "abc"
)

// fdFinding builds a Finding with the supplied level, code, message and span,
// leaving the optional fields (Hint/Fix/Detail/Spans) at their zero values.
func fdFinding(level Level, code, msg string, start, end Position) Finding {
	return Finding{Level: level, Code: code, Message: msg, Start: start, End: end}
}

func TestFDFixRangeDefault(t *testing.T) {
	f := Finding{Start: Position{Line: 0, Char: 3}, End: Position{Line: 0, Char: 7}}
	start, end := f.FixRange()
	if start != f.Start {
		t.Errorf("FixRange start = %+v, want %+v", start, f.Start)
	}
	if end != f.End {
		t.Errorf("FixRange end = %+v, want %+v", end, f.End)
	}
}

func TestFDFixRangeExplicit(t *testing.T) {
	f := Finding{
		Start:    Position{Line: 0, Char: 3},
		End:      Position{Line: 0, Char: 7},
		FixStart: Position{Line: 1, Char: 2},
		FixEnd:   Position{Line: 1, Char: 5},
	}
	start, end := f.FixRange()
	if start != f.FixStart {
		t.Errorf("FixRange start = %+v, want %+v", start, f.FixStart)
	}
	if end != f.FixEnd {
		t.Errorf("FixRange end = %+v, want %+v", end, f.FixEnd)
	}
}

func TestFDFixRangePartial(t *testing.T) {
	// A non-zero FixEnd alone is enough to opt into the fix range, per the
	// contract (either of the fix positions being set selects the fix span).
	f := Finding{Start: Position{Line: 0, Char: 1}, End: Position{Line: 0, Char: 2},
		FixEnd: Position{Line: 0, Char: 9}}
	start, end := f.FixRange()
	if start != (Position{}) {
		t.Errorf("FixRange start = %+v, want zero Position", start)
	}
	if end != f.FixEnd {
		t.Errorf("FixRange end = %+v, want %+v", end, f.FixEnd)
	}
}

func TestFDFixRangeZero(t *testing.T) {
	var f Finding
	start, end := f.FixRange()
	if start != (Position{}) || end != (Position{}) {
		t.Errorf("FixRange = (%+v, %+v), want zero positions", start, end)
	}
}

func TestFDIsError(t *testing.T) {
	cases := []struct {
		name  string
		level Level
		want  bool
	}{
		{"error", LevelError, true},
		{"warn", LevelWarn, false},
		{"off", LevelOff, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Finding{Level: tc.level}
			if got := f.IsError(); got != tc.want {
				t.Errorf("IsError = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFDHintMessage(t *testing.T) {
	cases := []struct {
		name string
		f    Finding
		want string
	}{
		{
			name: "no fix returns hint verbatim",
			f:    Finding{Hint: "add a has() check"},
			want: "add a has() check",
		},
		{
			name: "single line fix uses use-instead",
			f:    Finding{Fix: fdFooCall},
			want: "use `foo(bar)` instead",
		},
		{
			name: "multi line fix uses callee hint",
			f:    Finding{Fix: "has(m.a)\n&& b"},
			want: "use `has` instead",
		},
		{
			name: "empty everything",
			f:    Finding{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.HintMessage(); got != tc.want {
				t.Errorf("HintMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFDUseInstead(t *testing.T) {
	if got := UseInstead("has"); got != "use `has` instead" {
		t.Errorf("UseInstead = %q, want %q", got, "use `has` instead")
	}
}

func TestFDHintCallee(t *testing.T) {
	cases := []struct {
		fix  string
		want string
	}{
		{"foo(bar, baz)", fdFoo},
		{fdFooDotBar + "(baz)", fdFooDotBar},
		{"has(m.a)", "has"},
		{"standalone", "standalone"},
		{"", ""},
	}
	for _, tc := range cases {
		f := Finding{Fix: tc.fix}
		if got := f.HintCallee(); got != tc.want {
			t.Errorf("HintCallee(%q) = %q, want %q", tc.fix, got, tc.want)
		}
	}
}

func TestFDIsFix(t *testing.T) {
	cases := []struct {
		fix  string
		want bool
	}{
		{"", false},
		{"x", true},
		{" ", true},
	}
	for _, tc := range cases {
		f := Finding{Fix: tc.fix}
		if got := f.IsFix(); got != tc.want {
			t.Errorf("IsFix(%q) = %v, want %v", tc.fix, got, tc.want)
		}
	}
}

func TestFDFixText(t *testing.T) {
	cases := []struct {
		name string
		f    Finding
		want string
	}{
		{
			name: "single line untouched",
			f:    Finding{Fix: "foo(bar)", Start: Position{Char: 2}},
			want: "foo(bar)",
		},
		{
			name: "multiline with zero col untouched",
			f:    Finding{Fix: "a\nb", Start: Position{Char: 0}},
			want: "a\nb",
		},
		{
			name: "multiline with fix position indents continuation",
			f:    Finding{Fix: "a\nb\nc", FixStart: Position{Char: 2}},
			want: "a\n  b\n  c",
		},
		{
			name: "multiline uses FixRange fix positions",
			f:    Finding{Fix: "x\ny", FixStart: Position{Char: 3}, FixEnd: Position{Char: 4}},
			want: "x\n   y",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.FixText(); got != tc.want {
				t.Errorf("FixText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFDRenderFindingsEmpty(t *testing.T) {
	src := common.NewStringSource("", "empty.cel")
	if got := RenderFindings(src, nil); got != "" {
		t.Errorf("RenderFindings(nil) = %q, want empty", got)
	}
	if got := RenderFindings(src, []Finding{}); got != "" {
		t.Errorf("RenderFindings([]) = %q, want empty", got)
	}
}

func TestFDRenderFindingsSingle(t *testing.T) {
	src := common.NewStringSource(fdSrcOddComp, "single.cel")
	f := fdFinding(LevelError, CodeOddComparison, "odd comparison", Position{Line: 0, Char: 2}, Position{Line: 0, Char: 4})
	want := RenderFinding(src, &f)
	got := RenderFindings(src, []Finding{f})
	if got != want {
		t.Errorf("RenderFindings = %q, want %q", got, want)
	}
}

func TestFDRenderFindingsMultiple(t *testing.T) {
	src := common.NewStringSource("x == 1\nfoo", "multi.cel")
	f1 := fdFinding(LevelError, CodeOddComparison, "odd comparison", Position{Line: 0, Char: 2}, Position{Line: 0, Char: 4})
	f2 := fdFinding(LevelWarn, CodeManualMacro, "manual macro", Position{Line: 1, Char: 0}, Position{Line: 1, Char: 3})
	want := RenderFinding(src, &f1) + "\n\n" + RenderFinding(src, &f2)
	if got := RenderFindings(src, []Finding{f1, f2}); got != want {
		t.Errorf("RenderFindings = %q, want %q", got, want)
	}
}

func TestFDRenderFindingErrorContainsMessage(t *testing.T) {
	src := common.NewStringSource(fdSrcOddComp, "err.cel")
	f := fdFinding(LevelError, CodeOddComparison, "odd comparison", Position{Line: 0, Char: 2}, Position{Line: 0, Char: 4})
	out := RenderFinding(src, &f)
	if !strings.Contains(out, "odd comparison") {
		t.Errorf("output does not contain message: %q", out)
	}
	if !strings.Contains(out, CodeOddComparison) {
		t.Errorf("output does not contain code %q: %q", CodeOddComparison, out)
	}
	if !strings.Contains(out, fdSrcOddComp) {
		t.Errorf("output does not contain source line: %q", out)
	}
}

func TestFDRenderFindingWarning(t *testing.T) {
	src := common.NewStringSource(fdFoo, "warn.cel")
	f := fdFinding(LevelWarn, CodeManualMacro, "manual macro", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 3})
	out := RenderFinding(src, &f)
	if !strings.Contains(out, "manual macro") {
		t.Errorf("warning output does not contain message: %q", out)
	}
}

func TestFDRenderFindingNoPosition(t *testing.T) {
	src := common.NewStringSource("x", "nopos.cel")
	f := fdFinding(LevelError, CodeCompileError, "boom", Position{}, Position{})
	out := RenderFinding(src, &f)
	if !strings.Contains(out, "boom") {
		t.Errorf("no-position output does not contain message: %q", out)
	}
}

func TestFDRenderFindingDetailNote(t *testing.T) {
	src := common.NewStringSource("x", "detail.cel")
	f := fdFinding(LevelError, CodeCompileError, "boom", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 1})
	f.Detail = "extra detail"
	out := RenderFinding(src, &f)
	if !strings.Contains(out, "extra detail") {
		t.Errorf("output does not contain detail note: %q", out)
	}
}

func TestFDRenderFindingHintNote(t *testing.T) {
	src := common.NewStringSource("x", "hint.cel")
	f := fdFinding(LevelError, CodeCompileError, "boom", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 1})
	f.Hint = "try has(x)"
	out := RenderFinding(src, &f)
	if !strings.Contains(out, "hint") {
		t.Errorf("output does not contain hint label: %q", out)
	}
	if !strings.Contains(out, "try has(x)") {
		t.Errorf("output does not contain hint text: %q", out)
	}
}
