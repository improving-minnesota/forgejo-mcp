// SPDX-License-Identifier: GPL-3.0-or-later

package operation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// failingTool registers a tool whose handler always fails with a forge-style
// error, so a tools/call envelope can show what the client actually receives.
func failingTool(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("boom"), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("GET /api/v1/repos/o/r/actions/runs/42/jobs: 404 Not Found: GetRunById")
	})
}

func callFailingTool(t *testing.T, s *server.MCPServer) mcp.JSONRPCMessage {
	t.Helper()
	response := s.HandleMessage(context.Background(), []byte(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"tools/call",
		"params":{"name":"boom","arguments":{}}
	}`))
	if response == nil {
		t.Fatal("no response to tools/call")
	}
	return response
}

// TestInBandToolErrorsConvertsHandlerError pins the fix for the recurring
// "Server internal error [-32603]": a failed tool call must reach the client
// as an in-band error result carrying the underlying message, not as a
// protocol error that hides it.
func TestInBandToolErrorsConvertsHandlerError(t *testing.T) {
	s := server.NewMCPServer("forgejo-mcp", "test", InBandToolErrors())
	failingTool(s)

	rpc, ok := callFailingTool(t, s).(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected a JSON-RPC response, got %#v", callFailingTool(t, s))
	}
	result, ok := rpc.Result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("expected a CallToolResult, got %#v", rpc.Result)
	}
	if !result.IsError {
		t.Fatalf("expected IsError=true, got %#v", result)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "404 Not Found") {
		t.Fatalf("expected the underlying message in the content, got %#v", result.Content)
	}
}

// TestWithoutInBandToolErrorsProtocolErrorRemains is the control: it pins the
// mcp-go behavior InBandToolErrors exists to change, so a library upgrade that
// alters it is noticed rather than silently absorbed.
func TestWithoutInBandToolErrorsProtocolErrorRemains(t *testing.T) {
	s := server.NewMCPServer("forgejo-mcp", "test")
	failingTool(s)

	rpc, ok := callFailingTool(t, s).(mcp.JSONRPCError)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %#v", callFailingTool(t, s))
	}
	if rpc.Error.Code != mcp.INTERNAL_ERROR {
		t.Fatalf("expected internal error code, got %d", rpc.Error.Code)
	}
}
