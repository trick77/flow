// Package sandbox is the boundary between Flow and the container runtime: everything Flow needs from Incus, as an
// interface. The real client (sandbox/incus, Incus REST API over HTTPS with a project-restricted client certificate
// and a pinned server certificate) and the in-memory fake (sandbox/sandboxfake) implement it; nothing else in Flow talks to
// Incus.
//
// Calls are short: no method holds a connection beyond its own operation. Exec is for bounded administrative
// commands only (bootstrap, systemctl); every long-lived process runs as a systemd unit inside the sandbox, started
// by flowd (docs/architecture.md).
package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"time"
)

// Errors every implementation returns, wrapped as it likes; callers test with errors.Is.
var (
	ErrNotFound = errors.New("sandbox: instance not found")
	ErrExists   = errors.New("sandbox: instance exists")
	ErrState    = errors.New("sandbox: wrong state for the operation")
	// ErrNetworkMissing: the host bridge sandboxes attach to does not exist (Docker deletes it on
	// `docker network rm`, also with sandboxes attached). Starting cannot help until the host has it again.
	ErrNetworkMissing = errors.New("sandbox: host bridge missing")
)

// Status of an instance.
type Status string

// Status values, Incus' own words lower-cased.
const (
	StatusStopped Status = "stopped"
	StatusRunning Status = "running"
	StatusOther   Status = "other" // starting, stopping, frozen, error: wait and look again
)

// Limits are the instance's resource bounds (limits.cpu, limits.memory). Setting a memory limit also keeps the
// instance's cgroup from swapping (spike finding 7). Zero leaves a bound unset.
type Limits struct {
	CPUs        int
	MemoryBytes int64
	Processes   int
}

// Spec describes an instance to create from the base image.
type Spec struct {
	// Name is the Incus instance name, unique in Flow's project.
	Name string
	// Image is the base image alias or fingerprint.
	Image  string
	Limits Limits
	// Nesting allows Docker inside (security.nesting; the project must allow it).
	Nesting bool
	// NIC is the instance's one network interface.
	NIC NIC
}

// Volume is a custom storage volume, such as the one home volume.
type Volume struct {
	Name string
	// Shifted maps uids through an idmapped mount (security.shifted), for instances with isolated idmaps.
	Shifted bool
}

// Mount attaches a volume to an instance at Path.
type Mount struct {
	Volume string
	Path   string
}

// File is pushed into an instance; the file is replaced whole.
type File struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
	UID     int
	GID     int
}

// Command is a short command run inside an instance. Implementations bound it with Timeout and never leave it
// running when the call returns.
type Command struct {
	Args    []string
	Env     map[string]string
	Dir     string
	UID     int
	Timeout time.Duration
}

// Result is a finished Command.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// State is an instance as Incus reports it now.
type State struct {
	Name   string
	Status Status
	// Arch is the Go architecture of the instance (amd64, arm64), from Incus' x86_64 / aarch64.
	Arch string
	// IPv4 is the address the guest reports on eth0, empty until its network is up.
	IPv4 string
	MAC  string
	// Address is the static address configured on the NIC (invalid when it has none); HostIface is the NIC's host
	// side while running, a port of the bridge unless the bridge was deleted and made again under it.
	Address   netip.Addr
	HostIface string
	// MemoryLimitBytes is the configured limits.memory, 0 when unset.
	MemoryLimitBytes int64
	// Usage, present while running.
	MemoryBytes     int64
	MemoryPeakBytes int64
	CPUNanos        int64
	DiskBytes       int64
	Processes       int
	CreatedAt       time.Time
	LastUsedAt      time.Time
}

// Runtime is everything Flow does with Incus.
type Runtime interface {
	// Create makes a stopped instance from the base image, its NIC and guest network configured.
	Create(ctx context.Context, s Spec) error
	// Clone makes a copy-on-write copy of a stopped or running instance under a new name. The copy has no NIC, so
	// it can never come up on the source's address: SetNIC gives it its own.
	Clone(ctx context.Context, from, to string) error
	// SetNIC replaces a stopped instance's NIC and rewrites its guest network configuration (NIC.GuestFiles)
	// through the file API: no exec, nothing running inside. Setting the same NIC again changes nothing.
	SetNIC(ctx context.Context, name string, n NIC) error
	// BridgePorts lists the host interfaces attached to a host bridge; ErrNetworkMissing when it does not exist.
	BridgePorts(ctx context.Context, bridge string) ([]string, error)
	// Start starts an instance; ErrNetworkMissing when its NIC's bridge does not exist.
	Start(ctx context.Context, name string) error
	// Stop stops cleanly within timeout, then forcibly. The filesystem stays.
	Stop(ctx context.Context, name string, timeout time.Duration) error
	Delete(ctx context.Context, name string) error
	SetLimits(ctx context.Context, name string, l Limits) error
	// EnsureVolume creates the volume when missing; an existing one is left as it is.
	EnsureVolume(ctx context.Context, v Volume) error
	// Attach mounts a volume into an instance; attaching the same volume at the same path again is a no-op.
	Attach(ctx context.Context, name string, m Mount) error
	// Push writes files into a running instance, creating parent directories.
	Push(ctx context.Context, name string, files ...File) error
	// Exec runs a short command in a running instance and waits for it.
	Exec(ctx context.Context, name string, c Command) (Result, error)
	State(ctx context.Context, name string) (State, error)
	List(ctx context.Context) ([]State, error)
}
