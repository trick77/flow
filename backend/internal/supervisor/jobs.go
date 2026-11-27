package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host/claude"
	"github.com/trick77/flow/internal/layout"
)

// Jobs: a repository's .flow/setup.sh, run once per instance when Flow builds the sandbox. Each job is a root unit
// flow-job-<id> running `flowd job` (RunJob): the runner owns the job's log and result in layout.JobDir (root 0700,
// files opened without following links) and runs the script as the agent user, with a session's limits and the
// environment a terminal of the subscription_login mode gets (caches, git identity, gh; no Claude credential, no MCP
// token). flowd keeps nothing about a job in memory: a result on disk means it ended (and it is never started
// again), an active unit that it runs, neither that it never ran or was cut off (the sandbox stopped under it), and
// only then does StartJob start it.

// Bounds of a job's log: lines past jobLineMax bytes are cut, and past jobLogMax bytes no more lines are kept (a
// closing line counts what was dropped). Line numbers stay stable: the log only grows.
const (
	jobLogMax  = 4 << 20
	jobLineMax = 4 << 10
)

// jobGrace is how long a job's processes get after SIGTERM (time limit reached, unit stopped) before SIGKILL.
const jobGrace = 10 * time.Second

// jobResult is layout.JobResult.
type jobResult struct {
	ID       string             `json:"id"`
	Kind     flowdproto.JobKind `json:"kind"`
	Repo     string             `json:"repo"`
	Commit   string             `json:"commit"`
	Script   string             `json:"script"`
	ExitCode int                `json:"exitCode"`
	TimedOut bool               `json:"timedOut,omitempty"`
	Lines    int                `json:"lines"`
	Started  time.Time          `json:"started"`
	Ended    time.Time          `json:"ended"`
}

// validJobID: a layout name, or a setup job's (layout.SetupJobID of one).
func validJobID(id string) bool {
	rest, ok := strings.CutPrefix(id, "setup-")
	return layout.ValidName(id) || ok && layout.ValidName(rest)
}

// openNoFollow opens a file in layout.JobDir for reading, refusing a link at its name.
func openNoFollow(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // layout path of a checked job id
}

// jobDir makes layout.JobDir root 0700 and refuses anything else at its name (a link, a file, another owner).
func (s *Supervisor) jobDir() error {
	dir := s.p(layout.JobDir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o711); err != nil { //nolint:gosec // traverse only, as mkdirAgent
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 && st.Uid != 0 {
		return fmt.Errorf("%s is not root's", dir)
	}
	return nil
}

// regularSetupScript reports whether checkout holds layout.SetupScript as a regular file below a real directory
// (no link at either name).
func regularSetupScript(checkout string) bool {
	d, err := os.Lstat(filepath.Join(checkout, ".flow"))
	if err != nil || !d.IsDir() {
		return false
	}
	f, err := os.Lstat(filepath.Join(checkout, layout.SetupScript))
	return err == nil && f.Mode().IsRegular()
}

// setupScript is the checkout's HEAD and the setup script's blob there: the script must be a regular file in the
// commit (mode 100644 or 100755, not a link) and in the work tree.
func (s *Supervisor) setupScript(ctx context.Context, checkout string) (commit, blob string, err error) {
	if !regularSetupScript(checkout) {
		return "", "", invalid("no regular %s in %s", layout.SetupScript, checkout)
	}
	if commit, err = s.git(ctx, checkout, "rev-parse", "HEAD"); err != nil {
		return "", "", err
	}
	out, err := s.git(ctx, checkout, "ls-tree", "HEAD", "--", layout.SetupScript)
	if err != nil {
		return "", "", err
	}
	f := strings.Fields(out)
	if len(f) < 3 || f[1] != "blob" || f[0] != "100644" && f[0] != "100755" {
		return "", "", invalid("%s is not a committed regular file in %s", layout.SetupScript, checkout)
	}
	return commit, f[2], nil
}

// StartJob implements flowdproto.API for setup jobs: a job that ended (its result) or runs is reported as it is;
// otherwise its unit is started. The id must be the repository's layout.SetupJobID.
func (s *Supervisor) StartJob(ctx context.Context, j flowdproto.JobSpec) (flowdproto.JobState, error) {
	if j.Kind != flowdproto.JobSetup {
		return flowdproto.JobState{}, &codeError{code: flowdproto.CodeNotImplemented,
			err: fmt.Errorf("%w: job kind %q", flowdproto.ErrNotImplemented, j.Kind)}
	}
	dir, ok := repoDir(j.Repo)
	if !ok || j.ID != layout.SetupJobID(dir) || j.TimeoutSeconds < 0 || j.Limits.MemoryMaxBytes < 0 ||
		j.Limits.CPUQuotaPercent < 0 || j.Limits.TasksMax < 0 {
		return flowdproto.JobState{}, invalid("job %q of repo %q", j.ID, j.Repo)
	}
	unit := layout.JobUnit(j.ID)
	defer s.lock(unit)()
	st, err := s.job(ctx, j.ID)
	switch {
	case err == nil && (st.Done || st.Running()):
		return st, nil
	case err != nil && !errors.Is(err, flowdproto.ErrNotFound):
		return st, err
	}
	if err := s.envReady(); err != nil {
		return flowdproto.JobState{}, err
	}
	if err := s.jobDir(); err != nil {
		return flowdproto.JobState{}, err
	}
	checkout := s.p(layout.Checkout(dir))
	commit, blob, err := s.setupScript(ctx, checkout)
	if err != nil {
		return flowdproto.JobState{}, err
	}
	if st.Unit != "" && st.Unit != flowdproto.UnitMissing {
		if err := s.o.Units.Stop(ctx, unit); err != nil { // a failed or ending unit keeps its name loaded
			return flowdproto.JobState{}, err
		}
	}
	// A log left by a run that was cut off goes: the new run starts at line 1. Remove never follows a link.
	if err := os.Remove(s.p(layout.JobLog(j.ID))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return flowdproto.JobState{}, err
	}
	envFile, err := s.writeSessionEnv(flowdproto.SessionSpec{ID: "job-" + j.ID, Provider: event.ProviderClaude,
		ClaudeAuth: event.ClaudeAuthSubscriptionLogin})
	if err != nil {
		return flowdproto.JobState{}, err
	}
	timeout := flowdproto.JobTimeout
	if j.TimeoutSeconds > 0 {
		timeout = time.Duration(j.TimeoutSeconds) * time.Second
	}
	argv := []string{s.o.Exe, "job", "--id", j.ID, "--repo", j.Repo, "--dir", checkout, "--script", layout.SetupScript,
		"--log", s.p(layout.JobLog(j.ID)), "--result", s.p(layout.JobResult(j.ID)), "--env-file", envFile,
		"--timeout", timeout.String(), "--commit", commit, "--blob", blob}
	var props []string
	if j.Limits.MemoryMaxBytes > 0 {
		props = append(props, fmt.Sprintf("MemoryMax=%d", j.Limits.MemoryMaxBytes))
	}
	if j.Limits.CPUQuotaPercent > 0 {
		props = append(props, fmt.Sprintf("CPUQuota=%d%%", j.Limits.CPUQuotaPercent))
	}
	if j.Limits.TasksMax > 0 {
		props = append(props, fmt.Sprintf("TasksMax=%d", j.Limits.TasksMax))
	}
	// systemd's own bound behind the runner's: the runner ends the script at the limit and needs jobGrace to kill it.
	props = append(props, fmt.Sprintf("RuntimeMaxSec=%d", int((timeout+2*jobGrace+30*time.Second).Seconds())),
		"EnvironmentFile="+envFile)
	if err := s.o.Units.StartRoot(ctx, unit, argv, props); err != nil {
		return flowdproto.JobState{}, err
	}
	s.o.Log.Info("job started", "job", j.ID, "repo", j.Repo, "commit", commit, "script", blob, "unit", unit)
	return s.job(ctx, j.ID)
}

// Job implements flowdproto.API.
func (s *Supervisor) Job(ctx context.Context, id string) (flowdproto.JobState, error) {
	if !validJobID(id) {
		return flowdproto.JobState{}, invalid("job id %q", id)
	}
	return s.job(ctx, id)
}

// job is a job's state from its result, else its unit and log; ErrNotFound when it has none of them (Unit is set
// all the same).
func (s *Supervisor) job(ctx context.Context, id string) (flowdproto.JobState, error) {
	unit, err := s.o.Units.State(ctx, layout.JobUnit(id))
	if err != nil {
		return flowdproto.JobState{}, err
	}
	st := flowdproto.JobState{ID: id, Kind: flowdproto.JobSetup, Unit: unit}
	if r, err := s.readResult(id); err == nil {
		st.Done, st.ExitCode, st.TimedOut, st.Lines, st.Commit, st.Script = true, r.ExitCode, r.TimedOut, r.Lines,
			r.Commit, r.Script
		return st, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return st, err
	}
	n, err := s.countLines(id)
	if errors.Is(err, os.ErrNotExist) {
		if st.Running() {
			return st, nil // started, its runner has not opened the log yet
		}
		return st, fmt.Errorf("%w: job %s", flowdproto.ErrNotFound, id)
	}
	st.Lines = n
	return st, err
}

func (s *Supervisor) readResult(id string) (jobResult, error) {
	var r jobResult
	f, err := openNoFollow(s.p(layout.JobResult(id)))
	if err != nil {
		return r, err
	}
	defer func() { _ = f.Close() }()
	if err := json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&r); err != nil {
		return r, fmt.Errorf("job %s result: %w", id, err)
	}
	return r, nil
}

func (s *Supervisor) countLines(id string) (int, error) {
	f, err := openNoFollow(s.p(layout.JobLog(id)))
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	n := 0
	buf := make([]byte, 64<<10)
	for {
		k, err := f.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// JobLog implements flowdproto.API: the runner writes whole lines only, so every line read is complete.
func (s *Supervisor) JobLog(_ context.Context, id string, after int) ([]flowdproto.LogLine, error) {
	if !validJobID(id) {
		return nil, invalid("job id %q", id)
	}
	f, err := openNoFollow(s.p(layout.JobLog(id)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: job %s", flowdproto.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := []flowdproto.LogLine{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 2*jobLineMax)
	for n := 1; sc.Scan(); n++ {
		if n > after {
			out = append(out, flowdproto.LogLine{N: n, Text: sc.Text()})
			if len(out) == flowdproto.JobLogPage {
				break
			}
		}
	}
	return out, sc.Err()
}

// jobRoutes mounts the job routes on mux.
func (s *Supervisor) jobRoutes(mux *http.ServeMux) {
	mux.HandleFunc(flowdproto.RouteJobPut, func(w http.ResponseWriter, r *http.Request) {
		var spec flowdproto.JobSpec
		if decode(w, r, &spec) {
			if spec.ID != r.PathValue("id") {
				reply(w, nil, invalid("job id in path and body differ"))
				return
			}
			st, err := s.StartJob(r.Context(), spec)
			reply(w, st, err)
		}
	})
	mux.HandleFunc(flowdproto.RouteJobGet, func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Job(r.Context(), r.PathValue("id"))
		reply(w, st, err)
	})
	mux.HandleFunc(flowdproto.RouteJobLog, func(w http.ResponseWriter, r *http.Request) {
		after := 0
		if v := r.URL.Query().Get("after"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				reply(w, nil, invalid("after %q", v))
				return
			}
			after = n
		}
		lines, err := s.JobLog(r.Context(), r.PathValue("id"), after)
		reply(w, lines, err)
	})
}

// jobArgs are `flowd job`'s flags.
type jobArgs struct {
	id, repo, dir, script, log, result, envFile, commit, blob string
	timeout                                                   time.Duration
}

// RunJob is `flowd job`, a job unit's runner (root): it runs the script with bash as the agent user in its
// checkout, writes its output to the bounded log and, when the script ended (or its time limit did), the result. A
// runner stopped from outside (the unit stopped, the sandbox shutting down) kills the script and records no result:
// the job counts as cut off and the next StartJob runs it again.
func RunJob(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("job", flag.ContinueOnError)
	var a jobArgs
	for name, p := range map[string]*string{"id": &a.id, "repo": &a.repo, "dir": &a.dir, "script": &a.script,
		"log": &a.log, "result": &a.result, "env-file": &a.envFile, "commit": &a.commit, "blob": &a.blob} {
		fs.StringVar(p, name, "", name)
	}
	fs.DurationVar(&a.timeout, "timeout", flowdproto.JobTimeout, "time limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if a.id == "" || a.dir == "" || a.script == "" || a.log == "" || a.result == "" || a.timeout <= 0 {
		return errors.New("job: --id, --dir, --script, --log, --result and a positive --timeout are required")
	}
	return runJob(ctx, a, log)
}

func runJob(ctx context.Context, a jobArgs, log *slog.Logger) error {
	if a.envFile != "" {
		// Read by systemd at the unit's start; nothing needs it afterwards.
		defer func() { _ = os.Remove(a.envFile) }()
	}
	started := time.Now()
	// O_EXCL never follows a link at the name, and JobDir is root's.
	f, err := os.OpenFile(a.log, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // flowd's path
	if err != nil {
		return err
	}
	w := &jobLog{f: f}
	defer func() { _ = f.Close() }()
	w.note(fmt.Sprintf("[flow] %s of %s at %.12s, as %s, time limit %s", a.script, a.repo, a.commit,
		layout.AgentUser, a.timeout))
	code, timedOut, err := w.run(ctx, a)
	if err == nil && !timedOut && code > 128 {
		// A unit stop signals the whole cgroup: the script may die of it before the runner sees its own stop. Such
		// an end is a cut-off, not the script's result.
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	if ctx.Err() != nil {
		log.Warn("job stopped before its end: no result recorded", "job", a.id)
		return nil
	}
	if err != nil {
		w.note("[flow] " + a.script + " could not run: " + err.Error())
		code = -1
	}
	switch {
	case timedOut:
		w.note(fmt.Sprintf("[flow] %s ran past its time limit of %s and was ended", a.script, a.timeout))
	case err == nil:
		w.note(fmt.Sprintf("[flow] %s exited with status %d", a.script, code))
	}
	if w.dropped > 0 {
		w.note(fmt.Sprintf("[flow] %d more lines (%d bytes) not kept: the log keeps its first %d MiB", w.dropped,
			w.droppedBytes, jobLogMax>>20))
	}
	r := jobResult{ID: a.id, Kind: flowdproto.JobSetup, Repo: a.repo, Commit: a.commit, Script: a.blob, ExitCode: code,
		TimedOut: timedOut, Lines: w.lines, Started: started, Ended: time.Now()}
	if err := writeResult(a.result, r); err != nil {
		return err
	}
	log.Info("job ended", "job", a.id, "exit", code, "timedOut", timedOut, "lines", w.lines)
	return nil
}

// writeResult writes the result beside the log: a new file (O_EXCL) renamed into place, never through a link.
func writeResult(p string, r jobResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".result-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// jobEnv is the script's environment from the runner's (the unit's, with its EnvironmentFile): what claude.Env
// passes a subscription_login agent, as the agent user.
func jobEnv(base []string) []string {
	base = append(base, "HOME="+layout.Home, "USER="+layout.AgentUser, "LOGNAME="+layout.AgentUser, "SHELL=/bin/bash")
	env, _ := claude.Env(base, event.ClaudeAuthSubscriptionLogin)
	return env
}

// agentGroups are the agent user's supplementary groups (docker among them): setting only uid and gid would drop
// them.
func agentGroups() []uint32 {
	u, err := user.LookupId(strconv.Itoa(layout.AgentUID))
	if err != nil {
		return nil
	}
	ids, err := u.GroupIds()
	if err != nil {
		return nil
	}
	var out []uint32
	for _, id := range ids {
		if n, err := strconv.ParseUint(id, 10, 32); err == nil {
			out = append(out, uint32(n))
		}
	}
	return out
}

// run runs the script to its end, its time limit, or ctx's end, and returns its exit status (128 + the signal that
// ended it).
func (w *jobLog) run(ctx context.Context, a jobArgs) (code int, timedOut bool, err error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = pr.Close() }()
	cmd := exec.Command("/bin/bash", a.script) //nolint:gosec // the repository's setup script, run as the agent
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = a.dir, jobEnv(os.Environ()), pw, pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: layout.AgentUID, Gid: layout.AgentGID,
			Groups: agentGroups()}
	}
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return 0, false, err
	}
	_ = pw.Close()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		w.copy(pr)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	pgid := cmd.Process.Pid
	limit := time.NewTimer(a.timeout)
	defer limit.Stop()
	var werr error
	select {
	case werr = <-exited:
	case <-limit.C:
		timedOut = true
		werr = endGroup(pgid, exited)
	case <-ctx.Done():
		_ = endGroup(pgid, exited)
		return 0, false, ctx.Err()
	}
	// A process the script left behind may hold the pipe open: its output is taken for a moment, then cut.
	select {
	case <-copied:
	case <-time.After(5 * time.Second):
		_ = pr.Close()
		<-copied
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
		return 0, timedOut, nil
	case errors.As(werr, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), timedOut, nil
		}
		return ee.ExitCode(), timedOut, nil
	}
	return 0, timedOut, werr
}

// endGroup ends the script's process group: SIGTERM, jobGrace, SIGKILL; it returns the script's wait result.
func endGroup(pgid int, exited <-chan error) error {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case err := <-exited:
		_ = syscall.Kill(-pgid, syscall.SIGKILL) // whatever of the group is left
		return err
	case <-time.After(jobGrace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return <-exited
	}
}

// jobLog is the bounded log a runner writes: one write per whole line.
type jobLog struct {
	f                     *os.File
	bytes, lines          int
	dropped, droppedBytes int
}

// line keeps one output line, while the log is under jobLogMax.
func (w *jobLog) line(b []byte) {
	if w.dropped > 0 || w.bytes+len(b)+1 > jobLogMax {
		w.dropped++
		w.droppedBytes += len(b) + 1
		return
	}
	w.write(b)
}

// note writes one of the runner's own lines, past the bound too.
func (w *jobLog) note(s string) { w.write([]byte(s)) }

func (w *jobLog) write(b []byte) {
	if _, err := w.f.Write(append(bytes.Clone(b), '\n')); err == nil {
		w.bytes += len(b) + 1
		w.lines++
	}
}

// copy splits r into lines, each cut at jobLineMax bytes, a carriage return before the newline dropped.
func (w *jobLog) copy(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var cur []byte
	cut := false
	emit := func() {
		line := bytes.TrimSuffix(cur, []byte{'\r'})
		if cut {
			line = append(line, " [line cut]"...)
		}
		w.line(line)
		cur, cut = cur[:0], false
	}
	for {
		chunk, err := br.ReadSlice('\n')
		body, nl := bytes.CutSuffix(chunk, []byte{'\n'})
		if room := jobLineMax - len(cur); room < len(body) {
			cur, cut = append(cur, body[:max(room, 0)]...), true
		} else {
			cur = append(cur, body...)
		}
		if nl {
			emit()
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(cur) > 0 || cut {
				emit()
			}
			return
		}
	}
}
