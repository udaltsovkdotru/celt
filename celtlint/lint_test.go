package celtlint

import (
	"fmt"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Rule names and finding codes reused across the Analyzer tests.
const (
	lnRunName     = "ln-run"
	lnPlainName   = "ln-plain"
	lnOvrName     = "ln-ovr"
	lnOffName     = "ln-off"
	lnBoomName    = "ln-boom"
	lnRawName     = "ln-raw"
	lnDescName    = "ln-desc"
	lnFindingCode = "ln-finding"
	lnTestContent = "1 + 2"
)

// lnTestRule is a minimal configurable rule used to drive the Analyzer. It
// records the config applied via ApplyConfig and delegates Check to an
// injected function.
type lnTestRule struct {
	RuleMeta
	check   func(ctx *Context)
	apply   func(data []byte) error
	applied []byte
}

func (r *lnTestRule) Check(ctx *Context) {
	if r.check != nil {
		r.check(ctx)
	}
}

func (r *lnTestRule) ApplyConfig(data []byte) error {
	r.applied = append([]byte(nil), data...)
	if r.apply != nil {
		return r.apply(data)
	}
	return nil
}

// lnPlainRule is a non-configurable rule (does not implement Configurable).
type lnPlainRule struct {
	RuleMeta
	check func(ctx *Context)
}

func (r *lnPlainRule) Check(ctx *Context) {
	if r.check != nil {
		r.check(ctx)
	}
}

// lnProvider is a ConfigProvider backed by maps.
type lnProvider struct {
	levels map[string]Level
	raws   map[string][]byte
}

func (p lnProvider) Level(name string) (Level, bool) {
	if p.levels == nil {
		return 0, false
	}
	l, ok := p.levels[name]
	return l, ok
}

func (p lnProvider) Raw(name string) ([]byte, bool) {
	if p.raws == nil {
		return nil, false
	}
	r, ok := p.raws[name]
	return r, ok
}

// lnEnv builds a bare cel.Env for the lint tests.
func lnEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv()
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

func TestNewWithRules(t *testing.T) {
	env := lnEnv(t)
	r := &lnTestRule{RuleMeta: RuleMeta{Name: lnRunName, Defaults: RuleParams{Level: LevelWarn}}}
	a, err := New(env, WithRules(r))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(a.rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(a.rules))
	}
	if a.rules[0] != r {
		t.Errorf("rules[0] is not the provided rule")
	}
	if a.env != env {
		t.Errorf("analyzer env is not the provided env")
	}
}

func TestNewWithDescriptor(t *testing.T) {
	env := lnEnv(t)
	fdset := &descriptorpb.FileDescriptorSet{}
	r := &lnTestRule{RuleMeta: RuleMeta{Name: lnDescName}}
	a, err := New(env, WithDescriptor(fdset), WithRules(r))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("New returned nil Analyzer")
	}
	if len(a.rules) != 1 {
		t.Errorf("rules = %d, want 1", len(a.rules))
	}
}

// TestNewRawOverride verifies that a Configurable rule receives the provider's
// raw config (merged over its defaults) via ApplyConfig at construction time.
func TestNewRawOverride(t *testing.T) {
	env := lnEnv(t)
	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{name: "override", raw: []byte("override"), want: "override"},
		{name: "no override uses default", raw: nil, want: "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &lnTestRule{RuleMeta: RuleMeta{Name: lnRawName, Defaults: RuleParams{Level: LevelError, Config: []byte("default")}}}
			raws := map[string][]byte{}
			if tt.raw != nil {
				raws[lnRawName] = tt.raw
			}
			p := lnProvider{raws: raws}
			if _, err := New(env, WithRules(r), WithConfigProvider(p)); err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := string(r.applied); got != tt.want {
				t.Errorf("ApplyConfig received %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewApplyConfigError(t *testing.T) {
	env := lnEnv(t)
	r := &lnTestRule{
		RuleMeta: RuleMeta{Name: lnBoomName},
		apply:    func([]byte) error { return fmt.Errorf("bad config") },
	}
	if _, err := New(env, WithRules(r)); err == nil {
		t.Fatal("New: expected ApplyConfig error to propagate, got nil")
	}
}

func TestAnalyzeRunsRule(t *testing.T) {
	env := lnEnv(t)
	r := &lnPlainRule{
		RuleMeta: RuleMeta{Name: lnPlainName, Defaults: RuleParams{Level: LevelWarn}},
		check: func(ctx *Context) {
			ctx.Report(&Finding{Level: LevelWarn, Code: lnFindingCode, Message: "plain rule ran"})
		},
	}
	a, err := New(env, WithRules(r))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fs := a.Analyze("test.cel", lnTestContent)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Code != lnFindingCode {
		t.Errorf("Code = %q, want %q", fs[0].Code, lnFindingCode)
	}
	if fs[0].Level != LevelWarn {
		t.Errorf("Level = %v, want LevelWarn", fs[0].Level)
	}
}

func TestAnalyzeNoRules(t *testing.T) {
	env := lnEnv(t)
	a, err := New(env)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if fs := a.Analyze("test.cel", lnTestContent); len(fs) != 0 {
		t.Errorf("findings = %d, want 0", len(fs))
	}
}

// TestAnalyzeLevelOverride verifies that a provider level override is applied
// to findings the rule reported without an explicit level, and that LevelOff
// disables the rule entirely.
func TestAnalyzeLevelOverride(t *testing.T) {
	env := lnEnv(t)
	tests := []struct {
		name         string
		level        Level
		wantFindings int
		wantLevel    Level
	}{
		{name: "override to warn", level: LevelWarn, wantFindings: 1, wantLevel: LevelWarn},
		{name: "override to error", level: LevelError, wantFindings: 1, wantLevel: LevelError},
		{name: "off disables rule", level: LevelOff, wantFindings: 0, wantLevel: LevelOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &lnTestRule{
				RuleMeta: RuleMeta{Name: lnOvrName, Defaults: RuleParams{Level: LevelError}},
				check: func(ctx *Context) {
					// No explicit level: the override, if present, must fill it in.
					ctx.Report(&Finding{Code: lnFindingCode, Message: "no level set"})
				},
			}
			p := lnProvider{levels: map[string]Level{lnOvrName: tt.level}}
			a, err := New(env, WithRules(r), WithConfigProvider(p))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			fs := a.Analyze("test.cel", lnTestContent)
			if len(fs) != tt.wantFindings {
				t.Fatalf("findings = %d, want %d", len(fs), tt.wantFindings)
			}
			if tt.wantFindings == 0 {
				return
			}
			if fs[0].Level != tt.wantLevel {
				t.Errorf("Level = %v, want %v", fs[0].Level, tt.wantLevel)
			}
		})
	}
}

// TestAnalyzeRuleLevelNotOverridden verifies that a rule's explicit finding
// level is preserved when the provider does not override the rule.
func TestAnalyzeRuleLevelNotOverridden(t *testing.T) {
	env := lnEnv(t)
	r := &lnTestRule{
		RuleMeta: RuleMeta{Name: lnOvrName, Defaults: RuleParams{Level: LevelError}},
		check: func(ctx *Context) {
			ctx.Report(&Finding{Level: LevelError, Code: lnFindingCode, Message: "explicit error"})
		},
	}
	p := lnProvider{levels: map[string]Level{}}
	a, err := New(env, WithRules(r), WithConfigProvider(p))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fs := a.Analyze("test.cel", lnTestContent)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Level != LevelError {
		t.Errorf("Level = %v, want LevelError", fs[0].Level)
	}
}

func TestAnalyzeSource(t *testing.T) {
	env := lnEnv(t)
	r := &lnPlainRule{
		RuleMeta: RuleMeta{Name: lnPlainName, Defaults: RuleParams{Level: LevelWarn}},
		check: func(ctx *Context) {
			ctx.Report(&Finding{Level: LevelWarn, Code: lnFindingCode, Message: "via source"})
		},
	}
	a, err := New(env, WithRules(r))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src := common.NewStringSource(lnTestContent, "virtual.cel")
	fs := a.AnalyzeSource(src)
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if fs[0].Message != "via source" {
		t.Errorf("Message = %q, want %q", fs[0].Message, "via source")
	}
}

func TestContextReport(t *testing.T) {
	ctx := &Context{}
	ctx.Report(&Finding{Code: "a"})
	ctx.ReportAll([]Finding{{Code: "b"}, {Code: "c"}})
	if len(ctx.Findings) != 3 {
		t.Fatalf("Findings = %d, want 3", len(ctx.Findings))
	}
	for i, want := range []string{"a", "b", "c"} {
		if ctx.Findings[i].Code != want {
			t.Errorf("Findings[%d].Code = %q, want %q", i, ctx.Findings[i].Code, want)
		}
	}
}

func TestContextReportAllEmpty(t *testing.T) {
	ctx := &Context{}
	ctx.ReportAll(nil)
	if len(ctx.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(ctx.Findings))
	}
}

func TestContextCompileFailed(t *testing.T) {
	if (&Context{}).CompileFailed() {
		t.Error("CompileFailed = true for a Context with no issues")
	}
	env := lnEnv(t)
	_, iss := env.CompileSource(common.NewStringSource("undeclaredRef", "t"))
	ctx := &Context{Err: iss}
	if !ctx.CompileFailed() {
		t.Error("CompileFailed = false, want true for a source with compile errors")
	}
}
