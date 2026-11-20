package celtlint

import (
	"strings"
	"testing"

	"cel.dev/cel-go/common"
)

func TestFDSummaryZero(t *testing.T) {
	if got := Summary(Stats{}, ""); got != "    Finished checking 0 files" {
		t.Errorf("Summary(zero) = %q", got)
	}
}

func TestFDSummarySuccess(t *testing.T) {
	if got := Summary(Stats{Checked: 3}, "celt"); got != "    Finished checking 3 files" {
		t.Errorf("Summary(3 files) = %q", got)
	}
	if got := Summary(Stats{Checked: 1}, "celt"); got != "    Finished checking 1 file" {
		t.Errorf("Summary(1 file) = %q", got)
	}
}

func TestFDSummaryWarnings(t *testing.T) {
	cases := []struct {
		name string
		s    Stats
		bin  string
		want string
	}{
		{
			name: "single warning",
			s:    Stats{Warnings: 1},
			want: "warning: celt generated 1 warning\n    Finished checking 0 files",
		},
		{
			name: "plural warnings",
			s:    Stats{Warnings: 2},
			want: "warning: celt generated 2 warnings\n    Finished checking 0 files",
		},
		{
			name: "warnings with single fixable",
			s:    Stats{Warnings: 2, Fixable: 1},
			want: "warning: celt generated 2 warnings (run `celt --fix` to apply 1 suggestion)\n    Finished checking 0 files",
		},
		{
			name: "warnings with plural fixable",
			s:    Stats{Warnings: 1, Fixable: 2},
			want: "warning: celt generated 1 warning (run `celt --fix` to apply 2 suggestions)\n    Finished checking 0 files",
		},
		{
			name: "custom bin name",
			s:    Stats{Warnings: 1},
			bin:  "mylint",
			want: "warning: mylint generated 1 warning\n    Finished checking 0 files",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := tc.bin
			if bin == "" {
				bin = "celt"
			}
			if got := Summary(tc.s, bin); got != tc.want {
				t.Errorf("Summary(%+v, %q) = %q, want %q", tc.s, bin, got, tc.want)
			}
		})
	}
}

func TestFDSummaryErrors(t *testing.T) {
	cases := []struct {
		name string
		s    Stats
		want string
	}{
		{
			name: "single error no warnings",
			s:    Stats{Errors: 1},
			want: "error: check failed due to 1 previous error",
		},
		{
			name: "plural errors no warnings",
			s:    Stats{Errors: 2},
			want: "error: check failed due to 2 previous errors",
		},
		{
			name: "errors with warnings",
			s:    Stats{Errors: 1, Warnings: 3},
			want: "warning: celt generated 3 warnings\nerror: check failed due to 1 previous error; 3 warnings emitted",
		},
		{
			name: "errors with single warning",
			s:    Stats{Errors: 1, Warnings: 1},
			want: "warning: celt generated 1 warning\nerror: check failed due to 1 previous error; 1 warning emitted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Summary(tc.s, "celt"); got != tc.want {
				t.Errorf("Summary(%+v) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

func TestFDSummaryEmptyBinDefaultsToCelt(t *testing.T) {
	// binName=="" must fall back to "celt"; a warning-only run still ends with
	// the Finished line because Summary falls through to the success branch.
	if got := Summary(Stats{Warnings: 1}, ""); got != "warning: celt generated 1 warning\n    Finished checking 0 files" {
		t.Errorf("Summary(empty bin) = %q", got)
	}
}

func fdRenderOne(t *testing.T, src common.Source, f *Finding) string {
	t.Helper()
	return RenderFinding(src, f)
}

func TestFDRenderAllEmpty(t *testing.T) {
	if got := RenderAll(nil); got != "" {
		t.Errorf("RenderAll(nil) = %q, want empty", got)
	}
	if got := RenderAll([]LintedSource{}); got != "" {
		t.Errorf("RenderAll([]) = %q, want empty", got)
	}
}

func TestFDRenderAllSkipsEmptySources(t *testing.T) {
	srcs := []LintedSource{
		{Name: "empty.cel", Content: "", Findings: []Finding{}},
		{Name: "clean.cel", Content: "x", Findings: []Finding{}},
	}
	if got := RenderAll(srcs); got != "" {
		t.Errorf("RenderAll(empty findings) = %q, want empty", got)
	}
}

func TestFDRenderAllSingleSource(t *testing.T) {
	content := fdSrcOddComp
	f := fdFinding(LevelError, CodeOddComparison, "odd comparison", Position{Line: 0, Char: 2}, Position{Line: 0, Char: 4})
	ls := []LintedSource{{Name: "a.cel", Content: content, Findings: []Finding{f}}}
	want := fdRenderOne(t, common.NewStringSource(content, "a.cel"), &f)
	if got := RenderAll(ls); got != want {
		t.Errorf("RenderAll = %q, want %q", got, want)
	}
}

func TestFDRenderAllMultipleSources(t *testing.T) {
	c1 := fdSrcOddComp
	c2 := fdFoo
	f1 := fdFinding(LevelError, CodeOddComparison, "odd comparison", Position{Line: 0, Char: 2}, Position{Line: 0, Char: 4})
	f2 := fdFinding(LevelWarn, CodeManualMacro, "manual macro", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 3})
	ls := []LintedSource{
		{Name: "a.cel", Content: c1, Findings: []Finding{f1}},
		{Name: "b.cel", Content: c2, Findings: []Finding{f2}},
	}
	want := fdRenderOne(t, common.NewStringSource(c1, "a.cel"), &f1) + "\n\n" +
		fdRenderOne(t, common.NewStringSource(c2, "b.cel"), &f2)
	if got := RenderAll(ls); got != want {
		t.Errorf("RenderAll = %q, want %q", got, want)
	}
}

func TestFDRenderAllSkipsSourceNameInOutput(t *testing.T) {
	// The source name is used as the rendered description/snippet origin.
	content := "x"
	f := fdFinding(LevelError, CodeCompileError, "boom", Position{Line: 0, Char: 0}, Position{Line: 0, Char: 1})
	ls := []LintedSource{{Name: "unique-name.cel", Content: content, Findings: []Finding{f}}}
	out := RenderAll(ls)
	if !strings.Contains(out, "unique-name.cel") {
		t.Errorf("RenderAll output does not reference source name: %q", out)
	}
}
