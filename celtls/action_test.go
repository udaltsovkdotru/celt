package celtls

import (
	"context"
	"encoding/json"
	"testing"

	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtlint"
)

func TestFindingRange(t *testing.T) {
	f := &celtlint.Finding{
		Start:    celtlint.Position{Line: 2, Char: 4},
		End:      celtlint.Position{Line: 2, Char: 9},
		FixStart: celtlint.Position{Line: 0, Char: 1},
		FixEnd:   celtlint.Position{Line: 0, Char: 3},
	}
	rng := FindingRange("abcd", f)
	if rng.Start.Line != 0 || rng.Start.Character != 1 {
		t.Errorf("Start = %+v, want {0 1}", rng.Start)
	}
	if rng.End.Line != 0 || rng.End.Character != 3 {
		t.Errorf("End = %+v, want {0 3}", rng.End)
	}
}

func TestFindingRangeFallsBackToFindingSpan(t *testing.T) {
	f := &celtlint.Finding{
		Start: celtlint.Position{Line: 0, Char: 2},
		End:   celtlint.Position{Line: 0, Char: 5},
	}
	rng := FindingRange("abcdef", f)
	if rng.Start.Character != 2 || rng.End.Character != 5 {
		t.Errorf("range = %+v, want chars 2..5", rng)
	}
}

func TestRangesOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b protocol.Range
		want bool
	}{
		{name: "overlapping", a: lsRange(0, 0, 0, 5), b: lsRange(0, 3, 0, 8), want: true},
		{name: "disjoint", a: lsRange(0, 0, 0, 2), b: lsRange(0, 5, 0, 8), want: false},
		{name: "touchingBoundary", a: lsRange(0, 0, 0, 2), b: lsRange(0, 2, 0, 5), want: true},
		{name: "samePoint", a: lsRange(1, 2, 1, 2), b: lsRange(1, 2, 1, 2), want: true},
		{name: "bBeforeA", a: lsRange(2, 0, 2, 3), b: lsRange(1, 0, 1, 5), want: false},
		{name: "crossLine", a: lsRange(0, 8, 1, 3), b: lsRange(1, 0, 2, 0), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RangesOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("RangesOverlap(%+v, %+v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// lsStubAction is a pluggable Action whose Apply behavior is supplied by the
// test.
type lsStubAction struct {
	ActionMeta
	fn func(ctx ActionContext) []protocol.CodeAction
}

// lsStubActionMeta is the shared metadata for the stub actions used across
// code action tests.
var lsStubActionMeta = ActionMeta{Name: "stub", Desc: "stub action"}

func (a lsStubAction) Apply(ctx ActionContext) []protocol.CodeAction {
	if a.fn == nil {
		return nil
	}
	return a.fn(ctx)
}

func TestServerCodeAction(t *testing.T) {
	const content = "service"
	want := []protocol.CodeAction{
		{Title: "action-1", Kind: protocol.QuickFix},
		{Title: "action-2", Kind: "source.format"},
	}
	var gotCtx ActionContext
	stub := lsStubAction{
		ActionMeta: lsStubActionMeta,
		fn: func(ctx ActionContext) []protocol.CodeAction {
			gotCtx = ctx
			return want
		},
	}
	s := New(lsNewAnalyzer(t), WithActions(stub))
	s.docs[string(lsTestURI)] = content

	params := protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Range:        lsRange(0, 0, 0, 1),
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.codeAction(context.Background(), reply, raw); err != nil {
		t.Fatalf("codeAction: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	actions, ok := got.result.([]protocol.CodeAction)
	if !ok {
		t.Fatalf("codeAction result = %#v, want []protocol.CodeAction", got.result)
	}
	if len(actions) != 2 {
		t.Fatalf("len(actions) = %d, want 2", len(actions))
	}
	for i, a := range actions {
		if a.Title != want[i].Title {
			t.Errorf("actions[%d].Title = %q, want %q", i, a.Title, want[i].Title)
		}
	}
	if gotCtx.URI != lsTestURI {
		t.Errorf("ActionContext.URI = %q, want %q", gotCtx.URI, lsTestURI)
	}
	if gotCtx.Content != content {
		t.Errorf("ActionContext.Content = %q, want %q", gotCtx.Content, content)
	}
	if gotCtx.Range != params.Range {
		t.Errorf("ActionContext.Range = %+v, want %+v", gotCtx.Range, params.Range)
	}
}

func TestServerCodeActionUnknownURI(t *testing.T) {
	stub := lsStubAction{
		ActionMeta: lsStubActionMeta,
		fn: func(ActionContext) []protocol.CodeAction {
			return []protocol.CodeAction{{Title: "should-not-appear"}}
		},
	}
	s := New(lsNewAnalyzer(t), WithActions(stub))
	params := protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Range:        lsRange(0, 0, 0, 1),
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.codeAction(context.Background(), reply, raw); err != nil {
		t.Fatalf("codeAction: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	actions, ok := got.result.([]protocol.CodeAction)
	if !ok {
		t.Fatalf("codeAction result = %#v, want []protocol.CodeAction", got.result)
	}
	if len(actions) != 0 {
		t.Errorf("len(actions) = %d, want 0 for unknown document", len(actions))
	}
}

func TestServerCodeActionSkipsNilAction(t *testing.T) {
	stub := lsStubAction{
		ActionMeta: lsStubActionMeta,
		fn: func(ActionContext) []protocol.CodeAction {
			return []protocol.CodeAction{{Title: "one"}}
		},
	}
	s := New(lsNewAnalyzer(t), WithActions(stub, nil))
	s.docs[string(lsTestURI)] = "service"
	params := protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: lsTestURI},
		Range:        lsRange(0, 0, 0, 1),
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	reply, got := lsCapture()
	if err := s.codeAction(context.Background(), reply, raw); err != nil {
		t.Fatalf("codeAction: %v", err)
	}
	if got.err != nil {
		t.Fatalf("reply err: %v", got.err)
	}
	actions, ok := got.result.([]protocol.CodeAction)
	if !ok {
		t.Fatalf("codeAction result = %#v, want []protocol.CodeAction", got.result)
	}
	if len(actions) != 1 {
		t.Errorf("len(actions) = %d, want 1 (nil action skipped)", len(actions))
	}
}
