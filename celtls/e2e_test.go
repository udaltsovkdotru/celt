package celtls

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtlint/rules"
)

// e2e identifiers and LSP method names reused across the e2e scenarios.
const (
	e2eVarName   = "service"
	e2eFuncName  = "processData"
	e2eMacroName = "matches_any"

	e2eMethodInitialize = "initialize"
	e2eMethodDidOpen    = "textDocument/didOpen"
	e2eMethodHover      = "textDocument/hover"
	e2eMethodCompletion = "textDocument/completion"
	e2eMethodFormatting = "textDocument/formatting"
	e2eMethodCodeAction = "textDocument/codeAction"
	e2eMethodPublish    = "textDocument/publishDiagnostics"

	e2eSourceFormatKind = "source.format"
)

// e2e document URIs exercised through the LSP server.
const (
	e2eLifecycleURI = protocol.DocumentURI("file:///lifecycle.cel")
	e2eMacroURI     = protocol.DocumentURI("file:///macro.cel")
	e2eFuncURI      = protocol.DocumentURI("file:///function.cel")
	e2eFixURI       = protocol.DocumentURI("file:///fix.cel")
	e2eUnknownURI   = protocol.DocumentURI("file:///unknown.cel")
)

// e2eEnv builds a real cel.Env registering a variable, a global function and
// a signature-only macro (mirroring how the config package registers macros)
// so hover and completion have real symbols to resolve.
func e2eEnv(t *testing.T) *cel.Env {
	t.Helper()
	noop := func(eh parser.ExprHelper, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		if len(args) == 0 {
			return eh.NewCall(e2eMacroName), nil
		}
		return eh.NewCall(e2eMacroName, args...), nil
	}
	env, err := cel.NewEnv(
		cel.EnableMacroCallTracking(),
		cel.Variable(e2eVarName, cel.StringType),
		cel.Function(e2eFuncName,
			cel.Overload("processData_str", []*cel.Type{cel.StringType}, cel.BoolType)),
		cel.Macros(cel.GlobalVarArgMacro(e2eMacroName, noop)),
		cel.Function(e2eMacroName,
			cel.Overload("matches_any_1", []*cel.Type{cel.DynType}, cel.BoolType),
			cel.Overload("matches_any_2", []*cel.Type{cel.DynType, cel.DynType}, cel.BoolType)),
	)
	if err != nil {
		t.Fatalf("e2e: cel.NewEnv: %v", err)
	}
	return env
}

// e2eAnalyzer wires the misspell rule so fixable findings (unknown identifiers
// with a close match) are produced, which the quick-fix action turns into code
// actions.
func e2eAnalyzer(t *testing.T) *celtlint.Analyzer {
	t.Helper()
	an, err := celtlint.New(e2eEnv(t), celtlint.WithRules(rules.Misspell{RuleMeta: celtlint.RuleMeta{
		Name:     "misspell",
		Desc:     "flag unknown identifiers and suggest corrections",
		Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}))
	if err != nil {
		t.Fatalf("e2e: celtlint.New: %v", err)
	}
	return an
}

// e2eEmptyCfg is a celtfmt.Config contributing no rules, so the format action
// and the printer fall back to their defaults.
type e2eEmptyCfg struct{}

func (e2eEmptyCfg) Rules() []celtfmt.FormatRule { return nil }

// e2eQuickFix and e2eFormat replicate the built-in celtls/actions behaviors
// without importing that package (which imports celtls and would create an
// import cycle from an in-package test).
type e2eQuickFix struct {
	ActionMeta
}

func (e2eQuickFix) Apply(ctx ActionContext) []protocol.CodeAction {
	var out []protocol.CodeAction
	for i := range ctx.Findings {
		f := &ctx.Findings[i]
		if !f.IsFix() {
			continue
		}
		fRange := FindingRange(ctx.Content, f)
		if !RangesOverlap(ctx.Range, fRange) {
			continue
		}
		out = append(out, protocol.CodeAction{
			Title:       celtlint.UseInstead(f.HintCallee()),
			Kind:        protocol.QuickFix,
			IsPreferred: true,
			Edit: &protocol.WorkspaceEdit{
				Changes: map[protocol.DocumentURI][]protocol.TextEdit{
					ctx.URI: {{Range: fRange, NewText: f.FixText()}},
				},
			},
		})
	}
	return out
}

type e2eFormat struct {
	ActionMeta
	cfg celtfmt.Config
}

func (a e2eFormat) Apply(ctx ActionContext) []protocol.CodeAction {
	edit, err := DocFormat(ctx.Content, protocol.FormattingOptions{}, a.cfg)
	if err != nil || edit == nil {
		return nil
	}
	return []protocol.CodeAction{{
		Title: "Format document",
		Kind:  e2eSourceFormatKind,
		Edit: &protocol.WorkspaceEdit{
			Changes: map[protocol.DocumentURI][]protocol.TextEdit{
				ctx.URI: {*edit},
			},
		},
	}}
}

// e2eConn is an in-memory io.ReadWriteCloser serving as the jsonrpc2
// transport. The server writes framed notifications into out; Read is never
// exercised because the connection is not started with Go().
type e2eConn struct {
	mu  sync.Mutex
	out *bytes.Buffer
}

func (*e2eConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *e2eConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Write(p)
}

func (*e2eConn) Close() error { return nil }

// e2eMsgReader is a read-only io.ReadWriteCloser over a byte slice, letting a
// captured notification buffer be re-read through a jsonrpc2.Stream.
type e2eMsgReader struct {
	*bytes.Reader
}

func (e2eMsgReader) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (e2eMsgReader) Close() error              { return nil }

// e2eNewServer builds a Server over a real Analyzer with quick-fix and format
// code actions, backed by an in-memory jsonrpc2 transport so didOpen can
// publish diagnostics.
func e2eNewServer(t *testing.T) (*Server, *e2eConn) {
	t.Helper()
	s := New(e2eAnalyzer(t), WithActions(
		e2eQuickFix{ActionMeta: ActionMeta{Name: "quick-fix", Desc: "apply finding fixes"}},
		e2eFormat{ActionMeta: ActionMeta{Name: "format", Desc: "format the document"}, cfg: e2eEmptyCfg{}},
	))
	conn := &e2eConn{out: &bytes.Buffer{}}
	s.conn = jsonrpc2.NewConn(jsonrpc2.NewStream(conn))
	return s, conn
}

// e2eResult captures what the jsonrpc2.Replier is called with.
type e2eResult struct {
	result any
	err    error
}

// e2eDispatch drives a single request through the server's handle dispatch
// path (the same one a live connection uses) and returns the captured reply.
func e2eDispatch(t *testing.T, s *Server, method string, params any) e2eResult {
	t.Helper()
	req, err := jsonrpc2.NewCall(jsonrpc2.NewNumberID(1), method, params)
	if err != nil {
		t.Fatalf("e2e: NewCall %s: %v", method, err)
	}
	var got e2eResult
	reply := func(_ context.Context, result any, err error) error {
		got.result = result
		got.err = err
		return nil
	}
	if err := s.handle(context.Background(), reply, req); err != nil {
		t.Fatalf("e2e: handle %s: %v", method, err)
	}
	if got.err != nil {
		t.Fatalf("e2e: %s reply err: %v", method, got.err)
	}
	return got
}

// e2eOpen publishes a document through the real didOpen handler.
func e2eOpen(t *testing.T, s *Server, docURI protocol.DocumentURI, content string) {
	t.Helper()
	e2eDispatch(t, s, e2eMethodDidOpen, protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: docURI, Text: content},
	})
}

// e2eDecodeNotifs reads every notification the server wrote to the transport.
func e2eDecodeNotifs(t *testing.T, c *e2eConn) []*jsonrpc2.Notification {
	t.Helper()
	c.mu.Lock()
	data := make([]byte, c.out.Len())
	copy(data, c.out.Bytes())
	c.mu.Unlock()

	stream := jsonrpc2.NewStream(&e2eMsgReader{Reader: bytes.NewReader(data)})
	var notifs []*jsonrpc2.Notification
	for {
		msg, _, err := stream.Read(context.Background())
		if err != nil {
			break
		}
		if n, ok := msg.(*jsonrpc2.Notification); ok {
			notifs = append(notifs, n)
		}
	}
	return notifs
}

// e2ePublishedFor reports whether the transport carried a
// publishDiagnostics notification for the given URI.
func e2ePublishedFor(t *testing.T, c *e2eConn, docURI protocol.DocumentURI) bool {
	t.Helper()
	for _, n := range e2eDecodeNotifs(t, c) {
		if n.Method() != e2eMethodPublish {
			continue
		}
		var pp protocol.PublishDiagnosticsParams
		if err := json.Unmarshal(n.Params(), &pp); err != nil {
			continue
		}
		if pp.URI == docURI {
			return true
		}
	}
	return false
}

// e2ePublishedNonEmpty reports whether a publishDiagnostics notification for
// the given URI carried at least one diagnostic.
func e2ePublishedNonEmpty(t *testing.T, c *e2eConn, docURI protocol.DocumentURI) bool {
	t.Helper()
	for _, n := range e2eDecodeNotifs(t, c) {
		if n.Method() != e2eMethodPublish {
			continue
		}
		var pp protocol.PublishDiagnosticsParams
		if err := json.Unmarshal(n.Params(), &pp); err != nil {
			continue
		}
		if pp.URI == docURI && len(pp.Diagnostics) > 0 {
			return true
		}
	}
	return false
}

// TestE2EInitialize asserts the initialize handshake advertises the server
// capabilities.
func TestE2EInitialize(t *testing.T) {
	s, _ := e2eNewServer(t)

	initRes := e2eDispatch(t, s, e2eMethodInitialize, protocol.InitializeParams{})
	init, ok := initRes.result.(*protocol.InitializeResult)
	if !ok || init == nil {
		t.Fatalf("e2e: initialize result = %#v, want *protocol.InitializeResult", initRes.result)
	}
	if hv, _ := init.Capabilities.HoverProvider.(bool); !hv {
		t.Error("e2e: HoverProvider not advertised")
	}
	if ca, _ := init.Capabilities.CodeActionProvider.(bool); !ca {
		t.Error("e2e: CodeActionProvider not advertised")
	}
	if df, _ := init.Capabilities.DocumentFormattingProvider.(bool); !df {
		t.Error("e2e: DocumentFormattingProvider not advertised")
	}
	if init.Capabilities.CompletionProvider == nil {
		t.Error("e2e: CompletionProvider not advertised")
	}
	if ts, _ := init.Capabilities.TextDocumentSync.(protocol.TextDocumentSyncKind); ts != protocol.TextDocumentSyncKindFull {
		t.Errorf("e2e: TextDocumentSync = %v, want %v", ts, protocol.TextDocumentSyncKindFull)
	}
	if init.ServerInfo == nil || init.ServerInfo.Name == "" {
		t.Errorf("e2e: ServerInfo = %+v, want non-empty name", init.ServerInfo)
	}
}

// TestE2EOpenPublishesDiagnostics asserts opening a document emits a
// textDocument/publishDiagnostics notification over the transport.
func TestE2EOpenPublishesDiagnostics(t *testing.T) {
	s, conn := e2eNewServer(t)
	e2eOpen(t, s, e2eLifecycleURI, e2eVarName)
	if !e2ePublishedFor(t, conn, e2eLifecycleURI) {
		t.Errorf("e2e: no publishDiagnostics after opening %q", e2eLifecycleURI)
	}
}

// TestE2EHoverIdentifier drives open -> hover on a declared identifier.
func TestE2EHoverIdentifier(t *testing.T) {
	s, _ := e2eNewServer(t)
	e2eOpen(t, s, e2eLifecycleURI, e2eVarName)

	hv := e2eDispatch(t, s, e2eMethodHover, protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eLifecycleURI},
		Position:     protocol.Position{Line: 0, Character: 0},
	})
	hover, ok := hv.result.(*protocol.Hover)
	if !ok || hover == nil {
		t.Fatalf("e2e: hover result = %#v, want *protocol.Hover", hv.result)
	}
	if hover.Contents.Kind != protocol.Markdown {
		t.Errorf("e2e: hover kind = %v, want %v", hover.Contents.Kind, protocol.Markdown)
	}
	if !strings.Contains(hover.Contents.Value, e2eVarName) {
		t.Errorf("e2e: hover %q missing identifier %q", hover.Contents.Value, e2eVarName)
	}
}

// TestE2EHoverMacro drives open -> hover on a declared macro invocation.
func TestE2EHoverMacro(t *testing.T) {
	s, _ := e2eNewServer(t)
	e2eOpen(t, s, e2eMacroURI, `matches_any("a")`)

	hv := e2eDispatch(t, s, e2eMethodHover, protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eMacroURI},
		Position:     protocol.Position{Line: 0, Character: 2},
	})
	hover, ok := hv.result.(*protocol.Hover)
	if !ok || hover == nil {
		t.Fatalf("e2e: macro hover result = %#v, want *protocol.Hover", hv.result)
	}
	if !strings.Contains(hover.Contents.Value, e2eMacroName) {
		t.Errorf("e2e: macro hover %q missing %q", hover.Contents.Value, e2eMacroName)
	}
}

// TestE2EHoverFunction drives open -> hover on a declared function.
func TestE2EHoverFunction(t *testing.T) {
	s, _ := e2eNewServer(t)
	e2eOpen(t, s, e2eFuncURI, `processData("x")`)

	hv := e2eDispatch(t, s, e2eMethodHover, protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eFuncURI},
		Position:     protocol.Position{Line: 0, Character: 0},
	})
	hover, ok := hv.result.(*protocol.Hover)
	if !ok || hover == nil {
		t.Fatalf("e2e: function hover result = %#v, want *protocol.Hover", hv.result)
	}
	if !strings.Contains(hover.Contents.Value, e2eFuncName) {
		t.Errorf("e2e: function hover %q missing %q", hover.Contents.Value, e2eFuncName)
	}
}

// TestE2ECompletion drives open -> completion for a declared variable.
func TestE2ECompletion(t *testing.T) {
	s, _ := e2eNewServer(t)
	e2eOpen(t, s, e2eLifecycleURI, e2eVarName)

	cp := e2eDispatch(t, s, e2eMethodCompletion, protocol.CompletionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eLifecycleURI},
		Position:     protocol.Position{Line: 0, Character: 2},
	})
	items, ok := cp.result.([]protocol.CompletionItem)
	if !ok {
		t.Fatalf("e2e: completion result = %#v, want []protocol.CompletionItem", cp.result)
	}
	found := false
	for _, it := range items {
		if it.Label == e2eVarName && it.Kind == protocol.CompletionItemKindVariable {
			found = true
		}
	}
	if !found {
		t.Errorf("e2e: completion items %v missing %q variable", items, e2eVarName)
	}
}

// TestE2EFormatting drives open -> formatting and asserts the returned TextEdit
// reformats the document.
func TestE2EFormatting(t *testing.T) {
	s, _ := e2eNewServer(t)
	e2eOpen(t, s, e2eLifecycleURI, e2eVarName)

	fm := e2eDispatch(t, s, e2eMethodFormatting, protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eLifecycleURI},
		Options:      protocol.FormattingOptions{InsertSpaces: true, TabSize: 4},
	})
	edits, ok := fm.result.([]protocol.TextEdit)
	if !ok {
		t.Fatalf("e2e: formatting result = %#v, want []protocol.TextEdit", fm.result)
	}
	if len(edits) != 1 {
		t.Fatalf("e2e: formatting edits = %d, want 1", len(edits))
	}
	if edits[0].NewText == e2eVarName || !strings.HasSuffix(edits[0].NewText, "\n") {
		t.Errorf("e2e: format edit %q did not reformat the document", edits[0].NewText)
	}
}

// TestE2ECodeActions opens a document with a fixable finding and asserts the
// server publishes diagnostics and returns a quick-fix plus a source.format
// code action.
func TestE2ECodeActions(t *testing.T) {
	s, conn := e2eNewServer(t)

	const fixDoc = "servic"
	e2eOpen(t, s, e2eFixURI, fixDoc)
	if !e2ePublishedNonEmpty(t, conn, e2eFixURI) {
		t.Errorf("e2e: expected non-empty diagnostics for %q", e2eFixURI)
	}

	ca := e2eDispatch(t, s, e2eMethodCodeAction, protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eFixURI},
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 0},
			End:   protocol.Position{Line: 0, Character: uint32(len(fixDoc))},
		},
	})
	acts, ok := ca.result.([]protocol.CodeAction)
	if !ok {
		t.Fatalf("e2e: codeAction result = %#v, want []protocol.CodeAction", ca.result)
	}
	if len(acts) == 0 {
		t.Fatal("e2e: codeAction returned no actions")
	}

	quickFix := false
	formatAction := false
	for _, a := range acts {
		if a.Title == "Format document" && a.Kind == e2eSourceFormatKind {
			formatAction = true
		}
		if a.Kind != protocol.QuickFix || a.Edit == nil {
			continue
		}
		for _, edits := range a.Edit.Changes {
			for _, e := range edits {
				if e.NewText == e2eVarName {
					quickFix = true
				}
			}
		}
	}
	if !quickFix {
		t.Errorf("e2e: expected quick-fix replacing %q, got %+v", e2eVarName, acts)
	}
	if !formatAction {
		t.Errorf("e2e: expected a source.format action, got %+v", acts)
	}
}

// TestE2EUnknownDocument asserts every read handler degrades to nil/empty for
// an unregistered document without panicking.
func TestE2EUnknownDocument(t *testing.T) {
	s, _ := e2eNewServer(t)

	hv := e2eDispatch(t, s, e2eMethodHover, protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eUnknownURI},
		Position:     protocol.Position{Line: 0, Character: 0},
	})
	if hv.result != nil {
		t.Errorf("e2e: hover for unknown doc = %#v, want nil", hv.result)
	}

	cp := e2eDispatch(t, s, e2eMethodCompletion, protocol.CompletionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eUnknownURI},
		Position:     protocol.Position{Line: 0, Character: 2},
	})
	if items, ok := cp.result.([]protocol.CompletionItem); !ok || len(items) != 0 {
		t.Errorf("e2e: completion for unknown doc = %#v, want empty", cp.result)
	}

	fm := e2eDispatch(t, s, e2eMethodFormatting, protocol.DocumentFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eUnknownURI},
		Options:      protocol.FormattingOptions{},
	})
	if edits, ok := fm.result.([]protocol.TextEdit); !ok || len(edits) != 0 {
		t.Errorf("e2e: formatting for unknown doc = %#v, want empty", fm.result)
	}

	ca := e2eDispatch(t, s, e2eMethodCodeAction, protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: e2eUnknownURI},
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 0},
			End:   protocol.Position{Line: 0, Character: 1},
		},
	})
	if acts, ok := ca.result.([]protocol.CodeAction); !ok || len(acts) != 0 {
		t.Errorf("e2e: codeAction for unknown doc = %#v, want empty", ca.result)
	}
}
