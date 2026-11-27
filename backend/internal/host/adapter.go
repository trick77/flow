package host

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host/claude"
	"github.com/trick77/flow/internal/layout"
)

// ErrNotImplemented is a provider the host has no adapter for yet (flowdproto.CodeNotImplemented).
var ErrNotImplemented = errors.New("host: " + flowdproto.CodeNotImplemented)

// mcpCommand is the binary serving the flow MCP server ("<it> mcp"), empty while flowd has no mcp subcommand: the
// start line then names no MCP server, and the sidecar configures none. Set it to the host's own executable once
// flowd mcp exists.
const mcpCommand = ""

// Paths are the files the Claude adapter needs; empty Sidecar means the release's bundle beside this binary.
type Paths struct {
	Node, Sidecar, Claude, Settings string
}

// newAdapter picks the session's adapter: the replay when the spec asks for it, else by provider.
func newAdapter(spec Spec, pace Pace, paths Paths, logPath, version string, environ []string,
	state func() AgentState, log *slog.Logger) (Adapter, error) {
	s := spec.Session
	if s.Replay {
		return NewDemoReplay(pace)
	}
	switch s.Provider {
	case event.ProviderClaude:
	case event.ProviderOpencode:
		return nil, fmt.Errorf("%w: provider %s", ErrNotImplemented, s.Provider)
	default:
		return nil, fmt.Errorf("host: unknown provider %q", s.Provider)
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil, err
	}
	if paths.Sidecar == "" {
		paths.Sidecar = filepath.Join(filepath.Dir(exe), layout.SidecarDir)
	}
	env, ok := claude.Env(environ, s.ClaudeAuth)
	if !ok {
		log.Warn("no credential for the session's Claude mode; Claude Code will report itself signed out",
			"claudeAuth", s.ClaudeAuth, "variable", claude.CredentialVar(s.ClaudeAuth))
	}
	cfg := claude.Config{
		Argv: []string{paths.Node, filepath.Join(paths.Sidecar, "dist", "main.js")},
		Dir:  spec.AgentDir(),
		Env:  env,
		Session: claude.Session{ID: s.ID, Cwd: spec.AgentDir(), ClaudeAuth: s.ClaudeAuth, ClaudeExecutable: paths.Claude,
			SettingsPath: paths.Settings, Version: version, MCPCommand: mcpCommand, MCPToken: lookup(environ, claude.MCPTokenVar)},
		State: func() claude.State {
			a := state()
			return claude.State{Resume: a.Resume, PriorCostUSD: a.PriorCostUSD, Model: a.Model, Effort: a.Effort, Mode: a.Mode,
				ModeCap: a.ModeCap, Worktrees: a.Worktrees}
		},
		Log: log,
	}
	if s.Record {
		cfg.RecordPath = filepath.Join(filepath.Dir(logPath), layout.SDKRecording)
	}
	return claude.New(cfg), nil
}

func lookup(environ []string, name string) string {
	for _, kv := range environ {
		if len(kv) > len(name) && kv[len(name)] == '=' && kv[:len(name)] == name {
			return kv[len(name)+1:]
		}
	}
	return ""
}
