// Package layout names every path and systemd unit inside a sandbox, so Flow, flowd, the session hosts and the
// terminals agree on them. docs/architecture.md ("Sandbox layout") gives the reasons.
//
// Three storage classes, never mixed:
//   - the sandbox's own root filesystem (persistent, per project): work trees, event logs, flowd state;
//   - the home volume (one, mounted into every sandbox): agent logins and config, shared caches; never work trees,
//     logs or sockets, or projects would see each other's files;
//   - /run (tmpfs, gone at reboot): sockets and the current credentials.
package layout

import (
	"path"
	"regexp"
)

// AgentUser is the unprivileged account agents, hosts and terminals run as. Not 1000: that uid belongs to the
// host's first user, and files on a shifted volume keep their raw uid on the host.
const (
	AgentUser = "flow"
	AgentUID  = 65532
	AgentGID  = 65532
)

// Root filesystem.
const (
	Home     = "/home/flow"
	Work     = Home + "/work"         // main checkouts: Work/<repo>
	Sessions = Work + "/.sessions"    // session directories: Sessions/<name>/<repo>
	BinDir   = "/opt/flow/bin"        // flowd, multi-call: also git-credential-flow and the make shim
	Releases = "/opt/flow/releases"   // Releases/<version>/{flowd,sidecar/}: never overwritten in place
	State    = "/var/lib/flow"        // flowd state
	Logs     = State + "/sessions"    // Logs/<session>/events.jsonl
	Config   = "/etc/flow/flowd.json" // flowd's config and token, pushed by Flow, root 0600
	Unit     = "/etc/systemd/system/flowd.service"
	Settings = "/etc/flow/claude-settings.json" // the Flow settings layer for Claude Code (--settings)
)

// Home volume, mounted at HomeVolume in every sandbox.
const (
	HomeVolume    = "/srv/flow-home"
	ClaudeConfig  = HomeVolume + "/claude"       // CLAUDE_CONFIG_DIR: login, settings, transcripts
	XDGConfig     = HomeVolume + "/config"       // XDG_CONFIG_HOME: opencode/ (gh uses GHConfigDir)
	XDGData       = HomeVolume + "/data"         // XDG_DATA_HOME: opencode's data
	AgentConfig   = HomeVolume + "/agent-config" // checkout of the owner's config repository
	GoModCache    = HomeVolume + "/cache/go-mod" // GOMODCACHE
	NPMCache      = HomeVolume + "/cache/npm"    // npm_config_cache
	PlaywrightDir = HomeVolume + "/cache/ms-playwright"
)

// /run (tmpfs).
const (
	Run         = "/run/flow"
	Credentials = Run + "/credentials" // 0700 flow
	// GHConfigDir is GH_CONFIG_DIR of every unit: flowd rewrites its hosts.yml atomically (temp file + rename) on
	// every token renewal, and gh reads it on every call, so a process running for days sees the current token.
	// Never GH_TOKEN: an environment variable is frozen at process start.
	GHConfigDir = Credentials + "/gh"
	GHHosts     = GHConfigDir + "/hosts.yml"
	// ProviderEnv is the EnvironmentFile of the opencode serve unit: OpenCode provider keys, which change only when
	// the owner edits them (flowd restarts opencode serve at idle then). Never ANTHROPIC_API_KEY: that one is set per
	// session, only for sessions started in API-key mode.
	ProviderEnv = Credentials + "/providers.env"
	// FlowdSocket takes local requests: git's credential helper (asked on every git operation), the MCP server, the
	// gate shim.
	FlowdSocket = Run + "/flowd.sock"
)

// GitCredentialHelper is the helper line flowd writes into /etc/gitconfig for github.com: flowd's multi-call
// binary, answering from its current token through FlowdSocket.
const GitCredentialHelper = BinDir + "/git-credential-flow"

// OpenCodePort is where opencode serve listens, on localhost, one per sandbox.
const OpenCodePort = 4096

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidName reports whether s can be a session directory, a repository directory or a unit-name part: lower case,
// no slashes, no leading dot.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// SessionDir is the agent's working directory: one sibling worktree per repository below it.
func SessionDir(name string) string { return path.Join(Sessions, name) }

// Worktree is a repository's worktree in a session.
func Worktree(session, repo string) string { return path.Join(Sessions, session, repo) }

// Checkout is a repository's main checkout, the source of its worktrees.
func Checkout(repo string) string { return path.Join(Work, repo) }

// EventLog is a session's append-only log.
func EventLog(sessionID string) string { return path.Join(Logs, sessionID, "events.jsonl") }

// HostSocket is where a session host takes commands and live subscriptions.
func HostSocket(sessionID string) string { return path.Join(Run, "sessions", sessionID, "host.sock") }

// TerminalSocket is where a terminal unit serves its PTY.
func TerminalSocket(id string) string { return path.Join(Run, "terminals", id+".sock") }

// Release is the directory of one delivered flowd version.
func Release(version string) string { return path.Join(Releases, version) }

// Units. Session hosts and terminals are transient units (systemd-run) named by id, so flowd finds them again
// with systemctl list-units after its own restart.
const (
	FlowdUnit    = "flowd.service"
	OpenCodeUnit = "flow-opencode.service"
)

// HostUnit is a session host's unit.
func HostUnit(sessionID string) string { return "flow-session-" + sessionID + ".service" }

// JobUnit is a job's unit (setup script, config sync).
func JobUnit(id string) string { return "flow-job-" + id + ".service" }

// JobLog is a job's output, kept after the job ends.
func JobLog(id string) string { return path.Join(State, "jobs", id+".log") }

// TerminalUnit is a terminal's unit.
func TerminalUnit(id string) string { return "flow-term-" + id + ".service" }
