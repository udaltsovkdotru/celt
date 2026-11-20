package rules

import (
	"testing"

	"cel.dev/cel-go/cel"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtlint"
)

const (
	opRuleName = "optional-fields"
	opPath     = "test.cel"
)

// opDescriptorSet builds a descriptor set with a proto3 test.Person (a proto3
// `optional` nickname string, a plain singular int32 age, and an address of
// message type test.Address), a proto3 test.Address (a proto3 `optional` city
// string and a plain singular int32 zip), and a proto2 legacy.Legacy carrying
// an explicit-presence (optional) string note. This exercises every presence
// kind the optional-fields rule understands.
func opDescriptorSet() *descriptorpb.FileDescriptorSet {
	return &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			{
				Name:    proto.String("test.proto"),
				Package: proto.String("test"),
				Syntax:  proto.String("proto3"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Person"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:           proto.String("nickname"),
								Number:         proto.Int32(1),
								Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								Proto3Optional: proto.Bool(true),
							},
							{
								Name:   proto.String("age"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
							{
								Name:     proto.String("address"),
								Number:   proto.Int32(3),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
								TypeName: proto.String(".test.Address"),
							},
						},
					},
					{
						Name: proto.String("Address"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:           proto.String("city"),
								Number:         proto.Int32(1),
								Label:          descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:           descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								Proto3Optional: proto.Bool(true),
							},
							{
								Name:   proto.String("zip"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
						},
					},
				},
			},
			{
				Name:    proto.String("legacy.proto"),
				Package: proto.String("legacy"),
				Syntax:  proto.String("proto2"),
				MessageType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("Legacy"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:   proto.String("note"),
								Number: proto.Int32(1),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
							},
							{
								Name:   proto.String("count"),
								Number: proto.Int32(2),
								Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:   descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
							},
						},
					},
				},
			},
		},
	}
}

// opEnv builds a CEL environment registering the descriptor set and declaring
// Person-, Person-, and Legacy-typed variables.
func opEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.TypeDescs(opDescriptorSet()),
		cel.Variable("p", cel.ObjectType("test.Person")),
		cel.Variable("q", cel.ObjectType("test.Person")),
		cel.Variable("l", cel.ObjectType("legacy.Legacy")),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env
}

// opOptionalRule drives optionalFindings through the Rule framework with a
// caller-supplied presence config.
type opOptionalRule struct {
	celtlint.RuleMeta
	cfg OptionalConfig
}

func (r *opOptionalRule) Check(ctx *celtlint.Context) {
	ctx.ReportAll(optionalFindings(ctx, r.cfg))
}

// opAnalyze runs the optional-fields rule over content and returns the
// findings it emits.
func opAnalyze(t *testing.T, cfg OptionalConfig, content string) []celtlint.Finding {
	t.Helper()
	an, err := celtlint.New(
		opEnv(t),
		celtlint.WithDescriptor(opDescriptorSet()),
		celtlint.WithRules(&opOptionalRule{
			RuleMeta: celtlint.RuleMeta{Name: opRuleName, Defaults: celtlint.RuleParams{Level: celtlint.LevelWarn}},
			cfg:      cfg,
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return an.Analyze(opPath, content)
}

// opCountByCode returns the number of findings with the given code.
func opCountByCode(fs []celtlint.Finding, code string) int {
	n := 0
	for i := range fs {
		if fs[i].Code == code {
			n++
		}
	}
	return n
}

// opFindByCode returns the first finding with the given code.
func opFindByCode(fs []celtlint.Finding, code string) (celtlint.Finding, bool) {
	for i := range fs {
		if fs[i].Code == code {
			return fs[i], true
		}
	}
	return celtlint.Finding{}, false
}

// TestOptionalMissingHasCheck drives the rule through New+Analyze and asserts
// each enabled presence kind flags an unguarded access with the correct hint.
func TestOptionalMissingHasCheck(t *testing.T) {
	cases := []struct {
		name    string
		cfg     OptionalConfig
		content string
		hint    string
	}{
		{
			name:    "proto3 optional unguarded",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.nickname == "bob"`,
			hint:    "guard access with has(p.nickname)",
		},
		{
			name:    "proto3 value singular",
			cfg:     OptionalConfig{Proto3Value: true},
			content: `p.age == 1`,
			hint:    "guard access with has(p.age)",
		},
		{
			name:    "proto2 explicit presence",
			cfg:     OptionalConfig{Proto2Existence: true},
			content: `l.note == "x"`,
			hint:    "guard access with has(l.note)",
		},
		{
			name:    "chained optional receiver",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.address.city == "nyc"`,
			hint:    "guard access with has(p.address.city)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := opAnalyze(t, tc.cfg, tc.content)
			f, ok := opFindByCode(fs, celtlint.CodeMissingHasCheck)
			if !ok {
				t.Fatalf("no missing-has-check finding in %+v", fs)
			}
			if f.Level != celtlint.LevelWarn {
				t.Errorf("Level = %v, want warn", f.Level)
			}
			if f.Message != "accessing an optional field without a has() check" {
				t.Errorf("Message = %q, want %q", f.Message, "accessing an optional field without a has() check")
			}
			if f.Hint != tc.hint {
				t.Errorf("Hint = %q, want %q", f.Hint, tc.hint)
			}
		})
	}
}

// TestOptionalNoFalsePositive asserts guarded accesses, non-optional fields,
// and a fully-disabled config produce no findings.
func TestOptionalNoFalsePositive(t *testing.T) {
	cases := []struct {
		name    string
		cfg     OptionalConfig
		content string
	}{
		{
			name:    "has guard on same field",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `has(p.nickname) && p.nickname == "bob"`,
		},
		{
			name:    "non optional field under proto3-optional config",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `p.age == 1`,
		},
		{
			name:    "has guard on chained optional field",
			cfg:     OptionalConfig{Proto3Optional: true},
			content: `has(p.address.city) && p.address.city == "nyc"`,
		},
		{
			name:    "empty config disables rule",
			cfg:     OptionalConfig{},
			content: `p.nickname == "bob"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := opAnalyze(t, tc.cfg, tc.content)
			if n := opCountByCode(fs, celtlint.CodeMissingHasCheck); n != 0 {
				t.Errorf("got %d missing-has-check findings, want 0: %+v", n, fs)
			}
		})
	}
}

// TestOptionalGuardMustBeOnField asserts that guarding a different optional
// field (or only a non-optional parent) does not suppress the finding.
func TestOptionalGuardMustBeOnField(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{
			name:    "guarding another optional field is not enough",
			content: `has(p.address.city) && p.nickname == "bob"`,
			want:    1,
		},
		{
			name:    "guarding a non-optional parent is not enough",
			content: `has(p.address) && p.address.city == "nyc"`,
			want:    1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := OptionalConfig{Proto3Optional: true}
			fs := opAnalyze(t, cfg, tc.content)
			if n := opCountByCode(fs, celtlint.CodeMissingHasCheck); n != tc.want {
				t.Errorf("got %d missing-has-check findings, want %d: %+v", n, tc.want, fs)
			}
		})
	}
}

// TestOptionalMultipleFindings asserts one finding per unguarded optional
// field access in the same expression.
func TestOptionalMultipleFindings(t *testing.T) {
	fs := opAnalyze(t, OptionalConfig{Proto3Optional: true},
		`p.nickname == "bob" || p.address.city == "nyc"`)
	if n := opCountByCode(fs, celtlint.CodeMissingHasCheck); n != 2 {
		t.Errorf("got %d missing-has-check findings, want 2: %+v", n, fs)
	}
}
