// Package claude is the session host's Claude adapter: it runs the Node sidecar (sidecar/, the Claude Agent SDK) as
// the host's child over stdio and supervises it. stdin carries the start line, then the commands of the kinds marked
// sidecar; stdout carries event drafts, which the host numbers and logs; stderr goes to the unit's journal, capped.
// The sidecar is started on the first message and again on the next message after it ended, resuming the session;
// whether to resume, the cost to continue from, model and modes come from the host's log, never from the sidecar.
// docs/architecture.md ("Process ownership", "Claude SDK settings", "Failures") gives the rules.
package claude

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/trick77/flow/internal/event"
)

// StartKind is the sidecar's own first command (sidecar/src/options.ts START_KIND); not a contract kind.
const StartKind = "sidecar.start"

// StartSpec is the data of the start line, sidecar/src/options.ts StartSpec.
type StartSpec struct {
	SessionID string `json:"sessionId"`
	// Resume: the provider has the session already; the sidecar then passes resume instead of sessionId.
	Resume  bool                 `json:"resume"`
	Cwd     string               `json:"cwd"`
	Mode    event.PermissionMode `json:"mode"`
	ModeCap event.PermissionMode `json:"modeCap,omitempty"`
	Model   string               `json:"model,omitempty"`
	// Effort: the session's effort; default (or absent) has the sidecar pass none, so Claude Code uses its own.
	Effort           event.Effort         `json:"effort,omitempty"`
	ClaudeAuth       event.ClaudeAuth     `json:"claudeAuth,omitempty"`
	ClaudeExecutable string               `json:"claudeExecutable"`
	SettingsPath     string               `json:"settingsPath"`
	MCPServers       map[string]MCPServer `json:"mcpServers,omitempty"`
	// AdditionalDirectories are the worktrees outside Cwd (a repository added to a session that works in its first
	// one), so Claude Code's file tools reach them without asking.
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	// AppendSystemPrompt is the layout note: which directory holds which repository, when Cwd is not the session's
	// one worktree.
	AppendSystemPrompt string `json:"appendSystemPrompt,omitempty"`
	// Env is added over the sidecar's own environment for Claude Code; the host leaves it empty and controls the
	// sidecar's process environment instead (Env).
	Env map[string]string `json:"env,omitempty"`
	// Version names the Flow release in CLAUDE_AGENT_SDK_CLIENT_APP=flow/<version>; the sidecar sets that and
	// CLAUDE_CODE_ENTRYPOINT=flow itself.
	Version string `json:"version,omitempty"`
	// PriorCostUSD is sent on a resume only: the SDK's cumulative total continues from the session's earlier cost.
	PriorCostUSD *float64 `json:"priorCostUsd,omitempty"`
}

// MCPServer is one stdio MCP server (the SDK's McpStdioServerConfig).
type MCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Session is what stays fixed for one host: the spec's identity and the paths.
type Session struct {
	ID               string
	Cwd              string
	ClaudeAuth       event.ClaudeAuth
	ClaudeExecutable string
	SettingsPath     string
	Version          string
	// MCPCommand is the flowd binary serving "flowd mcp"; MCPToken identifies the session to it (MCPTokenVar).
	MCPCommand string
	MCPToken   string
}

// State is what the host's log says now (host.AgentState).
type State struct {
	Resume       bool
	PriorCostUSD float64
	Model        string
	Effort       event.Effort
	Mode         event.PermissionMode
	ModeCap      event.PermissionMode
	// Worktrees are the session's worktrees as logged, in the order they were added.
	Worktrees []event.WorktreeAdded
}

// worktreeLayout is what the agent is told about its worktrees beyond its working directory: the ones outside cwd
// (to pass as additional directories) and a note naming every repository's directory. Neither when cwd is the
// session's one worktree. The note depends only on the worktrees, in their logged order, so it is the same at every
// start until a worktree is added.
func worktreeLayout(cwd string, wts []event.WorktreeAdded) (outside []string, note string) {
	if len(wts) == 0 || len(wts) == 1 && wts[0].Path == cwd {
		return nil, ""
	}
	var b strings.Builder
	b.WriteString("The session's repositories, each a git worktree in its own directory " +
		"(run git commands inside the repository's directory):\n")
	for _, w := range wts {
		if w.Path != cwd && !strings.HasPrefix(w.Path, cwd+"/") {
			outside = append(outside, w.Path)
		}
		fmt.Fprintf(&b, "- %s: %s, branch %s, based on %s\n", w.Path, w.Repo, w.Branch, w.BaseRef)
	}
	return outside, strings.TrimSuffix(b.String(), "\n")
}

// NewStartSpec builds the start line's data. The MCP list is the same for every session (stable tool list).
func NewStartSpec(s Session, st State) StartSpec {
	mode := st.Mode
	if mode == "" {
		mode = event.PermissionModeAsk // the sidecar refuses a start without a mode
	}
	spec := StartSpec{SessionID: s.ID, Resume: st.Resume, Cwd: s.Cwd, Mode: mode, ModeCap: st.ModeCap, Model: st.Model,
		Effort: st.Effort, ClaudeAuth: s.ClaudeAuth, ClaudeExecutable: s.ClaudeExecutable, SettingsPath: s.SettingsPath, Version: s.Version}
	spec.AdditionalDirectories, spec.AppendSystemPrompt = worktreeLayout(s.Cwd, st.Worktrees)
	if s.MCPCommand != "" {
		srv := MCPServer{Type: "stdio", Command: s.MCPCommand, Args: []string{"mcp"}}
		if s.MCPToken != "" {
			srv.Env = map[string]string{MCPTokenVar: s.MCPToken}
		}
		spec.MCPServers = map[string]MCPServer{"flow": srv}
	}
	if st.Resume {
		c := math.Round(st.PriorCostUSD*1e9) / 1e9
		spec.PriorCostUSD = &c
	}
	return spec
}

// StartLine is the start command as one stdin line, the first the sidecar reads.
func StartLine(id string, spec StartSpec) ([]byte, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(struct {
		V    int             `json:"v"`
		ID   string          `json:"id"`
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}{event.Version, id, StartKind, data})
	return append(b, '\n'), err
}
