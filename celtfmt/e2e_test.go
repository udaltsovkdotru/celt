package celtfmt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtfmt/rules"
)

// e2eConfig is an external-package implementation of the public celtfmt.Config
// interface. It lets the e2e tests drive formatting through the exported API
// exactly as a downstream consumer would, backed by the real rules package.
type e2eConfig struct {
	rules []celtfmt.FormatRule
}

func (c e2eConfig) Rules() []celtfmt.FormatRule { return c.rules }

// e2eCfg builds a Config from a custom indent string and line width, using the
// production indent/width rules shipped in celtfmt/rules.
func e2eCfg(indent string, width int) celtfmt.Config {
	return e2eConfig{
		rules: []celtfmt.FormatRule{
			rules.NewIndentRule(indent),
			rules.NewWidthRule(width),
		},
	}
}

// e2eRealisticDoc is a whole-document CEL policy combining a leading doc
// header, a boolean chain that overflows the line width, nested function
// calls, a map literal and a list literal, plus trailing // comments inside
// the map and list.
const e2eRealisticDoc = `// Evaluate whether the remote principal is authorized to mutate the
// target resource within the parent scope before permitting the request.
authorized(principal, resource) &&
  role_grants_access(resource, { "admin": true, "editor": true, // policy flags
                                 "audit": true }) &&
  verify_signature(request, headers, [ "x-signature", "x-timestamp", // sig inputs
                                       "x-nonce" ])`

// TestE2ERealisticDocument formats a realistic multi-line document through the
// public API and asserts that the leading doc header is preserved verbatim
// while the expression is re-rendered to the configured indent and width.
func TestE2ERealisticDocument(t *testing.T) {
	cfg := e2eCfg("    ", 60)
	out, err := celtfmt.Format(e2eRealisticDoc, cfg)
	if err != nil {
		t.Fatalf("Format(realistic doc): %v", err)
	}

	// 1. The leading // doc header must be preserved, followed by a blank line
	//    before the formatted expression.
	wantHeader := "// Evaluate whether the remote principal is authorized to mutate the\n" +
		"// target resource within the parent scope before permitting the request.\n\n"
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("doc header not preserved verbatim, want prefix %q, got:\n%s", wantHeader, out)
	}

	// 2. The expression body must be reformatted (not passed through unchanged).
	if out == e2eRealisticDoc {
		t.Error("expected the expression to be reformatted, got input unchanged")
	}

	// 3. The configured indent unit (4 spaces) must appear on continuation lines.
	if !strings.Contains(out, "\n    ") {
		t.Errorf("expected 4-space indented continuation lines, got:\n%s", out)
	}

	// 4. The structural elements must all survive: the && chain, the nested
	//    calls, the map braces and the list brackets.
	for _, want := range []string{"&&", "role_grants_access", "verify_signature", "{", "}", "[", "]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q, got:\n%s", want, out)
		}
	}

	// 5. Trailing // comments on the map and list elements must be retained.
	for _, want := range []string{"policy flags", "sig inputs"} {
		if !strings.Contains(out, want) {
			t.Errorf("output dropped trailing comment %q, got:\n%s", want, out)
		}
	}
}

// TestE2EIdempotent runs Format(Format(x)) == Format(x) across several
// representative whole documents — the idempotence property a real formatter
// consumer relies on.
func TestE2EIdempotent(t *testing.T) {
	tests := []struct {
		name string
		src  string
		cfg  celtfmt.Config
	}{
		{
			name: "realistic policy doc",
			src:  e2eRealisticDoc,
			cfg:  e2eCfg("    ", 60),
		},
		{
			name: "deeply nested calls",
			src:  "dispatch(parse(headers, \"x-tenant\"), route(segments[0], segments[1]), retry(3))",
			cfg:  e2eCfg("  ", 40),
		},
		{
			name: "map and list with comments",
			src: `matching({ "region": "eu", "env": "prod", // primary
                                   "tier": "gold" }, [ "a", "b", "c", // scope
                                                        "d" ])`,
			cfg: e2eCfg("    ", 48),
		},
		{
			name: "boolean chain with idents",
			src:  "alpha_enabled && beta_enabled && gamma_enabled && delta_enabled && epsilon_enabled",
			cfg:  e2eCfg("  ", 30),
		},
		{
			name: "doc header with narrow width",
			src:  "// header line\n\nfoo(alpha, beta, gamma, delta, epsilon)",
			cfg:  e2eCfg("  ", 20),
		},
		{
			name: "comparison chain",
			src:  "request.attempts < 3 && request.latency_ms <= 250 && principal.risk >= 0.5",
			cfg:  e2eCfg("  ", 100),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			once, err := celtfmt.Format(tt.src, tt.cfg)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			twice, err := celtfmt.Format(once, tt.cfg)
			if err != nil {
				t.Fatalf("Format(Format(x)): %v", err)
			}
			if twice != once {
				t.Errorf("Format(Format(x)) != Format(x)\n got:  %q\n want: %q", twice, once)
			}
		})
	}
}

// TestE2ECustomIndentAndWidth propagates a 4-space indent and a narrow width
// through the public API and asserts both reach the rendered output.
func TestE2ECustomIndentAndWidth(t *testing.T) {
	src := "foo(alpha, beta, gamma, delta)"
	cfg := e2eCfg("    ", 12)

	narrow, err := celtfmt.Format(src, cfg)
	if err != nil {
		t.Fatalf("Format(narrow): %v", err)
	}

	// The narrow width must force the call onto multiple indented lines using
	// the configured 4-space indent.
	if !strings.Contains(narrow, "\n    alpha,\n") {
		t.Errorf("narrow output missing 4-space indent break, got:\n%s", narrow)
	}
	for _, line := range strings.Split(strings.TrimRight(narrow, "\n"), "\n") {
		if len(line) > 12 {
			t.Errorf("line %q exceeds configured width 12 (len=%d)", line, len(line))
		}
	}

	// The same expression at the default (wide) width must stay on one line,
	// proving the width setting is what drove the break.
	wide, err := celtfmt.Format(src, e2eCfg("    ", 100))
	if err != nil {
		t.Fatalf("Format(wide): %v", err)
	}
	if wide != "foo(alpha, beta, gamma, delta)\n" {
		t.Errorf("wide output = %q, want single line", wide)
	}

	// Reformatting the already-formatted narrow output must be idempotent.
	again, err := celtfmt.Format(narrow, cfg)
	if err != nil {
		t.Fatalf("Format(format): %v", err)
	}
	if again != narrow {
		t.Errorf("reformat = %q, want %q", again, narrow)
	}
}

// TestE2EIndentDiffersFromDefault confirms a custom indent actually changes the
// rendered output relative to the two-space default, exercising propagation of
// the IndentProvider through the public Config interface.
func TestE2EIndentDiffersFromDefault(t *testing.T) {
	src := "foo(alpha, beta, gamma, delta, epsilon)"
	def, err := celtfmt.Format(src, e2eCfg("  ", 16))
	if err != nil {
		t.Fatalf("Format(default indent): %v", err)
	}
	custom, err := celtfmt.Format(src, e2eCfg("\t", 16))
	if err != nil {
		t.Fatalf("Format(tab indent): %v", err)
	}
	if !strings.Contains(def, "\n  alpha") {
		t.Errorf("default output missing two-space indent, got:\n%s", def)
	}
	if !strings.Contains(custom, "\n\talpha") {
		t.Errorf("tab output missing tab indent, got:\n%s", custom)
	}
	if custom == def {
		t.Error("custom indent produced identical output to default")
	}
}

// TestE2EWriteFilePreservesPerms verifies WriteFile overwrites an existing file
// while preserving its permission bits.
func TestE2EWriteFilePreservesPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rule.cel")

	if err := os.WriteFile(path, []byte("a && b\n"), 0o755); err != nil { //nolint:gosec // intentionally exercising permission preservation
		t.Fatalf("setup WriteFile: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o755 {
		t.Fatalf("setup perms = %o, want 0755", fi.Mode().Perm())
	}

	content := "a && b && c\n"
	if err := celtfmt.WriteFile(path, []byte(content)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("WriteFile preserved perms = %o, want 0755", got)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != content {
		t.Errorf("WriteFile content = %q, want %q", got, content)
	}
}

// TestE2EWriteFileNewDefaultPerm verifies WriteFile creates a new file with the
// default 0644 permission bits.
func TestE2EWriteFileNewDefaultPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.cel")

	if err := celtfmt.WriteFile(path, []byte("foo(bar)\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("WriteFile new-file perms = %o, want 0644", got)
	}
}

// TestE2EChainBreaksWithinWidth guards against the chain "hang" bug: an &&/||
// chain whose operands are calls ending in `)` used to be hung inline past the
// configured width. Every output line must stay within the width.
func TestE2EChainBreaksWithinWidth(t *testing.T) {
	src := `is_allowed(principal, action, resource, tenant, region, zone, tags) && is_denied(principal, action, resource) && is_reviewed(resource, action, principal)`
	out, err := celtfmt.Format(src, e2eCfg("  ", 40))
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 40 {
			t.Errorf("line exceeds width 40 (%d chars): %q", len(line), line)
		}
	}
}
