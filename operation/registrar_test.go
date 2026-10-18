// SPDX-License-Identifier: GPL-3.0-or-later

package operation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func withRegistrars(t *testing.T, fns ...ToolRegistrar) {
	t.Helper()
	saved := extraRegistrars
	extraRegistrars = nil
	t.Cleanup(func() { extraRegistrars = saved })
	for _, fn := range fns {
		AddToolRegistrar(fn)
	}
}

func registerEchoTool(name string) ToolRegistrar {
	return func(s *server.MCPServer) {
		s.AddTool(mcp.NewTool(name, mcp.WithDescription("test tool")),
			func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText("from " + name), nil
			})
	}
}

func listToolNames(t *testing.T, s *server.MCPServer) map[string]bool {
	t.Helper()
	response := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(decoded.Result.Tools))
	for _, tool := range decoded.Result.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestExtraToolAppearsInToolsListAndIsCallable(t *testing.T) {
	withRegistrars(t, registerEchoTool("extension_echo"))

	s := newMCPServer("test")
	RegisterTool(s)
	builtIn := len(s.ListTools())
	if err := registerExtraTools(s); err != nil {
		t.Fatal(err)
	}

	names := listToolNames(t, s)
	if !names["extension_echo"] {
		t.Fatal("tools/list does not include the extension tool")
	}
	if len(names) != builtIn+1 {
		t.Fatalf("tools/list has %d tools, want the %d built-in ones plus one", len(names), builtIn)
	}

	response := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"extension_echo","arguments":{}}}`))
	raw, _ := json.Marshal(response)
	if !strings.Contains(string(raw), "from extension_echo") {
		t.Fatalf("calling the extension tool returned %s", raw)
	}
}

func TestExtraToolCannotReplaceABuiltInTool(t *testing.T) {
	s := newMCPServer("test")
	RegisterTool(s)
	var builtIn string
	for name := range s.ListTools() {
		builtIn = name
		break
	}
	original := s.GetTool(builtIn).Tool.Description

	withRegistrars(t, registerEchoTool(builtIn))
	err := registerExtraTools(s)
	if err == nil || !strings.Contains(err.Error(), builtIn) {
		t.Fatalf("registerExtraTools = %v, want a refusal naming %q", err, builtIn)
	}
	if got := s.GetTool(builtIn).Tool.Description; got != original {
		t.Fatalf("built-in tool %q was replaced: description %q", builtIn, got)
	}
}

func TestExtraToolsCannotCollideWithEachOther(t *testing.T) {
	withRegistrars(t, registerEchoTool("extension_echo"), registerEchoTool("extension_echo"))

	s := newMCPServer("test")
	if err := registerExtraTools(s); err == nil {
		t.Fatal("a second registrar reusing a tool name was accepted")
	}
}

func TestAddToolRegistrarRejectsNil(t *testing.T) {
	withRegistrars(t)
	defer func() {
		if recover() == nil {
			t.Fatal("AddToolRegistrar(nil) did not panic")
		}
	}()
	AddToolRegistrar(nil)
}
