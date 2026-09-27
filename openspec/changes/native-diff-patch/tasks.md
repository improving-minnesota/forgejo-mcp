## 1. Implementation

- [x] 1.1 Add the `apply_diff_patch` MCP tool.
- [x] 1.2 Resolve the source branch commit before mutation.
- [x] 1.3 Forward the unified diff unchanged to Forgejo's native diffpatch endpoint.
- [x] 1.4 Project the native commit identity in the tool result.
- [x] 1.5 Register and document the tool.

## 2. Verification

- [x] 2.1 Verify tool registration and public schema.
- [x] 2.2 Verify exactly one native mutation after source-branch resolution.
- [x] 2.3 Verify invalid MCP inputs fail before network access.
- [x] 2.4 Verify source-branch resolution failures fail closed without mutation.
- [x] 2.5 Verify Forgejo owns malformed-patch, authorization, branch-protection,
      context-mismatch, and Git-rejection semantics without fallback.
- [x] 2.6 Verify optional new-branch behavior.
- [x] 2.7 Run strict OpenSpec validation and repository quality gates.
