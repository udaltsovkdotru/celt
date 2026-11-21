package celtls_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtlint/rules"
	"github.com/udaltsovkdotru/celt/celtls"
	"github.com/udaltsovkdotru/celt/celtls/actions"
)

// JSON-RPC method and field names repeated across the transport fixtures.
const (
	tjTextDocument    = "textDocument"
	tjCapabilities    = "capabilities"
	tjDiagnostics     = "diagnostics"
	tjQuickfix        = "quickfix"
	tjOnly            = "only"
	tjPosition        = "position"
	tjCharacter       = "character"
	tjDocumentChanges = "documentChanges"
	tjApplyEdit       = "applyEdit"
	tjCompletion      = "completion"
	tjText            = "text"
	tjURI             = "uri"
	tjLine            = "line"
	tjWorkspace       = "workspace"
	tjCompletionItem  = "completionItem"
	tjVersion         = "version"
	tjWorkspaceEdit   = "workspaceEdit"
)

type integrationFormatConfig struct{}

func (integrationFormatConfig) Rules() []celtfmt.FormatRule { return nil }

// TestTransportServerProcess runs the production stdio server in a subprocess.
// The parent communicates with it using the framed JSON-RPC transport.
func TestTransportServerProcess(t *testing.T) {
	if os.Getenv("CELTLS_TEST_SERVER") != "1" {
		return
	}
	env, err := cel.NewEnv(
		cel.Variable("service", cel.StringType),
		cel.Variable("w", cel.ObjectType("test.Widget")),
		cel.Function("weirdStr", cel.MemberOverload("weird_str", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType)),
	)
	if err != nil {
		t.Fatal(err)
	}
	desc := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name: proto.String("test.proto"), Package: proto.String("test"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Widget"), Field: []*descriptorpb.FieldDescriptorProto{{
			Name: proto.String("name"), Number: proto.Int32(1),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		}}}},
	}}}
	analyzer, err := celtlint.New(env, celtlint.WithDescriptor(desc), celtlint.WithRules(rules.Misspell{RuleMeta: celtlint.RuleMeta{
		Name: "misspell", Desc: "suggest corrections", Defaults: celtlint.RuleParams{Level: celtlint.LevelError},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	s := celtls.New(analyzer, celtls.WithActions(actions.All(integrationFormatConfig{})...))
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
}

type integrationPipe struct {
	io.Reader
	io.WriteCloser
}

type integrationClient struct {
	stream     jsonrpc2.Stream
	nextID     int32
	applyEdits int
}

func newIntegrationClient(t *testing.T) *integrationClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTransportServerProcess$") // #nosec G204,G702 -- fixed test flags, not tainted input
	cmd.Env = append(os.Environ(), "CELTLS_TEST_SERVER=1")
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	return &integrationClient{stream: jsonrpc2.NewStream(integrationPipe{Reader: out, WriteCloser: in})}
}

func (c *integrationClient) call(t *testing.T, method string, params, result any) {
	t.Helper()
	response := c.request(t, method, params)
	if err := response.Err(); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if err := json.Unmarshal(response.Result(), result); err != nil {
		t.Fatalf("decode %s result: %v", method, err)
	}
}

func (c *integrationClient) writeRequest(t *testing.T, method string, params any) jsonrpc2.ID {
	t.Helper()
	c.nextID++
	id := jsonrpc2.NewNumberID(c.nextID)
	request, err := jsonrpc2.NewCall(id, method, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.stream.Write(context.Background(), request); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
	return id
}

func (c *integrationClient) request(t *testing.T, method string, params any) *jsonrpc2.Response {
	t.Helper()
	id := c.writeRequest(t, method, params)
	for {
		msg, _, err := c.stream.Read(context.Background())
		if err != nil {
			t.Fatalf("read %s response: %v", method, err)
		}
		if applyEdit, ok := msg.(*jsonrpc2.Call); ok && applyEdit.Method() == "workspace/applyEdit" {
			c.applyEdits++
			ack, err := jsonrpc2.NewResponse(applyEdit.ID(), protocol.ApplyWorkspaceEditResponse{Applied: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.stream.Write(context.Background(), ack); err != nil {
				t.Fatalf("acknowledge workspace edit: %v", err)
			}
			continue
		}
		response, ok := msg.(*jsonrpc2.Response)
		if !ok { // publishDiagnostics notifications can precede the response.
			continue
		}
		if response.ID() != id {
			t.Fatalf("%s: response ID = %v, want %v", method, response.ID(), id)
		}
		return response
	}
}

func (c *integrationClient) notify(t *testing.T, method string, params any) {
	t.Helper()
	msg, err := jsonrpc2.NewNotification(method, params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.stream.Write(context.Background(), msg); err != nil {
		t.Fatalf("write %s: %v", method, err)
	}
}

func (c *integrationClient) initialize(t *testing.T, capabilities any) {
	t.Helper()
	var result json.RawMessage
	c.call(t, "initialize", map[string]any{"processId": nil, "rootUri": nil, tjCapabilities: capabilities}, &result)
	c.notify(t, "initialized", map[string]any{})
}

func (c *integrationClient) open(t *testing.T, uri, text string) {
	t.Helper()
	c.notify(t, "textDocument/didOpen", map[string]any{tjTextDocument: map[string]any{
		tjURI: uri, "languageId": "cel", tjVersion: 1, tjText: text,
	}})
}

func integrationCodeActionParams(uri string) map[string]any {
	return map[string]any{
		tjTextDocument: map[string]any{tjURI: uri},
		"range":        map[string]any{"start": map[string]int{tjLine: 0, tjCharacter: 0}, "end": map[string]int{tjLine: 0, tjCharacter: 6}},
		"context":      map[string]any{tjDiagnostics: []any{}},
	}
}

func TestTransportCodeActionOnlyQuickFix(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjTextDocument: map[string]any{
		"codeAction": map[string]any{"codeActionLiteralSupport": map[string]any{
			"codeActionKind": map[string]any{"valueSet": []string{tjQuickfix, "source", "source.format"}},
		}},
	}})
	const uri = "file:///fix.cel"
	c.open(t, uri, "servic")
	params := integrationCodeActionParams(uri)
	var all []protocol.CodeAction
	c.call(t, "textDocument/codeAction", params, &all)
	foundFormat := false
	for _, action := range all {
		if action.Kind == "source.format" {
			foundFormat = true
		}
	}
	if !foundFormat {
		t.Fatalf("fixture did not produce the production format action: %+v", all)
	}
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var got []protocol.CodeAction
	c.call(t, "textDocument/codeAction", params, &got)
	foundFix := false
	for _, action := range got {
		if action.Kind != protocol.QuickFix && !strings.HasPrefix(string(action.Kind), "quickfix.") {
			t.Errorf("quickfix-only request returned %q action %q", action.Kind, action.Title)
		}
		if action.Kind == protocol.QuickFix && action.Edit != nil {
			for _, edit := range action.Edit.Changes[protocol.DocumentURI(uri)] {
				if edit.NewText == "service" {
					foundFix = true
				}
			}
		}
	}
	if !foundFix {
		t.Errorf("missing production quick fix for servic: %+v", got)
	}
}

func TestTransportLiteralQuickFixUsesVersionedEdit(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{
		tjWorkspace: map[string]any{tjWorkspaceEdit: map[string]any{tjDocumentChanges: true}},
		tjTextDocument: map[string]any{"codeAction": map[string]any{"codeActionLiteralSupport": map[string]any{
			"codeActionKind": map[string]any{"valueSet": []string{tjQuickfix}},
		}}},
	})
	const uri = "file:///literal-versioned.cel"
	c.open(t, uri, "servic")
	params := integrationCodeActionParams(uri)
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var acts []protocol.CodeAction
	c.call(t, "textDocument/codeAction", params, &acts)
	if len(acts) != 1 || acts[0].Edit == nil {
		t.Fatalf("expected one literal quick fix with an edit: %+v", acts)
	}
	edit := acts[0].Edit
	changes := edit.DocumentChanges
	if len(edit.Changes) != 0 || len(changes) != 1 || changes[0].TextDocument.URI.String() != uri ||
		changes[0].TextDocument.Version == nil || *changes[0].TextDocument.Version != 1 {
		t.Fatalf("quick fix must target %s at version 1 using documentChanges: %+v", uri, edit)
	}
	if len(changes[0].Edits) != 1 || changes[0].Edits[0].NewText != "service" {
		t.Errorf("expected quick fix for servic: %+v", changes[0].Edits)
	}
}

func TestTransportLegacyCommandRejectsReopenedDocument(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{
		tjApplyEdit: true, tjWorkspaceEdit: map[string]any{tjDocumentChanges: true},
	}})
	const uri = "file:///reopened.cel"
	c.open(t, uri, "servic")
	params := integrationCodeActionParams(uri)
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var commands []protocol.Command
	c.call(t, "textDocument/codeAction", params, &commands)
	if len(commands) != 1 {
		t.Fatalf("expected one legacy quick-fix command: %+v", commands)
	}
	c.notify(t, "textDocument/didClose", map[string]any{tjTextDocument: map[string]any{tjURI: uri}})
	c.open(t, uri, "true || servic")
	response := c.request(t, "workspace/executeCommand", protocol.ExecuteCommandParams{
		Command: commands[0].Command, Arguments: commands[0].Arguments,
	})
	if c.applyEdits != 0 {
		t.Errorf("sent %d edits from an action created before the document was closed", c.applyEdits)
	}
	if response.Err() == nil {
		var outcome struct {
			Applied *bool `json:"applied"`
		}
		if err := json.Unmarshal(response.Result(), &outcome); err != nil || outcome.Applied == nil || *outcome.Applied {
			t.Errorf("reopened document should reject the saved command: result=%s", response.Result())
		}
	}
}

func TestTransportCodeActionWithoutLiteralSupport(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{tjApplyEdit: true}})
	const uri = "file:///legacy.cel"
	c.open(t, uri, "servic")
	var acts []map[string]json.RawMessage
	c.call(t, "textDocument/codeAction", integrationCodeActionParams(uri), &acts)
	for _, action := range acts {
		if _, hasEdit := action["edit"]; hasEdit {
			t.Errorf("client without literal support received a code-action edit: %s", action["edit"])
		}
		var command string
		if err := json.Unmarshal(action["command"], &command); err != nil || command == "" {
			t.Errorf("client without literal support received a non-executable action: %v", action)
			continue
		}
		params := map[string]any{"command": command}
		if arguments, ok := action["arguments"]; ok {
			params["arguments"] = arguments
		}
		if err := c.request(t, "workspace/executeCommand", params).Err(); err != nil {
			t.Errorf("returned command %q could not be executed: %v", command, err)
		}
	}
}

func TestTransportLegacyCodeActionOnlyQuickFix(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{tjApplyEdit: true}})
	const uri = "file:///unformatted.cel"
	c.open(t, uri, "1+2+34")
	params := integrationCodeActionParams(uri)
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var acts []protocol.Command
	c.call(t, "textDocument/codeAction", params, &acts)
	if len(acts) != 0 {
		t.Errorf("quickfix-only request for valid, unformatted CEL returned commands: %+v", acts)
	}
}

func TestTransportLegacyCommandHandlesDeclinedVersionedEdit(t *testing.T) {
	t.Run("unchanged document", func(t *testing.T) { testTransportDeclinedVersionedEdit(t, false) })
	t.Run("changed after action request", func(t *testing.T) { testTransportDeclinedVersionedEdit(t, true) })
}

func testTransportDeclinedVersionedEdit(t *testing.T, stale bool) {
	t.Helper()
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{
		tjApplyEdit: true, tjWorkspaceEdit: map[string]any{tjDocumentChanges: true},
	}})
	const uri = "file:///legacy-versioned.cel"
	c.open(t, uri, "servic")
	params := integrationCodeActionParams(uri)
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var commands []protocol.Command
	c.call(t, "textDocument/codeAction", params, &commands)
	if len(commands) == 0 {
		t.Fatal("expected a legacy quick-fix command")
	}
	if stale {
		c.notify(t, "textDocument/didChange", map[string]any{
			tjTextDocument:   map[string]any{tjURI: uri, tjVersion: 2},
			"contentChanges": []any{map[string]any{tjText: "true || servic"}},
		})
	}
	id := c.writeRequest(t, "workspace/executeCommand", protocol.ExecuteCommandParams{
		Command: commands[0].Command, Arguments: commands[0].Arguments,
	})

	var response *jsonrpc2.Response
	declined := false
	for response == nil || (!stale && !declined) {
		msg, _, err := c.stream.Read(context.Background())
		if err != nil {
			t.Fatalf("read executeCommand exchange: %v", err)
		}
		switch m := msg.(type) {
		case *jsonrpc2.Response:
			if m.ID() != id {
				t.Fatalf("response ID = %v, want %v", m.ID(), id)
			}
			response = m
		case *jsonrpc2.Call:
			declined = ackDeclinedEdit(t, c, m, uri)
		}
	}
	if response.Err() == nil {
		verifyCommandOutcome(t, response)
	}
}

// ackDeclinedEdit answers a workspace/applyEdit request by declining it, and
// verifies the edit targets the initiating document at version 1 using
// versioned document changes.
func ackDeclinedEdit(t *testing.T, c *integrationClient, m *jsonrpc2.Call, uri string) bool {
	t.Helper()
	if m.Method() != "workspace/applyEdit" {
		t.Fatalf("unexpected server request %q", m.Method())
	}
	var p protocol.ApplyWorkspaceEditParams
	if err := json.Unmarshal(m.Params(), &p); err != nil {
		t.Fatal(err)
	}
	changes := p.Edit.DocumentChanges
	if len(p.Edit.Changes) != 0 || len(changes) != 1 || changes[0].TextDocument.URI.String() != uri ||
		changes[0].TextDocument.Version == nil || *changes[0].TextDocument.Version != 1 {
		t.Errorf("edit must target %s at version 1 using documentChanges: %s", uri, m.Params())
	}
	ack, err := jsonrpc2.NewResponse(m.ID(), protocol.ApplyWorkspaceEditResponse{
		Applied: false, FailureReason: "edit declined by test client",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.stream.Write(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	return true
}

// verifyCommandOutcome asserts the executeCommand response surfaced the
// declined edit.
func verifyCommandOutcome(t *testing.T, response *jsonrpc2.Response) {
	t.Helper()
	var outcome struct {
		Applied *bool `json:"applied"`
	}
	if err := json.Unmarshal(response.Result(), &outcome); err != nil || outcome.Applied == nil || *outcome.Applied {
		t.Errorf("executeCommand did not report the declined edit: result=%s", response.Result())
	}
}

func TestTransportDoesNotOfferLegacyCommandsWithoutApplyEdit(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{tjApplyEdit: false}})
	const uri = "file:///legacy-no-apply-edit.cel"
	c.open(t, uri, "servic")
	var acts []protocol.Command
	c.call(t, "textDocument/codeAction", integrationCodeActionParams(uri), &acts)
	if len(acts) != 0 {
		t.Errorf("got commands requiring workspace/applyEdit from a client without support: %+v", acts)
	}
}

func TestTransportPlaintextDocumentation(t *testing.T) {
	t.Run("plaintext only", func(t *testing.T) { testTransportPlaintextDocumentation(t, []string{"plaintext"}) })
	t.Run("plaintext preferred", func(t *testing.T) { testTransportPlaintextDocumentation(t, []string{"plaintext", "markdown"}) })
}

func testTransportPlaintextDocumentation(t *testing.T, formats []string) {
	t.Helper()
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjTextDocument: map[string]any{
		"hover": map[string]any{"contentFormat": formats},
		tjCompletion: map[string]any{tjCompletionItem: map[string]any{
			"documentationFormat": formats,
		}},
	}})
	const uri = "file:///plaintext.cel"
	c.open(t, uri, "service\nw.name.we")
	var hover protocol.Hover
	c.call(t, "textDocument/hover", map[string]any{
		tjTextDocument: map[string]any{tjURI: uri}, tjPosition: map[string]int{tjLine: 0, tjCharacter: 1},
	}, &hover)
	if hover.Contents.Kind != protocol.PlainText || !strings.Contains(hover.Contents.Value, "service") || strings.Contains(hover.Contents.Value, "```") {
		t.Errorf("hover should contain plaintext variable documentation: %+v", hover.Contents)
	}
	var items []struct {
		Label         string          `json:"label"`
		Documentation json.RawMessage `json:"documentation"`
	}
	c.call(t, "textDocument/completion", map[string]any{
		tjTextDocument: map[string]any{tjURI: uri}, tjPosition: map[string]int{tjLine: 1, tjCharacter: 9},
	}, &items)
	for _, item := range items {
		if item.Label != "weirdStr" {
			continue
		}
		var plain string
		if json.Unmarshal(item.Documentation, &plain) == nil && strings.Contains(plain, item.Label) && !strings.Contains(plain, "```") {
			return
		}
		var doc protocol.MarkupContent
		if err := json.Unmarshal(item.Documentation, &doc); err != nil || doc.Kind != protocol.PlainText ||
			!strings.Contains(doc.Value, item.Label) || strings.Contains(doc.Value, "```") {
			t.Errorf("completion should include plaintext method documentation, got %s", item.Documentation)
		}
		return
	}
	t.Fatalf("missing method in completion items: %+v", items)
}

func TestTransportHoverOnCarriageReturnLine(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{})
	const uri = "file:///cr-only.cel"
	c.open(t, uri, `true &&`+"\r"+`service == "x"`)
	response := c.request(t, "textDocument/hover", map[string]any{
		tjTextDocument: map[string]any{tjURI: uri}, tjPosition: map[string]int{tjLine: 1, tjCharacter: 1},
	})
	if err := response.Err(); err != nil {
		t.Fatal(err)
	}
	var hover *protocol.Hover
	if data := response.Result(); len(data) != 0 {
		if err := json.Unmarshal(data, &hover); err != nil {
			t.Fatalf("invalid hover response: %v", err)
		}
	}
	if hover == nil || !strings.Contains(hover.Contents.Value, "service") {
		t.Errorf("hover after CR should resolve service: %+v", hover)
	}
}

func TestTransportRejectsLegacyCommandForClosedVersionZeroDocument(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{tjApplyEdit: true}})
	const uri = "file:///closed-version-zero.cel"
	c.notify(t, "textDocument/didOpen", map[string]any{tjTextDocument: map[string]any{
		tjURI: uri, "languageId": "cel", tjVersion: 0, tjText: "servic",
	}})
	params := integrationCodeActionParams(uri)
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var commands []protocol.Command
	c.call(t, "textDocument/codeAction", params, &commands)
	if len(commands) != 1 {
		t.Fatalf("expected one quick-fix command for an open version-zero document: %+v", commands)
	}
	c.notify(t, "textDocument/didClose", map[string]any{tjTextDocument: map[string]any{tjURI: uri}})
	response := c.request(t, "workspace/executeCommand", protocol.ExecuteCommandParams{
		Command: commands[0].Command, Arguments: commands[0].Arguments,
	})
	if response.Err() == nil || c.applyEdits != 0 {
		t.Errorf("command from a closed document should be rejected with no edit: error=%v, edits=%d", response.Err(), c.applyEdits)
	}
}

func TestTransportRejectsUnregisteredCommand(t *testing.T) {
	c := newIntegrationClient(t)
	c.initialize(t, map[string]any{tjWorkspace: map[string]any{tjApplyEdit: true}})
	for _, command := range []string{"example.notRegistered", "celt.notRegistered"} {
		response := c.request(t, "workspace/executeCommand", protocol.ExecuteCommandParams{
			Command: command, Arguments: []any{"", 0, 0, protocol.WorkspaceEdit{}},
		})
		if response.Err() == nil || c.applyEdits != 0 {
			t.Errorf("unregistered command %q should be rejected with no edit: error=%v, edits=%d", command, response.Err(), c.applyEdits)
		}
	}
}

func TestTransportAdvertisedFallbackCommandIsExecutable(t *testing.T) {
	c := newIntegrationClient(t)
	var init protocol.InitializeResult
	c.call(t, "initialize", map[string]any{tjCapabilities: map[string]any{tjWorkspace: map[string]any{tjApplyEdit: true}}}, &init)
	c.notify(t, "initialized", map[string]any{})
	const fallback = "celt.codeAction"
	if init.Capabilities.ExecuteCommandProvider == nil {
		return
	}
	advertised := false
	for _, cmd := range init.Capabilities.ExecuteCommandProvider.Commands {
		advertised = advertised || cmd == fallback
	}
	if !advertised {
		return
	}
	const uri = "file:///advertised-command.cel"
	c.open(t, uri, "servic")
	params := integrationCodeActionParams(uri)
	params["context"] = map[string]any{tjDiagnostics: []any{}, tjOnly: []string{tjQuickfix}}
	var commands []protocol.Command
	c.call(t, "textDocument/codeAction", params, &commands)
	if len(commands) != 1 {
		t.Fatalf("expected command with a valid workspace edit: %+v", commands)
	}
	response := c.request(t, "workspace/executeCommand", protocol.ExecuteCommandParams{Command: fallback, Arguments: commands[0].Arguments})
	if response.Err() != nil || c.applyEdits != 1 {
		t.Errorf("advertised fallback should apply the edit: error=%v edits=%d", response.Err(), c.applyEdits)
	}
}

func TestTransportRequestErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		initialize bool
		method     string
		params     any
		want       jsonrpc2.Code
	}{
		{
			name: "before initialize", method: "textDocument/hover",
			params: map[string]any{tjTextDocument: map[string]any{tjURI: "file:///untitled.cel"}, tjPosition: map[string]int{tjLine: 0, tjCharacter: 0}},
			want:   jsonrpc2.ServerNotInitialized,
		},
		{
			name: "malformed formatting params", initialize: true, method: "textDocument/formatting",
			params: map[string]any{tjTextDocument: 1}, want: jsonrpc2.InvalidParams,
		},
		{
			name: "duplicate initialize", initialize: true, method: "initialize",
			params: map[string]any{tjCapabilities: map[string]any{}}, want: jsonrpc2.InvalidRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newIntegrationClient(t)
			if tc.initialize {
				c.initialize(t, map[string]any{})
			}
			response := c.request(t, tc.method, tc.params)
			var rpcErr *jsonrpc2.Error
			if err := response.Err(); !errors.As(err, &rpcErr) {
				t.Fatalf("response error = %v, want JSON-RPC code %d", err, tc.want)
			}
			if rpcErr.Code != tc.want {
				t.Errorf("response error code = %d, want %d: %v", rpcErr.Code, tc.want, rpcErr)
			}
		})
	}
}

func TestTransportCompletionSnippetCapability(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities any
		snippets     bool
	}{
		{name: "omitted", capabilities: map[string]any{}},
		{name: "false", capabilities: map[string]any{tjTextDocument: map[string]any{tjCompletion: map[string]any{tjCompletionItem: map[string]any{"snippetSupport": false}}}}},
		{name: "true", capabilities: map[string]any{tjTextDocument: map[string]any{tjCompletion: map[string]any{tjCompletionItem: map[string]any{"snippetSupport": true}}}}, snippets: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newIntegrationClient(t)
			c.initialize(t, tc.capabilities)
			const uri = "file:///completion.cel"
			c.open(t, uri, "w.name.we")
			var got []protocol.CompletionItem
			c.call(t, "textDocument/completion", map[string]any{
				tjTextDocument: map[string]any{tjURI: uri}, tjPosition: map[string]int{tjLine: 0, tjCharacter: 9},
			}, &got)
			found := false
			for _, item := range got {
				text := item.InsertText
				if item.TextEdit != nil {
					text = item.TextEdit.NewText
				}
				if !tc.snippets && (item.InsertTextFormat == protocol.InsertTextFormatSnippet || strings.Contains(text, "$0")) {
					t.Errorf("client without snippet support got snippet: %+v", item)
				}
				if item.Label == "weirdStr" {
					found = true
					if tc.snippets && (item.InsertTextFormat != protocol.InsertTextFormatSnippet || text != "weirdStr($0)") {
						t.Errorf("snippet-capable client got %+v", item)
					}
				}
			}
			if !found {
				t.Fatalf("missing weirdStr member completion: %+v", got)
			}
		})
	}
}
