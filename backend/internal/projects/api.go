package projects

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/store"
)

var nowFunc = time.Now

// Sandboxes is what the projects area needs of the sandboxes area.
type Sandboxes interface {
	// Setup starts preparing a new project's sandbox in the background.
	Setup(projectID string)
	// Describe is the project's sandbox as the API shows it.
	Describe(ctx context.Context, projectID string) apitypes.Sandbox
	// Delete stops and deletes the project's sandbox instance and keeps anything from bringing it up again; an
	// *apitypes.Error (sandbox_busy, sandbox_unavailable) when it cannot.
	Delete(ctx context.Context, projectID string) error
}

// API implements httpapi.Projects (list, create, get, delete; the rest answer not_implemented).
type API struct {
	httpapi.NotImplemented
	Store     Store
	Sandboxes Sandboxes
	// DefaultBranch finds a repository's default branch; RemoteDefaultBranch by default.
	DefaultBranch func(ctx context.Context, fullName string) (string, error)
	// ActiveSessions counts a project's sessions that are not archived: a delete is refused while one exists. nil
	// counts none.
	ActiveSessions func(ctx context.Context, projectID string) (int, error)
	// Publish sends an app event (project.updated, project.removed); nil: none.
	Publish func(apitypes.AppEvent)
}

func (a *API) publish(e apitypes.AppEvent) {
	if a.Publish != nil {
		a.Publish(e)
	}
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)

// NormalizeRepo turns owner/name or a public https clone URL of github.com into owner/name. The GitHub App is not
// wired yet: repositories are cloned anonymously, so only public ones work.
func NormalizeRepo(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	if !repoRE.MatchString(s) || strings.HasSuffix(s, ".") {
		return "", false
	}
	return s, true
}

// RemoteDefaultBranch asks github.com anonymously (git's smart HTTP ref advertisement) which branch HEAD names.
func RemoteDefaultBranch(base string, client *http.Client) func(ctx context.Context, fullName string) (string, error) {
	return func(ctx context.Context, fullName string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+fullName+".git/info/refs?service=git-upload-pack", nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s: %s", fullName, resp.Status)
		}
		br := bufio.NewReader(io.LimitReader(resp.Body, 1<<20))
		for {
			line, err := br.ReadString('\n')
			if _, ref, ok := strings.Cut(line, "symref=HEAD:refs/heads/"); ok {
				// The branch ends at the next capability (space), the capability list's NUL or the line's end.
				if i := strings.IndexAny(ref, " \x00\r\n"); i >= 0 {
					ref = ref[:i]
				}
				if ref != "" {
					return ref, nil
				}
				return "", fmt.Errorf("%s: no HEAD in the ref advertisement", fullName)
			}
			if err != nil {
				return "", fmt.Errorf("%s: no HEAD in the ref advertisement", fullName)
			}
		}
	}
}

func (a *API) project(ctx context.Context, p Project) (apitypes.Project, error) {
	repos, err := a.Store.Repos(ctx, p.ID)
	if err != nil {
		return apitypes.Project{}, err
	}
	out := apitypes.Project{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, Repos: []apitypes.ProjectRepo{},
		Sandbox: a.Sandboxes.Describe(ctx, p.ID)}
	for _, r := range repos {
		out.Repos = append(out.Repos, apitypes.ProjectRepo{ID: r.ID, FullName: r.FullName, DefaultBranch: r.DefaultBranch})
	}
	return out, nil
}

// ListProjects implements httpapi.Projects.
func (a *API) ListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := a.Store.List(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	out := apitypes.ProjectList{Items: []apitypes.Project{}}
	for _, p := range ps {
		ap, err := a.project(r.Context(), p)
		if err != nil {
			httpapi.WriteError(w, err)
			return
		}
		out.Items = append(out.Items, ap)
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// GetProject implements httpapi.Projects.
func (a *API) GetProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.Get(r.Context(), r.PathValue("projectId"))
	if errors.Is(err, ErrNotFound) {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeNotFound, ""))
		return
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	ap, err := a.project(r.Context(), p)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, ap)
}

// CreateProject implements httpapi.Projects: the project, its repositories with their default branches, and the
// start of its sandbox's setup.
func (a *API) CreateProject(w http.ResponseWriter, r *http.Request) {
	var in apitypes.ProjectCreate
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 { // characters, as the form and its message count
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "name"})
		return
	}
	if len(in.Repos) == 0 {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "repos"})
		return
	}
	p := Project{ID: store.NewID(), Name: name}
	// Repositories become directories named after them (layout.RepoDir): a name that is no directory name, or two
	// repositories that would share one, are refused here, before anything reaches a sandbox.
	dirs := map[string]bool{}
	var repos []Repo
	for _, raw := range in.Repos {
		full, ok := NormalizeRepo(raw)
		dir, valid := layout.RepoDir(full)
		if !ok || !valid || dirs[dir] {
			httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "repos", Detail: raw})
			return
		}
		dirs[dir] = true
		branch, err := a.DefaultBranch(r.Context(), full)
		if err != nil {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeUpstream, err.Error()))
			return
		}
		repos = append(repos, Repo{ID: store.NewID(), ProjectID: p.ID, FullName: full, DefaultBranch: branch})
	}
	if err := a.Store.Create(r.Context(), p, repos); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeConflict, Field: "name"})
			return
		}
		httpapi.WriteError(w, err)
		return
	}
	p, err := a.Store.Get(r.Context(), p.ID)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	ap, err := a.project(r.Context(), p)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	// The project's event goes out before its setup starts: a browser drops a sandbox.updated of a project it does
	// not know yet, and a bring-up that fails at once would otherwise be followed by this event's older state.
	a.publish(apitypes.AppEvent{Kind: apitypes.AppEventKindProjectUpdated, Project: &ap, ProjectID: ap.ID})
	a.Sandboxes.Setup(p.ID)
	ap.Sandbox = a.Sandboxes.Describe(r.Context(), p.ID)
	httpapi.WriteJSON(w, http.StatusCreated, ap)
}

// DeleteProject implements httpapi.Projects: refused while a session of the project is not archived (the owner
// archives first: archiving keeps uncommitted work on disk and says so, a delete would not); then the sandbox
// instance goes, then the project's rows with its archived sessions.
func (a *API) DeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectId")
	if _, err := a.Store.Get(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
		}
		httpapi.WriteError(w, err)
		return
	}
	if a.ActiveSessions != nil {
		n, err := a.ActiveSessions(r.Context(), id)
		if err != nil {
			httpapi.WriteError(w, err)
			return
		}
		if n > 0 {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeProjectHasSessions, ""))
			return
		}
	}
	if err := a.Sandboxes.Delete(r.Context(), id); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	// The instance is gone whatever the caller does now: the rows must follow it.
	if err := a.Store.Delete(context.WithoutCancel(r.Context()), id); err != nil && !errors.Is(err, ErrNotFound) {
		if errors.Is(err, ErrHasSessions) {
			err = apitypes.Fail(apitypes.ErrorCodeProjectHasSessions, "")
		}
		httpapi.WriteError(w, err)
		return
	}
	a.publish(apitypes.AppEvent{Kind: apitypes.AppEventKindProjectRemoved, ProjectID: id})
	w.WriteHeader(http.StatusNoContent)
}
