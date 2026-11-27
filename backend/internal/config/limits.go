package config

import "github.com/trick77/ship/kit/env"

// Resource defaults. One project's full check (loom's make check) peaks at about 4 GiB in its sandbox, page cache
// included, with about 2.4 GiB anonymous; an idle sandbox with Docker running holds about 108 MiB (measured in the
// image lab). 4 GiB was the floor, so a sandbox gets 6 GiB.
const (
	DefaultSandboxMemoryMiB = 6 << 10
	// DefaultSandboxCPUs: half of the target host's 8 threads, so one sandbox's build leaves the other half to Flow,
	// the host's other containers and a second sandbox.
	DefaultSandboxCPUs = 4
	// DefaultSessionTasks bounds the threads and processes of one session (agent, its builds, test binaries,
	// browsers): far above a race-enabled Go build or a Playwright run, low enough to stop a fork bomb before the
	// sandbox's own units (flowd, terminals, other sessions) starve.
	DefaultSessionTasks = 4096
)

// SessionLimits bound one session host unit and everything its agent runs. Zero leaves a bound unset.
type SessionLimits struct {
	MemoryBytes int64
	CPUPercent  int
	Tasks       int
}

// loadSessionLimits reads BACKEND_SESSION_MEMORY_MIB, _CPU_PERCENT and _TASKS_MAX. The defaults follow the
// sandbox's: memory is the sandbox's less 1 GiB (5 GiB of 6: a full check plus Claude Code and the sidecar, and room
// left for flowd, Docker's daemon and a terminal), CPU all but one of its CPUs, so the session past its bound is
// what the kernel kills or throttles, never the sandbox.
func loadSessionLimits(r *env.Reader, sandboxMemory int64, sandboxCPUs int) SessionLimits {
	defMem := 0
	if mib := int(sandboxMemory >> 20); mib > 0 {
		defMem = max(mib-1024, mib/2)
	}
	defCPU := 0
	if sandboxCPUs > 0 {
		defCPU = max(sandboxCPUs-1, 1) * 100
	}
	l := SessionLimits{
		MemoryBytes: int64(r.Int("BACKEND_SESSION_MEMORY_MIB", defMem)) << 20,
		CPUPercent:  r.Int("BACKEND_SESSION_CPU_PERCENT", defCPU),
		Tasks:       r.Int("BACKEND_SESSION_TASKS_MAX", DefaultSessionTasks),
	}
	switch {
	case l.MemoryBytes < 0, sandboxMemory > 0 && l.MemoryBytes > sandboxMemory:
		r.Fail("BACKEND_SESSION_MEMORY_MIB: %d MiB, want 0 (unset) up to the sandbox's %d MiB", l.MemoryBytes>>20,
			sandboxMemory>>20)
	}
	switch {
	case l.CPUPercent < 0, sandboxCPUs > 0 && l.CPUPercent > sandboxCPUs*100:
		r.Fail("BACKEND_SESSION_CPU_PERCENT: %d, want 0 (unset) up to the sandbox's %d CPUs × 100", l.CPUPercent,
			sandboxCPUs)
	}
	if l.Tasks < 0 {
		r.Fail("BACKEND_SESSION_TASKS_MAX: %d, want 0 (unset) or more", l.Tasks)
	}
	if sandboxMemory < 0 {
		r.Fail("BACKEND_SANDBOX_MEMORY_MIB: want 0 (unset) or more")
	}
	if sandboxCPUs < 0 {
		r.Fail("BACKEND_SANDBOX_CPUS: want 0 (all of the host's) or more")
	}
	return l
}
