package supervisor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host/claude"
	"github.com/trick77/flow/internal/layout"
)

// checkProvider refuses a session no host adapter can run: OpenCode is not built yet. A replay session runs whatever
// its provider says.
func checkProvider(sp flowdproto.SessionSpec) error {
	switch {
	case sp.Replay, sp.Provider == event.ProviderClaude:
		return nil
	case sp.Provider == event.ProviderOpencode:
		return &codeError{code: flowdproto.CodeNotImplemented,
			err: fmt.Errorf("%w: provider %s", flowdproto.ErrNotImplemented, sp.Provider)}
	}
	return invalid("provider %q", sp.Provider)
}

// writeSessionEnv writes the host unit's EnvironmentFile (layout.SessionEnvFile, tmpfs, root 0600 in a root 0700
// directory) and returns its
// path: the one Claude credential of the session's mode (never another: Claude Code prefers an API key over a token
// over a stored login, so a second one would switch who pays), the git identity, and a fresh MCP token. Written on
// every host start, so a replaced credential reaches the session then. Never in spec.json, which is on disk.
func (s *Supervisor) writeSessionEnv(sp flowdproto.SessionSpec) (string, error) {
	s.mu.Lock()
	e := s.envSet
	s.mu.Unlock()
	vars := [][2]string{}
	if e.GitName != "" {
		vars = append(vars, [2]string{"GIT_AUTHOR_NAME", e.GitName}, [2]string{"GIT_COMMITTER_NAME", e.GitName})
	}
	if e.GitEmail != "" {
		vars = append(vars, [2]string{"GIT_AUTHOR_EMAIL", e.GitEmail}, [2]string{"GIT_COMMITTER_EMAIL", e.GitEmail})
	}
	switch name := claude.CredentialVar(sp.ClaudeAuth); {
	case sp.Provider != event.ProviderClaude || sp.Replay:
	case name == claude.OAuthTokenVar && e.ClaudeOAuthToken != "":
		vars = append(vars, [2]string{name, e.ClaudeOAuthToken})
	case name == claude.APIKeyVar && e.AnthropicAPIKey != "":
		vars = append(vars, [2]string{name, e.AnthropicAPIKey})
	case name != "":
		s.o.Log.Warn("no credential for the session's Claude mode", "session", sp.ID, "claudeAuth", sp.ClaudeAuth)
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return "", err
	}
	vars = append(vars, [2]string{claude.MCPTokenVar, hex.EncodeToString(tok)})
	var b strings.Builder
	for _, v := range vars {
		b.WriteString(v[0] + "=" + envQuote(v[1]) + "\n")
	}
	path := s.p(layout.SessionEnvFile(sp.ID))
	dir := filepath.Dir(path)
	// /run/flow, if this makes it, stays traversable for the agent user's sockets below it (as mkdirAgent).
	if err := os.MkdirAll(filepath.Dir(dir), 0o711); err != nil { //nolint:gosec // traverse only
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	tmp := path + ".tmp"
	// The path is layout's, from a session id EnsureSession checked with layout.ValidName.
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil { //nolint:gosec // see above
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// envQuote is a value for a systemd EnvironmentFile: double quotes, inside which systemd unescapes \" \\ \` \$, and
// no newline (a profile name or token never has one; one would end the assignment).
func envQuote(v string) string {
	v = strings.NewReplacer("\r", "", "\n", " ").Replace(v)
	v = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(v)
	return `"` + v + `"`
}
