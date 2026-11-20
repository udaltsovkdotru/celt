package celtls

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtlint"
)

const lsTestURI = protocol.DocumentURI("file:///test.cel")

// lsFormattedDoc is the canonical formatting of "1 + 2".
const lsFormattedDoc = "1 + 2\n"

// lsNewAnalyzer builds an Analyzer over an env whose standard library plus the
// given options are registered, mirroring how the celt tools configure the
// server.
func lsNewAnalyzer(t *testing.T, opts ...cel.EnvOption) *celtlint.Analyzer {
	t.Helper()
	env, err := cel.NewEnv(opts...)
	if err != nil {
		t.Fatalf("cel.NewEnv: %v", err)
	}
	an, err := celtlint.New(env)
	if err != nil {
		t.Fatalf("celtlint.New: %v", err)
	}
	return an
}

// lsReply captures the result and error a jsonrpc2.Replier is given.
type lsReply struct {
	result any
	err    error
}

// lsCapture returns a Replier that records what it is called with.
func lsCapture() (jsonrpc2.Replier, *lsReply) {
	got := new(lsReply)
	reply := func(_ context.Context, result any, err error) error {
		got.result = result
		got.err = err
		return nil
	}
	return reply, got
}

// lsRange builds a protocol.Range from start/end line/character values.
func lsRange(sl, sc, el, ec uint32) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: sl, Character: sc},
		End:   protocol.Position{Line: el, Character: ec},
	}
}

func TestDiagnosticSeverity(t *testing.T) {
	tests := []struct {
		name  string
		level celtlint.Level
		want  protocol.DiagnosticSeverity
	}{
		{name: "error", level: celtlint.LevelError, want: protocol.DiagnosticSeverityError},
		{name: "warn", level: celtlint.LevelWarn, want: protocol.DiagnosticSeverityWarning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnosticSeverity(&celtlint.Finding{Level: tt.level})
			if got != tt.want {
				t.Errorf("diagnosticSeverity(level=%d) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestServerHoverKnownURI(t *testing.T) {
	const content = "service"
	s := New(lsNewAnalyzer(t, cel.Variable("service", cel.StringType)))
	s.docs[string(lsTestURI)] = content

	params := protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Position:     protocol.Position{Line: 0, Character: 0},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.hover(context.Background(), reply, raw); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	hover, ok := got.result.(*protocol.Hover)
	if !ok || hover == nil {
		t.Fatalf("hover result = %#v, want *protocol.Hover", got.result)
	}
	if hover.Contents.Kind != protocol.Markdown {
		t.Errorf("hover kind = %v, want %v", hover.Contents.Kind, protocol.Markdown)
	}
	if !strings.Contains(hover.Contents.Value, "service") {
		t.Errorf("hover contents %q missing identifier", hover.Contents.Value)
	}
}

func TestServerHoverUnknownURI(t *testing.T) {
	s := New(lsNewAnalyzer(t))
	params := protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Position:     protocol.Position{Line: 0, Character: 0},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.hover(context.Background(), reply, raw); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	if got.result != nil {
		t.Errorf("hover result = %#v, want nil for unknown document", got.result)
	}
}

func TestServerCompletionKnownURI(t *testing.T) {
	const content = "service"
	s := New(lsNewAnalyzer(t, cel.Variable("service", cel.StringType)))
	s.docs[string(lsTestURI)] = content

	params := protocol.CompletionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Position:     protocol.Position{Line: 0, Character: uint32(len(content))},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.completion(context.Background(), reply, raw); err != nil {
		t.Fatalf("completion: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	items, ok := got.result.([]protocol.CompletionItem)
	if !ok {
		t.Fatalf("completion result = %#v, want []protocol.CompletionItem", got.result)
	}
	if len(items) == 0 {
		t.Fatal("completion returned no items")
	}
	found := false
	for _, it := range items {
		if it.Label == "" {
			t.Error("completion item has empty label")
		}
		if it.Label == "service" {
			found = true
		}
	}
	if !found {
		t.Errorf("completion items %v missing declared variable", items)
	}
}

func TestServerCompletionUnknownURI(t *testing.T) {
	s := New(lsNewAnalyzer(t))
	params := protocol.CompletionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Position:     protocol.Position{Line: 0, Character: 2},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.completion(context.Background(), reply, raw); err != nil {
		t.Fatalf("completion: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	items, ok := got.result.([]protocol.CompletionItem)
	if !ok {
		t.Fatalf("completion result = %#v, want []protocol.CompletionItem", got.result)
	}
	if len(items) != 0 {
		t.Errorf("completion items = %v, want empty for unknown document", items)
	}
}

func TestServerFormattingKnownURI(t *testing.T) {
	s := New(lsNewAnalyzer(t))
	s.docs[string(lsTestURI)] = "1 + 2"

	params := protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Options:      protocol.FormattingOptions{InsertSpaces: true, TabSize: 4},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.formatting(context.Background(), reply, raw); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	edits, ok := got.result.([]protocol.TextEdit)
	if !ok {
		t.Fatalf("formatting result = %#v, want []protocol.TextEdit", got.result)
	}
	if len(edits) != 1 {
		t.Fatalf("len(edits) = %d, want 1", len(edits))
	}
	if edits[0].NewText != lsFormattedDoc {
		t.Errorf("NewText = %q, want %q", edits[0].NewText, lsFormattedDoc)
	}
}

func TestServerFormattingNoop(t *testing.T) {
	s := New(lsNewAnalyzer(t))
	s.docs[string(lsTestURI)] = lsFormattedDoc

	params := protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Options:      protocol.FormattingOptions{},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.formatting(context.Background(), reply, raw); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	edits, ok := got.result.([]protocol.TextEdit)
	if !ok {
		t.Fatalf("formatting result = %#v, want []protocol.TextEdit", got.result)
	}
	if len(edits) != 0 {
		t.Errorf("len(edits) = %d, want 0 for already-formatted document", len(edits))
	}
}

func TestServerFormattingUnknownURI(t *testing.T) {
	s := New(lsNewAnalyzer(t))
	params := protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Options:      protocol.FormattingOptions{},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.formatting(context.Background(), reply, raw); err != nil {
		t.Fatalf("formatting: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	edits, ok := got.result.([]protocol.TextEdit)
	if !ok {
		t.Fatalf("formatting result = %#v, want []protocol.TextEdit", got.result)
	}
	if len(edits) != 0 {
		t.Errorf("len(edits) = %d, want 0 for unknown document", len(edits))
	}
}

func TestDocFormat(t *testing.T) {
	t.Run("unformattedProducesEdit", func(t *testing.T) {
		edit, err := DocFormat("1 + 2", protocol.FormattingOptions{}, nil)
		if err != nil {
			t.Fatalf("DocFormat: %v", err)
		}
		if edit == nil {
			t.Fatal("DocFormat returned nil edit for unformatted content")
		}
		if edit.NewText != lsFormattedDoc {
			t.Errorf("NewText = %q, want %q", edit.NewText, lsFormattedDoc)
		}
	})
	t.Run("alreadyFormattedReturnsNil", func(t *testing.T) {
		edit, err := DocFormat(lsFormattedDoc, protocol.FormattingOptions{}, nil)
		if err != nil {
			t.Fatalf("DocFormat: %v", err)
		}
		if edit != nil {
			t.Errorf("DocFormat returned edit %+v for already-formatted content", edit)
		}
	})
	t.Run("tabSizeClamped", func(t *testing.T) {
		// A malformed client sending a huge TabSize must not trigger an
		// unbounded allocation; the indent is clamped to a sane bound.
		edit, err := DocFormat("a + b", protocol.FormattingOptions{InsertSpaces: true, TabSize: 4000000000}, nil)
		if err != nil {
			t.Fatalf("DocFormat: %v", err)
		}
		if edit == nil {
			t.Fatal("DocFormat returned nil edit")
		}
		for _, line := range strings.Split(edit.NewText, "\n") {
			if indentLen := len(line) - len(strings.TrimLeft(line, " ")); indentLen > maxIndentTabSize {
				t.Errorf("line indent = %d, want <= %d", indentLen, maxIndentTabSize)
			}
		}
	})
}

func TestFullDocRange(t *testing.T) {
	t.Run("singleLine", func(t *testing.T) {
		rng := FullDocRange("1 + 2")
		if rng.Start != (protocol.Position{Line: 0, Character: 0}) {
			t.Errorf("Start = %+v, want {0 0}", rng.Start)
		}
		if rng.End.Line != 0 || rng.End.Character != 5 {
			t.Errorf("End = %+v, want line 0 char 5", rng.End)
		}
	})
	t.Run("trailingNewline", func(t *testing.T) {
		rng := FullDocRange(lsFormattedDoc)
		if rng.End.Line != 1 || rng.End.Character != 0 {
			t.Errorf("End = %+v, want line 1 char 0", rng.End)
		}
	})
}

// lsFakeCfg is a Config whose Rules() returns the given rules.
type lsFakeCfg struct {
	rules []celtfmt.FormatRule
}

func (c lsFakeCfg) Rules() []celtfmt.FormatRule { return c.rules }

// lsFakeRule is a minimal FormatRule used to check indentOverride ordering.
type lsFakeRule struct {
	celtfmt.FormatMeta
}

func (lsFakeRule) Rewrite(a *celast.AST) (*celast.AST, error) { return a, nil }

func TestIndentOverride(t *testing.T) {
	base := lsFakeCfg{rules: []celtfmt.FormatRule{lsFakeRule{}}}
	o := indentOverride{base: base, indent: "    "}
	rules := o.Rules()
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d, want base + indent rules", len(rules))
	}
	ir, ok := rules[len(rules)-1].(celtfmt.IndentProvider)
	if !ok {
		t.Fatalf("last rule = %T, want an IndentProvider", rules[len(rules)-1])
	}
	if ir.Indent() != "    " {
		t.Errorf("Indent() = %q, want 4 spaces", ir.Indent())
	}
}

func TestIndentOverrideNilBase(t *testing.T) {
	o := indentOverride{indent: "\t"}
	if got := len(o.Rules()); got != 1 {
		t.Errorf("len(Rules()) = %d, want 1 with nil base", got)
	}
}
