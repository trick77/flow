package sandboxes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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

// Manager brings a project's sandbox to warm: creates the instance from the base image, starts it, delivers flowd
// when its version differs (file API plus one bounded exec), greets flowd and makes sure the project's main
// checkouts exist. One sandbox per project; calls for the same project are serialized.
type Manager struct {
	Store    Store
	Projects projects.Store
	Runtime  sandbox.Runtime
	Dialer   flowdproto.Dialer
	Sealer   credentials.Sealer
	// Image is the base image alias, Network the bridge, Memory the instance's memory limit in bytes.
	Image   string
	Network string
	Memory  int64
	// Flowd returns the flowd binary for a Go architecture.
	Flowd func(arch string) ([]byte, error)
	// Version is this build's: the release flowd must report, else it is delivered again.
	Version string
	Log     *slog.Logger
	// Wait bounds each wait (IPv4, flowd answering); Poll is the interval.
	Wait, Poll time.Duration
	// Changed is told when a project's sandbox state changes (the app event stream).
	Changed func(projectID string)

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	apis  map[string]flowdproto.API
}

func (m *Manager) lock(projectID string) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks, m.apis = map[string]*sync.Mutex{}, map[string]flowdproto.API{}
	}
	l, ok := m.locks[projectID]
	if !ok {
		l = &sync.Mutex{}
		m.locks[projectID] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (m *Manager) cached(projectID string) flowdproto.API {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.apis[projectID]
}

func (m *Manager) remember(projectID string, api flowdproto.API) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if api == nil {
		delete(m.apis, projectID)
	} else {
		m.apis[projectID] = api
	}
}

// Forget drops the cached connection, so the next Ensure checks the instance and flowd again.
func (m *Manager) Forget(projectID string) {
	m.lock(projectID)()
	m.remember(projectID, nil)
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

// bringTimeout bounds one bring-up (create, start, deliver flowd, clone the project's repositories).
const bringTimeout = 15 * time.Minute

// Ensure brings the project's sandbox to warm and returns its flowd.
func (m *Manager) Ensure(ctx context.Context, projectID string) (flowdproto.API, error) {
	return m.ensure(ctx, projectID, true)
}

// ensure is Ensure; with mayStart false a sandbox that is not running is ErrNotWarm (and recorded as stopped)
// instead of being started.
func (m *Manager) ensure(ctx context.Context, projectID string, mayStart bool) (flowdproto.API, error) {
	defer m.lock(projectID)()
	// Bring-up runs to its end on its own context: a caller that leaves (a page reload) must not leave a healthy
	// sandbox marked failed, nor a half-made one.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bringTimeout)
	defer cancel()
	sb, err := m.Store.ByProject(ctx, projectID)
	if api := m.cached(projectID); api != nil && err == nil {
		// One cheap look at the instance: a sandbox stopped behind Flow's back must not keep its stale connection.
		if st, serr := m.Runtime.State(ctx, sb.Instance); serr == nil && st.Status == sandbox.StatusRunning {
			return api, nil
		}
		m.remember(projectID, nil)
	}
	if !mayStart {
		if err != nil {
			return nil, err
		}
		if sb.State != StateWarm {
			return nil, ErrNotWarm
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
	if sb.State != StateWarm {
		m.setState(ctx, &sb, StateSetup, "")
	}
	api, err := m.bring(ctx, &sb)
	if err != nil {
		m.Log.Error("sandbox setup", "sandbox", sb.Instance, "err", err)
		m.setState(context.WithoutCancel(ctx), &sb, StateFailed, string(apitypes.ErrorCodeSandboxUnavailable))
		return nil, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
	}
	m.remember(projectID, api)
	return api, nil
}

func (m *Manager) bring(ctx context.Context, sb *Sandbox) (flowdproto.API, error) {
	t0 := time.Now()
	st, err := m.Runtime.State(ctx, sb.Instance)
	if errors.Is(err, sandbox.ErrNotFound) {
		err = m.Runtime.Create(ctx, sandbox.Spec{Name: sb.Instance, Image: m.Image, Network: m.Network, Nesting: true,
			Limits: sandbox.Limits{MemoryBytes: m.Memory}})
		if err != nil {
			return nil, fmt.Errorf("create: %w", err)
		}
		m.Log.Info("sandbox created", "sandbox", sb.Instance, "ms", time.Since(t0).Milliseconds())
		st, err = m.Runtime.State(ctx, sb.Instance)
	}
	if err != nil {
		return nil, err
	}
	started := false
	if st.Status != sandbox.StatusRunning {
		started = true
		t1 := time.Now()
		if err := m.Runtime.Start(ctx, sb.Instance); err != nil {
			return nil, fmt.Errorf("start: %w", err)
		}
		m.Log.Info("sandbox started", "sandbox", sb.Instance, "ms", time.Since(t1).Milliseconds())
	}
	st, err = m.poll(ctx, func() (sandbox.State, bool, error) {
		st, err := m.Runtime.State(ctx, sb.Instance)
		return st, st.IPv4 != "", err
	})
	if err != nil {
		return nil, fmt.Errorf("no address: %w", err)
	}
	token, err := m.token(ctx, sb)
	if err != nil {
		return nil, err
	}
	api := m.Dialer.Dial(flowdproto.Endpoint{Sandbox: sb.Instance, Address: net.JoinHostPort(st.IPv4,
		strconv.Itoa(flowdproto.Port)), Token: token})
	// A sandbox that just booted with flowd delivered before needs a moment until flowd listens; delivering again
	// would only restart it.
	hello, err := m.hello(ctx, api, started && sb.FlowdVersion != "")
	if err != nil || hello.Version != m.Version {
		t2 := time.Now()
		if err := m.deliver(ctx, sb.Instance, st.Arch, token); err != nil {
			return nil, fmt.Errorf("deliver flowd: %w", err)
		}
		if hello, err = m.hello(ctx, api, true); err != nil {
			return nil, err
		}
		m.Log.Info("flowd delivered", "sandbox", sb.Instance, "version", hello.Version, "ms", time.Since(t2).Milliseconds())
	}
	if !flowdproto.Compatible(hello) {
		return nil, fmt.Errorf("%w: flowd %s", flowdproto.ErrProtocol, hello.Version)
	}
	repos, err := m.Projects.Repos(ctx, sb.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		if _, err := api.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: r.FullName, DefaultBranch: r.DefaultBranch}); err != nil {
			return nil, fmt.Errorf("repo %s: %w", r.FullName, err)
		}
	}
	now := time.Now()
	sb.Arch, sb.FlowdVersion, sb.StartedAt, sb.LastUsedAt = st.Arch, hello.Version, &now, &now
	sb.State, sb.ProgressCode, sb.ErrorCode = StateWarm, "", ""
	if err := m.Store.Put(ctx, *sb); err != nil {
		return nil, err
	}
	m.changed(sb.ProjectID)
	m.Log.Info("sandbox warm", "sandbox", sb.Instance, "address", st.IPv4, "flowd", hello.Version,
		"ms", time.Since(t0).Milliseconds())
	return api, nil
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

func (m *Manager) hello(ctx context.Context, api flowdproto.API, wait bool) (flowdproto.Hello, error) {
	deadline := time.Now().Add(m.Wait)
	for {
		hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		h, err := api.Hello(hctx)
		cancel()
		if err == nil || !wait || errors.Is(err, flowdproto.ErrProtocol) || time.Now().After(deadline) {
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
func (m *Manager) token(ctx context.Context, sb *Sandbox) (string, error) {
	if len(sb.FlowdToken) > 0 {
		b, err := m.Sealer.Open(sb.FlowdToken)
		return string(b), err
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

// deliver pushes this release of flowd, its unit and its config, then one bounded exec switches the release and
// restarts flowd. Releases are never overwritten: the binary goes to a temporary name and is moved into place only
// when the release does not exist yet. Running hosts keep their binary, and Incus refuses to write onto a running
// executable anyway (measured: 500 "open …/flowd: text file busy").
func (m *Manager) deliver(ctx context.Context, instance, arch, token string) error {
	bin, err := m.Flowd(arch)
	if err != nil {
		return err
	}
	cfg, _ := json.Marshal(map[string]string{"token": token, "sandbox": instance})
	release := layout.Release(m.Version)
	err = m.Runtime.Push(ctx, instance,
		sandbox.File{Path: release + "/flowd.new", Content: bin, Mode: 0o755},
		sandbox.File{Path: layout.Unit, Content: []byte(Unit), Mode: 0o644},
		sandbox.File{Path: layout.Config, Content: cfg, Mode: 0o600})
	if err != nil {
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

// ErrNotWarm is Warm's answer for a sandbox Flow has not brought up (or the owner stopped).
var ErrNotWarm = errors.New("sandboxes: not warm")

// Warm returns the flowd of a sandbox that is warm by Flow's records, reconnecting after Flow's own restart; it
// never starts a stopped sandbox.
func (m *Manager) Warm(ctx context.Context, projectID string) (flowdproto.API, error) {
	return m.ensure(ctx, projectID, false)
}

// Stop stops a project's sandbox; its filesystem stays.
func (m *Manager) Stop(ctx context.Context, projectID string) error {
	defer m.lock(projectID)()
	m.remember(projectID, nil)
	sb, err := m.Store.ByProject(ctx, projectID)
	if err != nil {
		return err
	}
	m.setState(ctx, &sb, StateStopping, "")
	if err := m.Runtime.Stop(ctx, sb.Instance, 30*time.Second); err != nil {
		m.setState(context.WithoutCancel(ctx), &sb, StateFailed, string(apitypes.ErrorCodeSandboxUnavailable))
		return err
	}
	m.setState(ctx, &sb, StateStopped, "")
	return nil
}

// Setup brings a sandbox up in the background.
func (m *Manager) Setup(projectID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if _, err := m.Ensure(ctx, projectID); err != nil {
			m.Log.Error("sandbox setup", "project", projectID, "err", err)
		}
	}()
}

// Describe is the API's view of a project's sandbox.
func (m *Manager) Describe(ctx context.Context, projectID string) apitypes.Sandbox {
	sb, err := m.Store.ByProject(ctx, projectID)
	if err != nil {
		return apitypes.Sandbox{State: apitypes.SandboxStateNone}
	}
	return apitypes.Sandbox{State: apitypes.SandboxState(sb.State), ProgressCode: sb.ProgressCode,
		ErrorCode: apitypes.ErrorCode(sb.ErrorCode), LastUsedAt: sb.LastUsedAt, FlowdVersion: sb.FlowdVersion}
}

// API implements httpapi.Sandboxes: state, start and stop (setup logs and rebuild answer not_implemented).
type API struct {
	httpapi.NotImplemented
	Manager *Manager
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
