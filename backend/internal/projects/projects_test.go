package projects

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/store"
)

func open(t *testing.T) SQL {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return SQL{DB: db}
}

func TestSQL(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	p := Project{ID: "p1", Name: "demo"}
	if err := s.Create(ctx, p, []Repo{{ID: "r1", FullName: "a/b", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, Project{ID: "p2", Name: "demo"}, nil); err == nil {
		t.Error("duplicate name accepted")
	}
	if err := s.AddRepo(ctx, Repo{ID: "r2", ProjectID: "p1", FullName: "a/c", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	repos, err := s.Repos(ctx, "p1")
	if err != nil || len(repos) != 2 || repos[1].Position != 1 || repos[0].CreatedAt.IsZero() {
		t.Fatalf("Repos = %+v, %v", repos, err)
	}
	if r, err := s.Repo(ctx, "r2"); err != nil || r.FullName != "a/c" {
		t.Errorf("Repo = %+v, %v", r, err)
	}
	if _, err := s.Repo(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing repo: %v", err)
	}
	if err := s.Rename(ctx, "p1", "renamed"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, "p1"); err != nil || got.Name != "renamed" {
		t.Errorf("Get = %+v, %v", got, err)
	}
	if list, err := s.List(ctx); err != nil || len(list) != 1 {
		t.Errorf("List = %v, %v", list, err)
	}
	if err := s.RemoveRepo(ctx, "r2"); err != nil {
		t.Error(err)
	}
	for _, err := range []error{s.Rename(ctx, "x", "y"), s.Delete(ctx, "x"), s.RemoveRepo(ctx, "x")} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("on a missing row: %v", err)
		}
	}
	if err := s.Delete(ctx, "p1"); err != nil {
		t.Error(err)
	}
	if _, err := s.Get(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted project: %v", err)
	}
}

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{"trick77/ship": "trick77/ship", "https://github.com/trick77/toolkit": "trick77/toolkit",
		"https://github.com/trick77/toolkit.git": "trick77/toolkit", " github.com/a/b/ ": "a/b",
		"https://gitlab.com/a/b": "", "a": "", "a/b/c": "", "../x": "", "a/b.": ""} {
		got, ok := NormalizeRepo(in)
		if got != want || ok != (want != "") {
			t.Errorf("NormalizeRepo(%q) = %q, %v", in, got, ok)
		}
	}
}

func TestRemoteDefaultBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/b.git/info/refs":
			_, _ = w.Write([]byte("001e# service=git-upload-pack\n0000" +
				"0100abc HEAD\x00multi_ack symref=HEAD:refs/heads/trunk agent=git/2\n0000"))
		case "/a/nohead.git/info/refs":
			_, _ = w.Write([]byte("0000"))
		case "/a/emptyhead.git/info/refs": // the symref names no branch: an error, not a panic
			_, _ = w.Write([]byte("0100abc HEAD\x00symref=HEAD:refs/heads/\x00agent=git/2\n0000"))
		case "/a/bare.git/info/refs":
			_, _ = w.Write([]byte("0100abc HEAD\x00symref=HEAD:refs/heads/\n0000"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := RemoteDefaultBranch(srv.URL+"/", srv.Client())
	if b, err := f(t.Context(), "a/b"); err != nil || b != "trunk" {
		t.Errorf("branch = %q, %v", b, err)
	}
	for _, repo := range []string{"a/nohead", "a/emptyhead", "a/bare", "a/missing"} {
		if _, err := f(t.Context(), repo); err == nil {
			t.Errorf("%s: no error", repo)
		}
	}
	if _, err := RemoteDefaultBranch("http://127.0.0.1:1/", srv.Client())(t.Context(), "a/b"); err == nil {
		t.Error("unreachable server: no error")
	}
}

type fakeSandboxes struct {
	setups, deletes []string
	deleteErr       error
}

func (f *fakeSandboxes) Setup(id string) { f.setups = append(f.setups, id) }
func (f *fakeSandboxes) Describe(context.Context, string) apitypes.Sandbox {
	return apitypes.Sandbox{State: apitypes.SandboxStateSetup}
}
func (f *fakeSandboxes) Delete(ctx context.Context, id string, rows func(context.Context) error) error {
	f.deletes = append(f.deletes, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return rows(ctx)
}

// addSession puts a session with a worktree of repo into the project, archived or not, as the sessions area does.
func addSession(t *testing.T, s SQL, id, projectID, repoID string, archived bool) {
	t.Helper()
	var at any
	if archived {
		at = "2026-11-27T10:00:00.000Z"
	}
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, name, provider, model, mode, branch,
		archived_at) VALUES (?, ?, ?, 'claude', 'm', 'ask', 'b', ?)`, id, projectID, id, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(t.Context(), `INSERT INTO session_worktrees (id, session_id, repo_id, branch, path,
		base_ref) VALUES (?, ?, ?, 'b', '/w', 'origin/main')`, id+"-wt", id, repoID); err != nil {
		t.Fatal(err)
	}
}

// A project whose sessions have worktrees goes with them; other projects keep theirs.
func TestSQL_DeleteWithSessions(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	if err := s.Create(ctx, Project{ID: "p1", Name: "demo"}, []Repo{{ID: "r1", FullName: "a/b", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, Project{ID: "p2", Name: "other"}, []Repo{{ID: "r2", FullName: "a/c", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	addSession(t, s, "s1", "p1", "r1", true)
	addSession(t, s, "s2", "p2", "r2", false)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sandboxes (id, project_id, instance) VALUES ('sb1', 'p1', 'flow-1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	var n int
	for q, want := range map[string]int{
		`SELECT count(*) FROM sessions`:          1,
		`SELECT count(*) FROM session_worktrees`: 1,
		`SELECT count(*) FROM project_repos`:     1,
		`SELECT count(*) FROM sandboxes`:         0,
	} {
		if err := s.DB.QueryRowContext(ctx, q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d, %v; want %d", q, n, err, want)
		}
	}
	_ = s.DB.Close()
	if err := s.Delete(ctx, "p2"); err == nil {
		t.Error("closed database: no error")
	}
}

func TestAPI_DeleteProject(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	if err := s.Create(ctx, Project{ID: "p1", Name: "demo"}, []Repo{{ID: "r1", FullName: "a/b", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	addSession(t, s, "s1", "p1", "r1", true)
	sb := &fakeSandboxes{}
	active := 1
	var events []apitypes.AppEvent
	a := &API{Store: s, Sandboxes: sb,
		ActiveSessions: func(_ context.Context, id string) (int, error) {
			if id == "broken" {
				return 0, errors.New("down")
			}
			return active, nil
		},
		Publish: func(e apitypes.AppEvent) { events = append(events, e) }}
	path := map[string]string{"projectId": "p1"}
	del := func() *httptest.ResponseRecorder { return call(t, a.DeleteProject, "DELETE", "/", "", path) }
	if w := call(t, a.DeleteProject, "DELETE", "/", "", map[string]string{"projectId": "nope"}); w.Code != 404 {
		t.Errorf("missing project: %d", w.Code)
	}
	if w := del(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "project_has_sessions") ||
		len(sb.deletes) != 0 {
		t.Errorf("with a session in use: %d %s, deletes %v", w.Code, w.Body, sb.deletes)
	}
	active = 0
	sb.deleteErr = apitypes.Fail(apitypes.ErrorCodeSandboxBusy, "")
	if w := del(); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "sandbox_busy") {
		t.Errorf("busy sandbox: %d %s", w.Code, w.Body)
	}
	if _, err := s.Get(ctx, "p1"); err != nil {
		t.Errorf("a refused delete removed the project: %v", err)
	}
	sb.deleteErr = nil
	if w := del(); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := s.Get(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted project: %v", err)
	}
	if len(events) != 1 || events[0].Kind != apitypes.AppEventKindProjectRemoved || events[0].ProjectID != "p1" {
		t.Errorf("events %+v", events)
	}
	if err := s.Create(ctx, Project{ID: "broken", Name: "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if w := call(t, a.DeleteProject, "DELETE", "/", "", map[string]string{"projectId": "broken"}); w.Code != 500 {
		t.Errorf("counting sessions failed: %d", w.Code)
	}
	_ = s.DB.Close()
	if w := del(); w.Code != 500 {
		t.Errorf("closed database: %d", w.Code)
	}
}

// A session made after the API counted (its row there, not archived) keeps the project and every row: nothing is
// removed without its session.removed.
func TestAPI_DeleteProject_sessionMadeMeanwhile(t *testing.T) {
	s := open(t)
	ctx := t.Context()
	if err := s.Create(ctx, Project{ID: "p1", Name: "demo"}, []Repo{{ID: "r1", FullName: "a/b", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	addSession(t, s, "s1", "p1", "r1", true)
	addSession(t, s, "s2", "p1", "r1", false)
	if err := s.Delete(ctx, "p1"); !errors.Is(err, ErrHasSessions) {
		t.Errorf("Delete with an open session: %v", err)
	}
	// The count said none (it ran before the session was made).
	a := &API{Store: s, Sandboxes: &fakeSandboxes{},
		ActiveSessions: func(context.Context, string) (int, error) { return 0, nil }}
	w := call(t, a.DeleteProject, "DELETE", "/", "", map[string]string{"projectId": "p1"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "project_has_sessions") {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 2 {
		t.Errorf("sessions left %d, %v; want both", n, err)
	}
	if _, err := s.Get(ctx, "p1"); err != nil {
		t.Errorf("project: %v", err)
	}
}

// orderedSandboxes records Setup in the same log as the published events, and its bring-up fails at once.
type orderedSandboxes struct {
	fakeSandboxes
	log   *[]string
	state apitypes.SandboxState
}

func (o *orderedSandboxes) Setup(string) {
	*o.log = append(*o.log, "setup")
	o.state = apitypes.SandboxStateFailed
}

func (o *orderedSandboxes) Describe(context.Context, string) apitypes.Sandbox {
	return apitypes.Sandbox{State: o.state}
}

// A new project's event goes out before its setup starts, so a bring-up that fails at once is not followed by the
// project's older state; the answer carries the state after the start.
func TestAPI_CreateProject_publishesBeforeSetup(t *testing.T) {
	var log []string
	sb := &orderedSandboxes{log: &log, state: apitypes.SandboxStateNone}
	a := &API{Store: open(t), Sandboxes: sb,
		DefaultBranch: func(context.Context, string) (string, error) { return "main", nil },
		Publish:       func(e apitypes.AppEvent) { log = append(log, string(e.Kind)) }}
	w := call(t, a.CreateProject, "POST", "/api/projects", `{"name":"demo","repos":["a/b"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if strings.Join(log, ",") != "project.updated,setup" {
		t.Errorf("order %v", log)
	}
	var p apitypes.Project
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Sandbox.State != apitypes.SandboxStateFailed {
		t.Errorf("answer's sandbox %+v", p.Sandbox)
	}
}

// The name's limit counts characters, as the form does: 64 umlauts (128 bytes) pass, 65 do not.
func TestAPI_CreateProject_nameInCharacters(t *testing.T) {
	a := &API{Store: open(t), Sandboxes: &fakeSandboxes{},
		DefaultBranch: func(context.Context, string) (string, error) { return "main", nil }}
	for n, code := range map[int]int{64: http.StatusCreated, 65: http.StatusBadRequest} {
		body := `{"name":"` + strings.Repeat("ä", n) + `","repos":["a/b"]}`
		if w := call(t, a.CreateProject, "POST", "/api/projects", body, nil); w.Code != code {
			t.Errorf("%d characters: %d %s, want %d", n, w.Code, w.Body, code)
		}
	}
}

func call(t *testing.T, h http.HandlerFunc, method, target, body string, path map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range path {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestAPI(t *testing.T) {
	s := open(t)
	sb := &fakeSandboxes{}
	a := &API{Store: s, Sandboxes: sb, DefaultBranch: func(_ context.Context, full string) (string, error) {
		if full == "a/broken" {
			return "", errors.New("down")
		}
		return "master", nil
	}}
	w := call(t, a.CreateProject, "POST", "/api/projects", `{"name":"demo","repos":["https://github.com/trick77/toolkit","trick77/ship"]}`, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var p apitypes.Project
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if len(p.Repos) != 2 || p.Repos[0].FullName != "trick77/toolkit" || p.Sandbox.State != "setup" || len(sb.setups) != 1 {
		t.Errorf("project %+v, setups %v", p, sb.setups)
	}
	var published []apitypes.AppEvent
	a.Publish = func(e apitypes.AppEvent) { published = append(published, e) }
	if w := call(t, a.CreateProject, "POST", "/api/projects", `{"name":"second","repos":["a/b"]}`, nil); w.Code != 201 ||
		len(published) != 1 || published[0].Kind != apitypes.AppEventKindProjectUpdated || published[0].Project == nil {
		t.Errorf("create with an event stream: %d, events %+v", w.Code, published)
	}
	for body, code := range map[string]int{
		`{"name":"demo","repos":["a/b"]}`:           http.StatusConflict,
		`{"name":"","repos":["a/b"]}`:               http.StatusBadRequest,
		`{"name":"x","repos":[]}`:                   http.StatusBadRequest,
		`{"name":"x","repos":["nope"]}`:             http.StatusBadRequest,
		`{"name":"x","repos":["a/b","A/B"]}`:        http.StatusBadRequest,
		`{"name":"x","repos":["a/tool","b/Tool"]}`:  http.StatusBadRequest, // one directory for two repositories
		`{"name":"x","repos":["a/.github"]}`:        http.StatusBadRequest, // valid on GitHub, not as a directory
		`{"name":"x","repos":["a/_x"]}`:             http.StatusBadRequest,
		`{"name":"x","repos":["a/broken"]}`:         http.StatusBadGateway,
		`{"name":"x","repos":["a/b"],"extra":true}`: http.StatusBadRequest,
	} {
		if w := call(t, a.CreateProject, "POST", "/api/projects", body, nil); w.Code != code {
			t.Errorf("%s: %d, want %d", body, w.Code, code)
		}
	}
	if w := call(t, a.ListProjects, "GET", "/api/projects", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), p.ID) {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
	if w := call(t, a.GetProject, "GET", "/", "", map[string]string{"projectId": p.ID}); w.Code != 200 {
		t.Errorf("get: %d", w.Code)
	}
	if w := call(t, a.GetProject, "GET", "/", "", map[string]string{"projectId": "nope"}); w.Code != 404 {
		t.Errorf("get missing: %d", w.Code)
	}
	_ = s.DB.Close()
	for _, h := range []http.HandlerFunc{a.ListProjects, a.GetProject} {
		if w := call(t, h, "GET", "/", "", map[string]string{"projectId": p.ID}); w.Code != 500 {
			t.Errorf("closed database: %d", w.Code)
		}
	}
}
