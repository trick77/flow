package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/sandbox"
)

// Setup scripts: a bring-up that finds the sandbox not warm (its first after a create or a Rebuild, a start, a
// retry after a failure) runs, after the clones and in project order, the job of every repository whose main
// checkout holds .flow/setup.sh (flowdproto.JobSetup, id layout.SetupJobID). flowd records each job's result in the
// instance and never starts a job with a result again, so a stop and start, a reconnect, or a restarted Flow or flowd
// finds the job (and waits for one still running) instead of running the script twice. Main checkouts are fetched,
// never moved, so a script changed on the default branch runs only after a Rebuild, which makes the instance and
// its checkouts afresh. A script that fails or runs past its limit fails the bring-up (sandbox_setup_failed) and
// keeps its log; a later start reports the same failure until a Rebuild.

// ErrSetupFailed means a repository's setup script exited with a status other than 0, ran past its time limit, or ended
// without a result.
var ErrSetupFailed = errors.New("sandboxes: setup script failed")

// setupGrace is what Flow waits for a job past its time limit: flowd ends it at the limit and needs a moment to
// kill what is left and record the result.
const setupGrace = 2 * time.Minute

// setupPoll is how often a running job is asked about. A var for tests.
var setupPoll = 2 * time.Second

// setupLogPage bounds the lines one setup-log answer carries.
const setupLogPage = flowdproto.JobLogPage

func (m *Manager) setupTimeout() time.Duration {
	if m.SetupTimeout > 0 {
		return m.SetupTimeout
	}
	return flowdproto.JobTimeout
}

// setupBudget is the time a bring-up of the project may spend in setup scripts on top of bringTimeout: each
// repository's limit and grace, as they run one after another.
func (m *Manager) setupBudget(ctx context.Context, projectID string) time.Duration {
	repos, err := m.Projects.Repos(ctx, projectID)
	if err != nil {
		return 0 // the bring-up fails reading them again
	}
	return time.Duration(len(repos)) * (m.setupTimeout() + setupGrace)
}

// runSetup runs the setup job of each repository in repos (those with a script, in project order), one after
// another, and fails at the first that does not end with status 0.
func (m *Manager) runSetup(ctx context.Context, sb *Sandbox, api flowdproto.API, repos []flowdproto.RepoState) error {
	for _, r := range repos {
		dir, ok := layout.RepoDir(r.Repo)
		if !ok {
			return fmt.Errorf("repo %s: no valid directory name", r.Repo)
		}
		spec := flowdproto.JobSpec{ID: layout.SetupJobID(dir), Kind: flowdproto.JobSetup, Repo: r.Repo,
			Limits: m.SetupLimits, TimeoutSeconds: int(m.setupTimeout() / time.Second)}
		t0 := time.Now()
		jctx, cancel := context.WithTimeout(ctx, m.setupTimeout()+setupGrace)
		st, err := m.waitJob(jctx, api, spec)
		cancel()
		switch {
		case err != nil:
			return fmt.Errorf("setup script of %s: %w", r.Repo, err)
		case st.TimedOut:
			return fmt.Errorf("%w: %s of %s ran past its time limit of %s", ErrSetupFailed, layout.SetupScript, r.Repo,
				m.setupTimeout())
		case st.ExitCode != 0:
			return fmt.Errorf("%w: %s of %s exited with status %d", ErrSetupFailed, layout.SetupScript, r.Repo,
				st.ExitCode)
		}
		m.Log.Info("setup script done", "sandbox", sb.Instance, "repo", r.Repo, "commit", st.Commit,
			"lines", st.Lines, "ms", time.Since(t0).Milliseconds())
	}
	return nil
}

// waitJob starts the job (or finds it) and polls it until it ended. flowd away for a moment (its restart) is waited
// out: the start is asked again, which is idempotent, after the set is pushed again (a restarted flowd holds none
// and starts no unit without it). A job that ended without a result is a failure: it is started again only by a
// later bring-up.
func (m *Manager) waitJob(ctx context.Context, api flowdproto.API, spec flowdproto.JobSpec) (flowdproto.JobState, error) {
	st, err := api.StartJob(ctx, spec)
	for {
		switch {
		case err == nil && st.Done:
			return st, nil
		case err == nil && !st.Running():
			return st, fmt.Errorf("%w: job %s ended without a result (its unit was stopped)", ErrSetupFailed, spec.ID)
		case err != nil && !errors.Is(err, flowdproto.ErrUnavailable):
			return st, err
		}
		select {
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
			return st, fmt.Errorf("waiting for job %s: %w", spec.ID, err)
		case <-time.After(setupPoll):
		}
		if err != nil {
			if perr := m.pushEnv(ctx, api); perr != nil {
				m.Log.Debug("environment push while waiting for a job", "job", spec.ID, "err", perr)
			}
			st, err = api.StartJob(ctx, spec)
		} else {
			st, err = api.Job(ctx, spec.ID)
		}
	}
}

// noteDialed keeps the flowd a bring-up greets, so the setup log is read during the bring-up (which holds the
// project's lock) and after one that failed.
func (m *Manager) noteDialed(projectID string, c cachedAPI) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dialed == nil {
		m.dialed = map[string]cachedAPI{}
	}
	m.dialed[projectID] = c
}

// logAPI is a flowd for reading a sandbox's setup log without the project's lock: the cached or last dialled one,
// else one dialled now at the instance's address with the sealed token (never made anew here: that is a bring-up's).
// nil when there is none to reach.
func (m *Manager) logAPI(ctx context.Context, sb Sandbox) flowdproto.API {
	m.mu.Lock()
	c, ok := m.apis[sb.ProjectID]
	if !ok {
		c = m.dialed[sb.ProjectID]
	}
	m.mu.Unlock()
	if c.api != nil {
		return c.api
	}
	if len(sb.FlowdToken) == 0 {
		return nil
	}
	token, err := m.Sealer.Open(sb.FlowdToken)
	if err != nil {
		return nil
	}
	st, err := m.Runtime.State(ctx, sb.Instance)
	if err != nil || st.Status != sandbox.StatusRunning || st.IPv4 == "" {
		return nil
	}
	return m.Dialer.Dial(flowdproto.Endpoint{Sandbox: sb.Instance, Address: net.JoinHostPort(st.IPv4,
		strconv.Itoa(flowdproto.Port)), Token: string(token)})
}

// SetupLog is the setup log of the project's sandbox after line after: each repository's job log in project order
// after a header line, numbered across them. Jobs run in that order, one at a time, so every number stays what it
// was: only the last job's lines grow. A sandbox that is stopped, never made or being deleted has no log to read
// (sandbox_unavailable); one in setup whose flowd does not answer yet has no lines yet.
func (m *Manager) SetupLog(ctx context.Context, projectID string, after int) (apitypes.SetupLog, error) {
	return m.setupLog(ctx, projectID, after, 0)
}

// SetupLogTail is the last tail lines of the setup log (at most a page): where a failed script's error and exit
// status are. The first line's number says whether earlier ones exist.
func (m *Manager) SetupLogTail(ctx context.Context, projectID string, tail int) (apitypes.SetupLog, error) {
	return m.setupLog(ctx, projectID, 0, max(min(tail, setupLogPage), 1))
}

// setupLog is SetupLog; tail above 0 replaces after with the number tail lines before the end of the jobs' logs as
// their states count them now.
func (m *Manager) setupLog(ctx context.Context, projectID string, after, tail int) (apitypes.SetupLog, error) {
	sb, err := m.Store.ByProject(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return apitypes.SetupLog{}, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, "the sandbox was never set up")
	}
	if err != nil {
		return apitypes.SetupLog{}, err
	}
	out := apitypes.SetupLog{State: apitypes.SandboxState(sb.State), Lines: []apitypes.SetupLine{}}
	switch sb.State {
	case StateSetup, StateWarm, StateFailed:
	default:
		return apitypes.SetupLog{}, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable,
			"the sandbox is "+sb.State+": start it to read its setup log")
	}
	unreachable := func(err error) (apitypes.SetupLog, error) {
		if sb.State == StateSetup {
			return out, nil // flowd not up yet, or restarting: nothing to show yet
		}
		return apitypes.SetupLog{}, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	api := m.logAPI(ctx, sb)
	if api == nil {
		return unreachable(errors.New("the sandbox's flowd is not reachable"))
	}
	repos, err := m.Projects.Repos(ctx, projectID)
	if err != nil {
		return apitypes.SetupLog{}, err
	}
	type job struct {
		repo, id string
		st       flowdproto.JobState
	}
	var jobs []job
	for _, r := range repos {
		dir, ok := layout.RepoDir(r.FullName)
		if !ok {
			continue
		}
		id := layout.SetupJobID(dir)
		st, err := api.Job(ctx, id)
		if errors.Is(err, flowdproto.ErrNotFound) {
			continue
		}
		if err != nil {
			return unreachable(err)
		}
		jobs = append(jobs, job{repo: r.FullName, id: id, st: st})
	}
	if tail > 0 {
		end := 0
		for _, j := range jobs {
			end += 1 + j.st.Lines // its header, then its lines
		}
		after = max(end-tail, 0)
	}
	n := 0 // the last number given out
	for _, j := range jobs {
		id, st := j.id, j.st
		header := n + 1
		if header > after {
			out.Lines = append(out.Lines, apitypes.SetupLine{N: header,
				Text: "== " + j.repo + ": " + layout.SetupScript + " =="})
		}
		total := st.Lines
		for from := max(after-header, 0); from < total || st.Running(); {
			lines, err := api.JobLog(ctx, id, from)
			if err != nil {
				return unreachable(err)
			}
			for _, l := range lines {
				out.Lines = append(out.Lines, apitypes.SetupLine{N: header + l.N, Text: l.Text})
				from, total = l.N, max(total, l.N)
			}
			if len(lines) < flowdproto.JobLogPage || len(out.Lines) >= setupLogPage {
				break
			}
		}
		n = header + total
		if len(out.Lines) >= setupLogPage {
			out.Lines = out.Lines[:setupLogPage]
			break
		}
	}
	return out, nil
}

// GetSetupLog implements httpapi.Sandboxes.
func (a *API) GetSetupLog(w http.ResponseWriter, r *http.Request) {
	id, ok := a.known(w, r)
	if !ok {
		return
	}
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "after"})
			return
		}
		after = n
	}
	tail := 0
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || r.URL.Query().Has("after") {
			httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "tail"})
			return
		}
		tail = n
	}
	var log apitypes.SetupLog
	var err error
	if tail > 0 {
		log, err = a.Manager.SetupLogTail(r.Context(), id, tail)
	} else {
		log, err = a.Manager.SetupLog(r.Context(), id, after)
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, log)
}
