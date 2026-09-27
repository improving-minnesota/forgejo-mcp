# Change: Add native repository diff-patch writes

## Why

Forgejo provides a native repository diff-patch endpoint that can apply a textual
unified diff as one repository mutation. Exposing that endpoint directly avoids
client-side patch parsing and multi-request file mutation semantics.

## What Changes

- Add an `apply_diff_patch` MCP tool.
- Resolve the source branch before invoking the native Forgejo diff-patch endpoint.
- Use the resolved source commit identity only as the transport `sha` required by
  Forgejo.
- Forward the textual unified diff unchanged.
- Allow Forgejo to own patch parsing, authorization, branch protection, Git
  validation, and commit creation.
- Project the resulting commit SHA and URL into the MCP result.
- Document the new repository write tool.

## Scope

This change covers only native textual diff-patch repository writes.

It does not add client-side patch application, compare-and-swap semantics,
force-push behavior, atomic multi-file APIs, binary file content APIs, or
milestone operations.
