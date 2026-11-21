package main

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

type e2eWorkspacePipe struct {
	io.Reader
	io.WriteCloser
}

func TestE2ELSPUsesInitializedWorkspaceConfig(t *testing.T) {
	bin := e2eBinary(t)
	base := t.TempDir()
	project, launcher := filepath.Join(base, "workspace"), filepath.Join(base, "launcher")
	e2eWriteFile(t, filepath.Join(project, ".celt.yml"), "env: {variables: [{name: accountDecision, type: bool}]}\n")
	e2eWriteFile(t, filepath.Join(launcher, ".celt.yml"), "env: {}\n")
	file := filepath.Join(project, "policy.cel")
	e2eWriteFile(t, file, "accountDecision\n")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "lsp")
	cmd.Dir = launcher
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	conn := jsonrpc2.NewConn(jsonrpc2.NewStream(e2eWorkspacePipe{Reader: out, WriteCloser: in}))
	defer conn.Close()
	conn.Go(ctx, func(ctx context.Context, reply jsonrpc2.Replier, _ jsonrpc2.Request) error {
		return reply(ctx, nil, nil)
	})
	var init protocol.InitializeResult
	if _, err := conn.Call(ctx, "initialize", map[string]any{
		"rootUri": uri.File(project), "capabilities": map[string]any{},
		"workspaceFolders": []map[string]any{{"uri": uri.File(project), "name": "workspace"}},
	}, &init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := conn.Notify(ctx, "initialized", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Notify(ctx, "textDocument/didOpen", protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri.File(file), LanguageID: "cel", Version: 1, Text: "accountDecision\n"},
	}); err != nil {
		t.Fatal(err)
	}
	var hover *protocol.Hover
	if _, err := conn.Call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri.File(file)}, "position": map[string]int{"line": 0, "character": 1},
	}, &hover); err != nil {
		t.Fatalf("hover: %v", err)
	}
	if hover == nil || !strings.Contains(hover.Contents.Value, "accountDecision") {
		t.Errorf("workspace-configured symbol missing from hover: %+v", hover)
	}
}
