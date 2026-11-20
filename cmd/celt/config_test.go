package main

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/udaltsovkdotru/celt/celtlint"
)

// mustRuleNode parses a single-line-or-mapping YAML snippet into the yaml.Node
// that parseLintRule expects as the rule's value node.
func mustRuleNode(t *testing.T, text string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(text), &n); err != nil {
		t.Fatalf("yaml unmarshal %q: %v", text, err)
	}
	return n.Content[0]
}

// TestParseLintRule guards the parseLintRule contract: the reserved "level"
// key must not leak into the rule's raw custom config, so that configuring
// only a severity keeps the rule's declared defaults (regression for a bug
// where `optional-fields: {level: error}` silently wiped the defaults).
func TestParseLintRule(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantLvl   celtlint.Level
		wantLevel bool
		wantRaw   bool
	}{
		{name: "shorthand severity", yaml: "error", wantLvl: celtlint.LevelError, wantLevel: true, wantRaw: false},
		{name: "mapping with only level", yaml: "level: error", wantLvl: celtlint.LevelError, wantLevel: true, wantRaw: false},
		{name: "empty mapping", yaml: "{}", wantLvl: 0, wantLevel: false, wantRaw: false},
		{name: "level plus custom config", yaml: "level: warn\nproto3-value: true", wantLvl: celtlint.LevelWarn, wantLevel: true, wantRaw: true},
		{name: "custom config only", yaml: "proto3-value: true", wantLvl: 0, wantLevel: false, wantRaw: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &config{lint: map[string]lintRuleCfg{}}
			if err := c.parseLintRule("optional-fields", mustRuleNode(t, tt.yaml)); err != nil {
				t.Fatalf("parseLintRule: %v", err)
			}
			rc := c.lint["optional-fields"]
			if got, want := rc.hasLevel, tt.wantLevel; got != want {
				t.Errorf("hasLevel = %v, want %v", got, want)
			} else if tt.wantLevel && rc.level != tt.wantLvl {
				t.Errorf("level = %v, want %v", rc.level, tt.wantLvl)
			}
			if gotRaw := rc.raw != nil; gotRaw != tt.wantRaw {
				t.Errorf("raw present = %v, want %v (raw=%q)", gotRaw, tt.wantRaw, rc.raw)
			}
			// The ConfigProvider must report "unconfigured" when level was
			// not explicitly set, so the analyzer keeps the rule's defaults.
			if _, ok := c.Level("optional-fields"); ok != tt.wantLevel {
				t.Errorf("Level() reported = %v, want %v", ok, tt.wantLevel)
			}
		})
	}
}
