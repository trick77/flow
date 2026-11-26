// Package sandboxfake is an in-memory sandbox.Runtime for tests and the demo mode: instances, volumes and pushed
// files in maps, commands answered by a function the test sets.
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
	Spec    sandbox.Spec
	Status  sandbox.Status
	Limits  sandbox.Limits
	Mounts  []sandbox.Mount
	Files   map[string]sandbox.File
	IPv4    string
	Created time.Time
}

// Runtime is the fake. The zero value is not usable; call New.
type Runtime struct {
	mu        sync.Mutex
	instances map[string]*Instance
	volumes   map[string]sandbox.Volume
	nextIP    int
	// Arch is what State reports, amd64 by default.
	Arch string
	// ExecFunc answers Exec; nil answers every command with exit 0 and no output.
	ExecFunc func(name string, c sandbox.Command) (sandbox.Result, error)
	// Execs records every command run, in order.
	Execs []sandbox.Command
	// Now is the clock; time.Now by default.
	Now func() time.Time
}

var _ sandbox.Runtime = (*Runtime)(nil)

// New returns an empty fake.
func New() *Runtime {
	return &Runtime{instances: map[string]*Instance{}, volumes: map[string]sandbox.Volume{}, Arch: "amd64",
		Now: time.Now}
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

// Create implements sandbox.Runtime.
func (r *Runtime) Create(_ context.Context, s sandbox.Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.instances[s.Name]; ok {
		return fmt.Errorf("%w: %s", sandbox.ErrExists, s.Name)
	}
	r.instances[s.Name] = &Instance{Spec: s, Status: sandbox.StatusStopped, Limits: s.Limits,
		Files: map[string]sandbox.File{}, Created: r.Now()}
	return nil
}

// Clone implements sandbox.Runtime: the copy has the source's files and mounts, stopped.
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
	cp.Spec.Name, cp.Status, cp.IPv4, cp.Created = to, sandbox.StatusStopped, "", r.Now()
	cp.Mounts = slices.Clone(src.Mounts)
	cp.Files = maps.Clone(src.Files)
	r.instances[to] = &cp
	return nil
}

// Start implements sandbox.Runtime; the instance gets the next bridge address.
func (r *Runtime) Start(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, err := r.get(name)
	if err != nil {
		return err
	}
	if in.Status != sandbox.StatusRunning {
		r.nextIP++
		in.Status, in.IPv4 = sandbox.StatusRunning, fmt.Sprintf("10.77.0.%d", 10+r.nextIP)
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
	in.Status, in.IPv4 = sandbox.StatusStopped, ""
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
	s := sandbox.State{Name: in.Spec.Name, Status: in.Status, Arch: r.Arch, IPv4: in.IPv4, CreatedAt: in.Created}
	if in.Status == sandbox.StatusRunning {
		s.MAC = "00:16:3e:00:00:01"
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
