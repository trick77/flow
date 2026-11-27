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
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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
	o      Options
	mu     sync.Mutex
	env    int64
	envSet flowdproto.Env
	// locks serializes work on one repository's main checkout (git takes its own lock and fails instead of waiting).
	locks sync.Map
	// logs holds one reader per session whose log was read (guarded by mu), so its seq index and the status read
	// last are kept: a stream open or a status read seeks instead of parsing the log from its start.
	logs map[string]*sessionLog
}

// sessionLog is one session's log reader and what Session read from it last (guarded by its own mu).
type sessionLog struct {
	r       *eventlog.Reader
	mu      sync.Mutex
	lastSeq int64
	status  event.SessionStatus
}

// log is the session's reader, made on first use; callers have checked the id (readSpec).
func (s *Supervisor) log(id string) *sessionLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logs == nil {
		s.logs = map[string]*sessionLog{}
	}
	l, ok := s.logs[id]
	if !ok {
		l = &sessionLog{r: eventlog.NewReader(s.p(layout.EventLog(id))), status: event.SessionStatusIdle}
		s.logs[id] = l
	}
	return l
}

// forgetLog drops a session's reader (its session removed); a stream still using it keeps its own reference.
func (s *Supervisor) forgetLog(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.logs, id)
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
		Release: s.release(), Sandbox: s.o.Sandbox, Arch: runtime.GOARCH, BootID: strings.TrimSpace(string(boot)),
		EnvRevision: rev, Sessions: []flowdproto.SessionState{}, Terminals: []flowdproto.TerminalState{}}
	// Presence only: flowd never reads the login.
	if _, err := os.Stat(s.p(layout.ClaudeConfig + "/.credentials.json")); err == nil {
		h.ClaudeSignedIn = true
	}
	units, err := s.o.Units.List(ctx, "flow-session-*")
	if err != nil {
		return h, err
	}
	// One short budget for all host sockets, one for all terminal sockets: Flow gives the whole Hello 3 s, and a
	// stalled host or terminal must not cost the sandbox its flowd. One that does not answer in time is reported by
	// its unit state alone (no log read either: after a flowd restart a long log would take the time again).
	sctx, scancel := context.WithTimeout(ctx, helloSessionBudget)
	for _, u := range units {
		id := strings.TrimSuffix(strings.TrimPrefix(u, "flow-session-"), ".service")
		if st, err := s.session(ctx, sctx, id, false); err == nil {
			h.Sessions = append(h.Sessions, st)
		}
	}
	scancel()
	qctx, cancel := context.WithTimeout(ctx, helloTerminalBudget)
	defer cancel()
	if h.Terminals, err = s.terminals(ctx, qctx); err != nil {
		return h, err
	}
	return h, nil
}

// helloSessionBudget and helloTerminalBudget bound Hello's questions to the host sockets together and to the
// terminal sockets together.
const (
	helloSessionBudget  = 500 * time.Millisecond
	helloTerminalBudget = 500 * time.Millisecond
)

// release is the release directory flowd runs from (Hello.Release), "" when its binary is not in one.
func (s *Supervisor) release() string {
	if s.o.Exe == "" || filepath.Dir(filepath.Dir(s.o.Exe)) != s.p(layout.Releases) {
		return ""
	}
	return filepath.Base(filepath.Dir(s.o.Exe))
}

// errNoEnv: no set was pushed since flowd started, so a unit started now would run without its credential and git
// identity. Answered as busy (ErrUnavailable to Flow), which Flow meets by forgetting its connection and bringing
// the sandbox up again: that greeting pushes the set.
var errNoEnv = fmt.Errorf("%w: no environment pushed since flowd started", errHostDown)

// envReady refuses starting a unit before the first push.
func (s *Supervisor) envReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.env == 0 {
		return errNoEnv
	}
	return nil
}

// PutEnv implements flowdproto.API. The set is kept in memory (a restarted flowd reports revision 0, and Flow
// pushes again); session hosts get their part at their next start (sessionEnv), and none starts before the first
// push (envReady). git and gh delivery is not built.
// Every push also makes the home volume's directories: Flow pushes at every bring-up, so a volume attached to a
// running sandbox gets them without a flowd restart.
func (s *Supervisor) PutEnv(_ context.Context, e flowdproto.Env) error {
	s.homeDirs()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Revision > s.env {
		s.env = e.Revision
		s.envSet = e
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
	// The name may lie under the agent's own tree: a link planted there is refused, never followed by the chown.
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if os.Geteuid() == 0 {
		return os.Lchown(dir, layout.AgentUID, layout.AgentGID)
	}
	return nil
}

// writeSpec writes the spec beside the log, readable by the host (the agent user) only. The directory is the
// agent's: flowd (root) writes a new file and renames it over the spec, so a symlink or hard link the agent planted
// at the spec's name is replaced, never followed, and the chown goes through the new file's descriptor.
func (s *Supervisor) writeSpec(sp host.Spec) error {
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	// The path is layout's, from a session id EnsureSession checked with layout.ValidName.
	path := s.p(layout.SessionSpecFile(sp.Session.ID))
	f, err := os.CreateTemp(filepath.Dir(path), ".spec-*") // O_EXCL: never an existing file or link
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(0o600)
	if err == nil && os.Geteuid() == 0 {
		err = f.Chown(layout.AgentUID, layout.AgentGID)
	}
	if err == nil {
		_, err = f.Write(b)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
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
	return flowdproto.RepoState{Repo: r.Repo, Path: layout.Checkout(name), HeadSHA: head,
		SetupScript: regularSetupScript(dir)}, nil
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
		_, statErr := os.Stat(dir)
		existed := statErr == nil
		unlock := s.lock(checkout)
		err := s.newWorktree(ctx, checkout, dir, w)
		if err != nil {
			s.dropWorktree(ctx, checkout, dir, existed)
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

// readSpec reads a session's spec. Every route that takes a session id comes through here (or dialHost), so this is
// where an id that is no layout name (an encoded "../" arrives unescaped in the path value) is refused before it
// becomes a file path.
// newWorktree makes a worktree on a new branch from the freshly fetched base; callers hold the checkout's lock. A
// branch of that name left by an earlier session is never taken over as it is: it would put the new session on a
// stale commit while its recorded base says otherwise. One without commits of its own (all reachable from the base)
// is reset to the base; one with commits of its own is refused and kept, since only the owner may drop work.
func (s *Supervisor) newWorktree(ctx context.Context, checkout, dir string, w flowdproto.WorktreeSpec) error {
	_, err := s.git(ctx, checkout, "worktree", "add", "--quiet", "-b", w.Branch, dir, w.BaseRef)
	if err == nil || !strings.Contains(err.Error(), "a branch named '"+w.Branch+"' already exists") {
		return err
	}
	own, err := s.ownCommits(ctx, checkout, w.BaseRef, w.Branch)
	if err != nil {
		return err
	}
	if own > 0 {
		return invalid("branch %s holds %d commits not in %s: left for the owner, not reused", w.Branch, own, w.BaseRef)
	}
	_, err = s.git(ctx, checkout, "worktree", "add", "--quiet", "-B", w.Branch, dir, w.BaseRef)
	return err
}

// dropWorktree undoes a worktree add that failed or was killed midway (a cancelled request kills git, whose own
// cleanup then never runs): the half-made directory holds .git, and a later call would adopt it as finished.
// Callers hold the checkout's lock. Runs on after ctx ended; failures are logged, the add's error stands.
func (s *Supervisor) dropWorktree(ctx context.Context, checkout, dir string, existed bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := s.git(ctx, checkout, "worktree", "remove", "--force", "--force", dir); err != nil {
		s.o.Log.Debug("removing a failed worktree", "dir", dir, "err", err)
	}
	if !existed {
		if err := os.RemoveAll(dir); err != nil {
			s.o.Log.Warn("removing a failed worktree", "dir", dir, "err", err)
		}
	}
	if _, err := s.git(ctx, checkout, "worktree", "prune"); err != nil {
		s.o.Log.Warn("pruning after a failed worktree", "dir", dir, "err", err)
	}
}

// ownCommits counts the commits on branch that its base does not reach.
func (s *Supervisor) ownCommits(ctx context.Context, checkout, base, branch string) (int, error) {
	out, err := s.git(ctx, checkout, "rev-list", "--count", base+".."+"refs/heads/"+branch)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

func (s *Supervisor) readSpec(id string) (host.Spec, error) {
	var sp host.Spec
	if !layout.ValidName(id) {
		return sp, invalid("session id %q", id)
	}
	b, err := os.ReadFile(s.p(layout.SessionSpecFile(id)))
	if errors.Is(err, os.ErrNotExist) {
		return sp, fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, id)
	}
	if err != nil {
		return sp, err
	}
	if err := json.Unmarshal(b, &sp); err != nil {
		return sp, err
	}
	// The file lies in a directory the agent user owns: flowd (root) removes its Dir and claim by these fields, so
	// they must be what flowd wrote.
	if sp.Session.ID != id || !layout.ValidName(sp.Session.Name) || sp.Dir != layout.SessionDir(sp.Session.Name) ||
		!validCwd(sp) {
		return host.Spec{}, invalid("spec of session %s: id %q, name %q, dir %q or cwd %q not flowd's", id,
			sp.Session.ID, sp.Session.Name, sp.Dir, sp.Cwd)
	}
	return sp, nil
}

// validCwd: the agent works in its session directory or in one repository's worktree there (a terminal starts in
// it, so the field must not point anywhere else).
func validCwd(sp host.Spec) bool {
	return sp.Cwd == "" || sp.Cwd == sp.Dir || path.Dir(sp.Cwd) == sp.Dir && layout.ValidName(path.Base(sp.Cwd))
}

// agentCwd is the working directory a session's agent gets, chosen once (the spec keeps it): the worktree of a
// session that starts with exactly one repository, so the agent works inside it; the session directory above
// several. A session whose log already exists keeps the directory it ran in, as its first session.started names it
// (the session directory for one from before this choice, or a spec lost or rewritten without the field): Claude
// Code stores its transcript by working directory, and a resume must find it.
func (s *Supervisor) agentCwd(spec host.Spec, logged bool) string {
	if spec.Cwd != "" {
		return spec.Cwd
	}
	if logged {
		evs, err := eventlog.NewReader(s.p(layout.EventLog(spec.Session.ID))).Read(0, 1)
		if err == nil && len(evs) == 1 && evs[0].Kind == event.KindSessionStarted {
			var st event.SessionStarted
			if json.Unmarshal(evs[0].Data, &st) == nil {
				if c := (host.Spec{Session: spec.Session, Dir: spec.Dir, Cwd: st.Cwd}); st.Cwd != "" && validCwd(c) {
					return st.Cwd
				}
			}
		}
		return spec.Dir
	}
	if len(spec.Worktrees) == 1 {
		return spec.Worktrees[0].Path
	}
	return spec.Dir
}

// EnsureSession implements flowdproto.API: worktrees are created as needed, the spec is written beside the log and
// the host unit is started unless it runs.
func (s *Supervisor) EnsureSession(ctx context.Context, sp flowdproto.SessionSpec) (flowdproto.SessionState, error) {
	if !layout.ValidName(sp.ID) || !layout.ValidName(sp.Name) {
		return flowdproto.SessionState{}, invalid("session id %q or name %q", sp.ID, sp.Name)
	}
	if err := checkProvider(sp); err != nil {
		return flowdproto.SessionState{}, err
	}
	if sp.ResumeReason != "" && !slices.Contains(event.ResumeReasonValues, sp.ResumeReason) {
		return flowdproto.SessionState{}, invalid("resume reason %q", sp.ResumeReason)
	}
	unit := layout.HostUnit(sp.ID)
	defer s.lock(unit)()
	st, err := s.o.Units.State(ctx, unit)
	if err != nil {
		return flowdproto.SessionState{}, err
	} else if st == flowdproto.UnitActive || st == flowdproto.UnitActivating {
		return s.Session(ctx, sp.ID)
	}
	if err := s.envReady(); err != nil {
		return flowdproto.SessionState{}, err
	}
	if st != flowdproto.UnitMissing {
		// A host that ended is collected right after (--collect, a failed one too); one still deactivating is
		// stopped first, or systemd-run refuses the name "already loaded" (measured, systemd 257: a blocking stop
		// waits out the deactivation and leaves the name free). As for terminals.
		if err := s.o.Units.Stop(ctx, unit); err != nil {
			return flowdproto.SessionState{}, err
		}
	}
	spec := host.Spec{Session: sp, Dir: layout.SessionDir(sp.Name)}
	if old, err := s.readSpec(sp.ID); err == nil {
		spec.Worktrees, spec.Cwd = old.Worktrees, old.Cwd
	}
	for _, dir := range []string{s.p(layout.SessionDir(sp.Name)), filepath.Dir(s.p(layout.EventLog(sp.ID))),
		filepath.Dir(s.p(layout.HostSocket(sp.ID)))} {
		if err := s.mkdirAgent(dir); err != nil {
			return flowdproto.SessionState{}, err
		}
	}
	// The spec is written before the claim and again after every worktree, so whatever this call made is in it when
	// a later step fails: stop-with-remove works from the spec and undoes it all.
	if err := s.writeSpec(spec); err != nil {
		return flowdproto.SessionState{}, err
	}
	if err := s.claim(sp.Name, sp.ID); err != nil {
		return flowdproto.SessionState{}, err
	}
	// A host that resumes an existing log logs no worktree.added for its spec (only a first start does): worktrees
	// added while it was down get theirs as commands once it runs.
	resumed := false
	if fi, err := os.Stat(s.p(layout.EventLog(sp.ID))); err == nil && fi.Size() > 0 {
		resumed = true
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
		if err := s.writeSpec(spec); err != nil {
			return flowdproto.SessionState{}, err
		}
	}
	if spec.Cwd == "" {
		spec.Cwd = s.agentCwd(spec, resumed)
		if err := s.writeSpec(spec); err != nil {
			return flowdproto.SessionState{}, err
		}
	}
	specFile := s.p(layout.SessionSpecFile(sp.ID))
	argv := append([]string{s.o.Exe, "host", "--session", sp.ID, "--spec", specFile, "--log",
		s.p(layout.EventLog(sp.ID)), "--socket", s.p(layout.HostSocket(sp.ID))}, s.o.HostArgs...)
	if sp.ResumeReason != "" {
		argv = append(argv, "--resume", string(sp.ResumeReason))
	}
	var props []string
	if sp.Limits.MemoryMaxBytes > 0 {
		props = append(props, fmt.Sprintf("MemoryMax=%d", sp.Limits.MemoryMaxBytes))
	}
	if sp.Limits.CPUQuotaPercent > 0 {
		props = append(props, fmt.Sprintf("CPUQuota=%d%%", sp.Limits.CPUQuotaPercent))
	}
	if sp.Limits.TasksMax > 0 {
		props = append(props, fmt.Sprintf("TasksMax=%d", sp.Limits.TasksMax))
	}
	envFile, err := s.writeSessionEnv(sp)
	if err != nil {
		return flowdproto.SessionState{}, err
	}
	props = append(props, "EnvironmentFile="+envFile)
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
	if resumed {
		// Every spec worktree the log lacks, not only those made by this call: a start that failed after the spec
		// recorded one (host too slow, request cancelled) announced nothing, and its retry adds nothing new.
		logged, err := s.loggedWorktrees(sp.ID)
		if err != nil {
			return flowdproto.SessionState{}, err
		}
		for _, st := range spec.Worktrees {
			if logged[st.Repo] {
				continue
			}
			if err := s.announceWorktree(ctx, sp.ID, st); err != nil {
				return flowdproto.SessionState{}, err
			}
		}
	}
	state, err := s.Session(ctx, sp.ID)
	state.Started = err == nil
	return state, err
}

// announceWorktree has the host log worktree.added; the command id is the worktree's, so a repeat is a duplicate.
func (s *Supervisor) announceWorktree(ctx context.Context, id string, st flowdproto.WorktreeState) error {
	c, err := event.NewCommand("wt-"+id+"-"+strings.ReplaceAll(st.Repo, "/", "-"), id, event.CmdWorktreeAdd,
		event.WorktreeAdd{Repo: st.Repo, Branch: st.Branch, Path: st.Path, BaseRef: st.BaseRef, BaseSHA: st.BaseSHA})
	if err == nil {
		_, err = s.Command(ctx, c)
	}
	return err
}

// loggedWorktrees names the repositories the session's log holds a worktree.added for.
func (s *Supervisor) loggedWorktrees(id string) (map[string]bool, error) {
	evs, err := eventlog.NewReader(s.p(layout.EventLog(id))).Read(0, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range evs {
		if e.Kind != event.KindWorktreeAdded {
			continue
		}
		var w event.WorktreeAdded
		if json.Unmarshal(e.Data, &w) == nil {
			out[w.Repo] = true
		}
	}
	return out, nil
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
	return s.session(ctx, ctx, id, true)
}

// session is Session with qctx bounding the question to the host's socket; fromLog false reports a host that does
// not answer by its unit state alone.
func (s *Supervisor) session(ctx, qctx context.Context, id string, fromLog bool) (flowdproto.SessionState, error) {
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
	if h, err := s.hostHello(qctx, id); err == nil {
		st.HostVersion, st.HostProtocol, st.LastSeq, st.Status = h.Version, h.Protocol, h.LastSeq, h.Status
		return st, nil
	}
	if !fromLog {
		return st, nil
	}
	// From the log, read on from where the last read ended.
	l := s.log(id)
	l.mu.Lock()
	defer l.mu.Unlock()
	evs, err := l.r.Read(l.lastSeq, 0)
	for _, e := range evs {
		l.lastSeq = e.Seq
		if e.Kind == event.KindSessionStatus {
			var p event.SessionStatusChanged
			if json.Unmarshal(e.Data, &p) == nil {
				l.status = p.Status
			}
		}
	}
	st.LastSeq, st.Status = l.lastSeq, l.status
	return st, err
}

// StopSession implements flowdproto.API. The host is told to shut down first (host.shutdown, now), so a turn it runs
// ends in the log as interrupted with its open request expired, instead of staying open until a next start calls it
// lost; then the unit is stopped and its environment file removed. Those need no spec: one that is missing or
// unreadable (it lies in the agent's directory) must not keep a host with its credential running. Only the remove
// step works from the spec. Under the unit's lock, as EnsureSession and AddWorktree.
func (s *Supervisor) StopSession(ctx context.Context, id string, remove bool) error {
	if !layout.ValidName(id) {
		return invalid("session id %q", id)
	}
	unit := layout.HostUnit(id)
	defer s.lock(unit)()
	reason := event.ShutdownReasonStop
	if remove {
		reason = event.ShutdownReasonArchive
	}
	s.shutdownHost(ctx, id, reason)
	if err := s.o.Units.Stop(ctx, unit); err != nil {
		return err
	}
	if err := os.Remove(s.p(layout.SessionEnvFile(id))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sp, err := s.readSpec(id)
	if !remove {
		if errors.Is(err, flowdproto.ErrNotFound) {
			return err // a session flowd does not know: nothing ran (its unit, had there been one, is stopped)
		}
		return nil
	}
	if err != nil {
		return err
	}
	s.forgetLog(id)
	// Safe to run again after any partial run: a worktree already gone counts as removed.
	kept := false
	for _, w := range sp.Worktrees {
		dir, _ := repoDir(w.Repo)
		checkout := s.p(layout.Checkout(dir))
		unlock := s.lock(checkout)
		dirty, err := s.removeWorktree(ctx, checkout, s.p(w.Path), w)
		unlock()
		if err != nil {
			return err
		}
		if dirty {
			kept = true
			s.o.Log.Warn("worktree kept: it holds uncommitted or untracked changes", "session", id, "worktree", w.Path,
				"branch", w.Branch)
		}
	}
	if kept {
		// The directory and its claim stay with the kept work: a later session of that name must not adopt it.
		return nil
	}
	_ = os.Remove(s.p(sp.Dir)) // only when empty
	// The worktrees are gone: the directory's name may serve another session. Only this session's claim is freed.
	claim := s.p(layout.SessionClaim(sp.Session.Name))
	if b, err := os.ReadFile(claim); err == nil && strings.TrimSpace(string(b)) == id { //nolint:gosec // layout path
		if err := os.Remove(claim); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// shutdownWait bounds the host.shutdown StopSession sends before it stops the unit: a host that does not answer is
// stopped all the same.
const shutdownWait = 5 * time.Second

// shutdownHost asks a running host to stop now. Best effort: a host that is not running, refuses or does not answer
// in time is simply stopped by its unit.
func (s *Supervisor) shutdownHost(ctx context.Context, id string, reason event.ShutdownReason) {
	ctx, cancel := context.WithTimeout(ctx, shutdownWait)
	defer cancel()
	c, err := event.NewCommand("shutdown-"+strconv.FormatInt(time.Now().UnixNano(), 36), id, event.CmdHostShutdown,
		event.HostShutdown{Reason: reason})
	if err == nil {
		_, err = s.Command(ctx, c)
	}
	if err != nil && !errors.Is(err, errHostDown) {
		s.o.Log.Warn("host shutdown before stop", "session", id, "err", err)
	}
}

// removeWorktree removes one worktree of a session and its branch; callers hold the checkout's lock. A worktree with
// uncommitted or untracked changes is kept with its branch (dirty true): only the owner may drop work, and a status
// that cannot be read never counts as clean. The branch is deleted only when every commit on it is reachable from
// its base (nothing of its own, or merged since): a branch with commits of its own stays, pushed or not, for the
// owner to decide.
func (s *Supervisor) removeWorktree(ctx context.Context, checkout, dir string, w flowdproto.WorktreeState) (dirty bool, err error) {
	if _, err := os.Stat(dir); err == nil {
		status, err := s.git(ctx, dir, "status", "--porcelain")
		if err != nil {
			return false, fmt.Errorf("worktree %s: %w", dir, err)
		}
		if status != "" {
			return true, nil
		}
		if _, err := s.git(ctx, checkout, "worktree", "remove", "--force", dir); err != nil {
			return false, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, err := s.git(ctx, checkout, "worktree", "prune"); err != nil {
			return false, err
		}
	} else {
		return false, err
	}
	if _, err := s.git(ctx, checkout, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch); err != nil {
		return false, nil //nolint:nilerr // no such branch (removed before): done
	}
	own, err := s.ownCommits(ctx, checkout, w.BaseRef, w.Branch)
	if err != nil || own > 0 {
		return false, err
	}
	_, err = s.git(ctx, checkout, "branch", "-D", w.Branch)
	return false, err
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
	if !layout.ValidName(id) {
		return flowdproto.WorktreeState{}, invalid("session id %q", id)
	}
	// The spec is read, extended and written back under the unit's lock: another AddWorktree, a stop or a start in
	// between would otherwise lose one side's change.
	defer s.lock(layout.HostUnit(id))()
	sp, err := s.readSpec(id)
	if err != nil {
		return flowdproto.WorktreeState{}, err
	}
	st, err := s.addWorktree(ctx, sp.Session.Name, w)
	if err != nil || hasWorktree(sp.Worktrees, w.Repo) {
		return st, err
	}
	// The spec records the worktree only once the host has logged it: with the host down the call fails and a retry
	// sends the event again (its command id is the worktree's, so a repeat is a duplicate).
	if err := s.announceWorktree(ctx, id, st); err != nil {
		return st, err
	}
	sp.Worktrees = append(sp.Worktrees, st)
	return st, s.writeSpec(sp)
}

// Command implements flowdproto.API: relayed to the session's host.
func (s *Supervisor) Command(ctx context.Context, c event.Command) (flowdproto.CommandResult, error) {
	conn, err := s.dialHost(ctx, c.Session)
	if err != nil {
		return flowdproto.CommandResult{}, err
	}
	defer func() { _ = conn.Close() }()
	// A host that stops answering (stuck holding its lock) must not hold this handler and its connection forever:
	// the request's end closes the connection.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	resp, err := roundTrip(conn, hostproto.Request{ID: "c", Op: hostproto.OpCommand, Command: &c})
	if err != nil {
		if ctx.Err() != nil {
			return flowdproto.CommandResult{}, ctx.Err()
		}
		return flowdproto.CommandResult{}, err
	}
	switch resp.Error {
	case "":
		return flowdproto.CommandResult{ID: c.ID, Duplicate: resp.Duplicate, Queued: resp.Queued}, nil
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
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
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
