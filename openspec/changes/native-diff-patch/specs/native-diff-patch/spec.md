## ADDED Requirements

### Requirement: Native diff-patch repository write

The MCP server SHALL expose an `apply_diff_patch` tool that applies a textual
unified diff through Forgejo's native repository diff-patch endpoint.

#### Scenario: Apply a patch to an existing branch

- **WHEN** a caller supplies an owner, repository, commit message, source branch,
  and textual unified diff
- **THEN** the server SHALL resolve the source branch commit identity
- **AND** SHALL invoke Forgejo's native diff-patch endpoint exactly once
- **AND** SHALL send the resolved commit identity as Forgejo's required transport
  `sha`
- **AND** SHALL forward the textual unified diff unchanged

#### Scenario: Apply a patch to a new branch

- **WHEN** a caller additionally supplies `new_branch_name`
- **THEN** the server SHALL forward that branch name to the native diff-patch
  endpoint
- **AND** SHALL otherwise preserve the same source-branch resolution and native
  mutation semantics

### Requirement: Transport SHA is not caller-visible concurrency control

The server SHALL treat the source branch commit identity used for Forgejo's
required `sha` field as internal transport data and SHALL NOT expose it as
caller-controlled expected-HEAD or compare-and-swap semantics.

#### Scenario: Public tool schema

- **WHEN** the `apply_diff_patch` tool schema is inspected
- **THEN** it SHALL require `owner`, `repo`, `message`, `branch_name`, and `content`
- **AND** MAY expose `new_branch_name`
- **AND** SHALL NOT expose `sha`, `expected_head`, `expected_sha`, `cas_token`,
  `revision_token`, `force_push`, or a client-side `files` mutation model

### Requirement: Source branch resolution fails closed

The server SHALL NOT perform the native diff-patch mutation when the source
branch cannot be resolved to a usable commit identity.

#### Scenario: Source branch lookup fails

- **WHEN** Forgejo rejects or fails the source branch lookup
- **THEN** the server SHALL return an error
- **AND** SHALL NOT invoke the native diff-patch mutation

#### Scenario: Source branch has no usable commit identity

- **WHEN** the source branch response contains no non-blank commit identity
- **THEN** the server SHALL return an error
- **AND** SHALL NOT invoke the native diff-patch mutation

### Requirement: Forgejo owns patch and repository policy semantics

The server SHALL delegate patch parsing, authorization, branch protection, Git
validation, and commit creation to Forgejo and SHALL NOT perform a fallback
repository mutation when Forgejo rejects the native diff-patch request.

#### Scenario: Forgejo rejects the mutation

- **WHEN** Forgejo rejects the native diff-patch request because the patch is
  malformed or does not apply, authorization fails, branch protection rejects
  the write, or Git rejects the operation
- **THEN** the server SHALL return the Forgejo operation failure
- **AND** SHALL NOT attempt an alternative repository mutation

### Requirement: Native commit result projection

The server SHALL project the commit identity returned by Forgejo without
inventing a client-side commit identity.

#### Scenario: Forgejo returns a commit

- **WHEN** the native diff-patch operation succeeds and returns commit metadata
- **THEN** the MCP result SHALL expose that commit's SHA as `commit_sha`
- **AND** SHALL expose that commit's HTML URL as `commit_url`
