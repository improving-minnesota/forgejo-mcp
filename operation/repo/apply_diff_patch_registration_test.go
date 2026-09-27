// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

func TestRegisterToolIncludesApplyDiffPatchWithAdditiveSchema(t *testing.T) {
	s := server.NewMCPServer("apply-diff-patch-test", "0.0.0")
	RegisterTool(s)

	registered := s.GetTool(ApplyDiffPatchToolName)
	if registered == nil {
		t.Fatalf("%s is not registered", ApplyDiffPatchToolName)
	}
	if registered.Handler == nil {
		t.Fatalf("%s has no handler", ApplyDiffPatchToolName)
	}

	for _, existing := range []string{CreateFileToolName, UpdateFileToolName, DeleteFileToolName} {
		if s.GetTool(existing) == nil {
			t.Fatalf("existing repository write tool %s is not registered", existing)
		}
	}

	raw, err := json.Marshal(registered.Tool)
	if err != nil {
		t.Fatalf("marshal registered tool: %v", err)
	}
	var document struct {
		Name        string `json:"name"`
		InputSchema struct {
			Type       string                     `json:"type"`
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal registered tool: %v", err)
	}
	if document.Name != ApplyDiffPatchToolName {
		t.Fatalf("serialized tool name = %q, want %q", document.Name, ApplyDiffPatchToolName)
	}
	if document.InputSchema.Type != "object" {
		t.Fatalf("input schema type = %q, want object", document.InputSchema.Type)
	}

	for _, required := range []string{"owner", "repo", "message", "branch_name", "content"} {
		if !containsDiffPatchString(document.InputSchema.Required, required) {
			t.Errorf("input schema required fields %v do not include %q", document.InputSchema.Required, required)
		}
	}
	if containsDiffPatchString(document.InputSchema.Required, "new_branch_name") {
		t.Errorf("new_branch_name must remain optional")
	}
	for _, forbidden := range []string{
		"files",
		"sha",
		"expected_head",
		"expected_sha",
		"cas_token",
		"revision_token",
		"force_push",
	} {
		if _, exists := document.InputSchema.Properties[forbidden]; exists {
			t.Errorf("input schema unexpectedly contains %q", forbidden)
		}
	}
}

func containsDiffPatchString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
