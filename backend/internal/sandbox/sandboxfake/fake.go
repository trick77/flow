// Package sandboxfake is an in-memory sandbox.Runtime for tests and the demo mode: instances, volumes and pushed
// files in maps, commands answered by a function the test sets. Host bridges exist unless a test removes one; like
// Incus, an instance whose bridge was removed and made again stays cut off until it is restarted.
package sandboxfake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/trick77/flow/internal/sandbox"
)

// Instance is one fake instance.
type Instance struct {
	Spec   sandbox.Spec
	Status sandbox.Status
	Limits sandbox.Limits
	Mounts []sandbox.Mount
	Files  map[string]sandbox.File
	// NIC is the NIC as last set; Guest the network configuration written into the guest for it.
	NIC   sandbox.NIC
	Guest []sandbox.File
	IPv4  string
	// HostIface is the host side of the NIC while running; Attached whether it is still a port of the bridge.
	HostIface string
	Attached  bool
	Created   time.Time
}

// Runtime is the fake. The zero value is not usable; call New.
type Runtime struct {
	mu        sync.Mutex
	instances map[string]*Instance
	volumes   map[string]sandbox.Volume
	missing   map[string]bool
	ports     int
	// Arch is what State reports, amd64 by default.
	Arch string
	// ExecFunc answers Exec; nil answers every command with exit 0 and no output.
	ExecFunc func(name string, c sandbox.Command) (sandbox.Result, error)
	// Execs records every command run, in order.
	Execs []sandbox.Command
	// Starts counts Start calls that started an instance.
	Starts int
	// Now is the clock; time.Now by default.
	Now func() time.Time
}

var _ sandbox.Runtime = (*Runtime)(nil)

// New returns an empty fake.
func New() *Runtime {
	return &Runtime{instances: map[string]*Instance{}, volumes: map[string]sandbox.Volume{}, missing: map[string]bool{},
		Arch: "amd64", Now: time.Now}
}

// RemoveBridge deletes a host bridge as `docker network rm` does: running instances on it keep running, cut off.
func (r *Runtime) RemoveBridge(bridge string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missing[bridge] = true
	for _, in := range r.instances {
		if in.NIC.Bridge == bridge {
			in.Attached = false
		}
	}
}

// AddBridge makes a removed bridge exist again; instances cut off from it stay so until restarted.
func (r *Runtime) AddBridge(bridge string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.missing, bridge)
}

// Reachable reports whether Flow could reach the instance over its NIC now: running, attached, bridge present.
func (r *Runtime) Reachable(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.instances[name]
	return ok && in.Status == sandbox.StatusRunning && in.Attached && !r.missing[in.NIC.Bridge]
}

func (r *Runtime) get(name string) (*Instance, error) {
	in, ok := r.instances[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", sandbox.ErrNotFound, name)
	}
	return in, nil
}

func (r *Runtime) running(name string) (*Instance, error) {
	in, err := r.get(name)
	if err == nil && in.Status != sandbox.StatusRunning {
		err = fmt.Errorf("%w: %s is %s", sandbox.ErrState, name, in.Status)
	}
	return in, err
}

func guest(n sandbox.NIC) []sandbox.File {
	if !n.Address.IsValid() {
		return nil
	}
	return n.GuestFiles()
}

// Create implements sandbox.Runtime.
func (r *Runtime) Create(_ context.Context, s sandbox.Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[s.Name]; ok {
		return fmt.Errorf("%w: %s", sandbox.ErrExists, s.Name)
	}
	r.instances[s.Name] = &Instance{Spec: s, Status: sandbox.StatusStopped, Limits: s.Limits, NIC: s.NIC,
		Guest: guest(s.NIC), Files: map[string]sandbox.File{}, Created: r.Now()}
	return nil
}

// Clone implements sandbox.Runtime: the copy has the source's files and mounts, stopped, and no NIC.
func (r *Runtime) Clone(_ context.Context, from, to string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	src, err := r.get(from)
	if err != nil {
		return err
	}
	if _, ok := r.instances[to]; ok {
		return fmt.Errorf("%w: %s", sandbox.ErrExists, to)
	}
	cp := *src
	cp.Spec.Name, cp.Status, cp.IPv4, cp.HostIface, cp.Attached, cp.Created = to, sandbox.StatusStopped, "", "", false, r.Now()
	cp.NIC = sandbox.NIC{}
	cp.Mounts = slices.Clone(src.Mounts)
	cp.Files = maps.Clone(src.Files)
	cp.Guest = slices.Clone(src.Guest) // the guest's file is copied with the rest; SetNIC rewrites it
	r.instances[to] = &cp
	return nil
}

// SetNIC implements sandbox.Runtime.
func (r *Runtime) SetNIC(_ context.Context, name string, n sandbox.NIC) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	if in.Status != sandbox.StatusStopped {
		return fmt.Errorf("%w: %s is %s", sandbox.ErrState, name, in.Status)
	}
	in.NIC, in.Guest = n, guest(n)
	return nil
}

// BridgePorts implements sandbox.Runtime.
func (r *Runtime) BridgePorts(_ context.Context, bridge string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.missing[bridge] {
		return nil, fmt.Errorf("%w: %s", sandbox.ErrNetworkMissing, bridge)
	}
	var ports []string
	for _, name := range slices.Sorted(maps.Keys(r.instances)) {
		if in := r.instances[name]; in.Status == sandbox.StatusRunning && in.Attached && in.NIC.Bridge == bridge {
			ports = append(ports, in.HostIface)
		}
	}
	return ports, nil
}

// Start implements sandbox.Runtime; the instance comes up on its NIC's address.
func (r *Runtime) Start(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	if in.Status == sandbox.StatusRunning {
		return nil
	}
	if in.NIC.Bridge != "" {
		if r.missing[in.NIC.Bridge] {
			return fmt.Errorf("%w: Failed to start device \"eth0\": Parent device %q doesn't exist",
				sandbox.ErrNetworkMissing, in.NIC.Bridge)
		}
		if !in.NIC.Address.IsValid() {
			return fmt.Errorf("fake: %s: IPv4 filtering needs an address on an unmanaged parent", name)
		}
	}
	r.ports++
	r.Starts++
	in.Status, in.HostIface, in.Attached, in.IPv4 = sandbox.StatusRunning, fmt.Sprintf("veth%04d", r.ports), true, ""
	if in.NIC.Address.IsValid() {
		in.IPv4 = in.NIC.Address.Addr().String()
	}
	return nil
}

// Stop implements sandbox.Runtime.
func (r *Runtime) Stop(_ context.Context, name string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	in.Status, in.IPv4, in.HostIface, in.Attached = sandbox.StatusStopped, "", "", false
	return nil
}

// Delete implements sandbox.Runtime; a running instance must be stopped first, as Incus wants.
func (r *Runtime) Delete(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	if in.Status == sandbox.StatusRunning {
		return fmt.Errorf("%w: %s is running", sandbox.ErrState, name)
	}
	delete(r.instances, name)
	return nil
}

// SetLimits implements sandbox.Runtime.
func (r *Runtime) SetLimits(_ context.Context, name string, l sandbox.Limits) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	in.Limits = l
	return nil
}

// EnsureVolume implements sandbox.Runtime.
func (r *Runtime) EnsureVolume(_ context.Context, v sandbox.Volume) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.volumes[v.Name]; !ok {
		r.volumes[v.Name] = v
	}
	return nil
}

// Volume returns a volume EnsureVolume made.
func (r *Runtime) Volume(name string) (sandbox.Volume, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.volumes[name]
	return v, ok
}

// Attach implements sandbox.Runtime.
func (r *Runtime) Attach(_ context.Context, name string, m sandbox.Mount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	if _, ok := r.volumes[m.Volume]; !ok {
		return fmt.Errorf("%w: volume %s", sandbox.ErrNotFound, m.Volume)
	}
	if !slices.Contains(in.Mounts, m) {
		in.Mounts = append(in.Mounts, m)
	}
	return nil
}

// Push implements sandbox.Runtime.
func (r *Runtime) Push(_ context.Context, name string, files ...sandbox.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.running(name)
	if err != nil {
		return err
	}
	for _, f := range files {
		f.Content = slices.Clone(f.Content)
		in.Files[f.Path] = f
	}
	return nil
}

// Exec implements sandbox.Runtime.
func (r *Runtime) Exec(_ context.Context, name string, c sandbox.Command) (sandbox.Result, error) {
	r.mu.Lock()
	if _, err := r.running(name); err != nil {
		r.mu.Unlock()
		return sandbox.Result{}, err
	}
	r.Execs = append(r.Execs, c)
	fn := r.ExecFunc
	r.mu.Unlock()
	if fn == nil {
		return sandbox.Result{}, nil
	}
	return fn(name, c)
}

// State implements sandbox.Runtime.
func (r *Runtime) State(_ context.Context, name string) (sandbox.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return sandbox.State{}, err
	}
	return r.state(in), nil
}

func (r *Runtime) state(in *Instance) sandbox.State {
	s := sandbox.State{Name: in.Spec.Name, Status: in.Status, Arch: r.Arch, IPv4: in.IPv4, CreatedAt: in.Created,
		Address: in.NIC.Address.Addr(), MemoryLimitBytes: in.Limits.MemoryBytes}
	if in.Status == sandbox.StatusRunning {
		s.MAC, s.HostIface = "00:16:3e:00:00:01", in.HostIface
		s.MemoryBytes, s.Processes = 114<<20, 14
	}
	return s
}

// List implements sandbox.Runtime, sorted by name.
func (r *Runtime) List(_ context.Context) ([]sandbox.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sandbox.State, 0, len(r.instances))
	for _, name := range slices.Sorted(maps.Keys(r.instances)) {
		out = append(out, r.state(r.instances[name]))
	}
	return out, nil
}

// Instance returns a copy of the named instance, for assertions.
func (r *Runtime) Instance(name string) (Instance, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.instances[name]
	if !ok {
		return Instance{}, false
	}
	return *in, true
}
