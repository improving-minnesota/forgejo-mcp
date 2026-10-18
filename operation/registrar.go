// SPDX-License-Identifier: GPL-3.0-or-later

package operation

import (
	"fmt"
	"sort"

	"git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/log"

	"github.com/mark3labs/mcp-go/server"
)

// ToolRegistrar registers additional tools on the server it is given.
type ToolRegistrar func(s *server.MCPServer)

var extraRegistrars []ToolRegistrar

// AddToolRegistrar lets a downstream build add its own tools without forking
// this module: call it from main before cmd.Execute, and Run registers the
// tools after the built-in ones.
//
// Only tools are taken over. Each registrar runs against a scratch server, and
// Run copies the tools it registered into the real one, so a registrar cannot
// reach the server's transport or remove a built-in tool. A tool whose name is
// already registered, by this module or by an earlier registrar, makes Run
// refuse to start: replacing a built-in tool silently would change behaviour
// documented in the README without anything saying so.
//
// It is not safe for concurrent use and has no effect once Run has started.
func AddToolRegistrar(fn ToolRegistrar) {
	if fn == nil {
		panic("operation.AddToolRegistrar: nil registrar")
	}
	extraRegistrars = append(extraRegistrars, fn)
}

// registerExtraTools copies the tools of every registrar added through
// AddToolRegistrar into s, refusing a name s already has.
func registerExtraTools(s *server.MCPServer) error {
	for i, register := range extraRegistrars {
		scratch := server.NewMCPServer("forgejo-mcp-extension", "")
		register(scratch)

		extra := scratch.ListTools()
		names := make([]string, 0, len(extra))
		for name := range extra {
			names = append(names, name)
		}
		sort.Strings(names)

		tools := make([]server.ServerTool, 0, len(names))
		for _, name := range names {
			if s.GetTool(name) != nil {
				return fmt.Errorf("refusing to start: tool registrar %d registers %q, which is already registered", i, name)
			}
			tools = append(tools, *extra[name])
		}
		s.AddTools(tools...)
		log.Info("Registered extension tools",
			log.IntField("registrar", i),
			log.IntField("tools", len(tools)),
		)
	}
	return nil
}
