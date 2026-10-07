// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"context"
	"fmt"
	"net/http"

	"git.b4mad.industries/agentic-forges/forgejo-mcp/v3/pkg/forgejo"
)

// repoActionsState is the slice of the repo-info payload the hint needs.
type repoActionsState struct {
	HasActions bool `json:"has_actions"`
}

// withActionsDisabledHint rewrites err when owner/repo has the Actions unit
// disabled. Forgejo answers every /actions/* request for such a repo with a
// bare 404, which reads as "no such run" when the real cause is repo
// configuration. The diagnosis costs one repo-info GET, only on the failure
// path; if the probe itself fails the original error passes through
// untouched.
func withActionsDisabledHint(ctx context.Context, owner, repo string, err error) error {
	if err == nil {
		return nil
	}
	var state repoActionsState
	if probeErr := forgejo.DoJSON(ctx, http.MethodGet, forgejo.APIPath("repos", owner, repo), nil, &state); probeErr != nil {
		return err
	}
	if !state.HasActions {
		return fmt.Errorf("Forgejo Actions is disabled on %s/%s (has_actions=false): there are no action runs, jobs, artifacts or logs here. Enable Actions for the repo, or point the tool at a repo with Actions enabled", owner, repo)
	}
	return err
}
