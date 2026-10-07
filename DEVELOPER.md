# Developer Guide

This guide covers building, developing, and contributing to the Forgejo MCP Server.

## Prerequisites

- Go 1.24 or later
- make (optional, for convenience commands)

## Building

### Using Make

```bash
make build          # Build the binary (outputs ./forgejo-mcp)
make vendor         # Tidy and verify Go module dependencies
```

### Using Go Directly

```bash
go build -v         # Build the binary
go mod tidy         # Tidy dependencies
```

## Running Locally

```bash
# stdio mode (for MCP client integration)
./forgejo-mcp --transport stdio --url https://forgejo.example.org --token <token>

# SSE mode (for HTTP-based clients)
./forgejo-mcp --transport sse --url https://forgejo.example.org --token <token> --sse-port 8080

# With debug logging
./forgejo-mcp --transport sse --url <url> --token <token> --debug
```

Environment variables: `FORGEJO_URL`, `FORGEJO_ACCESS_TOKEN`, `FORGEJO_DEBUG`, `FORGEJO_USER_AGENT`

CLI options: `--url`, `--token`, `--transport`, `--sse-port`, `--http-port`, `--user-agent`,
`--host`, `--allowed-hosts`, `--allowed-origins`, `--allow-operator-token-fallback`

The last four govern the `sse` and `http` transports. `--host` defaults to `127.0.0.1`
and binds both loopback families; a non-loopback value requires `--allowed-hosts` or the
server refuses to start. On these transports every request must carry its own
`Authorization` header unless `--allow-operator-token-fallback` is set. See the
"Exposing the server to a network" section of README.md.

## Architecture

This is an MCP (Model Context Protocol) server that exposes Forgejo API operations as tools for AI assistants.

### Core Flow

```
main.go → cmd/cmd.go (CLI parsing) → operation/operation.go (tool registration) → operation/{domain}/*.go (tool handlers)
```

### Directory Structure

| Directory | Purpose |
|-----------|---------|
| `cmd/` | CLI entry point and command parsing |
| `operation/` | MCP tool definitions and handlers, organized by domain |
| `operation/issue/` | Issue-related tools |
| `operation/pull/` | Pull request tools |
| `operation/release/` | Release and release-attachment tools |
| `operation/repo/` | Repository and branch tools |
| `operation/search/` | Search tools (users, repos, teams) |
| `operation/user/` | User info tools |
| `operation/version/` | Server version tool |
| `pkg/forgejo/` | Singleton Forgejo SDK client wrapper |
| `pkg/to/` | Response formatting helpers (`TextResult`, `ErrorResult`) |
| `pkg/params/` | Shared parameter descriptions for tool definitions |
| `pkg/flag/` | Global configuration state |
| `pkg/log/` | Structured logging utilities |

## Adding a New Tool

### Step 1: Create or Modify a Domain File

Tools are organized by domain in `operation/{domain}/`. Create a new file or add to an existing one.

### Step 2: Define the Tool

```go
package mydomain

import (
    "context"
    "fmt"

    "git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/forgejo"
    "git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/params"
    "git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/to"
    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mark3labs/mcp-go/server"
)

// Tool definition
var MyTool = mcp.NewTool(
    "my_tool_name",
    mcp.WithDescription("What this tool does"),
    mcp.WithString("owner", mcp.Required(), mcp.Description(params.Owner)),
    mcp.WithString("repo", mcp.Required(), mcp.Description(params.Repo)),
    mcp.WithNumber("limit", mcp.Description("Page size"), mcp.DefaultNumber(20)),

    // Annotations (required — see rubric below)
    mcp.WithTitleAnnotation("My tool"),
    mcp.WithReadOnlyHintAnnotation(false),
    mcp.WithDestructiveHintAnnotation(false),
    mcp.WithIdempotentHintAnnotation(true),
    mcp.WithOpenWorldHintAnnotation(false),
)

// Handler function
func MyToolFn(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // Extract parameters (numbers come as float64)
    owner, _ := req.Params.Arguments["owner"].(string)
    repo, _ := req.Params.Arguments["repo"].(string)
    limit, _ := req.Params.Arguments["limit"].(float64)

    // Call Forgejo API
    result, _, err := forgejo.Client().SomeMethod(owner, repo, int(limit))
    if err != nil {
        return to.ErrorResult(fmt.Errorf("operation failed: %v", err))
    }

    // Return formatted result
    return to.TextResult(result)
}
```

#### Handler Errors Surface In-Band

Handlers return errors via `to.ErrorResult` and that is all they need to do.
The `InBandToolErrors` server middleware (see `operation/operation.go`)
converts every handler error into a `CallToolResult` with `IsError=true`
carrying the error text. Without it, mcp-go answers a failed tool call with
the JSON-RPC protocol error -32603 ("Server internal error"), which hides the
actionable message — a forge 404 because the repo has Actions disabled, say —
from the client. Do not return validation or API failures as anything other
than a handler error, and do not build `NewToolResultError` results by hand in
handlers.

#### Tool Annotations Are Required

Every `mcp.NewTool()` MUST declare accurate annotations. They are hints, but
clients act on them: plan mode in ZCode/Claude Code only runs tools marked
`readOnlyHint=true`. Worse, `mcp-go`'s `NewTool` silently pre-fills
`readOnlyHint=false, destructiveHint=true` — the spec defaults — so an
unannotated tool advertises itself as a destructive write.

Classification rubric:

| Class | Hints | Typical tools |
| ----- | ----- | ------------- |
| Read-only | `readOnly=true, destructive=false, idempotent=true` | `list_*`, `get_*`, `search_*`, `download_*_attachment`, `check_*` |
| Mutating | `readOnly=false, destructive=false` | `create_*`, `edit_*`/`update_*`, `add_*`, `remove_*`, `set_*`, `fork_repo`, `issue_state_change`, stopwatch controls |
| Destructive | `readOnly=false, destructive=true, idempotent=false` | `delete_*`, `reset_issue_time`, `merge_pull_request`, `create_or_update_*_action_secret` |

Rules that cut across the classes:

- `openWorldHint=false` on every tool — all tools target the single
  configured Forgejo instance.
- `idempotentHint=true` on a mutating tool only when replaying the same call
  has no additional effect: in-place edits, label add/remove, mark-as-read,
  same-state transitions. `false` for creates, comments, uploads, merges,
  and git-content writes (`create_file`, `update_file`, `update_wiki_page` —
  a replay can create another commit).
- `destructiveHint=true` is for operations that may destroy or irrecoverably
  lose data. Three shapes qualify:
  - removals — `delete_*`, `reset_issue_time`;
  - unrecoverable overwrites — `create_or_update_*_action_secret`: Forgejo
    never returns a secret's value, so overwriting one loses the old value
    for good, and the tool cannot tell a create from an overwrite;
    `destructive=true` is what makes clients ask for confirmation here, and
    the class pairs it with `idempotent=false` — deliberately conservative
    for a secrets write, even though replaying identical arguments would
    converge;
  - hard-to-undo writes — `merge_pull_request`: undoing a merge means a new
    revert commit re-touching main (and `delete_branch_after_merge` can drop
    the head branch entirely), and clients that gate confirmation on
    `destructiveHint` should prompt before merging; it is non-idempotent
    because replaying on an already-merged PR errors, and an error is not a
    side effect.
  Overwriting ordinary state in place — `edit_*`, `set_repo_topics` — stays
  mutating, not destructive: the tool's subject survives and the same
  arguments reach the same state.
- Add the tool to the golden lists in
  [operation/tool_annotations_test.go](operation/tool_annotations_test.go)
  (class list, plus `wantMutatingIdempotent` if replay-safe). The test fails
  on an unclassified tool or hints that contradict the class, which keeps the
  annotation layer honest as tools evolve.

### Step 3: Register the Tool

Add registration in the domain's file:

```go
func RegisterTool(s *server.MCPServer) {
    s.AddTool(MyTool, MyToolFn)
}
```

### Step 4: Wire Up New Domains

If you created a new domain, import and register it in `operation/operation.go`:

```go
import "git.b4mad.industries/agentic-forges/forgejo-mcp/v3/operation/mydomain"

func RegisterTools(s *server.MCPServer) {
    // ... existing registrations
    mydomain.RegisterTool(s)
}
```

## Key Patterns

### Parameter Handling

- String parameters: `value, _ := req.Params.Arguments["param"].(string)`
- Number parameters: `value, _ := req.Params.Arguments["num"].(float64)` (always float64)
- Optional with defaults: Check if value exists before using

### Response Formatting

Use helpers from `pkg/to/`:

```go
// Success response
return to.TextResult(data)

// Error response
return to.ErrorResult(fmt.Errorf("something went wrong: %v", err))
```

### Shared Parameter Descriptions

Reuse descriptions from `pkg/params/` for consistency:

```go
mcp.WithString("owner", mcp.Required(), mcp.Description(params.Owner))
mcp.WithString("repo", mcp.Required(), mcp.Description(params.Repo))
mcp.WithNumber("page", mcp.Description(params.Page), mcp.DefaultNumber(1))
```

## Dependencies

| Package | Purpose |
|---------|---------|
| `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v2` | Forgejo API client |
| `github.com/mark3labs/mcp-go` | MCP protocol implementation |
| `github.com/spf13/cobra` | CLI framework |

## Testing

Run with debug mode to troubleshoot issues:

```bash
FORGEJO_DEBUG=true ./forgejo-mcp --transport stdio --url <url> --token <token>
```

## OpenSpec conventions

Change deltas live in `openspec/changes/<change>/specs/<capability>/spec.md` and
are gated by `openspec validate --all --strict` in CI
(`.tekton/tasks/openspec-validate.yaml`).

**Requirement statements go on one unwrapped line.** `openspec` only inspects the
first physical line of a requirement paragraph when it looks for `SHALL`/`MUST`,
so a hard-wrapped statement is rejected with
`ADDED "<name>" must contain SHALL or MUST` even when the rest of the paragraph
is full of `SHALL`s.

```markdown
### Requirement: Attachment upload source

The `create_issue_attachment` and `create_release_attachment` tools SHALL accept optional string arguments `content` and `file_path` and SHALL require exactly one argument key to be present.
```

`.editorconfig` disables `max_line_length` under `openspec/` so editors do not
re-wrap these paragraphs. Run the gate locally before pushing:

```bash
scripts/ci/openspec-validate.sh
```

It also runs as a pre-commit hook (`openspec-validate`) and skips with a warning
if `openspec` is not installed.

## Reviewing a pull request: confirm CI actually ran

Pipelines-as-Code will not start a PipelineRun for an author who is not listed
in `OWNERS`. When it declines, it leaves exactly one commit status on the head
SHA:

```
op1st Pipelines as Code   pending   Pending approval, waiting for an /ok-to-test
```

There are no red checks — because there are no checks. **"Not red" is not
"green."** A maintainer must comment `/ok-to-test` on the PR before any
pipeline runs.

Do not check this by eye. Run the gate:

```bash
scripts/ci/check-pr-ci-ran.sh <pr-number>
scripts/ci/check-pr-ci-ran.sh --sha <commit-sha>
```

It reads the forge's combined-status API for the PR head and exits non-zero
when CI never ran, is still awaiting `/ok-to-test`, or a pipeline reported a
non-success context. It skips with a warning if `curl` or `jq` is missing. Set
`FORGEJO_URL` for another instance and `FORGEJO_TOKEN` for a private repo.

This is a reviewer-side gate, not a pre-commit hook: no local file change
implies it, so there is nothing for `pre-commit` to key on.

### A permanently-red PR is not necessarily a failing one

Pipelines-as-Code reports under two kinds of context:

```
op1st Pipelines as Code                          its own gating slot
op1st Pipelines as Code / forgejo-mcp-code-scans one actual PipelineRun
```

When PaC fails *before* creating any PipelineRun — a `.tekton` manifest that
will not parse is the usual cause — it writes a failure into the gating slot
and stops. A later successful retest creates the named run contexts, but
nothing owns the gating slot, so that failure is never overwritten. The forge
keeps reporting the commit as `aggregate=failure` even though every pipeline
that exists passed, and **only a new head SHA clears it** — retesting cannot,
however many times you try.

Observed on PR #491 at `16ec840`:

```
failure  op1st Pipelines as Code                                2026-08-13T23:19:10Z
success  op1st Pipelines as Code / forgejo-mcp-code-scans       2026-08-13T23:27:53Z
success  op1st Pipelines as Code / forgejo-mcp-on-pull-request  2026-08-13T23:29:43Z
```

This is upstream PaC behaviour, not something this repo can fix. The guard
therefore decides pass/fail from the named run contexts alone and reports the
gating slot without counting it: a red gating slot alongside green runs prints
a warning and still exits 0, and the warning says whether the failure predates
the newest run (almost certainly stale) or not (treat as live). A gating
failure with *no* run contexts at all still exits 1 — there, the empty gating
slot is the only evidence there is, and it says nothing ran.

The warning is worth reading rather than dismissing. A manifest that failed to
parse has no run context to be red, so green runs are not proof that every
intended pipeline ran — only that the ones which started passed.

## Blocked Features

Some planned features are blocked on upstream API or SDK support:

| Feature | Status | Details |
|---------|--------|---------|
| Wiki support | Blocked | Waiting for forgejo-sdk wiki API |
| Projects/Kanban | Blocked | Requires Gitea 1.26.0 API |

See `docs/plans/` for detailed status:
- `wiki-support.md` - Wiki API implementation plan
- `projects-support.md` - Projects/Kanban implementation plan

## Contributing

1. Fork the repository on Codeberg
2. Create a feature branch
3. Make your changes following the patterns above
4. Test locally with both stdio and SSE modes
5. Submit a pull request

### Code Style

- Follow standard Go conventions
- Use meaningful variable names
- Add tool descriptions that clearly explain what each tool does
- Reuse parameter descriptions from `pkg/params/` where applicable
