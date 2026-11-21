package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EUnknownLintRuleRejected(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	e2eWriteFile(t, filepath.Join(proj, ".celt.yml"), "lint:\n  misspelled-rule: error\n")
	e2eWriteFile(t, filepath.Join(proj, "valid.cel"), "true\n")

	_, stderr, code := e2eRun(t, bin, proj, "lint", "--dir", proj)
	if code == 0 {
		t.Errorf("unknown lint rule accepted: exit=%d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "misspelled-rule") {
		t.Errorf("error should identify unknown lint rule: %q", stderr)
	}
}

func TestE2ELintUsesTargetDirectoryConfig(t *testing.T) {
	bin := e2eBinary(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	e2eWriteFile(t, filepath.Join(root, ".celt.yml"), "env: {}\n")
	e2eWriteFile(t, filepath.Join(project, ".celt.yml"), "env:\n  variables:\n    - {name: allowed, type: bool}\n")
	e2eWriteFile(t, filepath.Join(project, "policy.cel"), "allowed\n")

	for _, cwd := range []string{project, outside} {
		t.Run(filepath.Base(cwd), func(t *testing.T) {
			_, stderr, code := e2eRun(t, bin, cwd, "lint", "--dir", project)
			if code != 0 {
				t.Errorf("lint exited %d: %s", code, stderr)
			}
		})
	}
}

func TestE2ELintRelativeDirectoryFindsAncestorConfig(t *testing.T) {
	bin := e2eBinary(t)
	root := t.TempDir()
	cwd := filepath.Join(root, "nested")
	target := filepath.Join(cwd, "policies")
	e2eWriteFile(t, filepath.Join(root, ".celt.yml"), "env:\n  variables:\n    - {name: allowed, type: bool}\n")
	e2eWriteFile(t, filepath.Join(target, "policy.cel"), "allowed\n")
	for _, dir := range []string{target, "policies"} {
		_, stderr, code := e2eRun(t, bin, cwd, "lint", "--dir", dir)
		if code != 0 {
			t.Errorf("lint --dir %q exited %d: %s", dir, code, stderr)
		}
	}
}

func TestE2ELintRejectsUnknownOptionalFieldSetting(t *testing.T) {
	bin := e2eBinary(t)
	root := t.TempDir()
	const key = "proto3-optoinal"
	e2eWriteFile(t, filepath.Join(root, ".celt.yml"), "lint:\n  optional-fields:\n    "+key+": true\n")
	e2eWriteFile(t, filepath.Join(root, "policy.cel"), "true\n")
	_, stderr, code := e2eRun(t, bin, root, "lint", "--dir", root)
	if code == 0 || !strings.Contains(stderr, key) {
		t.Errorf("lint should reject unknown optional-fields setting %q, got exit %d: %s", key, code, stderr)
	}
}

func TestE2ELintFixDoesNotRewriteLiterals(t *testing.T) {
	bin := e2eBinary(t)
	for _, tc := range []struct{ name, src string }{
		{name: "uppercase raw string", src: `R"hello" == "hello"`},
		{name: "raw bytes", src: `br"hello" == b"hello"`},
		{name: "raw string ending in backslash before normal string", src: `r"userNme\" == "userNme\\"`},
		{name: "triple-quoted string", src: `"""hello " userNme " world""" == 'hello " userNme " world'`},
		{name: "escaped quote before two quotes", src: `"""hello \""" userNme world""".contains("userNme")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			e2eWriteFile(t, filepath.Join(project, ".celt.yml"), `env:
  variables:
    - {name: rx, type: string}
    - {name: brx, type: bytes}
    - {name: userName, type: string}
`)
			file := filepath.Join(project, "policy.cel")
			want := tc.src + "\n"
			e2eWriteFile(t, file, want)

			_, stderr, code := e2eRun(t, bin, project, "lint", "--dir", project, "--fix")
			if code != 0 {
				t.Errorf("lint --fix exited %d: %s", code, stderr)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("lint --fix wrote %q, want unchanged %q", got, want)
			}
		})
	}
}

func TestE2ELintFixPreservesNonIdentifierNames(t *testing.T) {
	bin := e2eBinary(t)
	for _, tc := range []struct {
		name, config, src string
		wantExit          int
	}{
		{
			name: "message constructor and field", src: `Foo{names: "a"}.names == "a"`,
			config: `env:
  variables: [{name: name, type: string}]
  types:
    messages:
      - name: Foo
        fields: [{name: names, type: string}]
`,
		},
		{
			name: "qualified message constructor", src: `one.A{id: "x"}.id == "x"`,
			config: "env: {types: {messages: [{name: one.A, fields: [{name: id, type: string}]}]}}\n",
		},
		{
			name: "comprehension with unrelated compile error", src: `["x"].all(usr, usr == "x") && bogus`, wantExit: 1,
			config: "env:\n  variables: [{name: user, type: string}]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			e2eWriteFile(t, filepath.Join(project, ".celt.yml"), tc.config)
			file := filepath.Join(project, "policy.cel")
			want := tc.src + "\n"
			e2eWriteFile(t, file, want)
			_, stderr, code := e2eRun(t, bin, project, "lint", "--dir", project, "--fix")
			if code != tc.wantExit {
				t.Errorf("lint --fix exited %d, want %d: %s", code, tc.wantExit, stderr)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("lint --fix rewrote valid names: got %q, want %q", got, want)
			}
		})
	}
}

func TestE2EFormatUsesTargetProjectConfig(t *testing.T) {
	bin := e2eBinary(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	e2eWriteFile(t, filepath.Join(root, ".celt.yml"), "fmt:\n  width: 100\n")
	e2eWriteFile(t, filepath.Join(project, ".celt.yml"), "fmt:\n  width: 5\n  indent: \"\\t\"\n")
	file := filepath.Join(project, "policy.cel")
	e2eWriteFile(t, file, "a && b && c && d\n")
	want, stderr, code := e2eRun(t, bin, project, "format", file)
	if code != 0 || !strings.Contains(want, "\n\t") {
		t.Fatalf("expected wrapped target formatting, got exit %d, stdout=%q stderr=%q", code, want, stderr)
	}
	got, stderr, code := e2eRun(t, bin, root, "format", file)
	if code != 0 || got != want {
		t.Errorf("format from outside target: exit=%d stdout=%q, want %q; stderr=%s", code, got, want, stderr)
	}
}

func TestE2ELintRejectsUnknownConfigKeys(t *testing.T) {
	bin := e2eBinary(t)
	for _, tc := range []struct{ key, yaml string }{
		{key: "linnt", yaml: "linnt: {compile: none}\n"},
		{key: "variablez", yaml: "env: {variablez: [{name: foo, type: string}]}\n"},
		{key: "levell", yaml: "lint: {compile: {levell: none}}\n"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			project := t.TempDir()
			e2eWriteFile(t, filepath.Join(project, ".celt.yml"), tc.yaml)
			e2eWriteFile(t, filepath.Join(project, "policy.cel"), "true\n")
			_, stderr, code := e2eRun(t, bin, project, "lint", "--dir", project)
			if code == 0 || !strings.Contains(stderr, tc.key) {
				t.Errorf("lint should reject unknown key %q, got exit %d: %s", tc.key, code, stderr)
			}
		})
	}
}

func TestE2ELintRejectsUnexpectedPositionalArguments(t *testing.T) {
	bin := e2eBinary(t)
	project := t.TempDir()
	e2eWriteFile(t, filepath.Join(project, ".celt.yml"), "env: {}\n")
	e2eWriteFile(t, filepath.Join(project, "policy.cel"), "true\n")
	_, stderr, code := e2eRun(t, bin, project, "lint", "--dir", project, "junk", "--fix")
	if code == 0 || !strings.Contains(stderr, "junk") {
		t.Errorf("lint should reject the unexpected argument before ignoring --fix: exit=%d stderr=%q", code, stderr)
	}
}

func TestE2ELintFixDoesNotIntroduceCompilerErrors(t *testing.T) {
	bin := e2eBinary(t)
	const env = `env:
  variables: [{name: food, type: bool}]
  types:
    messages:
      - name: Foo
        fields: [{name: x, type: string}]
`
	const expr = `Foo {x: "a"}.x == "a"`
	for _, tc := range []struct{ name, config, src string }{
		{name: "unrelated existing compiler error", config: env, src: expr + " && bogus"},
		{name: "compiler rule disabled", config: env + "lint: {compile: none}\n", src: expr},
		{
			name:   "additional occurrence of an existing compiler error",
			config: "env: {variables: [{name: userName, type: int}]}\n",
			src:    `[true ? 1 : false, {"k": true ? userNme : false}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			e2eWriteFile(t, filepath.Join(project, ".celt.yml"), tc.config)
			file := filepath.Join(project, "policy.cel")
			want := tc.src + "\n"
			e2eWriteFile(t, file, want)
			_, stderr, code := e2eRun(t, bin, project, "lint", "--dir", project, "--fix")
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("lint --fix wrote a candidate with a new compiler error at exit %d: got %q, want %q; stderr=%s", code, got, want, stderr)
			}
		})
	}
}

func TestE2EFormatWriteUsesOnlyApplicableConfig(t *testing.T) {
	bin := e2eBinary(t)
	const valid = "fmt: {width: 100}\n"
	const invalid = "fmt: {width: not-an-integer}\n"
	for _, tc := range []struct {
		name, parent, target, want string
		wantError                  bool
	}{
		{name: "invalid target config", parent: valid, target: invalid, want: "a+b\n", wantError: true},
		{name: "valid target overrides invalid parent", parent: invalid, target: valid, want: "a + b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, "project")
			e2eWriteFile(t, filepath.Join(root, ".celt.yml"), tc.parent)
			e2eWriteFile(t, filepath.Join(project, ".celt.yml"), tc.target)
			file := filepath.Join(project, "policy.cel")
			e2eWriteFile(t, file, "a+b\n")
			_, stderr, code := e2eRun(t, bin, root, "format", "--write", file)
			if (code != 0) != tc.wantError {
				t.Errorf("format exited %d, want error=%v: %s", code, tc.wantError, stderr)
			}
			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("file = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestE2ELintRejectsIgnoredYAMLDocument(t *testing.T) {
	bin := e2eBinary(t)
	project := t.TempDir()
	e2eWriteFile(t, filepath.Join(project, ".celt.yml"), "env: {}\n---\nenv:\n  variablez: []\n")
	e2eWriteFile(t, filepath.Join(project, "policy.cel"), "true\n")
	_, stderr, code := e2eRun(t, bin, project, "lint", "--dir", project)
	if code == 0 {
		t.Errorf("lint silently accepted a second config document containing an unknown key: %s", stderr)
	}
}

func TestE2EFormatFailsWhenStdoutCannotBeWritten(t *testing.T) {
	out, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if os.IsNotExist(err) {
		t.Skip("requires /dev/full")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	root := t.TempDir()
	e2eWriteFile(t, filepath.Join(root, ".celt.yml"), "env: {}\n")
	file := filepath.Join(root, "input.cel")
	e2eWriteFile(t, file, "1+2\n")
	cmd := exec.CommandContext(t.Context(), e2eBinary(t), "format", file) // #nosec G204 -- fixed args, not tainted input
	cmd.Dir, cmd.Stdout = root, out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Errorf("format exited successfully even though stdout rejected the write: %s", &stderr)
	}
}

func TestE2ELintLoadsConfigWithYAMLAliases(t *testing.T) {
	bin := e2eBinary(t)
	for _, tc := range []struct{ name, config string }{
		{name: "shared severity", config: "lint: {compile: &sev error, misspell: *sev}\n"},
		{name: "env reference to fmt anchor", config: "fmt: {indent: &text '  '}\nenv: {variables: [{name: x, type: string, doc: *text}]}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			e2eWriteFile(t, filepath.Join(root, ".celt.yml"), tc.config)
			e2eWriteFile(t, filepath.Join(root, "policy.cel"), "true\n")
			_, stderr, code := e2eRun(t, bin, root, "lint", "--dir", root)
			if code != 0 {
				t.Errorf("valid YAML aliases rejected at exit %d: %s", code, stderr)
			}
		})
	}
}

func TestE2EFormatReportsInvalidConfigBeforeReadingStdin(t *testing.T) {
	bin := e2eBinary(t)
	for _, args := range [][]string{{"format"}, {"format", "--check"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			project := t.TempDir()
			e2eWriteFile(t, filepath.Join(project, ".celt.yml"), "fmt: {width: bad}\n")
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Dir = project
			cmd.Stdin = reader
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err = cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("format waited for EOF before reporting invalid config: %v", ctx.Err())
			}
			if err == nil || !strings.Contains(stderr.String(), "width") {
				t.Errorf("expected an error about width, got %v: %s", err, &stderr)
			}
		})
	}
}

func TestE2EFormatWriteStdinDoesNotWaitForInput(t *testing.T) {
	bin := e2eBinary(t)
	proj := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close() // Keep stdin open and empty until the process exits.

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "format", "--write", "-")
	cmd.Dir = proj
	cmd.Stdin = reader
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("format --write - waited for stdin before rejecting it: %v", ctx.Err())
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("format --write - error=%v, want exit 1; stderr=%q", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--write") || !strings.Contains(stderr.String(), "stdin") {
		t.Errorf("expected an explanatory stdin error, got %q", stderr.String())
	}
}
