package rules

import (
	"github.com/udaltsovkdotru/celt/celtlint"
)

// All returns the framework's built-in lint rules, to be registered on an
// analyzer via celtlint.WithRules.
func All() []celtlint.Rule {
	return []celtlint.Rule{
		&Optional{RuleMeta: celtlint.RuleMeta{
			Name:     "optional-fields",
			Desc:     "require has() guards around optional field access",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn, Config: []byte("proto3-optional: true\n")},
		}},
		Misspell{RuleMeta: celtlint.RuleMeta{
			Name:     "misspell",
			Desc:     "flag unknown identifiers and suggest corrections",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
		}},
		Compile{RuleMeta: celtlint.RuleMeta{
			Name:     "compile",
			Desc:     "report CEL compiler errors",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
		}},
		RawEnumValue{RuleMeta: celtlint.RuleMeta{
			Name:     "raw-enum-value",
			Desc:     "flag raw enum literals; prefer named values",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
		}},
		EnumComparison{RuleMeta: celtlint.RuleMeta{
			Name:     "enum-comparison",
			Desc:     "flag suspicious comparisons between enum values",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn},
		}},
		EnumTypeMismatch{RuleMeta: celtlint.RuleMeta{
			Name:     "enum-type-mismatch",
			Desc:     "flag enum comparisons of mismatched types",
			Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
		}},
	}
}
