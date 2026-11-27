package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

// Runner runs a short command and returns its combined output; AsAgent runs it as layout.AgentUser when flowd is
// root (git in the work tree: files the agent edits must be its own).
type Runner interface {
	Run(ctx context.Context, c Cmd) ([]byte, error)
}

// Cmd is one command for a Runner.
type Cmd struct {
	Name    string
	Args    []string
	Dir     string
	AsAgent bool
	Env     []string
	// Limit, when set, bounds the output kept: stdout only (stderr goes into the error), at most Limit+1 bytes, so
	// a caller sees it was cut by a result longer than Limit. The command still runs to its end.
	Limit int
}

// capped keeps the first max bytes written to it and drops the rest.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// ExecRunner is the Runner on the real machine.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, c Cmd) ([]byte, error) {
	// The names are flowd's own (git, systemd-run, systemctl); arguments are validated names and paths.
	cmd := exec.CommandContext(ctx, c.Name, c.Args...) //nolint:gosec // see above
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	if c.AsAgent && os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: layout.AgentUID, Gid: layout.AgentGID}}
		cmd.Env = append(cmd.Env, "HOME="+layout.Home, "USER="+layout.AgentUser)
	}
	if c.Limit > 0 {
		out, errOut := &capped{max: c.Limit + 1}, &capped{max: 4 << 10}
		cmd.Stdout, cmd.Stderr = out, errOut
		if err := cmd.Run(); err != nil {
			return out.buf.Bytes(), fmt.Errorf("%s %s: %w: %s", c.Name, strings.Join(c.Args, " "), err,
				strings.TrimSpace(errOut.buf.String()))
		}
		return out.buf.Bytes(), nil
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %w: %s", c.Name, strings.Join(c.Args, " "), err,
			strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

// Units manages the transient systemd units flowd starts. No unit is flowd's child: they outlive its restarts.
type Units interface {
	// Start runs argv as a transient service unit of the agent user with the given systemd properties.
	Start(ctx context.Context, unit string, argv []string, props []string) error
	// StartRoot is Start for a unit that runs as root: a job's runner, which owns the job's log and result and runs
	// the job's command as the agent user itself.
	StartRoot(ctx context.Context, unit string, argv []string, props []string) error
	State(ctx context.Context, unit string) (flowdproto.UnitState, error)
	// List names the loaded units matching a glob such as flow-session-*.
	List(ctx context.Context, glob string) ([]string, error)
	Stop(ctx context.Context, unit string) error
}

// Systemd is Units through systemd-run and systemctl.
type Systemd struct{ Run Runner }

// Start implements Units: a service (not a scope, so it is nobody's child), collected after it ends, never
// restarted by systemd (flowd restarts hosts on the next message, with resume), swap off. OOMPolicy=continue:
// systemd's default for services (stop) ends the whole unit when the kernel kills one of its processes at
// MemoryMax (measured, systemd 257: "Failed with result 'oom-kill'"), so an agent's oversized build would take its
// session host down with it; with continue only that process dies and the host reports the failed command.
func (s Systemd) Start(ctx context.Context, unit string, argv []string, props []string) error {
	return s.start(ctx, unit, argv, props, []string{"--uid=" + strconv.Itoa(layout.AgentUID),
		"--gid=" + strconv.Itoa(layout.AgentGID)})
}

// StartRoot implements Units: as Start, without the agent user.
func (s Systemd) StartRoot(ctx context.Context, unit string, argv []string, props []string) error {
	return s.start(ctx, unit, argv, props, nil)
}

func (s Systemd) start(ctx context.Context, unit string, argv, props, user []string) error {
	args := append([]string{"--unit=" + unit}, user...)
	args = append(args, "--collect", "--service-type=exec", "--quiet", "-p", "Restart=no", "-p", "MemorySwapMax=0",
		"-p", "OOMPolicy=continue", "--setenv=HOME="+layout.Home)
	for _, p := range props {
		args = append(args, "-p", p)
	}
	args = append(append(args, "--"), argv...)
	_, err := s.Run.Run(ctx, Cmd{Name: "systemd-run", Args: args})
	return err
}

// State implements Units.
func (s Systemd) State(ctx context.Context, unit string) (flowdproto.UnitState, error) {
	out, err := s.Run.Run(ctx, Cmd{Name: "systemctl", Args: []string{"show", "-p", "ActiveState,LoadState", unit}})
	if err != nil {
		return "", err
	}
	var active, load string
	for line := range strings.Lines(string(out)) {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "ActiveState":
			active = v
		case "LoadState":
			load = v
		}
	}
	switch {
	case load == "not-found":
		return flowdproto.UnitMissing, nil
	case active == "active", active == "reloading":
		return flowdproto.UnitActive, nil
	case active == "activating":
		return flowdproto.UnitActivating, nil
	case active == "failed":
		return flowdproto.UnitFailed, nil
	}
	return flowdproto.UnitInactive, nil
}

// List implements Units.
func (s Systemd) List(ctx context.Context, glob string) ([]string, error) {
	out, err := s.Run.Run(ctx, Cmd{Name: "systemctl", Args: []string{"list-units", "--all", "--plain", "--no-legend",
		"--type=service", glob}})
	if err != nil {
		return nil, err
	}
	var units []string
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	return units, nil
}

// Stop implements Units.
// A unit that is not loaded (never started, or collected after it ended) is stopped already.
func (s Systemd) Stop(ctx context.Context, unit string) error {
	_, err := s.Run.Run(ctx, Cmd{Name: "systemctl", Args: []string{"stop", unit}})
	if err != nil && strings.Contains(err.Error(), "not loaded") {
		return nil
	}
	return err
}
