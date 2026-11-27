package host

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/host/claude"
	"github.com/trick77/flow/internal/layout"
)

// The spec picks the adapter: replay on request, the sidecar for claude, not_implemented for opencode.
func TestNewAdapter_selection(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "s", "events.jsonl")
	paths := Paths{Node: layout.NodeBin, Claude: layout.ClaudeCLI, Settings: layout.Settings}
	environ := []string{"PATH=/bin", "ANTHROPIC_API_KEY=key", "CLAUDE_CODE_OAUTH_TOKEN=tok", "BACKEND_MCP_TOKEN=mcp"}
	state := func() AgentState { return AgentState{Model: "m", Effort: event.EffortXhigh} }
	log := slog.New(slog.DiscardHandler)
	mk := func(edit func(*Spec)) (Adapter, error) {
		s := spec()
		s.Session.Replay = false
		edit(&s)
		return newAdapter(s, Pace{}, paths, logPath, "1.2.3", environ, state, log)
	}

	if a, err := mk(func(s *Spec) { s.Session.Replay = true; s.Session.Provider = event.ProviderOpencode }); err != nil {
		t.Error(err)
	} else if _, ok := a.(*Replay); !ok {
		t.Errorf("replay spec got %T", a)
	}
	if _, err := mk(func(s *Spec) { s.Session.Provider = event.ProviderOpencode }); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("opencode: %v", err)
	}
	if _, err := mk(func(s *Spec) { s.Session.Provider = "other" }); err == nil {
		t.Error("an unknown provider got an adapter")
	}

	a, err := mk(func(s *Spec) { s.Session.Record = true; s.Session.ClaudeAuth = event.ClaudeAuthAPIKey })
	if err != nil {
		t.Fatal(err)
	}
	ca, ok := a.(*claude.Adapter)
	if !ok {
		t.Fatalf("claude spec got %T", a)
	}
	cfg := ca.Config()
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if want := []string{layout.NodeBin, filepath.Join(filepath.Dir(exe), layout.SidecarDir, "dist", "main.js")}; !slices.Equal(cfg.Argv, want) {
		t.Errorf("argv %v, want %v", cfg.Argv, want)
	}
	if cfg.RecordPath != filepath.Join(filepath.Dir(logPath), layout.SDKRecording) {
		t.Errorf("record path %q", cfg.RecordPath)
	}
	// The host's state reaches the start line: model and effort as the log has them.
	if st := cfg.State(); st.Model != "m" || st.Effort != event.EffortXhigh {
		t.Errorf("state %+v", st)
	}
	// No MCP server until flowd mcp exists: Claude Code would start a command that is not there.
	if s := claude.NewStartSpec(cfg.Session, claude.State{}); s.MCPServers != nil {
		t.Errorf("start line names MCP servers %v", s.MCPServers)
	}
	if cfg.Dir != spec().Dir || cfg.Session.Cwd != spec().Dir || cfg.Session.ID != sid || cfg.Session.MCPToken != "mcp" ||
		cfg.Session.MCPCommand != "" || cfg.Session.ClaudeExecutable != layout.ClaudeCLI || cfg.Session.Version != "1.2.3" {
		t.Errorf("session %+v", cfg.Session)
	}
	if !slices.Contains(cfg.Env, "ANTHROPIC_API_KEY=key") || slices.Contains(cfg.Env, "CLAUDE_CODE_OAUTH_TOKEN=tok") {
		t.Errorf("env %v", cfg.Env)
	}
	a, _ = mk(func(*Spec) {})
	if a.(*claude.Adapter).Config().RecordPath != "" {
		t.Error("recording without the switch")
	}
	// The agent works where the spec's Cwd says (the session's one worktree), the sidecar too.
	wt := spec().Worktrees[0].Path
	a, _ = mk(func(s *Spec) { s.Cwd = wt })
	if cfg := a.(*claude.Adapter).Config(); cfg.Dir != wt || cfg.Session.Cwd != wt {
		t.Errorf("cwd %q: dir %q, session cwd %q", wt, cfg.Dir, cfg.Session.Cwd)
	}
}
