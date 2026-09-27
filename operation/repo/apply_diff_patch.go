// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"git.b4mad.industries/agentic-forges/forgejo-mcp/v3/operation/params"
	"git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/forgejo"
	"git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/log"
	"git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/to"

	"github.com/mark3labs/mcp-go/mcp"
)

const ApplyDiffPatchToolName = "apply_diff_patch"

var ApplyDiffPatchTool = mcp.NewTool(
	ApplyDiffPatchToolName,
	mcp.WithDescription("Apply a textual unified diff after resolving the source branch, then perform exactly one native Forgejo diff-patch repository write. The resolved branch commit is internal transport data for Forgejo's required sha field; it is not expected-HEAD/CAS semantics. Forgejo owns patch parsing, authorization, branch protection, Git validation, and commit creation."),
	mcp.WithString("owner", mcp.Required(), mcp.Description(params.Owner)),
	mcp.WithString("repo", mcp.Required(), mcp.Description(params.Repo)),
	mcp.WithString("message", mcp.Required(), mcp.Description(params.Message)),
	mcp.WithString("branch_name", mcp.Required(), mcp.Description(params.BranchName)),
	mcp.WithString("new_branch_name", mcp.Description(params.NewBranchName)),
	mcp.WithString("content", mcp.Required(), mcp.Description("Textual unified diff patch forwarded unchanged to Forgejo's native diffpatch endpoint.")),
)

type applyDiffPatchOptions struct {
	BranchName    string `json:"branch"`
	NewBranchName string `json:"new_branch,omitempty"`
	Message       string `json:"message"`
	Content       string `json:"content"`
	SHA           string `json:"sha"`
}

type sourceBranchResponse struct {
	Commit *struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type nativeDiffPatchResponse struct {
	Commit *struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	} `json:"commit"`
}

type applyDiffPatchResult struct {
	CommitSHA string `json:"commit_sha,omitempty"`
	CommitURL string `json:"commit_url,omitempty"`
}

func ApplyDiffPatchFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Debugf("Called ApplyDiffPatchFn")

	owner, repo, options, err := parseApplyDiffPatchArguments(req.GetArguments())
	if err != nil {
		return to.ErrorResult(err)
	}

	transportSHA, err := resolveDiffPatchSourceBranch(ctx, owner, repo, options.BranchName)
	if err != nil {
		return to.ErrorResult(err)
	}
	options.SHA = transportSHA

	var response nativeDiffPatchResponse
	path := forgejo.APIPath("repos", owner, repo, "diffpatch")
	if err := forgejo.DoJSON(ctx, http.MethodPost, path, options, &response); err != nil {
		return to.ErrorResult(fmt.Errorf("apply diff patch error: %w", err))
	}

	result := applyDiffPatchResult{}
	if response.Commit != nil {
		result.CommitSHA = response.Commit.SHA
		result.CommitURL = response.Commit.HTMLURL
	}
	return to.TextResult(result)
}

func resolveDiffPatchSourceBranch(ctx context.Context, owner, repo, branchName string) (string, error) {
	var response sourceBranchResponse
	path := forgejo.APIPath("repos", owner, repo, "branches", branchName)
	if err := forgejo.DoJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return "", fmt.Errorf("resolve diff patch source branch error: %w", err)
	}
	if response.Commit == nil || strings.TrimSpace(response.Commit.ID) == "" {
		return "", fmt.Errorf("resolve diff patch source branch error: branch %q has no usable commit identity", branchName)
	}
	return response.Commit.ID, nil
}

func parseApplyDiffPatchArguments(args map[string]any) (string, string, applyDiffPatchOptions, error) {
	owner, err := requiredDiffPatchNonBlankString(args, "owner")
	if err != nil {
		return "", "", applyDiffPatchOptions{}, err
	}
	repo, err := requiredDiffPatchNonBlankString(args, "repo")
	if err != nil {
		return "", "", applyDiffPatchOptions{}, err
	}
	message, err := requiredDiffPatchNonBlankString(args, "message")
	if err != nil {
		return "", "", applyDiffPatchOptions{}, err
	}
	branchName, err := requiredDiffPatchNonBlankString(args, "branch_name")
	if err != nil {
		return "", "", applyDiffPatchOptions{}, err
	}
	content, err := requiredPatchString(args, "content")
	if err != nil {
		return "", "", applyDiffPatchOptions{}, err
	}

	newBranchName := ""
	if raw, ok := args["new_branch_name"]; ok {
		value, ok := raw.(string)
		if !ok {
			return "", "", applyDiffPatchOptions{}, fmt.Errorf("new_branch_name must be a string")
		}
		newBranchName = value
	}

	return owner, repo, applyDiffPatchOptions{
		BranchName:    branchName,
		NewBranchName: newBranchName,
		Message:       message,
		Content:       content,
	}, nil
}

func requiredDiffPatchNonBlankString(values map[string]any, name string) (string, error) {
	raw, ok := values[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be blank", name)
	}
	return value, nil
}

func requiredPatchString(values map[string]any, name string) (string, error) {
	raw, ok := values[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}
