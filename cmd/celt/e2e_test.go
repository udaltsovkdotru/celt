package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// e2eConfig is a representative .celt.yml exercising the declarative env
// section (variables, a function and a macro) together with lint and fmt
// sections. It is used by the lint and format e2e tests below.
const e2eConfig = `env:
  variables:
    - name: foo
      type: int
    - name: size
      type: int
  functions:
    - name: myFn
      overloads:
        - id: myFn_int
          args: ["int"]
          result: "int"
  macros:
    - name: myMacro
      fixed_arity: 1
      results: ["int"]
lint:
  misspell: error
  compile: error
fmt:
  indent: "  "
  width: 100
`

// The built binary is compiled once and reused across all tests.
var (
	e2eBinOnce sync.Once
	e2eBinPath string
	e2eBinErr  error
)

// e2eBinary builds the real celt binary once (into a temp dir) and returns its
// path. Building from the current package dir (cwd of the test) produces the
// actual CLI the tests exercise end-to-end.
func e2eBinary(t *testing.T) string {
	t.Helper()
	e2eBinOnce.Do(func() {
		pkgDir, err := os.Getwd()
		if err != nil {
			e2eBinErr = fmt.Errorf("getwd: %w", err)
			return
		}
		dir, err := os.MkdirTemp("", "celt-e2e-*")
		if err != nil {
			e2eBinErr = fmt.Errorf("mkdir temp: %w", err)
			return
		}
		e2eBinPath = filepath.Join(dir, "celt")
		cmd := exec.CommandContext(context.Background(), "go", "build", "-o", e2eBinPath, ".")
		cmd.Dir = pkgDir
		if out, err := cmd.CombinedOutput(); err != nil {
			e2eBinErr = fmt.Errorf("go build celt: %w\n%s", err, out)
		}
	})
	if e2eBinErr != nil {
		t.Fatalf("e2e: %v", e2eBinErr)
	}
	return e2eBinPath
}

// e2eRun runs the celt binary with args in dir and returns stdout, stderr and
// the exit code.
func e2eRun(t *testing.T, bin, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Dir = dir
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("e2e: run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// e2eWriteFile creates parent dirs and writes content to path.
func e2eWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("e2e: mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("e2e: write %s: %v", path, err)
	}
}

// e2eTestLintClean verifies that a valid .cel file against a valid env
// produces a clean run (exit 0) and prints the finished summary.
func TestE2ELintClean(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	e2eWriteFile(t, filepath.Join(proj, "valid.cel"), "foo + size\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--dir", proj)
	if code != 0 {
		t.Fatalf("lint clean: exit=%d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "Finished") {
		t.Errorf("lint clean: expected summary on stderr, got %q", stderr)
	}
}

// e2eTestLintFinding verifies that a misspelled identifier is reported: the
// process exits non-zero and the diagnostic names the offending token and file.
func TestE2ELintFinding(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	e2eWriteFile(t, filepath.Join(proj, "finding.cel"), "foo + szie\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--dir", proj)
	if code != 1 {
		t.Fatalf("lint finding: exit=%d, want 1; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "szie") {
		t.Errorf("lint finding: output should mention the offending token %q, got %q", "szie", stderr)
	}
	if !strings.Contains(stderr, "finding.cel") {
		t.Errorf("lint finding: output should mention the source file, got %q", stderr)
	}
}

// e2eTestLintMalformed verifies that a malformed .cel file surfaces a compile
// error: non-zero exit and the offending file is named, without panicking.
func TestE2ELintMalformed(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	e2eWriteFile(t, filepath.Join(proj, "malformed.cel"), "foo + * 3\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--dir", proj)
	if code != 1 {
		t.Fatalf("lint malformed: exit=%d, want 1; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "malformed.cel") {
		t.Errorf("lint malformed: output should mention the malformed file, got %q", stderr)
	}
	if strings.Contains(stderr, "panic") {
		t.Errorf("lint malformed: unexpected panic: %s", stderr)
	}
}

// e2eTestLintFix verifies that `lint --fix` rewrites the source on disk to the
// corrected content (misspelled identifier replaced) and exits 0.
func TestE2ELintFix(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	findingPath := filepath.Join(proj, "finding.cel")
	e2eWriteFile(t, findingPath, "foo + szie\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--fix", "--dir", proj)
	if code != 0 {
		t.Fatalf("lint --fix: exit=%d, want 0; stderr=%s", code, stderr)
	}
	got, err := os.ReadFile(findingPath)
	if err != nil {
		t.Fatalf("lint --fix: read file: %v", err)
	}
	if string(got) != "foo + size\n" {
		t.Errorf("lint --fix: got %q, want %q", string(got), "foo + size\n")
	}
}

// e2eTestLintFixPartial verifies that `lint --fix` applies the fixable
// misspelling but still exits non-zero when an unfixable error remains after
// fixing.
func TestE2ELintFixPartial(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	findingPath := filepath.Join(proj, "partial.cel")
	e2eWriteFile(t, findingPath, "foo + szie + (\"a\" + 1)\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--fix", "--dir", proj)
	if code != 1 {
		t.Fatalf("lint --fix partial: exit=%d, want 1; stderr=%s", code, stderr)
	}
	got, err := os.ReadFile(findingPath)
	if err != nil {
		t.Fatalf("lint --fix partial: read file: %v", err)
	}
	// The fixable misspelling is applied even though the type error remains.
	if !strings.Contains(string(got), "size") {
		t.Errorf("lint --fix partial: expected misspelling fixed, got %q", string(got))
	}
	if !strings.Contains(stderr, "fixed") {
		t.Errorf("lint --fix partial: expected 'fixed' summary on stderr, got %q", stderr)
	}
}

// e2eTestFormatWrite verifies that `format --write` rewrites the file on disk
// to the formatted content, reports the rewritten file, and exits non-zero
// when it changed (like --check), then exits 0 once the file is formatted.
func TestE2EFormatWrite(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	file := filepath.Join(proj, "fmt_me.cel")
	e2eWriteFile(t, file, "foo(bar,baz)\n")

	// First pass rewrites the file: non-zero exit and the file is reported.
	_, stderr, code := e2eRun(t, bin, proj, "format", "--write", file)
	if code != 1 {
		t.Fatalf("format --write (dirty): exit=%d, want 1; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "fmt_me.cel") {
		t.Errorf("format --write (dirty): expected the file to be reported, got %q", stderr)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("format --write: read file: %v", err)
	}
	if string(got) != "foo(bar, baz)\n" {
		t.Errorf("format --write: got %q, want %q", string(got), "foo(bar, baz)\n")
	}

	// Second pass on the now-formatted file is a no-op and exits 0.
	_, stderr, code = e2eRun(t, bin, proj, "format", "--write", file)
	if code != 0 {
		t.Errorf("format --write (clean): exit=%d, want 0; stderr=%s", code, stderr)
	}
}

// e2eTestFormatCheck verifies the `format --check` contract: exit 0 for a
// formatted file, exit 1 for an unformatted one.
func TestE2EFormatCheck(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	clean := filepath.Join(proj, "clean.cel")
	e2eWriteFile(t, clean, "foo(bar, baz)\n")
	dirty := filepath.Join(proj, "dirty.cel")
	e2eWriteFile(t, dirty, "foo(bar,baz)\n")

	_, _, code := e2eRun(t, bin, proj, "format", "--check", clean)
	if code != 0 {
		t.Errorf("format --check clean: exit=%d, want 0", code)
	}
	_, _, code = e2eRun(t, bin, proj, "format", "--check", dirty)
	if code != 1 {
		t.Errorf("format --check dirty: exit=%d, want 1", code)
	}
}

// e2eTestFormatPrint verifies the default (stdout) mode prints the formatted
// expression without modifying the file on disk.
func TestE2EFormatPrint(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), e2eConfig)
	file := filepath.Join(proj, "print.cel")
	e2eWriteFile(t, file, "foo(bar,baz)\n")

	stdout, _, code := e2eRun(t, bin, proj, "format", file)
	if code != 0 {
		t.Fatalf("format print: exit=%d, want 0", code)
	}
	if stdout != "foo(bar, baz)\n" {
		t.Errorf("format print: got %q, want %q", stdout, "foo(bar, baz)\n")
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("format print: read file: %v", err)
	}
	if string(got) != "foo(bar,baz)\n" {
		t.Errorf("format print: file was modified, got %q", string(got))
	}
}

// e2eTestMissingConfig verifies the tool's contract that a missing .celt.yml is
// not an error: lint falls back to defaults and clean sources still pass.
func TestE2EMissingConfig(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, "clean.cel"), "[1, 2, 3]\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--dir", proj)
	if code != 0 {
		t.Errorf("lint missing config: exit=%d, want 0; stderr=%s", code, stderr)
	}
}

// e2eTestBadConfig verifies that an invalid .celt.yml (here, a variable
// missing its type) is handled gracefully: non-zero exit, an explanatory
// config error on stderr, and no panic.
func TestE2EBadConfig(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), "env:\n  variables:\n    - name: foo\n")
	e2eWriteFile(t, filepath.Join(proj, "clean.cel"), "foo\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--dir", proj)
	if code == 0 {
		t.Errorf("lint bad config: exit=%d, want non-zero", code)
	}
	if strings.Contains(stderr, "panic") {
		t.Errorf("lint bad config: unexpected panic: %s", stderr)
	}
	if !strings.Contains(stderr, "missing type") {
		t.Errorf("lint bad config: expected config error on stderr, got %q", stderr)
	}
}
