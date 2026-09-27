// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	forgejo_flag "git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/flag"

	"github.com/mark3labs/mcp-go/mcp"
)

const testSourceBranchSHA = "0123456789abcdef0123456789abcdef01234567"

type capturedDiffPatchRequest struct {
	Branch    string `json:"branch"`
	NewBranch string `json:"new_branch"`
	Message   string `json:"message"`
	Content   string `json:"content"`
	SHA       string `json:"sha"`
}

type diffPatchServerConfig struct {
	branchStatus   int
	branchBody     string
	mutationStatus int
	mutationBody   string
}

type diffPatchServerCapture struct {
	lookups      int
	mutations    int
	lookupPath   string
	mutationBody []byte
}

func setupDiffPatchServer(t *testing.T, config diffPatchServerConfig) *diffPatchServerCapture {
	t.Helper()

	oldURL := forgejo_flag.URL
	oldToken := forgejo_flag.Token
	capture := &diffPatchServerCapture{}

	if config.branchStatus == 0 {
		config.branchStatus = http.StatusOK
	}
	if config.branchBody == "" {
		config.branchBody = fmt.Sprintf(`{"name":"main","commit":{"id":"%s"}}`, testSourceBranchSHA)
	}
	if config.mutationStatus == 0 {
		config.mutationStatus = http.StatusCreated
	}
	if config.mutationBody == "" {
		config.mutationBody = `{"commit":{"sha":"commit123","html_url":"https://forge.example/commit/commit123"},"content":null}`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token test-token" {
			t.Errorf("Authorization = %q, want token test-token", got)
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/testowner/testrepo/branches/main":
			capture.lookups++
			capture.lookupPath = r.URL.Path
			w.WriteHeader(config.branchStatus)
			_, _ = fmt.Fprint(w, config.branchBody)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/testowner/testrepo/diffpatch":
			capture.mutations++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading mutation body: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			capture.mutationBody = body
			w.WriteHeader(config.mutationStatus)
			_, _ = fmt.Fprint(w, config.mutationBody)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	forgejo_flag.URL = srv.URL
	forgejo_flag.Token = "test-token"
	t.Cleanup(func() {
		srv.Close()
		forgejo_flag.URL = oldURL
		forgejo_flag.Token = oldToken
	})

	return capture
}

func validApplyDiffPatchArgs(patch string) map[string]any {
	return map[string]any{
		"owner":           "testowner",
		"repo":            "testrepo",
		"message":         "apply patch",
		"branch_name":     "main",
		"new_branch_name": "feature/patched",
		"content":         patch,
	}
}

func validUnifiedDiff() string {
	return "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"
}

func multiFileUnifiedDiff() string {
	return validUnifiedDiff() + "diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-before\n+after\n"
}

func TestApplyDiffPatchFnResolvesSourceBranchAndSendsOneNativeMutation(t *testing.T) {
	capture := setupDiffPatchServer(t, diffPatchServerConfig{})
	patch := multiFileUnifiedDiff()

	result, err := ApplyDiffPatchFn(context.Background(), newCallToolRequest(validApplyDiffPatchArgs(patch)))
	if err != nil {
		t.Fatalf("ApplyDiffPatchFn returned error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("ApplyDiffPatchFn returned an error result")
	}
	if capture.lookups != 1 {
		t.Fatalf("branch lookups = %d, want exactly 1", capture.lookups)
	}
	if capture.mutations != 1 {
		t.Fatalf("native mutations = %d, want exactly 1", capture.mutations)
	}
	if capture.lookupPath != "/api/v1/repos/testowner/testrepo/branches/main" {
		t.Fatalf("lookup path = %q, want source branch main", capture.lookupPath)
	}

	var body capturedDiffPatchRequest
	if err := json.Unmarshal(capture.mutationBody, &body); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if body.Branch != "main" {
		t.Errorf("branch = %q, want main", body.Branch)
	}
	if body.NewBranch != "feature/patched" {
		t.Errorf("new_branch = %q, want feature/patched", body.NewBranch)
	}
	if body.Message != "apply patch" {
		t.Errorf("message = %q, want apply patch", body.Message)
	}
	if body.Content != patch {
		t.Errorf("content was changed before Forgejo request")
	}
	if body.SHA != testSourceBranchSHA {
		t.Errorf("sha = %q, want exact source branch commit %q", body.SHA, testSourceBranchSHA)
	}

	var raw map[string]any
	if err := json.Unmarshal(capture.mutationBody, &raw); err != nil {
		t.Fatalf("unmarshal request map: %v", err)
	}
	for _, forbidden := range []string{"files", "expected_head", "expected_sha", "force_push"} {
		if _, exists := raw[forbidden]; exists {
			t.Errorf("request unexpectedly contains %q", forbidden)
		}
	}
}

func TestApplyDiffPatchFnProjectsNativeCommitIdentity(t *testing.T) {
	capture := setupDiffPatchServer(t, diffPatchServerConfig{
		mutationBody: `{"commit":{"sha":"native-sha","html_url":"https://forge.example/commit/native-sha"},"content":null}`,
	})

	result, err := ApplyDiffPatchFn(context.Background(), newCallToolRequest(validApplyDiffPatchArgs(validUnifiedDiff())))
	if err != nil {
		t.Fatalf("ApplyDiffPatchFn returned error: %v", err)
	}
	if capture.lookups != 1 || capture.mutations != 1 {
		t.Fatalf("lookups/mutations = %d/%d, want 1/1", capture.lookups, capture.mutations)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("tool result content = %#v, want one text item", result)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("result content type = %T, want mcp.TextContent", result.Content[0])
	}
	var envelope struct {
		Result applyDiffPatchResult `json:"Result"`
	}
	if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
		t.Fatalf("unmarshal result envelope: %v", err)
	}
	if envelope.Result.CommitSHA != "native-sha" {
		t.Errorf("commit_sha = %q, want native-sha", envelope.Result.CommitSHA)
	}
	if envelope.Result.CommitURL != "https://forge.example/commit/native-sha" {
		t.Errorf("commit_url = %q, want native response URL", envelope.Result.CommitURL)
	}
}

func TestApplyDiffPatchFnRejectsMissingOrStructurallyInvalidInputsBeforeLookup(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing owner", mutate: func(args map[string]any) { delete(args, "owner") }},
		{name: "blank owner", mutate: func(args map[string]any) { args["owner"] = " " }},
		{name: "missing repo", mutate: func(args map[string]any) { delete(args, "repo") }},
		{name: "blank repo", mutate: func(args map[string]any) { args["repo"] = "" }},
		{name: "missing message", mutate: func(args map[string]any) { delete(args, "message") }},
		{name: "blank message", mutate: func(args map[string]any) { args["message"] = " " }},
		{name: "missing branch", mutate: func(args map[string]any) { delete(args, "branch_name") }},
		{name: "blank branch", mutate: func(args map[string]any) { args["branch_name"] = " " }},
		{name: "missing content", mutate: func(args map[string]any) { delete(args, "content") }},
		{name: "non-string content", mutate: func(args map[string]any) { args["content"] = 42 }},
		{name: "non-string new branch", mutate: func(args map[string]any) { args["new_branch_name"] = 42 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := setupDiffPatchServer(t, diffPatchServerConfig{})
			args := validApplyDiffPatchArgs(validUnifiedDiff())
			test.mutate(args)

			result, err := ApplyDiffPatchFn(context.Background(), newCallToolRequest(args))
			if err == nil {
				t.Fatalf("expected validation error, got result %#v", result)
			}
			if capture.lookups != 0 || capture.mutations != 0 {
				t.Fatalf("lookups/mutations = %d/%d, want 0/0 after local validation failure", capture.lookups, capture.mutations)
			}
		})
	}
}

func TestApplyDiffPatchFnFailsClosedWhenSourceBranchCannotBeResolved(t *testing.T) {
	tests := []struct {
		name         string
		branchStatus int
		branchBody   string
	}{
		{name: "not found", branchStatus: http.StatusNotFound, branchBody: `{"message":"branch not found"}`},
		{name: "authorization", branchStatus: http.StatusForbidden, branchBody: `{"message":"forbidden"}`},
		{name: "upstream error", branchStatus: http.StatusInternalServerError, branchBody: `{"message":"failure"}`},
		{name: "missing commit", branchStatus: http.StatusOK, branchBody: `{"name":"main"}`},
		{name: "null commit", branchStatus: http.StatusOK, branchBody: `{"name":"main","commit":null}`},
		{name: "empty commit id", branchStatus: http.StatusOK, branchBody: `{"name":"main","commit":{"id":""}}`},
		{name: "blank commit id", branchStatus: http.StatusOK, branchBody: `{"name":"main","commit":{"id":"   "}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := setupDiffPatchServer(t, diffPatchServerConfig{
				branchStatus: test.branchStatus,
				branchBody:   test.branchBody,
			})

			result, err := ApplyDiffPatchFn(context.Background(), newCallToolRequest(validApplyDiffPatchArgs(validUnifiedDiff())))
			if err == nil {
				t.Fatalf("expected source-branch resolution error, got result %#v", result)
			}
			if capture.lookups != 1 {
				t.Fatalf("branch lookups = %d, want 1", capture.lookups)
			}
			if capture.mutations != 0 {
				t.Fatalf("native mutations = %d, want 0 after source-branch lookup failure", capture.mutations)
			}
		})
	}
}

func TestApplyDiffPatchFnLeavesPatchAndPolicySemanticsToForgejoWithoutFallback(t *testing.T) {
	tests := []struct {
		name           string
		patch          string
		mutationStatus int
		response       string
	}{
		{name: "malformed patch", patch: "not a unified diff", mutationStatus: http.StatusUnprocessableEntity, response: `{"message":"malformed patch"}`},
		{name: "empty patch", patch: "", mutationStatus: http.StatusUnprocessableEntity, response: `{"message":"empty patch"}`},
		{name: "context mismatch", patch: validUnifiedDiff(), mutationStatus: http.StatusUnprocessableEntity, response: `{"message":"patch does not apply"}`},
		{name: "authorization rejection", patch: validUnifiedDiff(), mutationStatus: http.StatusForbidden, response: `{"message":"forbidden"}`},
		{name: "branch protection rejection", patch: validUnifiedDiff(), mutationStatus: http.StatusForbidden, response: `{"message":"protected branch"}`},
		{name: "git rejection", patch: validUnifiedDiff(), mutationStatus: http.StatusConflict, response: `{"message":"git rejected patch"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := setupDiffPatchServer(t, diffPatchServerConfig{
				mutationStatus: test.mutationStatus,
				mutationBody:   test.response,
			})
			args := validApplyDiffPatchArgs(test.patch)

			result, err := ApplyDiffPatchFn(context.Background(), newCallToolRequest(args))
			if err == nil {
				t.Fatalf("expected Forgejo rejection, got result %#v", result)
			}
			if capture.lookups != 1 || capture.mutations != 1 {
				t.Fatalf("lookups/mutations = %d/%d, want exactly 1/1 and no fallback mutation", capture.lookups, capture.mutations)
			}

			var body capturedDiffPatchRequest
			if err := json.Unmarshal(capture.mutationBody, &body); err != nil {
				t.Fatalf("unmarshal captured mutation body: %v", err)
			}
			if body.Content != test.patch {
				t.Errorf("Forgejo-owned patch content was changed locally")
			}
			if body.SHA != testSourceBranchSHA {
				t.Errorf("sha = %q, want source branch identity %q", body.SHA, testSourceBranchSHA)
			}
		})
	}
}

func TestApplyDiffPatchFnWithoutNewBranchOmitsNativeNewBranch(t *testing.T) {
	capture := setupDiffPatchServer(t, diffPatchServerConfig{})
	args := validApplyDiffPatchArgs(validUnifiedDiff())
	delete(args, "new_branch_name")

	result, err := ApplyDiffPatchFn(context.Background(), newCallToolRequest(args))
	if err != nil || result == nil || result.IsError {
		t.Fatalf("ApplyDiffPatchFn returned result=%#v err=%v", result, err)
	}
	if capture.lookups != 1 || capture.mutations != 1 {
		t.Fatalf("lookups/mutations = %d/%d, want 1/1", capture.lookups, capture.mutations)
	}
	var raw map[string]any
	if err := json.Unmarshal(capture.mutationBody, &raw); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if _, exists := raw["new_branch"]; exists {
		t.Fatalf("native request unexpectedly contains new_branch")
	}
	if got, ok := raw["sha"].(string); !ok || got != testSourceBranchSHA {
		t.Fatalf("native sha = %#v, want exact source branch identity", raw["sha"])
	}
}
