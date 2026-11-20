package celtfmt

import (
	"bytes"
	"errors"
	"testing"

	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
)

// ---- local rule implementations (fm-prefixed, distinct from format_test) --

type fmIndentRule struct{ s string }

func (fmIndentRule) Meta() FormatMeta                         { return FormatMeta{Name: "fm-indent"} }
func (fmIndentRule) Rewrite(*celast.AST) (*celast.AST, error) { return nil, nil }
func (r fmIndentRule) Indent() string                         { return r.s }

type fmWidthRule struct{ w int }

func (fmWidthRule) Meta() FormatMeta                         { return FormatMeta{Name: "fm-width"} }
func (fmWidthRule) Rewrite(*celast.AST) (*celast.AST, error) { return nil, nil }
func (r fmWidthRule) Width() int                             { return r.w }

type fmBreakRule struct{}

func (fmBreakRule) Meta() FormatMeta                         { return FormatMeta{Name: "fm-break"} }
func (fmBreakRule) Rewrite(*celast.AST) (*celast.AST, error) { return nil, nil }
func (fmBreakRule) MustBreak(e celast.Expr) bool {
	if e.Kind() != celast.CallKind {
		return false
	}
	switch e.AsCall().FunctionName() {
	case operators.LogicalAnd, operators.LogicalOr, operators.Conditional:
		return true
	}
	return false
}

type fmConfigurableRule struct {
	lastData []byte
	applyErr error
}

// fmConfigRuleName is the rule name reported by fmConfigurableRule, shared by
// the enablement/raw-config test cases.
const fmConfigRuleName = "fm-config"

func (fmConfigurableRule) Meta() FormatMeta                         { return FormatMeta{Name: fmConfigRuleName} }
func (fmConfigurableRule) Rewrite(*celast.AST) (*celast.AST, error) { return nil, nil }
func (r *fmConfigurableRule) ApplyConfig(data []byte) error {
	r.lastData = data
	return r.applyErr
}

type fmConfig struct{ rules []FormatRule }

func (c fmConfig) Rules() []FormatRule { return c.rules }

func fmConfigOf(rules ...FormatRule) Config { return fmConfig{rules: rules} }

// ---- interface contracts --------------------------------------------------

func TestRuleInterfaceContracts(_ *testing.T) {
	// Compile-time assertions that the concrete rule types satisfy the exact
	// interfaces rules.go declares.
	var (
		_ FormatRule     = fmIndentRule{}
		_ IndentProvider = fmIndentRule{}

		_ FormatRule    = fmWidthRule{}
		_ WidthProvider = fmWidthRule{}

		_ FormatRule = fmBreakRule{}
		_ BreakRule  = fmBreakRule{}

		_ FormatRule   = fmConfigurableRule{}
		_ Configurable = (*fmConfigurableRule)(nil)
	)
}

func TestFormatMetaReturnsSelf(t *testing.T) {
	m := FormatMeta{Name: "fm-meta", Desc: "descriptive", Defaults: FormatParams{Enabled: true}}
	got := m.Meta()
	if got.Name != m.Name || got.Desc != m.Desc || got.Defaults.Enabled != m.Defaults.Enabled {
		t.Errorf("Meta() = %+v, want %+v", got, m)
	}
}

// ---- settings extraction --------------------------------------------------

func TestResolveSettingsDefaults(t *testing.T) {
	s, _ := resolveSettings(nil)
	if s.indentUnit != defaultIndent {
		t.Errorf("indentUnit = %q, want %q", s.indentUnit, defaultIndent)
	}
	if s.lineWidth != defaultWidth {
		t.Errorf("lineWidth = %d, want %d", s.lineWidth, defaultWidth)
	}
	if len(s.breaks) != 0 {
		t.Errorf("breaks = %d rules, want 0", len(s.breaks))
	}
}

func TestResolveSettingsExtractsProviders(t *testing.T) {
	s, _ := resolveSettings(fmConfigOf(fmIndentRule{"def"}, fmWidthRule{40}, fmBreakRule{}))
	if s.indentUnit != "def" {
		t.Errorf("indentUnit = %q, want %q", s.indentUnit, "def")
	}
	if s.lineWidth != 40 {
		t.Errorf("lineWidth = %d, want 40", s.lineWidth)
	}
	if len(s.breaks) != 1 {
		t.Errorf("breaks = %d rules, want 1", len(s.breaks))
	}
}

func TestResolveSettingsLastWins(t *testing.T) {
	s, _ := resolveSettings(fmConfigOf(fmIndentRule{"=="}, fmIndentRule{"::"}, fmWidthRule{100}, fmWidthRule{20}))
	if s.indentUnit != "::" {
		t.Errorf("indentUnit = %q, want last provider %q", s.indentUnit, "::")
	}
	if s.lineWidth != 20 {
		t.Errorf("lineWidth = %d, want last provider 20", s.lineWidth)
	}
}

func TestResolveSettingsFallsBackOnEmpty(t *testing.T) {
	s, _ := resolveSettings(fmConfigOf(fmIndentRule{""}, fmWidthRule{0}))
	if s.indentUnit != defaultIndent {
		t.Errorf("indentUnit = %q, want default %q", s.indentUnit, defaultIndent)
	}
	if s.lineWidth != defaultWidth {
		t.Errorf("lineWidth = %d, want default %d", s.lineWidth, defaultWidth)
	}
}

func TestResolveSettingsSkipsNilRule(t *testing.T) {
	s, _ := resolveSettings(fmConfigOf(nil, fmWidthRule{30}))
	if s.lineWidth != 30 {
		t.Errorf("lineWidth = %d, want 30 (nil rule skipped)", s.lineWidth)
	}
}

// fmRuleConfig is a Config that also implements RuleConfig, driving the
// per-rule enablement and raw-config paths.
type fmRuleConfig struct {
	rules   []FormatRule
	enabled map[string]bool
	raw     map[string][]byte
}

func (c fmRuleConfig) Rules() []FormatRule { return c.rules }
func (c fmRuleConfig) Enabled(name string) (enabled, configured bool) {
	if v, ok := c.enabled[name]; ok {
		return v, true
	}
	return false, false
}
func (c fmRuleConfig) Raw(name string) (raw []byte, configured bool) {
	if r, ok := c.raw[name]; ok {
		return r, true
	}
	return nil, false
}

// TestActiveRulesRespectsEnabled verifies an off-by-default rule (Defaults.
// Enabled=false) is filtered out unless the RuleConfig explicitly enables it.
func TestActiveRulesRespectsEnabled(t *testing.T) {
	offByDefault := fmBreakRule{} // Meta() has no Defaults.Enabled -> false
	base := fmRuleConfig{rules: []FormatRule{offByDefault}}

	// Not enabled -> inactive.
	got, err := activeRules(base)
	if err != nil {
		t.Fatalf("activeRules: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("activeRules = %d rules, want 0 when off by default", len(got))
	}

	// Explicitly enabled -> active.
	base.enabled = map[string]bool{"fm-break": true}
	got, err = activeRules(base)
	if err != nil {
		t.Fatalf("activeRules: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("activeRules = %d rules, want 1 when explicitly enabled", len(got))
	}
}

// TestActiveRulesAppliesConfig verifies a Configurable rule receives its raw
// config from the RuleConfig, and that an ApplyConfig error propagates.
func TestActiveRulesAppliesConfig(t *testing.T) {
	cr := &fmConfigurableRule{}
	cfg := fmRuleConfig{
		rules:   []FormatRule{cr},
		enabled: map[string]bool{fmConfigRuleName: true},
		raw:     map[string][]byte{fmConfigRuleName: []byte("payload")},
	}
	if _, err := activeRules(cfg); err != nil {
		t.Fatalf("activeRules: %v", err)
	}
	if got := string(cr.lastData); got != "payload" {
		t.Errorf("ApplyConfig data = %q, want %q", got, "payload")
	}

	cr.applyErr = errors.New("bad config")
	if _, err := activeRules(cfg); err == nil {
		t.Error("activeRules = nil error, want propagated ApplyConfig error")
	}
}

// ---- Configurable ---------------------------------------------------------

func TestConfigurableApplyConfig(t *testing.T) {
	r := &fmConfigurableRule{}
	data := []byte("payload")
	if err := r.ApplyConfig(data); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if !bytes.Equal(r.lastData, data) {
		t.Errorf("lastData = %q, want %q", r.lastData, data)
	}
}

func TestConfigurableApplyError(t *testing.T) {
	r := &fmConfigurableRule{applyErr: errors.New("bad config")}
	if err := r.ApplyConfig(nil); err == nil {
		t.Error("ApplyConfig = nil error, want propagated error")
	}
}
