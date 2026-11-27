package sandboxes

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/credentials"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/layout"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandbox"
	"github.com/trick77/flow/internal/store"
)

// Unit is flowd.service as Flow delivers it. flowd's own restart never touches the session units: they are not in
// its cgroup.
const Unit = `[Unit]
Description=Flow supervisor (flowd)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=` + layout.BinDir + `/flowd
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`

// Manager brings a project's sandbox to warm: creates the instance from the base image with its NIC on the host
// bridge, starts it, delivers flowd when its version differs (file API plus one bounded exec), greets flowd at the
// sandbox's assigned address and makes sure the project's main checkouts exist. One sandbox per project; calls for
// the same project are serialized.
type Manager struct {
	Store    Store
	Projects projects.Store
	Runtime  sandbox.Runtime
	Dialer   flowdproto.Dialer
	Sealer   credentials.Sealer
	// Image is the base image alias, Network how sandboxes are networked, Memory and CPUs each instance's limits
	// (0: unset), set at create and again before every start, so a changed setting reaches a stopped sandbox.
	Image   string
	Network sandbox.Network
	Memory  int64
	CPUs    int
	// HomeVolume is the one custom volume mounted at layout.HomeVolume in every sandbox, created when missing and
	// attached at every bring-up (an instance made before it gets it then); "" mounts none.
	HomeVolume string
	// Sidecar returns the sidecar bundle delivered with flowd (layout.SidecarArchive, a gzip'd tar of the server
	// image's sidecar directory); nil delivers none. Its bytes are part of the release's identity.
	Sidecar func() ([]byte, error)
	// Env returns the sandbox-wide set (credentials, git identity) pushed to flowd at every bring-up and by PushEnv,
	// its Revision above every earlier one; nil pushes nothing.
	Env func(ctx context.Context) (flowdproto.Env, error)
	// MemoryTotal is the memory all running sandboxes may use together: a sandbox is created and started only while
	// the measured working sets of the running ones plus MemoryHeadroom fit under it, else ErrMemoryCeiling. A
	// limit is a cap, not a reservation: idle sandboxes hold a fraction of theirs. 0: no check.
	MemoryTotal, MemoryHeadroom int64
	// Addresses assigns sandbox addresses; nil uses a MemoryAddresses seeded from the instances' NICs.
	Addresses Addresses
	// Flowd returns the flowd binary for a Go architecture.
	Flowd func(arch string) ([]byte, error)
	// Version is this build's; the release flowd must report (Release) carries it.
	Version string
	Log     *slog.Logger
	// Wait bounds each wait (IPv4, flowd answering); Poll is the interval.
	Wait, Poll time.Duration
	// Changed is told when a project's sandbox state changes (the app event stream).
	Changed func(projectID string)
	// Warmed is told, in a goroutine of its own and outside the project's lock, after every bring-up that greeted
	// flowd (a start, the reconnect after a Flow restart or a dropped connection), with that flowd: the sessions area
	// finishes archives made while the sandbox was stopped. nil: nobody.
	Warmed func(projectID string, api flowdproto.API)
	// LockWait is how long Delete, Rebuild and the setup mark wait for a project's lock before they answer
	// sandbox_busy (or skip): short holders (a Warm from a session follower or an environment push) pass, a bring-up
	// does not. 0: not at all.
	LockWait time.Duration
	// ActiveSessions counts a project's sessions that are not archived, asked again under the project's lock by
	// Delete and Rebuild: a session made after the API's own count must not lose its sandbox. nil counts none.
	ActiveSessions func(ctx context.Context, projectID string) (int, error)
	// SetupTimeout is each setup script's time limit (0: flowdproto.JobTimeout); SetupLimits bound its unit, as a
	// session's (setup.go).
	SetupTimeout time.Duration
	SetupLimits  flowdproto.Limits

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	apis  map[string]cachedAPI
	// dialed is the flowd each project's last bring-up greeted (logAPI).
	dialed map[string]cachedAPI
	// addrMu guards setting up Addresses (addrReady).
	addrMu    sync.Mutex
	addrReady bool
	// admitMu makes each memory check and its reservation one step. reserved counts, per instance, the creates and
	// starts admitted and not finished yet: each holds MemoryHeadroom of its own (a sandbox just started measures
	// almost nothing), so concurrent ones cannot all take the same headroom.
	admitMu  sync.Mutex
	reserved map[string]int
	// volMu guards volReady: the home volume exists.
	volMu    sync.Mutex
	volReady bool
	// claudeSignedIn is the last ClaudeSignedIn a flowd reported (guarded by mu): every sandbox mounts the one home
	// volume, so any warm sandbox's answer holds for all.
	claudeSignedIn bool
}

// ClaudeSignedIn reports whether the home volume holds a Claude login: the last answer any flowd gave (false before
// the first), refreshed by one short Hello to one cached (warm) sandbox, so a login made in a terminal shows without
// a bring-up. Never starts a sandbox; a flowd that does not answer in time leaves the last value.
func (m *Manager) ClaudeSignedIn(ctx context.Context) bool {
	m.mu.Lock()
	var api flowdproto.API
	for _, c := range m.apis {
		api = c.api
		break
	}
	m.mu.Unlock()
	if api != nil {
		hctx, cancel := context.WithTimeout(ctx, signInWait)
		if h, err := api.Hello(hctx); err == nil {
			m.noteHello(h)
		}
		cancel()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claudeSignedIn
}

// signInWait bounds ClaudeSignedIn's refresh: it serves the agent catalog, which must answer at once.
const signInWait = time.Second

func (m *Manager) noteHello(h flowdproto.Hello) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claudeSignedIn = h.ClaudeSignedIn
}

// cachedAPI is a flowd connection and the address it dials.
type cachedAPI struct {
	api  flowdproto.API
	addr netip.Addr
}

// ErrMemoryCeiling means the running sandboxes' measured memory plus the headroom does not fit under
// Manager.MemoryTotal.
var ErrMemoryCeiling = errors.New("sandboxes: memory ceiling for all sandboxes reached")

// errMoved means a running instance's NIC carries another address than the sandbox's assignment.
var errMoved = errors.New("sandboxes: sandbox not on its assigned address")

// misplaced checks a running instance against its assignment addr: nil when its NIC carries addr and is a port of
// the bridge; errMoved when it carries another address; sandbox.ErrNetworkMissing when the bridge is gone or the NIC
// is not one of its ports (Docker made the bridge again under it; Incus never re-attaches a NIC). The ports are read
// now, after the instance's status is known: a list read earlier misses an instance that was still starting.
func (m *Manager) misplaced(ctx context.Context, st sandbox.State, addr netip.Addr) error {
	if st.Address != addr {
		return fmt.Errorf("%w: %s carries %s, not its assigned %s", errMoved, st.Name, st.Address, addr)
	}
	ports, err := m.Runtime.BridgePorts(ctx, m.Network.Bridge)
	if err != nil {
		return fmt.Errorf("bridge %s: %w", m.Network.Bridge, err)
	}
	if !slices.Contains(ports, st.HostIface) {
		return fmt.Errorf("%w: %s is cut off from %s, which was made again under it", sandbox.ErrNetworkMissing,
			st.Name, m.Network.Bridge)
	}
	return nil
}

// relocate reports whether a misplaced answer says the instance must be restarted onto its bridge and address; any
// other error (the bridge's ports could not be read) says nothing about the instance.
func relocate(err error) bool {
	return errors.Is(err, errMoved) || errors.Is(err, sandbox.ErrNetworkMissing)
}

// busy is the addresses on the NICs of every instance but name's own: whatever the assignment knows, none of them
// may go to this sandbox (an instance whose row is gone keeps its address while it exists).
func (m *Manager) busy(ctx context.Context, name string) ([]netip.Addr, error) {
	list, err := m.Runtime.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, st := range list {
		if st.Name != name && st.Address.IsValid() {
			out = append(out, st.Address)
		}
	}
	return out, nil
}

// failCode is the error code a failed bring-up records: a missing or re-made host bridge, a full memory ceiling and
// a failed setup script have their own, everything else is sandbox_unavailable (the returned error's detail names
// the cause).
func failCode(err error) apitypes.ErrorCode {
	switch {
	case errors.Is(err, ErrSetupFailed):
		return apitypes.ErrorCodeSandboxSetupFailed
	case errors.Is(err, sandbox.ErrNetworkMissing):
		return apitypes.ErrorCodeSandboxNetworkMissing
	case errors.Is(err, ErrMemoryCeiling):
		return apitypes.ErrorCodeSandboxMemoryFull
	}
	return apitypes.ErrorCodeSandboxUnavailable
}

// adopter is an Addresses that can take over assignments it did not make (SQLAddresses).
type adopter interface {
	Adopt(ctx context.Context, sandboxID string, a netip.Addr) error
}

// addresses is m.Addresses, set up on first use from the address on each instance's NIC: by default an in-memory
// assignment seeded with them, so a Flow restart keeps every sandbox's address and never hands out one an instance
// still carries; an adopter takes them over, so switching to it moves no sandbox.
func (m *Manager) addresses(ctx context.Context) (Addresses, error) {
	m.addrMu.Lock()
	defer m.addrMu.Unlock()
	if m.addrReady {
		return m.Addresses, nil
	}
	held, err := m.instanceAddresses(ctx)
	if err != nil {
		return nil, err
	}
	switch a := m.Addresses.(type) {
	case nil:
		m.Addresses = NewMemoryAddresses(m.Network, held)
	case adopter:
		for _, id := range slices.Sorted(maps.Keys(held)) {
			if strings.HasPrefix(id, "instance:") {
				continue // no row to record it in
			}
			if err := a.Adopt(ctx, id, held[id]); err != nil {
				return nil, err
			}
		}
	}
	m.addrReady = true
	return m.Addresses, nil
}

// instanceAddresses maps each sandbox id (or "instance:<name>" for an instance without a row) to the address on
// its NIC.
func (m *Manager) instanceAddresses(ctx context.Context) (map[string]netip.Addr, error) {
	rows, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	states, err := m.Runtime.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, r := range rows {
		ids[r.Instance] = r.ID
	}
	held := map[string]netip.Addr{}
	for _, st := range states {
		if !st.Address.IsValid() {
			continue
		}
		key, ok := ids[st.Name]
		if !ok {
			key = "instance:" + st.Name // not Flow's, but its address is taken all the same
		}
		held[key] = st.Address
	}
	return held, nil
}

// admit checks that the instance name may be created or started (step): the working sets of all other instances
// that are not stopped (Runtime.MemoryInUse), plus MemoryHeadroom for it and for every other create or start in
// flight (reserved), fit under MemoryTotal. An instance the metrics do not measure (they failed, or it is missing
// from them) counts with its cgroup usage page cache included (State.MemoryBytes): too much rather than nothing, so
// a broken measurement neither admits everything nor blocks for good. An instance list that fails refuses the start
// (sandbox_unavailable); the next start asks again. Admitted, it holds its headroom until release is called.
func (m *Manager) admit(ctx context.Context, name, step string) (release func(), err error) {
	if m.MemoryTotal <= 0 {
		return func() {}, nil
	}
	m.admitMu.Lock()
	defer m.admitMu.Unlock()
	list, err := m.Runtime.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("measuring sandbox memory: %w", err)
	}
	inUse, err := m.Runtime.MemoryInUse(ctx)
	used := int64(0)
	var unmeasured []string
	for _, s := range list {
		if s.Name == name || s.Status == sandbox.StatusStopped {
			continue
		}
		if v, ok := inUse[s.Name]; ok {
			used += v
			continue
		}
		used += s.MemoryBytes
		unmeasured = append(unmeasured, s.Name)
	}
	switch {
	case err != nil:
		m.Log.Warn("sandbox memory: metrics failed, counting usage with page cache", "sandbox", name,
			"unmeasured", unmeasured, "err", err)
	case len(unmeasured) > 0:
		// Incus serves its metrics from a cache: an instance started seconds ago is not in them yet (measured).
		m.Log.Info("sandbox memory: not in the metrics yet, counting usage with page cache", "sandbox", name,
			"unmeasured", unmeasured)
	}
	inFlight := 0
	for n := range m.reserved {
		if n != name {
			inFlight++
		}
	}
	headroom := m.MemoryHeadroom * int64(inFlight+1)
	attrs := []any{"sandbox", name, "step", step, "measured_mib", used >> 20, "headroom_mib", headroom >> 20,
		"in_flight", inFlight, "total_mib", m.MemoryTotal >> 20}
	if used+headroom > m.MemoryTotal {
		m.Log.Warn("sandbox refused: memory full", attrs...)
		return nil, fmt.Errorf("%w: running sandboxes use %d MiB, with %d MiB headroom past %d MiB", ErrMemoryCeiling,
			used>>20, headroom>>20, m.MemoryTotal>>20)
	}
	m.Log.Info("sandbox admitted", attrs...)
	if m.reserved == nil {
		m.reserved = map[string]int{}
	}
	m.reserved[name]++
	return sync.OnceFunc(func() {
		m.admitMu.Lock()
		defer m.admitMu.Unlock()
		if m.reserved[name]--; m.reserved[name] <= 0 {
			delete(m.reserved, name)
		}
	}), nil
}

func (m *Manager) projectLock(projectID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks, m.apis = map[string]*sync.Mutex{}, map[string]cachedAPI{}
	}
	l, ok := m.locks[projectID]
	if !ok {
		l = &sync.Mutex{}
		m.locks[projectID] = l
	}
	return l
}

func (m *Manager) lock(projectID string) func() {
	l := m.projectLock(projectID)
	l.Lock()
	return l.Unlock
}

// tryLock takes the project's lock when it is free within LockWait: a bring-up holds it for minutes, and an owner's
// delete or rebuild answers sandbox_busy instead of waiting that long; a Warm or an environment push holds it for a
// moment only and must not make them fail.
func (m *Manager) tryLock(projectID string) (func(), error) {
	l := m.projectLock(projectID)
	deadline := time.Now().Add(m.LockWait)
	for !l.TryLock() {
		if !time.Now().Before(deadline) {
			return nil, apitypes.Fail(apitypes.ErrorCodeSandboxBusy, "the sandbox is busy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return l.Unlock, nil
}

// noSessions refuses with project_has_sessions while a session of the project is not archived; asked under the
// project's lock, so a session's start (Ensure) cannot slip in between the count and the removal.
func (m *Manager) noSessions(ctx context.Context, projectID string) error {
	if m.ActiveSessions == nil {
		return nil
	}
	n, err := m.ActiveSessions(ctx, projectID)
	if err != nil {
		return err
	}
	if n > 0 {
		return apitypes.Fail(apitypes.ErrorCodeProjectHasSessions, "")
	}
	return nil
}

func (m *Manager) cached(projectID string) cachedAPI {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apis[projectID]
}

func (m *Manager) remember(projectID string, c cachedAPI) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.api == nil {
		delete(m.apis, projectID)
	} else {
		m.apis[projectID] = c
	}
}

// Forget drops the cached connection, so the next Ensure checks the instance and flowd again. It takes only the
// manager's own mutex, never the project's lock: a bring-up holds that for minutes, and the start endpoint calls
// Forget and must answer at once. A bring-up running meanwhile stores its own fresh connection when it ends.
func (m *Manager) Forget(projectID string) {
	m.remember(projectID, cachedAPI{})
}

func (m *Manager) changed(projectID string) {
	if m.Changed != nil {
		m.Changed(projectID)
	}
}

// setState records a state; setup steps have no progress codes yet.
func (m *Manager) setState(ctx context.Context, sb *Sandbox, state, errCode string) {
	sb.State, sb.ProgressCode, sb.ErrorCode = state, "", errCode
	if err := m.Store.SetState(ctx, sb.ID, state, "", errCode); err != nil {
		m.Log.Error("sandbox state", "sandbox", sb.Instance, "err", err)
	}
	m.changed(sb.ProjectID)
}

// InstanceName is a project's instance: a host name (letters first), unique by the id's random tail.
func InstanceName(projectID string) string {
	hexID := strings.ReplaceAll(projectID, "-", "")
	return "flow-" + hexID[max(len(hexID)-12, 0):]
}

// bringTimeout bounds one bring-up (create, start, deliver flowd, clone the project's repositories); a bring-up
// that may run setup scripts gets setupBudget on top.
const bringTimeout = 15 * time.Minute

// Ensure brings the project's sandbox to warm and returns its flowd.
func (m *Manager) Ensure(ctx context.Context, projectID string) (flowdproto.API, error) {
	return m.ensure(ctx, projectID, true)
}

// ensure is Ensure; with mayStart false a sandbox that is not running is ErrNotWarm (and recorded as stopped)
// instead of being started, one not warm by the records but maybe running ErrMayRun.
func (m *Manager) ensure(ctx context.Context, projectID string, mayStart bool) (flowdproto.API, error) {
	defer m.lock(projectID)()
	// Bring-up runs to its end on its own context: a caller that leaves (a page reload) must not leave a healthy
	// sandbox marked failed, nor a half-made one. One that may bring the sandbox up (no connection cached) may run
	// the setup scripts too.
	limit := bringTimeout
	if mayStart && m.cached(projectID).api == nil {
		limit += m.setupBudget(context.WithoutCancel(ctx), projectID)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()
	sb, err := m.Store.ByProject(ctx, projectID)
	if err == nil && sb.State == StateDeleting {
		// Its instance is gone or going, the project's rows follow: nothing may make it again (a queued setup, an
		// environment push, a session's start).
		if !mayStart {
			return nil, ErrNotWarm
		}
		return nil, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, "sandbox being deleted")
	}
	if c := m.cached(projectID); c.api != nil && err == nil {
		// Two cheap looks: a sandbox stopped behind Flow's back, or running but cut off from the bridge or on another
		// address, must not keep its dead connection. A look that fails (the bridge's ports not readable just now)
		// proves nothing: the connection stays.
		if st, serr := m.Runtime.State(ctx, sb.Instance); serr == nil && st.Status == sandbox.StatusRunning {
			if why := m.misplaced(ctx, st, c.addr); why == nil || !relocate(why) {
				return c.api, nil
			}
		}
		m.remember(projectID, cachedAPI{})
	}
	if !mayStart {
		if err != nil {
			return nil, err
		}
		if sb.State != StateWarm {
			// Not warm by the records (failed, a bring-up cut off by Flow's restart): only an instance that is gone or
			// stopped proves nothing of it runs.
			st, serr := m.Runtime.State(ctx, sb.Instance)
			if errors.Is(serr, sandbox.ErrNotFound) || serr == nil && st.Status == sandbox.StatusStopped {
				return nil, ErrNotWarm
			}
			return nil, fmt.Errorf("%w (recorded %s)", ErrMayRun, sb.State)
		}
		st, serr := m.Runtime.State(ctx, sb.Instance)
		if serr == nil && st.Status != sandbox.StatusRunning || errors.Is(serr, sandbox.ErrNotFound) {
			m.setState(ctx, &sb, StateStopped, "")
			return nil, ErrNotWarm
		}
	}
	if errors.Is(err, ErrNotFound) {
		if _, err := m.Projects.Get(ctx, projectID); err != nil {
			return nil, err
		}
		sb = Sandbox{ID: store.NewID(), ProjectID: projectID, Instance: InstanceName(projectID), State: StateSetup}
		err = m.Store.Put(ctx, sb)
	}
	if err != nil {
		return nil, err
	}
	// Setup scripts run in a bring-up that finds the sandbox not warm, never on a reconnect to a warm one (setup.go).
	setup := sb.State != StateWarm
	if setup {
		m.setState(ctx, &sb, StateSetup, "")
	}
	c, err := m.bring(ctx, &sb, mayStart, setup)
	if errors.Is(err, ErrNotWarm) {
		m.setState(ctx, &sb, StateStopped, "")
		return nil, err
	}
	if err != nil {
		m.Log.Error("sandbox setup", "sandbox", sb.Instance, "err", err)
		// A reconnect (no start) that failed leaves a running, warm sandbox warm: the next call tries again (a fetch
		// that failed, flowd briefly away). Only one cut off from its bridge or on another address needs a start, which
		// failed says.
		if mayStart || relocate(err) {
			m.setState(context.WithoutCancel(ctx), &sb, StateFailed, string(failCode(err)))
		}
		return nil, apitypes.Fail(failCode(err), err.Error())
	}
	m.remember(projectID, c)
	if m.Warmed != nil {
		go m.Warmed(projectID, c.api)
	}
	return c.api, nil
}

// bring brings the instance up. A missing bridge fails it before anything is created, started or delivered: Flow
// records the error and waits for the next bring-up, it never retries by itself. A running instance cut off from
// the bridge or carrying another address than its assigned one is restarted when mayStart, else reported; without
// mayStart a stopped instance is ErrNotWarm, never started. setup: run the repositories' setup scripts (greet).
func (m *Manager) bring(ctx context.Context, sb *Sandbox, mayStart, setup bool) (cachedAPI, error) {
	t0 := time.Now()
	if !m.Network.Configured() {
		return cachedAPI{}, errors.New("sandbox network not configured")
	}
	addrs, err := m.addresses(ctx)
	if err != nil {
		return cachedAPI{}, err
	}
	busy, err := m.busy(ctx, sb.Instance)
	if err != nil {
		return cachedAPI{}, err
	}
	addr, err := addrs.Assign(ctx, sb.ID, busy)
	if err != nil {
		return cachedAPI{}, err
	}
	nic := m.Network.NIC(addr)
	if _, err := m.Runtime.BridgePorts(ctx, nic.Bridge); err != nil {
		return cachedAPI{}, fmt.Errorf("bridge %s: %w", nic.Bridge, err)
	}
	st, err := m.Runtime.State(ctx, sb.Instance)
	releaseCreate := func() {}
	if errors.Is(err, sandbox.ErrNotFound) {
		// Before the create (about 40 s): a start refused afterwards would leave a whole instance behind. Its headroom
		// stays reserved until the start below returned; start checks again, the authoritative one.
		release, aerr := m.admit(ctx, sb.Instance, "create")
		if aerr != nil {
			return cachedAPI{}, aerr
		}
		defer release()
		releaseCreate = release
		err = m.Runtime.Create(ctx, sandbox.Spec{Name: sb.Instance, Image: m.Image, NIC: nic, Nesting: true,
			Limits: m.limits()})
		if err != nil {
			return cachedAPI{}, fmt.Errorf("create: %w", err)
		}
		m.Log.Info("sandbox created", "sandbox", sb.Instance, "address", addr, "ms", time.Since(t0).Milliseconds())
		// A new instance has no flowd, whatever the row says about one deleted before: deliver, do not wait. Kept in
		// the row at once, so a bring-up that fails later does not make the next one wait either.
		sb.FlowdVersion = ""
		if err := m.Store.Put(ctx, *sb); err != nil {
			return cachedAPI{}, err
		}
		st, err = m.Runtime.State(ctx, sb.Instance)
	}
	if err == nil && st.Status == sandbox.StatusOther {
		st, err = m.poll(ctx, func() (sandbox.State, bool, error) {
			st, err := m.Runtime.State(ctx, sb.Instance)
			return st, st.Status != sandbox.StatusOther, err
		})
	}
	if err != nil {
		return cachedAPI{}, err
	}
	if err := m.homeVolume(ctx, sb.Instance); err != nil {
		return cachedAPI{}, fmt.Errorf("home volume: %w", err)
	}
	// restart: a running instance stopped here to move it onto its bridge or address. Its start is not admitted
	// again: it ran a moment ago, and a refusal would leave it stopped with its turns gone.
	restart := false
	if st.Status == sandbox.StatusRunning {
		if why := m.misplaced(ctx, st, addr); why != nil {
			if !relocate(why) {
				return cachedAPI{}, why // a failed look proves nothing: never stop a healthy sandbox for it
			}
			if !mayStart {
				return cachedAPI{}, fmt.Errorf("%w: start it again", why)
			}
			if errors.Is(why, errMoved) {
				m.Log.Warn("sandbox not on its assigned address, restarting", "sandbox", sb.Instance,
					"address", st.Address, "assigned", addr)
			} else {
				m.Log.Warn("sandbox cut off from its bridge, restarting", "sandbox", sb.Instance, "bridge", nic.Bridge)
			}
			if err := m.Runtime.Stop(ctx, sb.Instance, 30*time.Second); err != nil {
				return cachedAPI{}, fmt.Errorf("stop: %w", err)
			}
			st.Status = sandbox.StatusStopped
			restart = true
		}
	}
	started := false
	if st.Status != sandbox.StatusRunning {
		if !mayStart {
			return cachedAPI{}, ErrNotWarm
		}
		started = true
		if err := m.start(ctx, st, nic, !restart); err != nil {
			return cachedAPI{}, err
		}
	}
	releaseCreate()
	api, err := m.greet(ctx, sb, addr, started, setup, t0)
	return cachedAPI{api: api, addr: addr}, err
}

// greet waits for the guest to have its address, then for flowd (delivering it when its version differs), makes
// sure the project's checkouts exist, runs their setup scripts when setup is set, and records the sandbox warm.
func (m *Manager) greet(ctx context.Context, sb *Sandbox, addr netip.Addr, started, setup bool, t0 time.Time) (flowdproto.API, error) {
	st, err := m.poll(ctx, func() (sandbox.State, bool, error) {
		st, err := m.Runtime.State(ctx, sb.Instance)
		return st, st.IPv4 == addr.String(), err
	})
	if err != nil {
		return nil, fmt.Errorf("address %s not up in the guest: %w", addr, err)
	}
	token, err := m.token(ctx, sb)
	if err != nil {
		return nil, err
	}
	api := m.Dialer.Dial(flowdproto.Endpoint{Sandbox: sb.Instance, Address: net.JoinHostPort(addr.String(),
		strconv.Itoa(flowdproto.Port)), Token: token})
	m.noteDialed(sb.ProjectID, cachedAPI{api: api, addr: addr})
	rel, err := m.Release(st.Arch)
	if err != nil {
		return nil, err
	}
	// A sandbox that just booted with flowd delivered before needs a moment until flowd listens; delivering again
	// would only restart it.
	hello, err := m.hello(ctx, api, started && sb.FlowdVersion != "", true)
	if err != nil || hello.Release != rel {
		t2 := time.Now()
		if err := m.deliver(ctx, sb.Instance, st.Arch, token, rel); err != nil {
			return nil, fmt.Errorf("deliver flowd: %w", err)
		}
		if hello, err = m.hello(ctx, api, true, false); err != nil {
			return nil, err
		}
		m.Log.Info("flowd delivered", "sandbox", sb.Instance, "release", rel, "version", hello.Version,
			"ms", time.Since(t2).Milliseconds())
	}
	if !flowdproto.Compatible(hello) {
		return nil, fmt.Errorf("%w: flowd %s", flowdproto.ErrProtocol, hello.Version)
	}
	// After any delivery's restart: a restarted flowd holds no set.
	if err := m.pushEnv(ctx, api); err != nil {
		return nil, fmt.Errorf("environment: %w", err)
	}
	repos, err := m.Projects.Repos(ctx, sb.ProjectID)
	if err != nil {
		return nil, err
	}
	var scripts []flowdproto.RepoState
	for _, r := range repos {
		rs, err := api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: r.FullName, DefaultBranch: r.DefaultBranch})
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", r.FullName, err)
		}
		if rs.SetupScript {
			scripts = append(scripts, rs)
		}
	}
	if setup {
		if err := m.runSetup(ctx, sb, api, scripts); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	sb.Arch, sb.FlowdVersion, sb.StartedAt, sb.LastUsedAt = st.Arch, cmp.Or(hello.Release, hello.Version), &now, &now
	sb.State, sb.ProgressCode, sb.ErrorCode = StateWarm, "", ""
	if err := m.Store.Put(ctx, *sb); err != nil {
		return nil, err
	}
	m.changed(sb.ProjectID)
	m.Log.Info("sandbox warm", "sandbox", sb.Instance, "address", st.IPv4, "flowd", hello.Version,
		"ms", time.Since(t0).Milliseconds())
	return api, nil
}

// start sets the NIC (the assigned address, also for an instance cloned or made before) and starts the instance,
// when the measured memory admits it (admit false: a restart of an instance that was running, see bring).
func (m *Manager) start(ctx context.Context, st sandbox.State, nic sandbox.NIC, admit bool) error {
	if admit {
		release, err := m.admit(ctx, st.Name, "start")
		if err != nil {
			return err
		}
		defer release()
	}
	// The configured limits, also for an instance made under other settings.
	if err := m.Runtime.SetLimits(ctx, st.Name, m.limits()); err != nil {
		return fmt.Errorf("set limits: %w", err)
	}
	if err := m.Runtime.SetNIC(ctx, st.Name, nic); err != nil {
		return fmt.Errorf("set NIC: %w", err)
	}
	t1 := time.Now()
	if err := m.Runtime.Start(ctx, st.Name); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	m.Log.Info("sandbox started", "sandbox", st.Name, "address", nic.Address.Addr(), "ms", time.Since(t1).Milliseconds())
	return nil
}

// poll calls f until it reports done, its error is final, or Wait passes.
func (m *Manager) poll(ctx context.Context, f func() (sandbox.State, bool, error)) (sandbox.State, error) {
	deadline := time.Now().Add(m.Wait)
	for {
		st, done, err := f()
		if err != nil || done {
			return st, err
		}
		if time.Now().After(deadline) {
			return st, errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(m.Poll):
		}
	}
}

// hello greets flowd, with wait retrying while it does not answer yet. refusedIsFinal: a token refusal ends the wait
// at once (a flowd that runs with another token, after a rotation, answers that way until the delivery).
func (m *Manager) hello(ctx context.Context, api flowdproto.API, wait, refusedIsFinal bool) (flowdproto.Hello, error) {
	deadline := time.Now().Add(m.Wait)
	for {
		hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		h, err := api.Hello(hctx)
		cancel()
		if err == nil {
			m.noteHello(h)
		}
		if err == nil || !wait || errors.Is(err, flowdproto.ErrProtocol) || time.Now().After(deadline) ||
			refusedIsFinal && errors.Is(err, flowdproto.ErrUnauthorized) {
			return h, err
		}
		select {
		case <-ctx.Done():
			return h, ctx.Err()
		case <-time.After(m.Poll):
		}
	}
}

// token is the sandbox's flowd token: made once, kept sealed, so flowd keeps answering Flow across Flow's restarts.
// A sealed token that no longer opens (a new BACKEND_SEAL_KEY, or dev mode's key made at each start) is rotated:
// flowd refuses the old one, so the bring-up delivers the config with the new one.
func (m *Manager) token(ctx context.Context, sb *Sandbox) (string, error) {
	if len(sb.FlowdToken) > 0 {
		b, err := m.Sealer.Open(sb.FlowdToken)
		if err == nil {
			return string(b), nil
		}
		m.Log.Warn("flowd token unreadable with this seal key, making a new one", "sandbox", sb.Instance)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := hex.EncodeToString(raw)
	sealed, err := m.Sealer.Seal([]byte(token))
	if err != nil {
		return "", err
	}
	sb.FlowdToken = sealed
	return token, m.Store.Put(ctx, *sb)
}

// limits are the instance limits from the settings.
func (m *Manager) limits() sandbox.Limits {
	return sandbox.Limits{MemoryBytes: m.Memory, CPUs: m.CPUs}
}

// homeVolume makes the home volume once per Flow process and attaches it to the instance (a no-op when attached).
func (m *Manager) homeVolume(ctx context.Context, instance string) error {
	if m.HomeVolume == "" {
		return nil
	}
	m.volMu.Lock()
	if !m.volReady {
		// Not shifted: idmapped mounts are not available on every pool (measured), and the sandboxes share the
		// project's idmap, so Incus maps the volume the same for all of them (layout.AgentUID).
		if err := m.Runtime.EnsureVolume(ctx, sandbox.Volume{Name: m.HomeVolume}); err != nil {
			m.volMu.Unlock()
			return err
		}
		m.volReady = true
	}
	m.volMu.Unlock()
	return m.Runtime.Attach(ctx, instance, sandbox.Mount{Volume: m.HomeVolume, Path: layout.HomeVolume})
}

// Release is the release directory name a sandbox of arch gets: the version and a hash of the flowd binary and the
// sidecar bundle, so two builds reporting the same version (an unstamped dev-<commit>-dirty) are told apart and
// never share a directory, which is never overwritten.
func (m *Manager) Release(arch string) (string, error) {
	bin, err := m.Flowd(arch)
	if err != nil {
		return "", err
	}
	bundle, err := m.bundle()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "flowd %d\n", len(bin))
	h.Write(bin)
	_, _ = fmt.Fprintf(h, "sidecar %d\n", len(bundle))
	h.Write(bundle)
	return m.Version + "-" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

func (m *Manager) bundle() ([]byte, error) {
	if m.Sidecar == nil {
		return nil, nil
	}
	return m.Sidecar()
}

// pushEnv pushes the current set to one flowd.
func (m *Manager) pushEnv(ctx context.Context, api flowdproto.API) error {
	if m.Env == nil {
		return nil
	}
	e, err := m.Env(ctx)
	if err != nil {
		return err
	}
	return api.PutEnv(ctx, e)
}

// PushEnv pushes the current set to every warm sandbox, never starting one (a stopped sandbox gets it at its next
// bring-up). Called when a credential changes; failures are logged, never with the set's values. The stored state
// is not filtered on: a sandbox in setup has had its set pushed already and turns warm with it, so Warm waits for
// that bring-up (the project's lock) and the push follows it. One goroutine per sandbox, so a long bring-up holds up
// no other; PushEnv returns when all are done.
func (m *Manager) PushEnv(ctx context.Context) {
	rows, err := m.Store.List(ctx)
	if err != nil {
		m.Log.Error("environment push: listing sandboxes", "err", err)
		return
	}
	var wg sync.WaitGroup
	for _, sb := range rows {
		wg.Go(func() {
			api, err := m.Warm(ctx, sb.ProjectID)
			if err == nil {
				err = m.pushEnv(ctx, api)
			}
			switch {
			case err == nil:
				m.Log.Info("environment pushed", "sandbox", sb.Instance)
			case !errors.Is(err, ErrNotWarm) && !errors.Is(err, ErrMayRun): // not warm: its next bring-up pushes
				m.Log.Warn("environment push", "sandbox", sb.Instance, "err", err)
			}
		})
	}
	wg.Wait()
}

// deliver pushes this release of flowd with the sidecar bundle, its unit and its config, then one bounded exec
// switches the release and restarts flowd, which unpacks the bundle before it answers. Releases are never
// overwritten: the binary goes to a temporary name and is moved into place only when the release does not exist
// yet, and flowd drops a bundle pushed into a release that has one. Running hosts keep their binary, and Incus
// refuses to write onto a running executable anyway (measured: 500 "open …/flowd: text file busy").
func (m *Manager) deliver(ctx context.Context, instance, arch, token, rel string) error {
	bin, err := m.Flowd(arch)
	if err != nil {
		return err
	}
	bundle, err := m.bundle()
	if err != nil {
		return err
	}
	cfg, _ := json.Marshal(map[string]string{"token": token, "sandbox": instance})
	release := layout.Release(rel)
	files := []sandbox.File{}
	if bundle != nil {
		files = append(files, sandbox.File{Path: release + "/" + layout.SidecarArchive, Content: bundle, Mode: 0o600})
	}
	files = append(files,
		sandbox.File{Path: release + "/flowd.new", Content: bin, Mode: 0o755},
		sandbox.File{Path: layout.Unit, Content: []byte(Unit), Mode: 0o644},
		sandbox.File{Path: layout.Config, Content: cfg, Mode: 0o600})
	if err := m.Runtime.Push(ctx, instance, files...); err != nil {
		return err
	}
	script := "set -e; if [ -e " + release + "/flowd ]; then rm -f " + release + "/flowd.new; else mv -f " + release +
		"/flowd.new " + release + "/flowd; fi; ln -sfn " + release + " " + layout.BinDir + ".new && mv -T " +
		layout.BinDir + ".new " + layout.BinDir + "; systemctl daemon-reload; systemctl enable --quiet " +
		layout.FlowdUnit + "; systemctl restart " + layout.FlowdUnit
	res, err := m.Runtime.Exec(ctx, instance, sandbox.Command{Args: []string{"sh", "-c", script}, Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("activate release: exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// ErrNotWarm is Warm's answer for a sandbox Flow has not brought up (or the owner stopped) whose instance is gone
// or stopped: nothing of it runs.
var ErrNotWarm = errors.New("sandboxes: not warm")

// ErrMayRun is Warm's answer for a sandbox that is not warm by Flow's records (failed, say) while its instance is
// not proven stopped: its session hosts and shells may still run. It is not ErrNotWarm, so nobody takes it for a
// stop.
var ErrMayRun = errors.New("sandboxes: not warm, but its instance may still run")

// Warm returns the flowd of a sandbox that is warm by Flow's records, reconnecting after Flow's own restart; it
// never starts a stopped sandbox. ErrNotWarm: nothing runs; ErrMayRun: not warm, the instance may run.
func (m *Manager) Warm(ctx context.Context, projectID string) (flowdproto.API, error) {
	return m.ensure(ctx, projectID, false)
}

// Stop stops a project's sandbox; its filesystem stays. An instance that does not exist (a bring-up failed before
// creating it, or deleted outside Flow) is stopped already.
func (m *Manager) Stop(ctx context.Context, projectID string) error {
	defer m.lock(projectID)()
	m.remember(projectID, cachedAPI{})
	sb, err := m.Store.ByProject(ctx, projectID)
	if err != nil {
		return err
	}
	if sb.State == StateDeleting {
		return nil // nothing runs, and the state must stay what keeps a bring-up away
	}
	m.setState(ctx, &sb, StateStopping, "")
	if err := m.Runtime.Stop(ctx, sb.Instance, 30*time.Second); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		m.setState(context.WithoutCancel(ctx), &sb, StateFailed, string(apitypes.ErrorCodeSandboxUnavailable))
		return err
	}
	m.setState(ctx, &sb, StateStopped, "")
	return nil
}

// removeTimeout bounds removing an instance (a clean stop of up to 30 s, then the delete). A var for tests.
var removeTimeout = 2 * time.Minute

// removeInstance stops an instance when it runs and deletes it; one that does not exist is removed already.
func (m *Manager) removeInstance(ctx context.Context, name string) error {
	st, err := m.Runtime.State(ctx, name)
	if errors.Is(err, sandbox.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Status != sandbox.StatusStopped {
		if err := m.Runtime.Stop(ctx, name, 30*time.Second); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
			return fmt.Errorf("stop: %w", err)
		}
	}
	if err := m.Runtime.Delete(ctx, name); err != nil && !errors.Is(err, sandbox.ErrNotFound) {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// Delete removes a project's sandbox instance for the project's deletion, refused with sandbox_busy while a
// bring-up holds the project. The row stays in state deleting, so nothing brings the sandbox up again (a queued
// setup, an environment push, a session start), until the project's rows take it with them; a project without a row
// gets one in that state for the same reason. The address is released. A removal that fails leaves the row failed
// and the project in place, so the delete can be tried again. The home volume stays: every sandbox shares it.
func (m *Manager) Delete(ctx context.Context, projectID string) error {
	unlock, err := m.tryLock(projectID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := m.noSessions(ctx, projectID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	m.remember(projectID, cachedAPI{})
	sb, err := m.Store.ByProject(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		sb = Sandbox{ID: store.NewID(), ProjectID: projectID, Instance: InstanceName(projectID), State: StateDeleting}
		err = m.Store.Put(ctx, sb)
	}
	if err != nil {
		return err
	}
	m.setState(ctx, &sb, StateDeleting, "")
	if err := m.removeInstance(ctx, sb.Instance); err != nil {
		// ctx may be what ran out: the failure is recorded all the same.
		m.setState(context.WithoutCancel(ctx), &sb, StateFailed, string(apitypes.ErrorCodeSandboxUnavailable))
		return apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	if addrs, err := m.addresses(ctx); err != nil {
		m.Log.Warn("sandbox deleted, its address not released", "sandbox", sb.Instance, "err", err)
	} else if err := addrs.Release(ctx, sb.ID); err != nil {
		m.Log.Warn("sandbox deleted, its address not released", "sandbox", sb.Instance, "err", err)
	}
	m.Log.Info("sandbox deleted", "sandbox", sb.Instance)
	return nil
}

// Rebuild makes the project's sandbox again from the base image: the instance is deleted (the row keeps its address
// and flowd token) and a bring-up in the background creates it, delivers flowd and clones every repository afresh.
// sandbox_busy while a bring-up holds the project. The caller makes sure no session needs the old instance: whatever
// was on its filesystem (worktrees, logs) is gone.
func (m *Manager) Rebuild(ctx context.Context, projectID string) error {
	unlock, err := m.tryLock(projectID)
	if err != nil {
		return err
	}
	err = m.noSessions(ctx, projectID)
	if err == nil {
		err = m.dropInstance(ctx, projectID)
	}
	unlock()
	if err != nil {
		return err
	}
	m.Setup(projectID)
	return nil
}

// dropInstance is Rebuild's first half, under the project's lock: the row turns setup and the instance goes.
func (m *Manager) dropInstance(ctx context.Context, projectID string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	m.remember(projectID, cachedAPI{})
	sb, err := m.Store.ByProject(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return nil // never brought up: the bring-up makes it
	}
	if err != nil {
		return err
	}
	if sb.State == StateDeleting {
		return apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, "sandbox being deleted")
	}
	m.setState(ctx, &sb, StateSetup, "")
	if err := m.removeInstance(ctx, sb.Instance); err != nil {
		// ctx may be what ran out: the failure is recorded all the same.
		m.setState(context.WithoutCancel(ctx), &sb, StateFailed, string(apitypes.ErrorCodeSandboxUnavailable))
		return apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	m.Log.Info("sandbox instance deleted for a rebuild", "sandbox", sb.Instance)
	return nil
}

// Setup brings a sandbox up in the background. One that is not up shows setup at once, so the answer to the request
// that asked for it (a project's creation, a start) already says so, before the app event of the bring-up's own
// state reaches a browser that may not know the project yet.
func (m *Manager) Setup(projectID string) {
	m.markSetup(projectID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if _, err := m.Ensure(ctx, projectID); err != nil {
			m.Log.Error("sandbox setup", "project", projectID, "err", err)
		}
	}()
}

// markSetup records a sandbox about to be brought up as setup: a project's first row, or a stopped or failed one.
// Only while no bring-up holds the project (that one records its own state); a sandbox being deleted stays so.
func (m *Manager) markSetup(projectID string) {
	unlock, err := m.tryLock(projectID)
	if err != nil {
		return
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sb, err := m.Store.ByProject(ctx, projectID)
	switch {
	case errors.Is(err, ErrNotFound):
		if _, err := m.Projects.Get(ctx, projectID); err != nil {
			return
		}
		sb = Sandbox{ID: store.NewID(), ProjectID: projectID, Instance: InstanceName(projectID), State: StateSetup}
		if err := m.Store.Put(ctx, sb); err != nil {
			m.Log.Warn("sandbox row", "project", projectID, "err", err)
			return
		}
		m.changed(projectID)
	case err != nil:
		m.Log.Warn("sandbox row", "project", projectID, "err", err)
	case sb.State == StateStopped || sb.State == StateFailed || sb.State == StateNone:
		m.setState(ctx, &sb, StateSetup, "")
	}
}

// Describe is the API's view of a project's sandbox.
func (m *Manager) Describe(ctx context.Context, projectID string) apitypes.Sandbox {
	sb, _ := m.Lookup(ctx, projectID)
	return sb
}

// Lookup is Describe with the store's error: state none for a project without a row (and no error), state none and
// the error when the row could not be read, so a caller can tell an unknown state from none.
func (m *Manager) Lookup(ctx context.Context, projectID string) (apitypes.Sandbox, error) {
	sb, err := m.Store.ByProject(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return apitypes.Sandbox{State: apitypes.SandboxStateNone}, nil
	}
	if err != nil {
		return apitypes.Sandbox{State: apitypes.SandboxStateNone}, err
	}
	return apitypes.Sandbox{State: apitypes.SandboxState(sb.State), ProgressCode: sb.ProgressCode,
		ErrorCode: apitypes.ErrorCode(sb.ErrorCode), LastUsedAt: sb.LastUsedAt, FlowdVersion: sb.FlowdVersion}, nil
}

// Reconcile is called at Flow's start, before anything else touches a sandbox: no bring-up, stop or removal can run
// then, so a row still in setup, stopping or deleting was left by one cut off (a restart mid-way, a store error after
// the state was written) and would keep the sandbox in that state for good. Such a row turns failed: the state that
// says "not warm, the instance may still run", from which a start, a rebuild and a delete all work. Incus is not
// asked: a start or a delete finds out what is left.
func (m *Manager) Reconcile(ctx context.Context) error {
	rows, err := m.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, sb := range rows {
		switch sb.State {
		case StateSetup, StateStopping, StateDeleting:
			m.Log.Warn("sandbox left mid-operation, recorded failed", "sandbox", sb.Instance, "state", sb.State)
			if err := m.Store.SetState(ctx, sb.ID, StateFailed, "", string(apitypes.ErrorCodeSandboxUnavailable)); err != nil {
				return err
			}
		}
	}
	return nil
}

// API implements httpapi.Sandboxes: state, start, stop, rebuild and the setup log (setup.go).
type API struct {
	httpapi.NotImplemented
	Manager *Manager
	// ActiveSessions counts a project's sessions that are not archived: a rebuild is refused while one exists (its
	// worktrees and host live on the instance). nil counts none.
	ActiveSessions func(ctx context.Context, projectID string) (int, error)
}

func (a *API) known(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("projectId")
	if _, err := a.Manager.Projects.Get(r.Context(), id); err != nil {
		if errors.Is(err, projects.ErrNotFound) {
			err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
		}
		httpapi.WriteError(w, err)
		return "", false
	}
	return id, true
}

// GetSandbox implements httpapi.Sandboxes.
func (a *API) GetSandbox(w http.ResponseWriter, r *http.Request) {
	if id, ok := a.known(w, r); ok {
		httpapi.WriteJSON(w, http.StatusOK, a.Manager.Describe(r.Context(), id))
	}
}

// StartSandbox implements httpapi.Sandboxes: setup runs in the background; poll GetSandbox.
func (a *API) StartSandbox(w http.ResponseWriter, r *http.Request) {
	if id, ok := a.known(w, r); ok {
		a.Manager.Forget(id)
		a.Manager.Setup(id)
		httpapi.WriteJSON(w, http.StatusAccepted, a.Manager.Describe(r.Context(), id))
	}
}

// StopSandbox implements httpapi.Sandboxes.
func (a *API) StopSandbox(w http.ResponseWriter, r *http.Request) {
	id, ok := a.known(w, r)
	if !ok {
		return
	}
	if err := a.Manager.Stop(r.Context(), id); err != nil && !errors.Is(err, ErrNotFound) {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error()))
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, a.Manager.Describe(r.Context(), id))
}

// RebuildSandbox implements httpapi.Sandboxes: refused while a session of the project is not archived; the bring-up
// runs in the background, poll GetSandbox or follow sandbox.updated.
func (a *API) RebuildSandbox(w http.ResponseWriter, r *http.Request) {
	id, ok := a.known(w, r)
	if !ok {
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
	if err := a.Manager.Rebuild(r.Context(), id); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, a.Manager.Describe(r.Context(), id))
}
