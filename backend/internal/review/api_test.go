package review

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/flowdproto/flowdfake"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/store"
)

type sbs struct {
	api     flowdproto.API
	stopped bool
}

func (s *sbs) Ensure(context.Context, string) (flowdproto.API, error) { return s.api, nil }
func (s *sbs) Warm(context.Context, string) (flowdproto.API, error) {
	if s.stopped {
		return nil, sandboxes.ErrNotWarm
	}
	return s.api, nil
}
func (s *sbs) Forget(string) {}

// The diff of a session's repository comes from flowd by the repository's full name; an unknown session or a
// repository without a worktree is not_found, a stopped sandbox sandbox_unavailable (never started for it).
func TestGetSessionDiff(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := projects.SQL{DB: db}
	_ = ps.Create(t.Context(), projects.Project{ID: "p1", Name: "demo"}, []projects.Repo{
		{ID: "r1", FullName: "a/b", DefaultBranch: "main"}, {ID: "r2", FullName: "a/c", DefaultBranch: "main"}})
	fake := flowdfake.New("sb")
	sb := &sbs{api: fake}
	svc := &sessions.Service{Store: sessions.SQL{DB: db}, Projects: ps, Sandboxes: sb, Log: slog.New(slog.DiscardHandler)}
	s, err := svc.Create(t.Context(), apitypes.SessionCreate{ProjectID: "p1", RepoIds: []string{"r1"}, Provider: "claude",
		Model: "m", Mode: "ask", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	id := s.Summary.ID
	fake.SetDiff(id, "a/b", apitypes.Diff{Repo: "a/b", BaseRef: "origin/main", BaseSHA: "abc",
		Files: []apitypes.DiffFile{{Path: "x", Change: apitypes.FileChangeKindModified, Added: 1, Hunks: []apitypes.DiffHunk{}}}})
	a := &API{Sessions: svc}
	get := func(session, repo string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("sessionId", session)
		req.SetPathValue("repoId", repo)
		a.GetSessionDiff(w, req)
		return w
	}
	w := get(id, "r1")
	var d apitypes.Diff
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != http.StatusOK || d.Repo != "a/b" || len(d.Files) != 1 {
		t.Fatalf("diff: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct{ session, repo string }{{"nope", "r1"}, {id, "r2"}, {id, "r9"}} {
		if w := get(tc.session, tc.repo); w.Code != http.StatusNotFound {
			t.Errorf("%v: %d", tc, w.Code)
		}
	}
	sb.stopped = true
	if w := get(id, "r1"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("stopped sandbox: %d", w.Code)
	}
}
