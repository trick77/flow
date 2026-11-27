package supervisor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

// scriptRemote makes a bare repository base/owner/name.git whose one commit holds script as .flow/setup.sh.
func scriptRemote(t *testing.T, base, full, script string) {
	t.Helper()
	bare := filepath.Join(base, full+".git")
	_ = os.MkdirAll(bare, 0o755)
	gitRun(t, bare, "init", "--quiet", "--bare", "--initial-branch=master")
	work := filepath.Join(t.TempDir(), "w")
	gitRun(t, "", "clone", "--quiet", bare, work)
	_ = os.MkdirAll(filepath.Join(work, ".flow"), 0o755)
	_ = os.WriteFile(filepath.Join(work, layout.SetupScript), []byte(script), 0o644)
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "--quiet", "-m", "setup")
	gitRun(t, work, "push", "--quiet", "origin", "HEAD:master")
}

// setupRepo makes the repository trick77/setup with script and its main checkout.
func (r *rig) setupRepo(script string) flowdproto.RepoState {
	r.t.Helper()
	scriptRemote(r.t, r.base, "trick77/setup", script)
	st, err := r.api.EnsureRepo(context.Background(), flowdproto.RepoSpec{Repo: "trick77/setup", DefaultBranch: "master"})
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

var setupSpec = flowdproto.JobSpec{ID: "setup-setup", Kind: flowdproto.JobSetup, Repo: "trick77/setup",
	Limits: flowdproto.Limits{MemoryMaxBytes: 1 << 30, CPUQuotaPercent: 200, TasksMax: 512}}

// waitJob polls the job until it ended.
func (r *rig) waitJob(id string) flowdproto.JobState {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := r.api.Job(context.Background(), id)
		if err == nil && st.Done {
			return st
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("job %s did not end: %+v, %v", id, st, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (r *rig) jobLog(id string) []string {
	r.t.Helper()
	lines, err := r.api.JobLog(context.Background(), id, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for i, l := range lines {
		if l.N != i+1 {
			r.t.Fatalf("line %d numbered %d", i+1, l.N)
		}
		out = append(out, l.Text)
	}
	return out
}

func runs(t *testing.T, counter string) int {
	t.Helper()
	b, _ := os.ReadFile(counter)
	return bytes.Count(b, []byte("ran\n"))
}

// The setup script runs once, in the main checkout, with its output and status in the log and the commit and blob
// it ran from in the result; asking again (a later bring-up of the same instance) reports the job, never runs it.
func TestSetupJob_runsOnceAndRecordsWhatRan(t *testing.T) {
	r := newRig(t)
	counter := filepath.Join(t.TempDir(), "count")
	st := r.setupRepo("echo ran >> " + counter + "\npwd\necho to stderr >&2\n")
	if !st.SetupScript {
		t.Fatal("EnsureRepo did not report the setup script")
	}
	ctx := context.Background()
	if _, err := r.api.StartJob(ctx, setupSpec); err != nil {
		t.Fatal(err)
	}
	done := r.waitJob(setupSpec.ID)
	if done.ExitCode != 0 || done.TimedOut || done.Commit != st.HeadSHA || len(done.Script) != 40 {
		t.Errorf("job %+v, head %s", done, st.HeadSHA)
	}
	lines := r.jobLog(setupSpec.ID)
	if done.Lines != len(lines) || !slices.Contains(lines, "to stderr") ||
		!slices.Contains(lines, filepath.Join(r.root, layout.Checkout("setup"))) &&
			!slices.ContainsFunc(lines, func(l string) bool { return strings.HasSuffix(l, layout.Checkout("setup")) }) ||
		lines[len(lines)-1] != "[flow] .flow/setup.sh exited with status 0" {
		t.Errorf("log %q", lines)
	}
	if again, err := r.api.StartJob(ctx, setupSpec); err != nil || !again.Done || again.Commit != done.Commit {
		t.Errorf("asked again: %+v, %v", again, err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := runs(t, counter); n != 1 {
		t.Errorf("the script ran %d times", n)
	}
	props := strings.Join(r.units.rootProps[layout.JobUnit(setupSpec.ID)], " ")
	for _, p := range []string{"MemoryMax=1073741824", "CPUQuota=200%", "TasksMax=512", "RuntimeMaxSec=1250",
		"EnvironmentFile=" + r.root + layout.JobEnvFile(setupSpec.ID)} {
		if !strings.Contains(props, p) {
			t.Errorf("unit props %q lack %s", props, p)
		}
	}
	if _, err := os.Stat(filepath.Join(r.root, layout.JobEnvFile(setupSpec.ID))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the job's environment file stayed: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(r.root, layout.JobDir)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("job dir %v, %v", fi.Mode(), err)
	}
}

// A failing script and one past its time limit end the job with their status, the log telling why.
func TestSetupJob_failureAndTimeout(t *testing.T) {
	r := newRig(t)
	r.setupRepo("echo building\nexit 3\n")
	if _, err := r.api.StartJob(context.Background(), setupSpec); err != nil {
		t.Fatal(err)
	}
	if st := r.waitJob(setupSpec.ID); st.ExitCode != 3 || st.TimedOut {
		t.Errorf("failed job %+v", st)
	}
	if lines := r.jobLog(setupSpec.ID); !slices.Contains(lines, "building") ||
		lines[len(lines)-1] != "[flow] .flow/setup.sh exited with status 3" {
		t.Errorf("log %q", lines)
	}

	r2 := newRig(t)
	r2.setupRepo("echo waiting\nsleep 30\n")
	spec := setupSpec
	spec.TimeoutSeconds = 1
	t0 := time.Now()
	if _, err := r2.api.StartJob(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	st := r2.waitJob(spec.ID)
	if !st.TimedOut || st.ExitCode == 0 || time.Since(t0) > 15*time.Second {
		t.Errorf("timed-out job %+v after %s", st, time.Since(t0))
	}
	if lines := r2.jobLog(spec.ID); !strings.Contains(lines[len(lines)-1], "ran past its time limit of 1s") {
		t.Errorf("log %q", lines)
	}
}

// A job still running across a flowd restart is found again and reported, never started a second time; one whose
// unit was stopped under it (no result) runs again from line 1.
func TestSetupJob_flowdRestartAndCutOff(t *testing.T) {
	r := newRig(t)
	counter := filepath.Join(t.TempDir(), "count")
	r.setupRepo("echo ran >> " + counter + "\nsleep 1\necho finished\n")
	ctx := context.Background()
	if st, err := r.api.StartJob(ctx, setupSpec); err != nil || !st.Running() {
		t.Fatalf("start: %+v, %v", st, err)
	}
	r.flowd() // no environment pushed yet: a running job is reported all the same
	if st, err := r.api.StartJob(ctx, setupSpec); err != nil || !st.Running() {
		t.Fatalf("after the restart: %+v, %v", st, err)
	}
	if st := r.waitJob(setupSpec.ID); st.ExitCode != 0 {
		t.Errorf("job %+v", st)
	}
	if n := runs(t, counter); n != 1 {
		t.Errorf("the script ran %d times across the restart", n)
	}

	r2 := newRig(t)
	counter2 := filepath.Join(t.TempDir(), "count")
	r2.setupRepo("echo ran >> " + counter2 + "\nsleep 30\n")
	if _, err := r2.api.StartJob(ctx, setupSpec); err != nil {
		t.Fatal(err)
	}
	for runs(t, counter2) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	_ = r2.units.Stop(ctx, layout.JobUnit(setupSpec.ID))
	st, err := r2.api.Job(ctx, setupSpec.ID)
	if err != nil || st.Done || st.Running() {
		t.Fatalf("cut-off job %+v, %v", st, err)
	}
	if st, err := r2.api.StartJob(ctx, setupSpec); err != nil || !st.Running() {
		t.Fatalf("restart of a cut-off job: %+v, %v", st, err)
	}
	for runs(t, counter2) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if lines := r2.jobLog(setupSpec.ID); len(lines) == 0 || !strings.HasPrefix(lines[0], "[flow] .flow/setup.sh of") {
		t.Errorf("log of the second run %q", lines)
	}
	_ = r2.units.Stop(ctx, layout.JobUnit(setupSpec.ID))
}

// A unit stop signals every process of the unit: a script killed by it a moment before the runner's own stop
// arrives records no result (the job is cut off and runs again), never a failure.
func TestRunJob_stopSignalIsNoResult(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	_ = os.MkdirAll(filepath.Join(dir, ".flow"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, layout.SetupScript), []byte("echo $$ > "+pidFile+"\nsleep 30\n"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := filepath.Join(dir, "result.json")
	done := make(chan error, 1)
	go func() {
		done <- runJob(ctx, jobArgs{id: "setup-x", dir: dir, script: layout.SetupScript, log: filepath.Join(dir, "log"),
			result: result, timeout: time.Minute}, slog.New(slog.DiscardHandler))
	}()
	var pid int
	for pid == 0 {
		b, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		time.Sleep(5 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a stopped job recorded a result: %v", err)
	}
}

// Only a committed regular file counts: a link at .flow/setup.sh is no setup script, a wrong id or kind is refused,
// and a link planted at a log's name is never read through.
func TestSetupJob_refusals(t *testing.T) {
	r := newRig(t)
	r.setupRepo("echo hi\n")
	ctx := context.Background()
	for _, spec := range []flowdproto.JobSpec{{ID: "setup-other", Kind: flowdproto.JobSetup, Repo: "trick77/setup"},
		{ID: "../x", Kind: flowdproto.JobSetup, Repo: "trick77/setup"}} {
		if _, err := r.api.StartJob(ctx, spec); !errors.Is(err, flowdproto.ErrInvalid) {
			t.Errorf("%+v: %v", spec, err)
		}
	}
	if _, err := r.api.StartJob(ctx, flowdproto.JobSpec{ID: "c1", Kind: flowdproto.JobConfigSync}); !errors.Is(err,
		flowdproto.ErrNotImplemented) {
		t.Errorf("config sync: %v", err)
	}
	if _, err := r.api.Job(ctx, "nothing"); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("unknown job: %v", err)
	}

	script := filepath.Join(r.root, layout.Checkout("setup"), layout.SetupScript)
	_ = os.Remove(script)
	if err := os.Symlink("/etc/passwd", script); err != nil {
		t.Fatal(err)
	}
	if st, err := r.api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "trick77/setup"}); err != nil || st.SetupScript {
		t.Errorf("a link counted as the setup script: %+v, %v", st, err)
	}
	if _, err := r.api.StartJob(ctx, setupSpec); !errors.Is(err, flowdproto.ErrInvalid) {
		t.Errorf("start with a link: %v", err)
	}

	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("not yours\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(r.root, layout.JobDir), 0o700)
	if err := os.Symlink(secret, filepath.Join(r.root, layout.JobLog("setup-planted"))); err != nil {
		t.Fatal(err)
	}
	if lines, err := r.api.JobLog(ctx, "setup-planted", 0); err == nil {
		t.Errorf("read through a link: %v", lines)
	}
}

// The log keeps whole lines, cuts long ones and stops at its bound with a closing count; the script's environment
// has git identity and caches but no Claude credential or MCP token.
func TestJobLog_boundsAndEnvironment(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	w := &jobLog{f: f}
	var in bytes.Buffer
	in.WriteString("short\r\n")
	in.WriteString(strings.Repeat("x", 3*jobLineMax) + "\n")
	for in.Len() < jobLogMax+(1<<20) {
		in.WriteString(strings.Repeat("y", 1000) + "\n")
	}
	in.WriteString("tail without newline")
	w.copy(&in)
	if w.dropped == 0 || w.bytes > jobLogMax {
		t.Errorf("kept %d bytes, dropped %d lines", w.bytes, w.dropped)
	}
	_ = f.Close()
	b, _ := os.ReadFile(f.Name())
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if lines[0] != "short" || lines[1] != strings.Repeat("x", jobLineMax)+" [line cut]" || len(lines) != w.lines {
		t.Errorf("first lines %q, %q; %d lines, counted %d", lines[0], lines[1][:20], len(lines), w.lines)
	}

	env := strings.Join(jobEnv([]string{"PATH=/bin", "GIT_AUTHOR_NAME=Dev", "CLAUDE_CODE_OAUTH_TOKEN=tok",
		"ANTHROPIC_API_KEY=key", "BACKEND_MCP_TOKEN=mcp", "HOME=/root", "USER=root"}), "\n")
	for _, want := range []string{"GIT_AUTHOR_NAME=Dev", "GOMODCACHE=" + layout.GoModCache, "HOME=" + layout.Home,
		"USER=" + layout.AgentUser, "GH_CONFIG_DIR=" + layout.GHConfigDir} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %s:\n%s", want, env)
		}
	}
	for _, not := range []string{"tok", "key", "mcp", "/root"} {
		if strings.Contains(env, "="+not) {
			t.Errorf("environment holds %s:\n%s", not, env)
		}
	}
}
