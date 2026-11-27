package review

import (
	"errors"
	"net/http"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/sessions"
)

// API implements httpapi.Review: the per-repository diff (pull requests answer not_implemented).
type API struct {
	httpapi.NotImplemented
	Sessions *sessions.Service
}

// GetSessionDiff implements httpapi.Review: computed by flowd in the worktree (bounded there), so a sandbox that does
// not run answers sandbox_unavailable; it is never started for a diff.
func (a *API) GetSessionDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	se, err := a.Sessions.Store.Get(ctx, r.PathValue("sessionId"))
	if errors.Is(err, sessions.ErrNotFound) {
		err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	wts, err := a.Sessions.Store.Worktrees(ctx, se.ID)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	repoID := r.PathValue("repoId")
	var full string
	for _, wt := range wts {
		if wt.RepoID == repoID {
			repo, err := a.Sessions.Projects.Repo(ctx, repoID)
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			full = repo.FullName
		}
	}
	if full == "" {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeNotFound, "no worktree of this repository"))
		return
	}
	api, err := a.Sessions.Sandboxes.Warm(ctx, se.ProjectID)
	if err != nil {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error()))
		return
	}
	d, err := api.Diff(ctx, se.ID, full)
	switch {
	case errors.Is(err, flowdproto.ErrNotFound):
		err = apitypes.Fail(apitypes.ErrorCodeNotFound, err.Error())
	case err != nil:
		err = apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, d)
}
