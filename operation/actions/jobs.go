package actions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codeberg.org/goern/forgejo-mcp/v2/operation/params"
	"codeberg.org/goern/forgejo-mcp/v2/pkg/flag"
	"codeberg.org/goern/forgejo-mcp/v2/pkg/log"
	"codeberg.org/goern/forgejo-mcp/v2/pkg/to"

	"github.com/mark3labs/mcp-go/mcp"
)

// These two tools use the Forgejo REST endpoints introduced after
// the SDK we vendor was last cut, so they bypass the SDK and call
// the API over plain HTTP using the same base URL + token the SDK
// is initialised with. Once the SDK gains coverage for these
// endpoints, switch to the typed methods.

const (
	ListWorkflowRunJobsToolName   = "list_workflow_run_jobs"
	GetWorkflowRunJobLogsToolName = "get_workflow_run_job_logs"
)

var (
	ListWorkflowRunJobsTool = mcp.NewTool(
		ListWorkflowRunJobsToolName,
		mcp.WithDescription("List jobs of a specific workflow run (Forgejo 16+ API). Returns job names, statuses, and 1-based indexes usable with get_workflow_run_job_logs."),
		mcp.WithString("owner", mcp.Required(), mcp.Description(params.Owner)),
		mcp.WithString("repo", mcp.Required(), mcp.Description(params.Repo)),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description(params.RunID)),
	)

	GetWorkflowRunJobLogsTool = mcp.NewTool(
		GetWorkflowRunJobLogsToolName,
		mcp.WithDescription("Fetch the plain-text log of a specific job in a workflow run. Job is addressed by its 1-based index within the run as returned by list_workflow_run_jobs (jobs[0] is index 1, jobs[1] is index 2, etc). Optional `tail` parameter trims the output to the last N characters — useful when the full log is large but you only need the failure tail."),
		mcp.WithString("owner", mcp.Required(), mcp.Description(params.Owner)),
		mcp.WithString("repo", mcp.Required(), mcp.Description(params.Repo)),
		mcp.WithNumber("run_id", mcp.Required(), mcp.Description(params.RunID)),
		mcp.WithNumber("job_index", mcp.Required(), mcp.Description("1-based index of the job within the run, matching the order returned by list_workflow_run_jobs")),
		mcp.WithNumber("tail", mcp.Description("If set and >0, return only the last N characters of the log instead of the full file")),
	)
)

// rawAPIRequest issues a single authenticated GET to the configured
// Forgejo instance. Returns the raw response body bytes so callers
// can do their own parsing or trimming.
func rawAPIRequest(ctx context.Context, path string) ([]byte, int, error) {
	base := strings.TrimRight(flag.URL, "/")
	url := base + "/api/v1" + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if flag.Token != "" {
		req.Header.Set("Authorization", "token "+flag.Token)
	}
	req.Header.Set("Accept", "*/*")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func ListWorkflowRunJobsFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Debugf("Called ListWorkflowRunJobsFn")

	owner, ok := req.GetArguments()["owner"].(string)
	if !ok || owner == "" {
		return to.ErrorResult(errors.New("owner is required"))
	}
	repo, ok := req.GetArguments()["repo"].(string)
	if !ok || repo == "" {
		return to.ErrorResult(errors.New("repo is required"))
	}
	runIDFloat, err := to.Float64(req.GetArguments()["run_id"])
	if err != nil {
		return to.ErrorResult(errors.New("run_id is required"))
	}
	runID := int64(runIDFloat)
	if runID <= 0 {
		return to.ErrorResult(errors.New("run_id must be a positive integer"))
	}

	body, status, err := rawAPIRequest(ctx,
		fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", owner, repo, runID))
	if err != nil {
		return to.ErrorResult(fmt.Errorf("failed to list jobs: %w", err))
	}
	if status != http.StatusOK {
		return to.ErrorResult(fmt.Errorf("Forgejo returned %d: %s", status, string(body)))
	}

	// The /jobs endpoint returns a JSON array of ActionRunJob objects.
	// Pass the raw JSON through — the LLM consumer gets full structure.
	return to.TextResult(string(body))
}

func GetWorkflowRunJobLogsFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	log.Debugf("Called GetWorkflowRunJobLogsFn")

	owner, ok := req.GetArguments()["owner"].(string)
	if !ok || owner == "" {
		return to.ErrorResult(errors.New("owner is required"))
	}
	repo, ok := req.GetArguments()["repo"].(string)
	if !ok || repo == "" {
		return to.ErrorResult(errors.New("repo is required"))
	}
	runIDFloat, err := to.Float64(req.GetArguments()["run_id"])
	if err != nil {
		return to.ErrorResult(errors.New("run_id is required"))
	}
	runID := int64(runIDFloat)
	if runID <= 0 {
		return to.ErrorResult(errors.New("run_id must be a positive integer"))
	}
	jobIdxFloat, err := to.Float64(req.GetArguments()["job_index"])
	if err != nil {
		return to.ErrorResult(errors.New("job_index is required"))
	}
	jobIdx := int64(jobIdxFloat)
	if jobIdx < 1 {
		return to.ErrorResult(errors.New("job_index must be a positive integer (1-based)"))
	}

	body, status, err := rawAPIRequest(ctx,
		fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs/%d/logs", owner, repo, runID, jobIdx))
	if err != nil {
		return to.ErrorResult(fmt.Errorf("failed to fetch logs: %w", err))
	}
	if status != http.StatusOK {
		return to.ErrorResult(fmt.Errorf("Forgejo returned %d: %s", status, string(body)))
	}

	out := string(body)

	// Optional tail trimming — useful when the full log is huge but
	// only the failure ending is interesting. Counts characters, not
	// lines, since logs can have very long lines (cmake output etc).
	if t, err := to.Float64(req.GetArguments()["tail"]); err == nil && int(t) > 0 {
		tailN := int(t)
		if tailN < len(out) {
			out = "...[truncated, showing last " + fmt.Sprintf("%d", tailN) + " chars]...\n" + out[len(out)-tailN:]
		}
	}

	return to.TextResult(out)
}
