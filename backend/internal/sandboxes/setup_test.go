package sandboxes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/projects"
)

func fastSetupPoll(t *testing.T) {
	t.Helper()
	old := setupPoll
	setupPoll = time.Millisecond
	t.Cleanup(func() { setupPoll = old })
}

// pidTwo is a project with two repositories, both with a setup script.
const pidTwo = "01a21998-5a4f-7828-81be-000000000002"

func (r *rig) twoRepoProject(t *testing.T) {
	t.Helper()
	if err := r.ps.Create(t.Context(), projects.Project{ID: pidTwo, Name: "two"}, []projects.Repo{
		{ID: "a1", FullName: "dev/alpha", DefaultBranch: "main"},
		{ID: "b1", FullName: "dev/beta", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	r.d.fake.SetupScripts = map[string]bool{"dev/alpha": true, "dev/beta": true}
	r.m.SetupLimits = flowdproto.Limits{MemoryMaxBytes: 5 << 30, CPUQuotaPercent: 300, TasksMax: 4096}
	r.m.SetupTimeout = 7 * time.Minute
}

// A sandbox's first bring-up runs each repository's setup script, in project order, with a session's limits and
// the time limit, before it turns warm; the log reads as one numbered run. A stop and start, and a reconnect, run
// nothing again.
func TestSetup_runsAtBuildInOrder(t *testing.T) {
	fastSetupPoll(t)
	r := newRig(t)
	r.twoRepoProject(t)
	r.d.fake.JobScript = func(j flowdproto.JobSpec) ([]string, int) { return []string{"installing " + j.Repo, "done"}, 0 }
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pidTwo); err != nil {
		t.Fatal(err)
	}
	specs := r.d.fake.JobSpecs()
	if len(specs) != 2 || specs[0].ID != "setup-alpha" || specs[1].ID != "setup-beta" ||
		specs[0].Kind != flowdproto.JobSetup || specs[0].Repo != "dev/alpha" || specs[0].TimeoutSeconds != 420 ||
		specs[0].Limits != r.m.SetupLimits {
		t.Fatalf("jobs %+v", specs)
	}
	if d := r.m.Describe(ctx, pidTwo); d.State != apitypes.SandboxStateWarm {
		t.Errorf("state %+v", d)
	}
	log, err := r.m.SetupLog(ctx, pidTwo, 0)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for i, l := range log.Lines {
		if l.N != i+1 {
			t.Errorf("line %d numbered %d", i+1, l.N)
		}
		texts = append(texts, l.Text)
	}
	want := []string{"== dev/alpha: .flow/setup.sh ==", "installing dev/alpha", "done",
		"== dev/beta: .flow/setup.sh ==", "installing dev/beta", "done"}
	if !slices.Equal(texts, want) || log.State != apitypes.SandboxStateWarm {
		t.Errorf("log %s %q", log.State, texts)
	}
	if page, _ := r.m.SetupLog(ctx, pidTwo, 4); len(page.Lines) != 2 || page.Lines[0].N != 5 {
		t.Errorf("after 4: %+v", page.Lines)
	}
	// The end first: the last lines, numbered as in the whole log; more than the log holds is all of it.
	if tail, _ := r.m.SetupLogTail(ctx, pidTwo, 2); len(tail.Lines) != 2 || tail.Lines[0].N != 5 ||
		tail.Lines[1].Text != "done" {
		t.Errorf("tail 2: %+v", tail.Lines)
	}
	if tail, _ := r.m.SetupLogTail(ctx, pidTwo, 50); len(tail.Lines) != 6 || tail.Lines[0].N != 1 {
		t.Errorf("tail 50: %+v", tail.Lines)
	}
	for q, want := range map[string]int{"tail=3": http.StatusOK, "tail=0": http.StatusBadRequest,
		"tail=2&after=1": http.StatusBadRequest} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/projects/"+pidTwo+"/sandbox/setup-log?"+q, nil)
		req.SetPathValue("projectId", pidTwo)
		(&API{Manager: r.m}).GetSetupLog(w, req)
		if w.Code != want || want == http.StatusOK && !strings.Contains(w.Body.String(), `"n":4`) {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}

	// Ask again after a stop: flowd reports the jobs that ran, so nothing starts.
	if err := r.m.Stop(ctx, pidTwo); err != nil {
		t.Fatal(err)
	}
	r.d.fake.JobScript = func(flowdproto.JobSpec) ([]string, int) { t.Error("a script ran again"); return nil, 0 }
	if _, err := r.m.Ensure(ctx, pidTwo); err != nil {
		t.Fatal(err)
	}
	r.m.Forget(pidTwo)
	if _, err := r.m.Warm(ctx, pidTwo); err != nil {
		t.Fatal(err)
	}
	if n := len(r.d.fake.JobSpecs()); n != 2 {
		t.Errorf("%d jobs started in all", n)
	}
}

// A reconnect to a warm sandbox never asks for setup jobs (one made before setup scripts existed keeps running as
// it is until its next start).
func TestSetup_notOnAWarmReconnect(t *testing.T) {
	fastSetupPoll(t)
	r := newRig(t)
	ctx := t.Context()
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.d.fake.SetupScripts = map[string]bool{"trick77/toolkit": true}
	r.m.Forget(pid)
	if _, err := r.m.Warm(ctx, pid); err != nil {
		t.Fatal(err)
	}
	r.m.Forget(pid)
	if _, err := r.m.Ensure(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if specs := r.d.fake.JobSpecs(); len(specs) != 0 {
		t.Errorf("jobs on a warm sandbox: %+v", specs)
	}
	if log, err := r.m.SetupLog(ctx, pid, 0); err != nil || len(log.Lines) != 0 {
		t.Errorf("log of a sandbox without scripts: %+v, %v", log, err)
	}
}

// A failing script fails the bring-up with sandbox_setup_failed and keeps its log; a plain start fails the same way
// without running it again; a rebuild (a new instance) runs it again.
func TestSetup_failureThenStartThenRebuild(t *testing.T) {
	fastSetupPoll(t)
	r := newRig(t)
	r.d.fake.SetupScripts = map[string]bool{"trick77/toolkit": true}
	r.d.fake.JobScript = func(flowdproto.JobSpec) ([]string, int) { return []string{"npm ci", "npm ERR! missing"}, 1 }
	ctx := t.Context()
	_, err := r.m.Ensure(ctx, pid)
	if code(err) != apitypes.ErrorCodeSandboxSetupFailed || !strings.Contains(err.Error(), "exited with status 1") {
		t.Fatalf("Ensure: %v", err)
	}
	if d := r.m.Describe(ctx, pid); d.State != apitypes.SandboxStateFailed ||
		d.ErrorCode != apitypes.ErrorCodeSandboxSetupFailed {
		t.Errorf("state %+v", d)
	}
	log, err := r.m.SetupLog(ctx, pid, 0)
	if err != nil || log.State != apitypes.SandboxStateFailed || len(log.Lines) != 3 ||
		log.Lines[2].Text != "npm ERR! missing" {
		t.Errorf("log %+v, %v", log, err)
	}
	if _, err := r.m.Ensure(ctx, pid); code(err) != apitypes.ErrorCodeSandboxSetupFailed {
		t.Errorf("start after the failure: %v", err)
	}
	if n := len(r.d.fake.JobSpecs()); n != 1 {
		t.Errorf("the failed script ran %d times", n)
	}

	r.d.fake.ForgetJobs()
	r.d.fake.JobScript = func(flowdproto.JobSpec) ([]string, int) { return []string{"fixed"}, 0 }
	if err := r.m.Rebuild(ctx, pid); err != nil {
		t.Fatal(err)
	}
	waitState(t, r, apitypes.SandboxStateWarm)
	if n := len(r.d.fake.JobSpecs()); n != 2 {
		t.Errorf("after the rebuild %d jobs started in all", n)
	}
}

// While a script runs the sandbox shows setup and its log is readable (the bring-up holds the project's lock); a
// stopped sandbox has no log to read.
func TestSetup_logDuringSetupAndStopped(t *testing.T) {
	fastSetupPoll(t)
	r := newRig(t)
	r.d.fake.SetupScripts = map[string]bool{"trick77/toolkit": true}
	r.d.fake.HoldJobs = true
	r.d.fake.JobScript = func(flowdproto.JobSpec) ([]string, int) { return []string{"still going"}, 0 }
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { _, err := r.m.Ensure(context.Background(), pid); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for len(r.d.fake.JobSpecs()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the job never started")
		}
		time.Sleep(time.Millisecond)
	}
	log, err := r.m.SetupLog(ctx, pid, 0)
	if err != nil || log.State != apitypes.SandboxStateSetup || len(log.Lines) != 2 ||
		log.Lines[1].Text != "still going" {
		t.Errorf("log during setup %+v, %v", log, err)
	}
	r.d.fake.FinishJob("setup-toolkit")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetupLog(ctx, pid, 0); code(err) != apitypes.ErrorCodeSandboxUnavailable {
		t.Errorf("log of a stopped sandbox: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/"+pid+"/sandbox/setup-log?after=-1", nil)
	req.SetPathValue("projectId", pid)
	(&API{Manager: r.m}).GetSetupLog(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("after=-1: %d %s", w.Code, w.Body)
	}
}

// flaky answers ErrUnavailable for its first calls, as a flowd restarting under a running job does.
type flaky struct {
	flowdproto.API
	left int
}

func (f *flaky) Job(ctx context.Context, id string) (flowdproto.JobState, error) {
	if f.left > 0 {
		f.left--
		return flowdproto.JobState{}, flowdproto.ErrUnavailable
	}
	return f.API.Job(ctx, id)
}

func (f *flaky) StartJob(ctx context.Context, j flowdproto.JobSpec) (flowdproto.JobState, error) {
	if f.left > 0 {
		f.left--
		return flowdproto.JobState{}, flowdproto.ErrUnavailable
	}
	return f.API.StartJob(ctx, j)
}

// Waiting for a job rides out flowd being away for a moment; a job that ended without a result is a failure.
func TestWaitJob_flowdAwayAndCutOff(t *testing.T) {
	fastSetupPoll(t)
	r := newRig(t)
	r.d.fake.HoldJobs = true
	spec := flowdproto.JobSpec{ID: "setup-toolkit", Kind: flowdproto.JobSetup, Repo: "trick77/toolkit"}
	api := &flaky{API: r.d.fake}
	if _, err := r.d.fake.StartJob(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	api.left = 3
	go func() {
		time.Sleep(20 * time.Millisecond)
		r.d.fake.FinishJob(spec.ID)
	}()
	st, err := r.m.waitJob(t.Context(), api, spec)
	if err != nil || !st.Done || len(r.d.fake.JobSpecs()) != 1 {
		t.Errorf("wait: %+v, %v, %d starts", st, err, len(r.d.fake.JobSpecs()))
	}

	cut := &cutOff{API: r.d.fake}
	if _, err := r.m.waitJob(t.Context(), cut, flowdproto.JobSpec{ID: "setup-x"}); !errors.Is(err, ErrSetupFailed) {
		t.Errorf("cut-off job: %v", err)
	}
}

// cutOff reports a job whose unit is gone without a result.
type cutOff struct{ flowdproto.API }

func (cutOff) StartJob(context.Context, flowdproto.JobSpec) (flowdproto.JobState, error) {
	return flowdproto.JobState{ID: "setup-x", Unit: flowdproto.UnitMissing}, nil
}
