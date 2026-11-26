// Package supervisor is flowd's core: the flowdproto server Flow dials over the bridge, the session hosts as
// transient systemd units (re-attached by unit name and socket after a flowd restart), repositories and worktrees in
// the agent user's name, and the event logs served by seq from disk, live frames from the hosts on top.
package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host"
	"github.com/trick77/flow/internal/hostproto"
	"github.com/trick77/flow/internal/layout"
)

// Options wire a Supervisor.
type Options struct {
	Version string
	Sandbox string
	// Root prefixes every layout path; "" on a real sandbox, a temporary directory in tests.
	Root  string
	Units Units
	Run   Runner
	// Exe is the flowd binary hosts are started from: the release's own file, never the /opt/flow/bin symlink, so a
	// host keeps its binary when flowd is updated.
	Exe string
	// HostArgs are appended to every host's command line (tests: --pace 0).
	HostArgs []string
	// CloneBase prefixes owner/name.git for clones; https://github.com/ by default.
	CloneBase string
	Log       *slog.Logger
	// HostWait bounds how long EnsureSession waits for a started host's socket.
	HostWait time.Duration
}

// Supervisor is flowd's state.
type Supervisor struct {
	o   Options
	mu  sync.Mutex
	env int64
	// locks serializes work on one repository's main checkout (git takes its own lock and fails instead of waiting).
	locks sync.Map
}

// New returns a Supervisor.
func New(o Options) *Supervisor {
	if o.CloneBase == "" {
		o.CloneBase = "https://github.com/"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.HostWait == 0 {
		o.HostWait = 15 * time.Second
	}
	return &Supervisor{o: o}
}

func (s *Supervisor) p(rel string) string { return filepath.Join(s.o.Root, rel) }

// Reattach finds the host units that survived flowd's restart and greets each.
func (s *Supervisor) Reattach(ctx context.Context) []flowdproto.SessionState {
	units, err := s.o.Units.List(ctx, "flow-session-*")
	if err != nil {
		s.o.Log.Warn("listing host units", "err", err)
		return nil
	}
	var out []flowdproto.SessionState
	for _, u := range units {
		id := strings.TrimSuffix(strings.TrimPrefix(u, "flow-session-"), ".service")
		st, err := s.Session(ctx, id)
		if err != nil {
			continue
		}
		s.o.Log.Info("reattached host", "session", id, "unit", st.Unit, "lastSeq", st.LastSeq, "status", st.Status)
		out = append(out, st)
	}
	return out
}

// Hello implements flowdproto.API.
func (s *Supervisor) Hello(ctx context.Context) (flowdproto.Hello, error) {
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	s.mu.Lock()
	rev := s.env
	s.mu.Unlock()
	h := flowdproto.Hello{Protocol: flowdproto.Protocol, MinProtocol: flowdproto.MinProtocol, Version: s.o.Version,
		Sandbox: s.o.Sandbox, Arch: runtime.GOARCH, BootID: strings.TrimSpace(string(boot)), EnvRevision: rev,
		Sessions: []flowdproto.SessionState{}, Terminals: []flowdproto.TerminalState{}}
	units, err := s.o.Units.List(ctx, "flow-session-*")
	if err != nil {
		return h, err
	}
	for _, u := range units {
		id := strings.TrimSuffix(strings.TrimPrefix(u, "flow-session-"), ".service")
		if st, err := s.Session(ctx, id); err == nil {
			h.Sessions = append(h.Sessions, st)
		}
	}
	return h, nil
}

// PutEnv implements flowdproto.API. The skeleton keeps only the revision: no credential reaches a sandbox yet.
func (s *Supervisor) PutEnv(_ context.Context, e flowdproto.Env) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Revision > s.env {
		s.env = e.Revision
	}
	return nil
}

// repoDir is a repository's directory name (layout.RepoDir); ok is false for one flowd refuses.
func repoDir(full string) (string, bool) {
	owner, _, _ := strings.Cut(full, "/")
	dir, ok := layout.RepoDir(full)
	return dir, ok && !strings.ContainsAny(owner, ".")
}

func (s *Supervisor) lock(key string) func() {
	m, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Supervisor) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := s.o.Run.Run(ctx, Cmd{Name: "git", Args: args, Dir: dir, AsAgent: true,
		Env: []string{"GIT_TERMINAL_PROMPT=0"}})
	return strings.TrimSpace(string(out)), err
}

// mkdirAgent creates dir, owned by the agent user when flowd is root. Missing parents (root's) are made traversable
// but not listable (0711): /run/flow/sessions is gone after every boot, and the agent user must reach its socket
// directory below it.
func (s *Supervisor) mkdirAgent(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o711); err != nil { //nolint:gosec // traverse only, see above
		return err
	}
	if err := os.Mkdir(dir, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(dir, layout.AgentUID, layout.AgentGID)
	}
	return nil
}

// writeSpec writes the spec beside the log, readable by the host (the agent user) only.
func (s *Supervisor) writeSpec(sp host.Spec) error {
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	path := s.p(layout.SessionSpecFile(sp.Session.ID))
	// The path is layout's, from a session id EnsureSession checked with layout.ValidName.
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // see above
		return err
	}
	if os.Geteuid() == 0 {
		return os.Chown(path, layout.AgentUID, layout.AgentGID)
	}
	return nil
}

// EnsureRepo implements flowdproto.API: a missing main checkout is cloned, an existing one fetched. A clone goes
// into a temporary directory that is renamed into place only when complete, and a checkout without a resolvable
// HEAD (left by an older flowd or a crash) counts as missing: otherwise one interrupted clone would fail every later
// fetch.
func (s *Supervisor) EnsureRepo(ctx context.Context, r flowdproto.RepoSpec) (flowdproto.RepoState, error) {
	name, ok := repoDir(r.Repo)
	if !ok {
		return flowdproto.RepoState{}, invalid("repo %q", r.Repo)
	}
	dir := s.p(layout.Checkout(name))
	defer s.lock(dir)()
	state, err := s.inspect(ctx, dir)
	if err != nil {
		return flowdproto.RepoState{}, err
	}
	switch state {
	case checkoutUsable:
		if _, err := s.git(ctx, dir, "fetch", "--quiet", "--prune", "origin"); err != nil {
			return flowdproto.RepoState{}, err
		}
	default:
		if err := s.clone(ctx, r, dir, state == checkoutReplaceable); err != nil {
			return flowdproto.RepoState{}, err
		}
	}
	head, err := s.git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return flowdproto.RepoState{}, err
	}
	_, setup := os.Stat(filepath.Join(dir, ".flow", "setup.sh"))
	return flowdproto.RepoState{Repo: r.Repo, Path: layout.Checkout(name), HeadSHA: head, SetupScript: setup == nil}, nil
}

// checkoutState is what inspect found at a main checkout's place.
type checkoutState int

const (
	checkoutAbsent      checkoutState = iota // nothing there: clone
	checkoutUsable                           // HEAD names a commit: fetch
	checkoutReplaceable                      // empty, or a repository without a single ref or worktree: re-clone
)

// inspect decides what EnsureRepo may do with dir. It deletes nothing and errs on the side of keeping: a main
// checkout holds every session's branches until they are pushed, so a failed or cancelled check, a repository with
// any ref or worktree, or a directory with files but no .git is an error (errRepoBroken for the last two), never a
// reason to clone over it. Only a provably empty repository (the trace of an interrupted clone) is replaceable.
func (s *Supervisor) inspect(ctx context.Context, dir string) (checkoutState, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return checkoutAbsent, nil
	case err != nil:
		return 0, err
	case len(entries) == 0:
		return checkoutReplaceable, nil
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("%w: %s holds files but no repository", errRepoBroken, dir)
	} else if err != nil {
		return 0, err
	}
	if _, err := s.git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		return checkoutUsable, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	refs, err := s.git(ctx, dir, "for-each-ref", "--count=1", "--format=%(refname)")
	if err != nil {
		return 0, fmt.Errorf("checking %s: %w", dir, err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	worktrees, err := os.ReadDir(filepath.Join(dir, ".git", "worktrees"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if refs != "" || len(worktrees) > 0 {
		return 0, fmt.Errorf("%w: %s has no usable HEAD but holds refs or worktrees", errRepoBroken, dir)
	}
	return checkoutReplaceable, nil
}

// clone makes a fresh clone in a temporary directory beside dir (a name no repository can have: leading dot) and
// moves it into place only once complete; replace allows removing what inspect found replaceable, and only then.
func (s *Supervisor) clone(ctx context.Context, r flowdproto.RepoSpec, dir string, replace bool) error {
	if err := s.mkdirAgent(filepath.Dir(dir)); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".clone")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if r.DefaultBranch != "" {
		args = append(args, "--branch", r.DefaultBranch)
	}
	if _, err := s.git(ctx, "", append(args, s.o.CloneBase+r.Repo+".git", tmp)...); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if replace {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dir)
}

// addWorktree creates one worktree of the session, a sibling of the others in the session directory, on a fresh
// fetch of its base.
func (s *Supervisor) addWorktree(ctx context.Context, session string, w flowdproto.WorktreeSpec) (flowdproto.WorktreeState, error) {
	name, ok := repoDir(w.Repo)
	if !ok || w.Branch == "" || w.BaseRef == "" {
		return flowdproto.WorktreeState{}, invalid("worktree %+v", w)
	}
	rel := layout.Worktree(session, name)
	st := flowdproto.WorktreeState{Repo: w.Repo, Branch: w.Branch, Path: rel, BaseRef: w.BaseRef}
	dir := s.p(rel)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if _, err := s.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: w.Repo}); err != nil {
			return st, err
		}
		checkout := s.p(layout.Checkout(name))
		unlock := s.lock(checkout)
		_, err := s.git(ctx, checkout, "worktree", "add", "--quiet", "-b", w.Branch, dir, w.BaseRef)
		if err != nil && strings.Contains(err.Error(), "already exists") {
			_, err = s.git(ctx, checkout, "worktree", "add", "--quiet", dir, w.Branch)
		}
		unlock()
		if err != nil {
			return st, err
		}
	}
	sha, err := s.git(ctx, dir, "rev-parse", w.BaseRef)
	if err != nil {
		return st, err
	}
	st.BaseSHA = sha
	return st, nil
}

func (s *Supervisor) readSpec(id string) (host.Spec, error) {
	var sp host.Spec
	b, err := os.ReadFile(s.p(layout.SessionSpecFile(id)))
	if errors.Is(err, os.ErrNotExist) {
		return sp, fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, id)
	}
	if err != nil {
		return sp, err
	}
	return sp, json.Unmarshal(b, &sp)
}

// EnsureSession implements flowdproto.API: worktrees are created as needed, the spec is written beside the log and
// the host unit is started unless it runs.
func (s *Supervisor) EnsureSession(ctx context.Context, sp flowdproto.SessionSpec) (flowdproto.SessionState, error) {
	if !layout.ValidName(sp.ID) || !layout.ValidName(sp.Name) {
		return flowdproto.SessionState{}, invalid("session id %q or name %q", sp.ID, sp.Name)
	}
	unit := layout.HostUnit(sp.ID)
	defer s.lock(unit)()
	if st, err := s.o.Units.State(ctx, unit); err != nil {
		return flowdproto.SessionState{}, err
	} else if st == flowdproto.UnitActive || st == flowdproto.UnitActivating {
		return s.Session(ctx, sp.ID)
	}
	spec := host.Spec{Session: sp, Dir: layout.SessionDir(sp.Name)}
	if old, err := s.readSpec(sp.ID); err == nil {
		spec.Worktrees = old.Worktrees
	}
	if err := s.claim(sp.Name, sp.ID); err != nil {
		return flowdproto.SessionState{}, err
	}
	for _, dir := range []string{s.p(layout.SessionDir(sp.Name)), filepath.Dir(s.p(layout.EventLog(sp.ID))),
		filepath.Dir(s.p(layout.HostSocket(sp.ID)))} {
		if err := s.mkdirAgent(dir); err != nil {
			return flowdproto.SessionState{}, err
		}
	}
	for _, w := range sp.Worktrees {
		if hasWorktree(spec.Worktrees, w.Repo) {
			continue
		}
		st, err := s.addWorktree(ctx, sp.Name, w)
		if err != nil {
			return flowdproto.SessionState{}, err
		}
		spec.Worktrees = append(spec.Worktrees, st)
	}
	if err := s.writeSpec(spec); err != nil {
		return flowdproto.SessionState{}, err
	}
	specFile := s.p(layout.SessionSpecFile(sp.ID))
	argv := append([]string{s.o.Exe, "host", "--session", sp.ID, "--spec", specFile, "--log",
		s.p(layout.EventLog(sp.ID)), "--socket", s.p(layout.HostSocket(sp.ID))}, s.o.HostArgs...)
	var props []string
	if sp.Limits.MemoryMaxBytes > 0 {
		props = append(props, fmt.Sprintf("MemoryMax=%d", sp.Limits.MemoryMaxBytes))
	}
	if sp.Limits.CPUQuotaPercent > 0 {
		props = append(props, fmt.Sprintf("CPUQuota=%d%%", sp.Limits.CPUQuotaPercent))
	}
	if err := s.o.Units.Start(ctx, unit, argv, props); err != nil {
		return flowdproto.SessionState{}, err
	}
	// systemd-run returns before the host listens.
	deadline := time.Now().Add(s.o.HostWait)
	for {
		if _, err := s.hostHello(ctx, sp.ID); err == nil {
			break
		} else if time.Now().After(deadline) {
			return flowdproto.SessionState{}, fmt.Errorf("host of %s did not come up: %w", sp.ID, err)
		}
		select {
		case <-ctx.Done():
			return flowdproto.SessionState{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	s.o.Log.Info("host started", "session", sp.ID, "unit", unit)
	return s.Session(ctx, sp.ID)
}

func hasWorktree(ws []flowdproto.WorktreeState, repo string) bool {
	for _, w := range ws {
		if w.Repo == repo {
			return true
		}
	}
	return false
}

// Session implements flowdproto.API: the unit, and the host's own view when it runs, else the log's.
func (s *Supervisor) Session(ctx context.Context, id string) (flowdproto.SessionState, error) {
	sp, err := s.readSpec(id)
	if err != nil {
		return flowdproto.SessionState{}, err
	}
	unit, err := s.o.Units.State(ctx, layout.HostUnit(id))
	if err != nil {
		return flowdproto.SessionState{}, err
	}
	st := flowdproto.SessionState{ID: id, Unit: unit, Dir: sp.Dir, Worktrees: sp.Worktrees, Status: event.SessionStatusIdle}
	if st.Worktrees == nil {
		st.Worktrees = []flowdproto.WorktreeState{}
	}
	if h, err := s.hostHello(ctx, id); err == nil {
		st.HostVersion, st.HostProtocol, st.LastSeq, st.Status = h.Version, h.Protocol, h.LastSeq, h.Status
		return st, nil
	}
	evs, err := eventlog.NewReader(s.p(layout.EventLog(id))).Read(0, 0)
	if err != nil {
		return st, err
	}
	for _, e := range evs {
		st.LastSeq = e.Seq
		if e.Kind == event.KindSessionStatus {
			var p event.SessionStatusChanged
			if json.Unmarshal(e.Data, &p) == nil {
				st.Status = p.Status
			}
		}
	}
	return st, nil
}

// StopSession implements flowdproto.API.
func (s *Supervisor) StopSession(ctx context.Context, id string, remove bool) error {
	sp, err := s.readSpec(id)
	if err != nil {
		return err
	}
	if err := s.o.Units.Stop(ctx, layout.HostUnit(id)); err != nil {
		return err
	}
	if !remove {
		return nil
	}
	for _, w := range sp.Worktrees {
		dir, _ := repoDir(w.Repo)
		checkout := s.p(layout.Checkout(dir))
		if _, err := s.git(ctx, checkout, "worktree", "remove", "--force", s.p(w.Path)); err != nil {
			return err
		}
	}
	// The worktrees are gone: the directory's name may serve another session.
	if err := os.Remove(s.p(layout.SessionClaim(sp.Session.Name))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// claim makes session id the owner of the session directory name, or refuses when another id owns it: worktrees
// found there belong to that session, and adopting them would hand one session's branch to another.
func (s *Supervisor) claim(name, id string) error {
	path := s.p(layout.SessionClaim(name))
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // layout path of a checked name
		if owner := strings.TrimSpace(string(b)); owner != id {
			return invalid("session directory %s belongs to session %s", name, owner)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id+"\n"), 0o600) //nolint:gosec // layout path of a checked name
}

// AddWorktree implements flowdproto.API: the worktree is created and the running host logs worktree.added.
func (s *Supervisor) AddWorktree(ctx context.Context, id string, w flowdproto.WorktreeSpec) (flowdproto.WorktreeState, error) {
	sp, err := s.readSpec(id)
	if err != nil {
		return flowdproto.WorktreeState{}, err
	}
	st, err := s.addWorktree(ctx, sp.Session.Name, w)
	if err != nil || hasWorktree(sp.Worktrees, w.Repo) {
		return st, err
	}
	sp.Worktrees = append(sp.Worktrees, st)
	if err := s.writeSpec(sp); err != nil {
		return st, err
	}
	c, err := event.NewCommand("wt-"+id+"-"+strings.ReplaceAll(w.Repo, "/", "-"), id, event.CmdWorktreeAdd,
		event.WorktreeAdd{Repo: st.Repo, Branch: st.Branch, Path: st.Path, BaseRef: st.BaseRef, BaseSHA: st.BaseSHA})
	if err == nil {
		_, err = s.Command(ctx, c)
	}
	return st, err
}

// Command implements flowdproto.API: relayed to the session's host.
func (s *Supervisor) Command(ctx context.Context, c event.Command) (flowdproto.CommandResult, error) {
	conn, err := s.dialHost(ctx, c.Session)
	if err != nil {
		return flowdproto.CommandResult{}, err
	}
	defer func() { _ = conn.Close() }()
	resp, err := roundTrip(conn, hostproto.Request{ID: "c", Op: hostproto.OpCommand, Command: &c})
	if err != nil {
		return flowdproto.CommandResult{}, err
	}
	switch resp.Error {
	case "":
		return flowdproto.CommandResult{ID: c.ID, Duplicate: resp.Duplicate}, nil
	case hostproto.CodeNotPending:
		return flowdproto.CommandResult{}, fmt.Errorf("%w: %s", flowdproto.ErrNotPending, c.ID)
	case hostproto.CodeShutdown:
		return flowdproto.CommandResult{}, fmt.Errorf("%w: host shutting down", errHostDown)
	}
	return flowdproto.CommandResult{}, invalid("host refused %s: %s", c.Kind, resp.Error)
}

// errRepoBroken is flowdproto.ErrRepoBroken.
var errRepoBroken = flowdproto.ErrRepoBroken

// errHostDown: the session has no running host; Flow calls EnsureSession and sends again.
var errHostDown = errors.New("session host not running")

func (s *Supervisor) dialHost(ctx context.Context, id string) (net.Conn, error) {
	if !layout.ValidName(id) {
		return nil, invalid("session %q", id)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", s.p(layout.HostSocket(id)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errHostDown, err)
	}
	return conn, nil
}

func (s *Supervisor) hostHello(ctx context.Context, id string) (hostproto.Hello, error) {
	conn, err := s.dialHost(ctx, id)
	if err != nil {
		return hostproto.Hello{}, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	resp, err := roundTrip(conn, hostproto.Request{ID: "h", Op: hostproto.OpHello})
	if err != nil {
		return hostproto.Hello{}, err
	}
	if resp.Hello == nil {
		return hostproto.Hello{}, errors.New("host sent no hello")
	}
	return *resp.Hello, nil
}

func roundTrip(conn net.Conn, req hostproto.Request) (hostproto.Response, error) {
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return hostproto.Response{}, err
	}
	var resp hostproto.Response
	err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp)
	return resp, err
}

// subscribe streams the host's frames into frames, re-dialling while ctx lives (the host may start later, or be
// restarted); each new subscription first wakes the reader.
func (s *Supervisor) subscribe(ctx context.Context, id string, wake chan<- struct{}, live chan<- event.Event) {
	for ctx.Err() == nil {
		if conn, err := s.dialHost(ctx, id); err == nil {
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			if json.NewEncoder(conn).Encode(hostproto.Request{ID: "s", Op: hostproto.OpSubscribe}) == nil {
				dec := json.NewDecoder(bufio.NewReader(conn))
				var resp hostproto.Response
				if dec.Decode(&resp) == nil && resp.Error == "" {
					for {
						var f hostproto.Frame
						if dec.Decode(&f) != nil {
							break
						}
						if f.Appended > 0 {
							select {
							case wake <- struct{}{}:
							default:
							}
						}
						if f.Live != nil {
							select {
							case live <- *f.Live:
							default: // a slow reader loses deltas, never durable events
							}
						}
					}
				}
			}
			stop()
			_ = conn.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// invalid is a request flowd refuses as malformed.
func invalid(format string, args ...any) error {
	return &codeError{code: flowdproto.CodeInvalid, err: fmt.Errorf(format, args...)}
}

type codeError struct {
	code string
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }
func (e *codeError) Unwrap() error { return e.err }
