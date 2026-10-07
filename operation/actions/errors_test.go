// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func serveRepoState(t *testing.T, repoStatus int, body string) {
	t.Helper()
	setupActionAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/actions/runs/42/jobs") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(repoStatus)
		_, _ = w.Write([]byte(body))
	})
}

func TestWithActionsDisabledHint_ReplacesErrorWhenDisabled(t *testing.T) {
	serveRepoState(t, http.StatusOK, `{"has_actions": false}`)
	original := errors.New("list action run jobs: 404")

	got := withActionsDisabledHint(context.Background(), "o", "r", original)
	if got == original {
		t.Fatalf("expected the error to be rewritten, got the original: %v", got)
	}
	if !strings.Contains(got.Error(), "Actions is disabled on o/r") {
		t.Fatalf("diagnosis missing from %q", got.Error())
	}
}

func TestWithActionsDisabledHint_KeepsErrorWhenEnabled(t *testing.T) {
	serveRepoState(t, http.StatusOK, `{"has_actions": true}`)
	original := errors.New("list action run jobs: 404")

	if got := withActionsDisabledHint(context.Background(), "o", "r", original); got != original {
		t.Fatalf("expected the original error, got %v", got)
	}
}

func TestWithActionsDisabledHint_KeepsErrorWhenProbeFails(t *testing.T) {
	serveRepoState(t, http.StatusInternalServerError, `{}`)
	original := errors.New("list action run jobs: 404")

	if got := withActionsDisabledHint(context.Background(), "o", "r", original); got != original {
		t.Fatalf("expected the original error, got %v", got)
	}
}

func TestListActionRunJobsFn_DiagnosesDisabledActions(t *testing.T) {
	serveRepoState(t, http.StatusOK, `{"has_actions": false, "has_issues": true}`)

	// The handler returns the diagnosis as a Go error; converting it to an
	// in-band tool result is the server middleware's job (see the
	// operation package's InBandToolErrors tests).
	_, err := ListActionRunJobsFn(context.Background(), newCallToolRequest(map[string]interface{}{
		"owner": "o", "repo": "r", "run_id": float64(42),
	}))
	if err == nil {
		t.Fatal("expected the disabled-actions diagnosis, got nil")
	}
	if !strings.Contains(err.Error(), "Actions is disabled on o/r") {
		t.Fatalf("expected the disabled-actions diagnosis, got %v", err)
	}
}
