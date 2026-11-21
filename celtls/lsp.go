package celtls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

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
	// applyEditArgCount is the fixed number of positional arguments in a
	// celt.<command> workspace/executeCommand invocation.
	applyEditArgCount = 4
	// applyEditTimeout bounds how long executeCommand waits for the client's
	// applyEdit response.
	applyEditTimeout = 5 * time.Second
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

// AnalyzerBuilder builds a fresh Analyzer (and the format config/actions that
// go with it) from a project directory, so the server can select the
// workspace's configuration from the initialize request instead of only the
// directory it was launched from.
type AnalyzerBuilder func(dir string) (Analyzer, celtfmt.Config, []Action, error)

// WithAnalyzerBuilder supplies the builder used to reconfigure the server for
// the workspace chosen at initialize time.
func WithAnalyzerBuilder(b AnalyzerBuilder) Option {
	return func(s *Server) { s.builder = b }
}

type Server struct {
	analyzer    Analyzer
	builder     AnalyzerBuilder
	conn        jsonrpc2.Conn
	docs        map[string]string
	versions    map[string]int32
	opens       map[string]int
	fmtCfg      celtfmt.Config
	name        string
	shutdown    bool
	initialized bool
	caps        protocol.ClientCapabilities
	extra       map[string]handler
	actions     []Action
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
		versions: map[string]int32{},
		opens:    map[string]int{},
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
	// Per the LSP spec, before the initialize request completes the server
	// must reject requests (but not notifications) with ServerNotInitialized.
	if !s.initialized && req.Method() != "initialize" {
		if _, isCall := req.(*jsonrpc2.Call); isCall {
			return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.ServerNotInitialized,
				fmt.Sprintf("server not initialized; request not allowed: %s", req.Method())))
		}
		// Notifications before initialize are dropped (replying with an error
		// would end the session).
		return nil
	}
	if h, ok := s.handlers()[req.Method()]; ok {
		return h(ctx, reply, req.Params())
	}
	return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.MethodNotFound,
		fmt.Sprintf("method not found: %s", req.Method())))
}

func (s *Server) handlers() map[string]handler {
	h := map[string]handler{
		"textDocument/didOpen":     s.didOpen,
		"textDocument/didChange":   s.didChange,
		"textDocument/didClose":    s.didClose,
		"textDocument/codeAction":  s.codeAction,
		"textDocument/formatting":  s.formatting,
		"textDocument/hover":       s.hover,
		"textDocument/completion":  s.completion,
		"workspace/executeCommand": s.executeCommand,
		"initialize":               s.initialize,
		"shutdown":                 s.doShutdown,
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
	docURI := p.TextDocument.URI.String()
	s.docs[docURI] = p.TextDocument.Text
	s.versions[docURI] = p.TextDocument.Version
	// Each open starts a new generation of the document. The generation (not
	// just the version) tags actions, so a command saved before a close/reopen
	// of the same URI is detected as stale even when the version matches.
	s.opens[docURI]++
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
	docURI := p.TextDocument.URI.String()
	s.docs[docURI] = text
	s.versions[docURI] = p.TextDocument.Version
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
	delete(s.versions, docURI)
	// Clear diagnostics for the closed document.
	return s.conn.Notify(ctx, "textDocument/publishDiagnostics", &protocol.PublishDiagnosticsParams{
		URI:         p.TextDocument.URI,
		Diagnostics: []protocol.Diagnostic{},
	})
}

func (s *Server) initialize(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	// The LSP lifecycle permits a single initialize request. A second one would
	// renegotiate capabilities mid-session and change protocol behavior, so it
	// is rejected instead.
	if s.initialized {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidRequest, "server already initialized"))
	}
	var p protocol.InitializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
	}
	s.caps = p.Capabilities
	s.initialized = true
	// Select the workspace's configuration: an editor may launch the server
	// outside the project and name the project via the initialize request. Use
	// the workspace root's config (if a builder is available) so a variable
	// declared only in the workspace's .celt.yml resolves for hover/completion.
	if s.builder != nil {
		if dir := workspaceDir(&p); dir != "" {
			if na, ncfg, nactions, err := s.builder(dir); err == nil {
				s.analyzer = na
				if ncfg != nil {
					s.fmtCfg = ncfg
				}
				if nactions != nil {
					s.actions = nactions
				}
			} else {
				s.logErr("initialize", fmt.Errorf("workspace config: %w", err))
			}
		}
	}
	return reply(ctx, &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync:           protocol.TextDocumentSyncKindFull,
			CodeActionProvider:         true,
			DocumentFormattingProvider: true,
			HoverProvider:              true,
			CompletionProvider:         &protocol.CompletionOptions{},
			ExecuteCommandProvider:     &protocol.ExecuteCommandOptions{Commands: s.advertisedCommands()},
		},
		ServerInfo: &protocol.ServerInfo{Name: s.name, Version: "0.1.0"},
	}, nil)
}

// workspaceDir returns the file-system path of the workspace chosen in the
// initialize request, from its workspace folders or root URI, or "" when none
// is given. It is used to select the workspace's config when the server was
// launched outside the project.
//
//nolint:staticcheck // RootURI is the legacy fallback when no WorkspaceFolders are sent
func workspaceDir(p *protocol.InitializeParams) string {
	for _, f := range p.WorkspaceFolders {
		if f.URI != "" {
			if pth := uri.URI(f.URI).Path(); pth != "" {
				return pth
			}
		}
	}
	if p.RootURI != "" {
		return p.RootURI.Path()
	}
	return ""
}

// advertisedCommands returns the celt.<name> commands advertised to the client,
// derived from the single supportedCommands registry so the advertisement and
// executeCommand's validation cannot drift apart.
func (*Server) advertisedCommands() []string {
	out := make([]string, 0, len(supportedCommands))
	for name := range supportedCommands {
		out = append(out, "celt."+name)
	}
	sort.Strings(out)
	return out
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
			// A secondary span without a message of its own must not be published
			// as a standalone hint with an empty message; the editor would show an
			// unexplained problem marker. It only makes sense attached to the
			// primary diagnostic, so skip it rather than emit blank output.
			if sp.Message == "" {
				continue
			}
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
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
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
	// context.only restricts the returned kinds (and their subtypes); it must
	// apply before converting to commands too.
	if len(p.Context.Only) > 0 {
		filtered := actions[:0]
		for _, action := range actions {
			if codeActionKindMatch(p.Context.Only, action.Kind) {
				filtered = append(filtered, action)
			}
		}
		actions = filtered
	}
	// A client that did not declare code-action literal support cannot
	// represent a workspace edit. Per the protocol it falls back to commands,
	// so return executable commands (each carrying the edit to apply) instead
	// of literal actions whose edit was stripped (which the client could not
	// apply). The command also carries the document version and open generation
	// the action's edit ranges were computed against, so executeCommand can
	// reject the action once the document has moved on instead of applying
	// stale offsets. Edit-bearing commands are offered only when the client can
	// apply them (workspace/applyEdit); command-only actions carry their own
	// editor command and are returned regardless.
	if !s.clientCodeActionLiterals() {
		return reply(ctx, s.legacyCommands(actCtx, actions), nil)
	}
	// A literal-capable client applies the edit itself. When it supports
	// versioned document changes, bind each target URI to its own document
	// version so the client can detect that a document moved on.
	for i := range actions {
		if actions[i].Edit != nil {
			ve := s.versionedWorkspaceEdit(*actions[i].Edit)
			actions[i].Edit = &ve
		}
	}
	return reply(ctx, actions, nil)
}

// legacyCommands converts literal code actions into executable commands for a
// client that did not declare code-action literal support (so it cannot
// represent a workspace edit). Each command carries the document version and
// open generation the action's edit ranges were computed against, so
// executeCommand can reject the action once the document has moved on instead
// of applying stale offsets. Edit-bearing commands are offered only when the
// client can apply them (workspace/applyEdit); command-only actions carry their
// own editor command and are returned regardless.
func (s *Server) legacyCommands(actCtx ActionContext, actions []protocol.CodeAction) []protocol.Command {
	commands := make([]protocol.Command, 0, len(actions))
	docURI := actCtx.URI.String()
	version := s.versions[docURI]
	generation := s.opens[docURI]
	for _, ca := range actions {
		// A command-only action (no edit to apply, e.g. editor.openGuide)
		// is returned as its original command; wrapping it in a null-edit
		// celt.codeAction would discard the command entirely. It carries its
		// own editor command and needs no workspace edit, so it is returned
		// even when the client lacks workspace/applyEdit support.
		if ca.Edit == nil && ca.Command != nil {
			commands = append(commands, *ca.Command)
			continue
		}
		// An edit-bearing action is offered only when the client can apply
		// it (workspace/applyEdit); otherwise there is no way to apply the
		// edit, so the action is dropped.
		if !s.clientApplyEdit() {
			continue
		}
		// Bind each target URI to its own version here, at build time, so a
		// later change to a target document cannot stamp a stale version
		// onto it when the command is executed.
		edit := ca.Edit
		if edit != nil {
			ve := s.versionedWorkspaceEdit(*edit)
			edit = &ve
		}
		commands = append(commands, protocol.Command{
			Title:     ca.Title,
			Command:   "celt." + commandName(&ca),
			Arguments: []any{actCtx.URI, version, generation, edit},
		})
	}
	return commands
}

// supportedCommands are the command IDs the server actually issues via
// celt.<name> and advertises to the client. A single registry drives both the
// advertisement (see initialize) and executeCommand's validation, so a client
// can execute every command the server returns or advertises and anything else
// in the celt. namespace is rejected. "codeAction" is the generic command used
// for any edit-bearing action whose kind has no dedicated command.
var supportedCommands = map[string]bool{
	"quickfix":   true,
	"format":     true,
	"codeAction": true,
}

// commandName derives a stable command id from a code action. The action's
// kind is namespaced under "celt." so workspace/executeCommand can route it. A
// kind with no dedicated command (e.g. an arbitrary custom action such as
// source.fixAll) maps to the generic codeAction command so every returned
// command is advertised and executable.
func commandName(ca *protocol.CodeAction) string {
	name := string(ca.Kind)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || !supportedCommands[name] {
		name = "codeAction"
	}
	return name
}

// executeCommand applies a celt.<action> command by asking the client to apply
// the workspace edit it carries (arguments [uri, version, generation,
// workspaceEdit]). When the client declares documentChanges support the edit
// is sent as a
// versioned text-document edit, and the command's response reports whether the
// client applied or declined it rather than claiming unconditional success. An
// action whose captured version or open generation no longer matches the
// document (the document changed, or was closed and reopened, after the
// action's offsets were computed) is rejected up front.
func (s *Server) executeCommand(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.ExecuteCommandParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
	}
	// Only commands this server issues (celt.<action>) are dispatched here;
	// command-only actions carry their original command and are never routed
	// through executeCommand. The namespace prefix alone is not a dispatch
	// check: reject a command that was never advertised (e.g. celt.notRegistered)
	// without issuing an edit.
	name := strings.TrimPrefix(p.Command, "celt.")
	if !strings.HasPrefix(p.Command, "celt.") || !supportedCommands[name] {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, "unknown command: "+p.Command))
	}
	if len(p.Arguments) < applyEditArgCount {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, "command missing arguments"))
	}
	uriStr, _ := p.Arguments[0].(string)
	var version int32
	if v, ok := p.Arguments[1].(float64); ok {
		version = int32(v)
	}
	var generation int
	if g, ok := p.Arguments[2].(float64); ok {
		generation = int(g)
	}
	editJSON, err := json.Marshal(p.Arguments[3])
	if err != nil {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
	}
	var edit protocol.WorkspaceEdit
	if err := json.Unmarshal(editJSON, &edit); err != nil {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
	}
	// The edit's ranges were computed against the version and open generation
	// captured when the action was built. If the document has moved on (a new
	// version within the same open, a close+reopen that resets the version, or
	// the document is no longer open), applying them would edit the wrong text;
	// refuse rather than send a stale edit. A closed document reads as version
	// zero and a matching open generation, so its open status is checked too.
	_, open := s.docs[uriStr]
	if uriStr != "" && (!open || s.versions[uriStr] != version || s.opens[uriStr] != generation) {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidRequest,
			"action is stale: the document changed after the action was requested"))
	}
	edit = s.versionedWorkspaceEdit(edit)

	// Handlers run synchronously in the connection's read loop, so a blocking
	// conn.Call here would deadlock (the applyEdit response could never be
	// read). Dispatch the apply request on its own goroutine and reply to the
	// command only once the client's applyEdit response arrives, so a declined
	// or failed edit is surfaced to the caller instead of being discarded.
	actx, cancel := context.WithTimeout(ctx, applyEditTimeout)
	go func() {
		defer cancel()
		var res protocol.ApplyWorkspaceEditResponse
		if _, err := s.conn.Call(actx, "workspace/applyEdit", &protocol.ApplyWorkspaceEditParams{Edit: edit}, &res); err != nil {
			_ = reply(ctx, nil, err)
			return
		}
		_ = reply(ctx, &res, nil)
	}()
	return nil
}

// versionedWorkspaceEdit rewrites a plain-changes edit into versioned document
// changes when the client declared documentChanges support, tying each target
// URI to its own document version so a multi-document action does not stamp
// the initiating document's version onto unrelated targets. Edits already
// carrying document changes are returned unchanged, as are edits for clients
// without documentChanges support.
func (s *Server) versionedWorkspaceEdit(edit protocol.WorkspaceEdit) protocol.WorkspaceEdit {
	if !s.clientVersionedEdits() || len(edit.DocumentChanges) > 0 || len(edit.Changes) == 0 {
		return edit
	}
	var dc []protocol.TextDocumentEdit
	for uri, edits := range edit.Changes {
		// An opened document binds to its version so the client can detect that
		// it moved on. A target that was never opened has no in-memory version
		// to compare against (the disk content is authoritative), so it carries
		// a null version instead of an invented one.
		var v *int32
		if _, open := s.docs[uri.String()]; open {
			ver := s.versions[uri.String()]
			v = &ver
		}
		dc = append(dc, protocol.TextDocumentEdit{
			TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
				Version:                v,
			},
			Edits: edits,
		})
	}
	edit.DocumentChanges = dc
	edit.Changes = nil
	return edit
}

// clientVersionedEdits reports whether the client declared support for
// versioned document changes in workspace edits.
func (s *Server) clientVersionedEdits() bool {
	return s.caps.Workspace != nil &&
		s.caps.Workspace.WorkspaceEdit != nil &&
		s.caps.Workspace.WorkspaceEdit.DocumentChanges
}

// clientApplyEdit reports whether the client declared support for the
// workspace/applyEdit request, which legacy (command-based) actions depend on.
func (s *Server) clientApplyEdit() bool {
	return s.caps.Workspace != nil && s.caps.Workspace.ApplyEdit
}

// hoverMarkdown reports whether hover content should be sent as Markdown. A
// client that lists supported content formats without Markdown (i.e. only
// plaintext) receives plaintext; an unspecified list defaults to Markdown.
func (s *Server) hoverMarkdown() bool {
	if s.caps.TextDocument == nil || s.caps.TextDocument.Hover == nil {
		return true
	}
	return kindsIncludeMarkdown(s.caps.TextDocument.Hover.ContentFormat)
}

// completionDocMarkdown reports whether completion item documentation should be
// sent as Markdown, mirroring hoverMarkdown for the completion capabilities.
func (s *Server) completionDocMarkdown() bool {
	if s.caps.TextDocument == nil || s.caps.TextDocument.Completion == nil ||
		s.caps.TextDocument.Completion.CompletionItem == nil {
		return true
	}
	return kindsIncludeMarkdown(s.caps.TextDocument.Completion.CompletionItem.DocumentationFormat)
}

// kindsIncludeMarkdown reports whether the client prefers Markdown. The listed
// formats are in order of preference, so the first entry decides; an empty list
// means the client did not restrict and defaults to Markdown. A client listing
// ['plaintext', 'markdown'] thus receives plaintext, not Markdown.
func kindsIncludeMarkdown(kinds []protocol.MarkupKind) bool {
	if len(kinds) == 0 {
		return true
	}
	return kinds[0] == protocol.Markdown
}

// markdownToPlaintext converts Markdown to plaintext by dropping code-fence
// delimiters, so a plaintext-only client does not receive Markdown markup.
func markdownToPlaintext(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (s *Server) formatting(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.DocumentFormattingParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
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
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
	}
	content, ok := s.docs[p.TextDocument.URI.String()]
	if !ok {
		return reply(ctx, nil, nil)
	}
	value := s.analyzer.Hover(content, int(p.Position.Line), int(p.Position.Character))
	if value == "" {
		return reply(ctx, nil, nil)
	}
	kind := protocol.Markdown
	if !s.hoverMarkdown() {
		value = markdownToPlaintext(value)
		kind = protocol.PlainText
	}
	return reply(ctx, &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  kind,
			Value: value,
		},
	}, nil)
}

func (s *Server) completion(ctx context.Context, reply jsonrpc2.Replier, params json.RawMessage) error {
	var p protocol.CompletionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return reply(ctx, nil, jsonrpc2.NewError(jsonrpc2.InvalidParams, err.Error()))
	}
	content, ok := s.docs[p.TextDocument.URI.String()]
	if !ok {
		return reply(ctx, []protocol.CompletionItem{}, nil)
	}
	items := s.analyzer.Completion(content, int(p.Position.Line), int(p.Position.Character))
	snippets := s.clientSnippets()
	out := make([]protocol.CompletionItem, 0, len(items))
	for _, it := range items {
		ci := protocol.CompletionItem{
			Label: it.Label,
			Kind:  completionKind(it.Kind),
		}
		if it.InsertText != "" {
			insert := it.InsertText
			if it.InsertTextFormat == celtlint.InsertTextFormatSnippet {
				if snippets {
					ci.InsertTextFormat = protocol.InsertTextFormatSnippet
				} else {
					// A client without snippet support cannot use `$N` tab stops
					// or `${N:placeholder}` defaults; strip all snippet syntax so
					// no `$` remains in the plain insert text.
					insert = snippetPlaceholderRx.ReplaceAllString(insert, "")
				}
			}
			ci.InsertText = insert
		}

		if it.Documentation != "" {
			doc := it.Documentation
			kind := protocol.Markdown
			if !s.completionDocMarkdown() {
				doc = markdownToPlaintext(doc)
				kind = protocol.PlainText
			}
			ci.Documentation = protocol.MarkupContent{
				Kind:  kind,
				Value: doc,
			}
		}
		out = append(out, ci)
	}
	return reply(ctx, out, nil)
}

// snippetPlaceholderRx matches LSP snippet tab-stop and placeholder syntax
// (`$1`, `${1:label}`) so it can be stripped for clients without snippet
// support, leaving plain insert text with no `$`.
var snippetPlaceholderRx = regexp.MustCompile(`\$\{\d+:[^}]*\}|\$\d+`)

// clientSnippets reports whether the client declared completion snippet
// support. A client that omits or disables snippetSupport receives plain-text
// insert text.
func (s *Server) clientSnippets() bool {
	return s.caps.TextDocument != nil &&
		s.caps.TextDocument.Completion != nil &&
		s.caps.TextDocument.Completion.CompletionItem != nil &&
		s.caps.TextDocument.Completion.CompletionItem.SnippetSupport
}

// clientCodeActionLiterals reports whether the client declared support for code
// action literals (the structured form carrying a workspace edit). Without it
// the server must not send the edit field.
func (s *Server) clientCodeActionLiterals() bool {
	return s.caps.TextDocument != nil &&
		s.caps.TextDocument.CodeAction != nil &&
		s.caps.TextDocument.CodeAction.CodeActionLiteralSupport != nil
}

// codeActionKindMatch reports whether an action kind is a member of the
// requested kinds or a subtype of one (kinds form a dot-separated hierarchy).
func codeActionKindMatch(only []protocol.CodeActionKind, kind protocol.CodeActionKind) bool {
	for _, r := range only {
		if kind == r || strings.HasPrefix(string(kind), string(r)+".") {
			return true
		}
	}
	return false
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
// override indent, deferring all other rules (and their enablement/raw
// configuration) to the underlying config. It implements RuleConfig so a
// per-rule disabled override such as `fmt.break-operators: false` survives the
// editor's tab settings instead of being silently re-enabled.
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

func (o indentOverride) Enabled(name string) (enabled, ok bool) {
	if name == "indent" {
		// The editor-supplied indentation always applies.
		return true, true
	}
	if rc, ok := o.base.(celtfmt.RuleConfig); ok {
		return rc.Enabled(name)
	}
	// A plain Config (one that supplies rules via Rules() but does not
	// implement RuleConfig) explicitly enables whatever rules it provides, per
	// celtfmt's activeRules semantics. Preserve that enablement through the
	// indentation override; otherwise a rule the user explicitly enabled (e.g.
	// break-operators) would silently fall back to its disabled default.
	for _, r := range o.base.Rules() {
		if r != nil && r.Meta().Name == name {
			return true, true
		}
	}
	return false, false
}

func (o indentOverride) Raw(name string) ([]byte, bool) {
	if rc, ok := o.base.(celtfmt.RuleConfig); ok {
		return rc.Raw(name)
	}
	return nil, false
}

func FullDocRange(content string) protocol.Range {
	// Count LSP line breaks: \n, \r, or \r\n each end a line. The end position
	// is the start of the line after the last break, so its line index equals
	// the number of breaks and its character is the length of the final line.
	lineBreaks := 0
	lastLineStart := 0
	i := 0
	for i < len(content) {
		switch content[i] {
		case '\r':
			lineBreaks++
			i++
			if i < len(content) && content[i] == '\n' {
				i++
			}
			lastLineStart = i
		case '\n':
			lineBreaks++
			i++
			lastLineStart = i
		default:
			i++
		}
	}
	return protocol.Range{
		Start: protocol.Position{Line: 0, Character: 0},
		End:   protocol.Position{Line: uint32(lineBreaks), Character: uint32(celtsource.UTF16Len(content[lastLineStart:]))}, // #nosec G115
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
