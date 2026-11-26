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
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := RemoteDefaultBranch(srv.URL+"/", srv.Client())
	if b, err := f(t.Context(), "a/b"); err != nil || b != "trunk" {
		t.Errorf("branch = %q, %v", b, err)
	}
	for _, repo := range []string{"a/nohead", "a/missing"} {
		if _, err := f(t.Context(), repo); err == nil {
			t.Errorf("%s: no error", repo)
		}
	}
	if _, err := RemoteDefaultBranch("http://127.0.0.1:1/", srv.Client())(t.Context(), "a/b"); err == nil {
		t.Error("unreachable server: no error")
	}
}

type fakeSandboxes struct{ setups []string }

func (f *fakeSandboxes) Setup(id string) { f.setups = append(f.setups, id) }
func (f *fakeSandboxes) Describe(context.Context, string) apitypes.Sandbox {
	return apitypes.Sandbox{State: apitypes.SandboxStateSetup}
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
