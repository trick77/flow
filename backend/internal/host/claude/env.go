package claude

import (
	"slices"
	"strings"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/layout"
)

// Credential variables, one per Claude mode (docs/architecture.md, "Credentials"). flowd puts the session's one
// into the host unit's EnvironmentFile; Env passes exactly that one on.
const (
	OAuthTokenVar = "CLAUDE_CODE_OAUTH_TOKEN" //nolint:gosec // a variable's name, not a credential
	APIKeyVar     = "ANTHROPIC_API_KEY"       //nolint:gosec // a variable's name, not a credential
)

// MCPTokenVar identifies the session to flowd mcp; flowd writes it into the unit's EnvironmentFile, the host hands
// it to the MCP server's environment only.
const MCPTokenVar = "BACKEND_MCP_TOKEN" //nolint:gosec // a variable's name, not a credential

// passed are the variables of the host's own environment the sidecar sees, and through it Claude Code and every
// command it runs: the SDK's env replaces the CLI's environment, and the sidecar fills it from its own. Everything
// else is dropped, so no provider credential, cloud-provider switch (CLAUDE_CODE_USE_BEDROCK, AWS_*, ...),
// ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, GH_TOKEN or the MCP token reaches it by accident.
// The image's default unit environment (PATH, caches, Playwright, auto-updater switches, locale) comes through as
// the unit has it; the layout values below fill in only what the unit lacks.
var passed = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TZ",
	"TMPDIR", "TERM", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	"GOMODCACHE", "GOPATH", "GOCACHE", "npm_config_cache", "PLAYWRIGHT_BROWSERS_PATH", "DISABLE_AUTOUPDATER",
	"OPENCODE_DISABLE_AUTOUPDATE"}

// defaultPath is systemd's PATH, for a host started without one.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// CredentialVar is the variable of a Claude mode; "" for subscription_login (the CLI login on the home volume).
// An unset mode is the default, subscription_token.
func CredentialVar(auth event.ClaudeAuth) string {
	switch auth {
	case event.ClaudeAuthSubscriptionLogin:
		return ""
	case event.ClaudeAuthAPIKey:
		return APIKeyVar
	default:
		return OAuthTokenVar
	}
}

// Env builds the sidecar's environment from the host's (base, os.Environ form): the allow-list, the home-volume
// locations, and the credential of the session's mode, never another. ok is false when that mode needs a
// credential the host was not given (Claude Code then reports itself signed out). Values are never logged.
func Env(base []string, auth event.ClaudeAuth) (env []string, ok bool) {
	have := map[string]string{}
	for _, kv := range base {
		if k, v, found := strings.Cut(kv, "="); found {
			have[k] = v
		}
	}
	set := map[string]string{}
	for _, k := range passed {
		if v, found := have[k]; found {
			set[k] = v
		}
	}
	for k, v := range map[string]string{
		"PATH":                     defaultPath,
		"HOME":                     layout.Home,
		"GOMODCACHE":               layout.GoModCache,
		"npm_config_cache":         layout.NPMCache,
		"PLAYWRIGHT_BROWSERS_PATH": layout.PlaywrightDir,
		"DISABLE_AUTOUPDATER":      "1",
	} {
		if set[k] == "" {
			set[k] = v
		}
	}
	// Flow's own locations: the agents' home-volume state and gh's token file, whatever the unit says.
	for k, v := range map[string]string{
		"CLAUDE_CONFIG_DIR": layout.ClaudeConfig,
		"XDG_CONFIG_HOME":   layout.XDGConfig,
		"XDG_DATA_HOME":     layout.XDGData,
		"GH_CONFIG_DIR":     layout.GHConfigDir,
	} {
		set[k] = v
	}
	ok = true
	if name := CredentialVar(auth); name != "" {
		if v := have[name]; v != "" {
			set[name] = v
		} else {
			ok = false
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	return env, ok
}
