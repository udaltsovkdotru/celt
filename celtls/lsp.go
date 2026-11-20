package celtls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtfmt/rules"
	"github.com/udaltsovkdotru/celt/celtsource"

	"github.com/udaltsovkdotru/celt/celtlint"
)

const (
	defaultServerName = "celt"
	// maxIndentTabSize caps a client-supplied indent width so a malformed LSP
	// client cannot force a huge allocation.
	maxIndentTabSize = 64
)

type stdioConn struct {
	in  *os.File
	out *os.File
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.in.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.out.Write(p) }
func (c *stdioConn) Close() error                { return c.in.Close() }

type handler func(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error

type Option func(*Server)

func WithFormatConfig(cfg celtfmt.Config) Option {
	return func(s *Server) { s.fmtCfg = cfg }
}

// WithActions registers code action rules. The built-in actions live in the
// celtls/actions subpackage and are wired in by the caller.
func WithActions(actions ...Action) Option {
	return func(s *Server) { s.actions = append(s.actions, actions...) }
}

type Server struct {
	analyzer Analyzer
	conn     jsonrpc2.Conn
	docs     map[string]string
	fmtCfg   celtfmt.Config
	name     string
	shutdown bool
	extra    map[string]handler
	actions  []Action
}

// Analyzer is the analysis backend the LSP server drives for diagnostics,
// hover and completion. Defining it as an interface decouples the transport
// layer from a specific engine and keeps the server testable with a fake.
type Analyzer interface {
	Analyze(path, content string) []celtlint.Finding
	Hover(content string, line, char int) string
	Completion(content string, line, char int) []celtlint.CompletionItem
}

func New(analyzer Analyzer, opts ...Option) *Server {
	s := &Server{
		analyzer: analyzer,
		docs:     map[string]string{},
		name:     defaultServerName,
		extra:    map[string]handler{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// logErr records a handler error to stderr without failing the connection.
// Notification handlers must not return errors (a non-nil error ends the LSP
// session), so unmarshal/analysis problems are surfaced here instead.
func (s *Server) logErr(where string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %s: %v\n", s.name, where, err)
}

// WithName sets the server name reported in LSP ServerInfo and on
// diagnostics. Defaults to "celt".
func WithName(name string) Option {
	return func(s *Server) { s.name = name }
}

func (s *Server) Register(method string, h handler) { s.extra[method] = h }

func (s *Server) Run() error {
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(&stdioConn{in: os.Stdin, out: os.Stdout}))
	s.conn = conn
	conn.Go(context.Background(), s.handle)
	<-conn.Done()
	if s.shutdown {
		return nil
	}
	return conn.Err()
}

func (s *Server) handle(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
	if req.Method() == "exit" {
		_ = s.conn.Close()
		return nil
	}
	// Per the LSP spec, after a shutdown request the server rejects further
	// requests with InvalidRequest and only terminates on the exit notification.
	if s.shutdown {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidRequest,
			fmt.Sprintf("server shut down; request not allowed: %s", req.Method())))
	}
	if h, ok := s.handlers()[req.Method()]; ok {
		return h(ctx, reply, req.Params())
	}
	return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.MethodNotFound,
		fmt.Sprintf("method not found: %s", req.Method())))
}

func (s *Server) handlers() map[string]handler {
	h := map[string]handler{
		"textDocument/didOpen":    s.didOpen,
		"textDocument/didChange":  s.didChange,
		"textDocument/didClose":   s.didClose,
		"textDocument/codeAction": s.codeAction,
		"textDocument/formatting": s.formatting,
		"textDocument/hover":      s.hover,
		"textDocument/completion": s.completion,
		"initialize":              s.initialize,
		"shutdown":                s.doShutdown,
	}
	for m, extra := range s.extra {
		h[m] = extra
	}
	return h
}

func (s *Server) didOpen(ctx context.Context, _ jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.DidOpenTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.logErr("didOpen", err)
		return nil
	}
	s.docs[p.TextDocument.URI.String()] = p.TextDocument.Text
	return s.publish(ctx, p.TextDocument.URI, p.TextDocument.Text)
}

func (s *Server) didChange(ctx context.Context, _ jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.DidChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.logErr("didChange", err)
		return nil
	}
	if len(p.ContentChanges) == 0 {
		return nil
	}
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	s.docs[p.TextDocument.URI.String()] = text
	return s.publish(ctx, p.TextDocument.URI, text)
}

// didClose drops the document so the docs map does not grow unboundedly over
// the life of a long-running server. Notification; errors are non-fatal.
func (s *Server) didClose(ctx context.Context, _ jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.DidCloseTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		s.logErr("didClose", err)
		return nil
	}
	docURI := p.TextDocument.URI.String()
	delete(s.docs, docURI)
	// Clear diagnostics for the closed document.
	return s.conn.Notify(ctx, "textDocument/publishDiagnostics", &protocol.PublishDiagnosticsParams{
		URI:         p.TextDocument.URI,
		Diagnostics: []protocol.Diagnostic{},
	})
}

func (s *Server) initialize(ctx context.Context, reply jsonrpc2.Replier, _ json.RawMessage) error {
	return reply(ctx, &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync:           protocol.TextDocumentSyncKindFull,
			CodeActionProvider:         true,
			DocumentFormattingProvider: true,
			HoverProvider:              true,
			CompletionProvider:         &protocol.CompletionOptions{},
		},
		ServerInfo: &protocol.ServerInfo{Name: s.name, Version: "0.1.0"},
	}, nil)
}

func (s *Server) doShutdown(ctx context.Context, reply jsonrpc2.Replier, _ json.RawMessage) error {
	s.shutdown = true
	return reply(ctx, nil, nil)
}

func (s *Server) publish(ctx context.Context, docURI uri.URI, content string) error {
	findings := s.analyzer.Analyze(docURI.String(), content)
	diagnostics := make([]protocol.Diagnostic, 0, len(findings)*2)
	for i := range findings {
		f := &findings[i]
		rng := protocol.Range{
			Start: toPosition(content, f.Start),
			End:   toPosition(content, f.End),
		}

		msg := f.Message
		if f.SpanMessage != "" {
			msg = fmt.Sprintf("%s: %s", f.Message, f.SpanMessage)
		}
		diagnostics = append(diagnostics, protocol.Diagnostic{
			Range:    rng,
			Severity: diagnosticSeverity(f),
			Code:     f.Code,
			Source:   s.name,
			Message:  msg,
		})
		if f.Hint != "" || f.Fix != "" {
			diagnostics = append(diagnostics, protocol.Diagnostic{
				Range:    FindingRange(content, f),
				Severity: protocol.DiagnosticSeverityHint,
				Source:   s.name,
				Message:  f.HintMessage(),
			})
		}
		for _, sp := range f.Spans {
			diagnostics = append(diagnostics, protocol.Diagnostic{
				Range: protocol.Range{
					Start: toPosition(content, sp.Start),
					End:   toPosition(content, sp.End),
				},
				Severity: protocol.DiagnosticSeverityHint,
				Source:   s.name,
				Message:  sp.Message,
			})
		}
	}
	return s.conn.Notify(ctx, "textDocument/publishDiagnostics", &protocol.PublishDiagnosticsParams{
		URI:         docURI,
		Diagnostics: diagnostics,
	})
}

func (s *Server) codeAction(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.CodeActionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, err)
	}
	content, ok := s.docs[p.TextDocument.URI.String()]
	if !ok {
		return reply(ctx, []protocol.CodeAction{}, nil)
	}

	actCtx := ActionContext{
		URI:      p.TextDocument.URI,
		Content:  content,
		Range:    p.Range,
		Findings: s.analyzer.Analyze(p.TextDocument.URI.String(), content),
	}
	var actions []protocol.CodeAction
	for _, a := range s.actions {
		if a == nil {
			continue
		}
		actions = append(actions, a.Apply(actCtx)...)
	}
	return reply(ctx, actions, nil)
}

func (s *Server) formatting(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.DocumentFormattingParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, err)
	}
	content, ok := s.docs[p.TextDocument.URI.String()]
	if !ok {
		return reply(ctx, []protocol.TextEdit{}, nil)
	}
	edit, err := DocFormat(content, p.Options, s.fmtCfg)
	if err != nil {
		return reply(ctx, nil, err)
	}
	if edit == nil {
		return reply(ctx, []protocol.TextEdit{}, nil)
	}
	return reply(ctx, []protocol.TextEdit{*edit}, nil)
}

func (s *Server) hover(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.HoverParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, err)
	}
	content, ok := s.docs[p.TextDocument.URI.String()]
	if !ok {
		return reply(ctx, nil, nil)
	}
	value := s.analyzer.Hover(content, int(p.Position.Line), int(p.Position.Character))
	if value == "" {
		return reply(ctx, nil, nil)
	}
	return reply(ctx, &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: value,
		},
	}, nil)
}

func (s *Server) completion(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, err)
	}
	content, ok := s.docs[p.TextDocument.URI.String()]
	if !ok {
		return reply(ctx, []protocol.CompletionItem{}, nil)
	}
	items := s.analyzer.Completion(content, int(p.Position.Line), int(p.Position.Character))
	out := make([]protocol.CompletionItem, 0, len(items))
	for _, it := range items {
		ci := protocol.CompletionItem{
			Label: it.Label,
			Kind:  completionKind(it.Kind),
		}
		if it.InsertText != "" {
			ci.InsertText = it.InsertText
			if it.InsertTextFormat == celtlint.InsertTextFormatSnippet {
				ci.InsertTextFormat = protocol.InsertTextFormatSnippet
			}
		}

		if it.Documentation != "" {
			ci.Documentation = protocol.MarkupContent{
				Kind:  protocol.Markdown,
				Value: it.Documentation,
			}
		}
		out = append(out, ci)
	}
	return reply(ctx, out, nil)
}

func completionKind(kind string) protocol.CompletionItemKind {
	switch kind {
	case celtlint.CompletionKindField:
		return protocol.CompletionItemKindField
	case celtlint.CompletionKindFunction:
		return protocol.CompletionItemKindFunction
	case celtlint.CompletionKindVariable:
		return protocol.CompletionItemKindVariable
	case celtlint.CompletionKindMacro:
		return protocol.CompletionItemKindKeyword
	case celtlint.CompletionKindEnum:
		return protocol.CompletionItemKindEnum
	case celtlint.CompletionKindValue:
		return protocol.CompletionItemKindEnumMember
	default:
		return protocol.CompletionItemKindText
	}
}

func DocFormat(content string, opts protocol.FormattingOptions, cfg celtfmt.Config) (*protocol.TextEdit, error) {
	var indent string
	switch {
	case opts.InsertSpaces:
		// TabSize is client-controlled and unbounded; clamp it so a malformed
		// client can't force a huge allocation.
		ts := int(opts.TabSize)
		if ts <= 0 {
			ts = 1
		} else if ts > maxIndentTabSize {
			ts = maxIndentTabSize
		}
		indent = strings.Repeat(" ", ts)
	case opts.TabSize > 0:
		indent = "\t"
	}
	if indent != "" {
		cfg = indentOverride{base: cfg, indent: indent}
	}
	formatted, err := celtfmt.Format(content, cfg)
	if err != nil {
		return nil, err
	}
	if formatted == content {
		return nil, nil
	}
	return &protocol.TextEdit{Range: FullDocRange(content), NewText: formatted}, nil
}

// indentOverride re-supplies the indent rule on an underlying Config with an
// override indent, deferring all other rules to it.
type indentOverride struct {
	base   celtfmt.Config
	indent string
}

func (o indentOverride) Rules() []celtfmt.FormatRule {
	var r []celtfmt.FormatRule
	if o.base != nil {
		r = append(r, o.base.Rules()...)
	}
	r = append(r, rules.NewIndentRule(o.indent))
	return r
}

func FullDocRange(content string) protocol.Range {
	lastStart := 0
	if i := strings.LastIndexByte(content, '\n'); i >= 0 {
		lastStart = i + 1
	}
	return protocol.Range{
		Start: protocol.Position{Line: 0, Character: 0},
		End:   protocol.Position{Line: uint32(strings.Count(content, "\n")), Character: uint32(celtsource.UTF16Len(content[lastStart:]))}, // #nosec G115
	}
}

func toPosition(content string, p celtlint.Position) protocol.Position {
	u := celtsource.UTF16Position(content, p)
	return protocol.Position{Line: uint32(u.Line), Character: uint32(u.Char)} // #nosec G115
}

func diagnosticSeverity(f *celtlint.Finding) protocol.DiagnosticSeverity {
	if f.IsError() {
		return protocol.DiagnosticSeverityError
	}
	return protocol.DiagnosticSeverityWarning
}

var _ io.ReadWriteCloser = (*stdioConn)(nil)
