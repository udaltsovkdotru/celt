// Package celtlint_test exercises the celtlint package end-to-end through its
// public API only, driving realistic whole-pipeline flows: building a cel.Env
// (variable + function + signature-only macro), analyzing clean and broken
// documents, applying fixes until convergence, and rendering reports.
package celtlint_test

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"

	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtlint/rules"
)

// e2eCleanDoc is a valid policy referencing the variable, function and macro
// declared by e2eEnv; it must produce no findings.
const e2eCleanDoc = `is_ok(requests[0]) && matches_any(requests)`

// e2eTypoDoc misspells the declared variable `requests` as `request`; it should
// be caught by the misspell rule and be auto-fixable to `requests`.
const e2eTypoDoc = `is_ok(request[0])`

// e2eTypeErrDoc calls the declared function with a String argument where an
// Int is expected; it should be caught by the compile rule as a type mismatch.
const e2eTypeErrDoc = `is_ok("nope")`

// e2eEnv builds a cel.Env exposing a typed variable, a user function, and a
// signature-only macro (matches_any), mirroring how a real consumer wires up
// its lint environment. The macro keeps its function overloads registered so
// that valid call sites type-check.
func e2eEnv(t *testing.T) *cel.Env {
	t.Helper()
	noop := func(eh parser.ExprHelper, target celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		if target != nil {
			return eh.NewMemberCall("matches_any", target, args...), nil
		}
		return eh.NewCall("matches_any", args...), nil
	}
	env, err := cel.NewEnv(
		cel.Variable("requests", cel.ListType(cel.IntType)),
		cel.Function("is_ok",
			cel.Overload("is_ok_int", []*cel.Type{cel.IntType}, cel.BoolType),
		),
		cel.EnableMacroCallTracking(),
		cel.Macros(cel.GlobalVarArgMacro("matches_any", noop)),
		cel.Function("matches_any",
			cel.Overload("matches_any_1", []*cel.Type{cel.DynType}, cel.BoolType),
			cel.Overload("matches_any_2", []*cel.Type{cel.DynType, cel.DynType}, cel.BoolType),
			cel.Overload("matches_any_3", []*cel.Type{cel.DynType, cel.DynType, cel.DynType}, cel.BoolType),
		),
	)
	if err != nil {
		t.Fatalf("e2eEnv NewEnv: %v", err)
	}
	return env
}

// e2eAnalyzer builds a fully-wired analyzer over e2eEnv with every built-in
// rule registered.
func e2eAnalyzer(t *testing.T) *celtlint.Analyzer {
	t.Helper()
	a, err := celtlint.New(e2eEnv(t), celtlint.WithRules(rules.All()...))
	if err != nil {
		t.Fatalf("e2eAnalyzer New: %v", err)
	}
	return a
}

// TestE2ECleanDocumentEndToEnd asserts that a document correctly referencing
// the declared variable, function and macro lints clean.
func TestE2ECleanDocumentEndToEnd(t *testing.T) {
	a := e2eAnalyzer(t)
	fs := a.Analyze("policy.cel", e2eCleanDoc)
	if len(fs) != 0 {
		t.Fatalf("Analyze(clean doc) = %d findings, want 0: %+v", len(fs), fs)
	}
}

// TestE2EMisspelledIdentifier asserts that a misspelled identifier is flagged
// and that the misspell finding carries an auto-fix correcting it.
func TestE2EMisspelledIdentifier(t *testing.T) {
	a := e2eAnalyzer(t)
	fs := a.Analyze("typo.cel", e2eTypoDoc)
	if len(fs) == 0 {
		t.Fatal("Analyze(typo doc) = 0 findings, want at least one")
	}
	var fix *celtlint.Finding
	for i := range fs {
		if fs[i].Code == celtlint.CodeUnknownIdentifier && fs[i].IsFix() {
			fix = &fs[i]
			break
		}
	}
	if fix == nil {
		t.Fatalf("no fixable unknown-identifier finding in %+v", fs)
	}
	if fix.Fix != "requests" {
		t.Errorf("Fix = %q, want %q", fix.Fix, "requests")
	}
	if !fix.IsError() {
		t.Errorf("misspell finding Level = %v, want LevelError", fix.Level)
	}
}

// TestE2ETypeError asserts that a document with a type/argument error is
// flagged by the compile rule. cel-go reports an int-typed single overload
// given a string argument as "no matching overload"; we accept either compile
// category that signals the wrong argument type.
func TestE2ETypeError(t *testing.T) {
	a := e2eAnalyzer(t)
	fs := a.Analyze("typeerr.cel", e2eTypeErrDoc)
	if len(fs) == 0 {
		t.Fatal("Analyze(type error doc) = 0 findings, want at least one")
	}
	found := false
	for i := range fs {
		switch fs[i].Code {
		case celtlint.CodeTypeMismatch, celtlint.CodeNoMatchingOverload:
			if fs[i].IsError() {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no compile type-error finding in %+v", fs)
	}
}

// TestE2EFixConverges asserts the auto-fix loop: a fixable finding is applied,
// and re-analyzing the fixed source yields no findings.
func TestE2EFixConverges(t *testing.T) {
	a := e2eAnalyzer(t)
	before := a.Analyze("typo.cel", e2eTypoDoc)

	fixed, kept, changed := celtlint.ApplyFixes(e2eTypoDoc, before)
	if !changed {
		t.Fatal("ApplyFixes = changed=false, want a fix to be applied")
	}
	if fixed == e2eTypoDoc {
		t.Error("ApplyFixes returned the source unchanged")
	}
	if !strings.Contains(fixed, "requests[0]") {
		t.Errorf("fixed source = %q, want the misspelling corrected to requests[0]", fixed)
	}
	for i := range kept {
		if kept[i].IsFix() {
			t.Errorf("kept findings still contain fixable finding %+v", kept[i])
		}
	}

	after := a.Analyze("fixed.cel", fixed)
	if len(after) != 0 {
		t.Errorf("re-Analyze(fixed source) = %d findings, want 0: %+v", len(after), after)
	}
}

// TestE2ERenderFindings asserts the rendered report for a realistic document
// with multiple findings surfaces the source text, the source name, and the
// finding messages.
func TestE2ERenderFindings(t *testing.T) {
	a := e2eAnalyzer(t)
	content := `is_ok(request[0]) && is_ok("nope")`
	fs := a.Analyze("reports/policy.cel", content)
	if len(fs) == 0 {
		t.Fatal("Analyze(report doc) = 0 findings, want findings to render")
	}
	src := common.NewStringSource(content, "reports/policy.cel")
	out := celtlint.RenderFindings(src, fs)
	if out == "" {
		t.Fatal("RenderFindings returned empty output")
	}
	for _, want := range []string{
		"reports/policy.cel",   // source name/description must appear
		"request",              // the source text must appear
		"unknown identifier",   // the misspell message
		"no matching overload", // the compile type/argument message
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderFindings output does not contain %q:\n%s", want, out)
		}
	}
}

// TestE2EReportSummary asserts the public summary/report API renders a coherent
// count of warnings, errors and fixable findings. When errors are present the
// summary returns early after the error line (no trailing "Finished" line).
func TestE2EReportSummary(t *testing.T) {
	out := celtlint.Summary(celtlint.Stats{Warnings: 2, Errors: 1, Fixable: 1, Checked: 3}, "celt")
	for _, want := range []string{"2 warnings", "1 previous error", "1 suggestion", "2 warnings emitted"} {
		if !strings.Contains(out, want) {
			t.Errorf("Summary output does not contain %q: %q", want, out)
		}
	}
}
