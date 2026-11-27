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
	"strings"
)

// AgentUser is the unprivileged account agents, hosts and terminals run as. Not 1000: that uid belongs to the
// host's first user. The home volume is not shifted (idmapped mounts: "Required idmapping abilities not available"
// on the lab's dir pool, measured), so Incus maps it like the root filesystems: every sandbox shares the project's
// one idmap (never security.idmap.isolated), and the volume's files carry host uid 1000000 + 65532 (measured).
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
	// NodeBin runs the sidecar (Node 26 or newer); ClaudeCLI is the image's Claude Code, never updated in place.
	NodeBin   = "/usr/bin/node"
	ClaudeCLI = "/usr/local/bin/claude"
	// SidecarDir is the sidecar bundle's directory inside a release, beside its flowd: package.json, dist/main.js
	// and the production node_modules. A host finds it next to its own (resolved) executable.
	SidecarDir = "sidecar"
	// SidecarArchive is the bundle as Flow pushes it into a release (tar, gzip): one file through the Incus file
	// API instead of thousands. flowd unpacks it into SidecarDir at its start, before it answers, and removes it.
	SidecarArchive = "sidecar.tar.gz"
	// NetworkConfig is eth0's systemd-networkd unit, written by Flow through the file API before every start
	// (sandbox.NIC.GuestFiles): the static address Flow assigned. Named to sort before the image's own units.
	NetworkConfig = "/etc/systemd/network/00-flow-eth0.network"
)

// Home volume, mounted at HomeVolume in every sandbox.
const (
	HomeVolume   = "/srv/flow-home"
	ClaudeConfig = HomeVolume + "/claude"       // CLAUDE_CONFIG_DIR: login, settings, transcripts
	XDGConfig    = HomeVolume + "/config"       // XDG_CONFIG_HOME: opencode/ (gh uses GHConfigDir)
	XDGData      = HomeVolume + "/data"         // XDG_DATA_HOME: opencode's data
	AgentConfig  = HomeVolume + "/agent-config" // checkout of the owner's config repository
	GoModCache   = HomeVolume + "/cache/go-mod" // GOMODCACHE
	NPMCache     = HomeVolume + "/cache/npm"    // npm_config_cache
)

// HomeDirs are the agent-owned directories flowd makes on the home volume (at its start and on every environment
// push), parents first. A new volume's root is root 0711, so the agent can make none of them itself; root must
// never run go or npm there either, or it leaves directories in the shared caches the agent cannot write.
// ClaudeConfig is 0700: it holds a subscription login.
var HomeDirs = []struct {
	Path string
	Mode uint32
}{
	{ClaudeConfig, 0o700}, {XDGConfig, 0o755}, {XDGData, 0o755}, {AgentConfig, 0o755},
	{HomeVolume + "/cache", 0o755}, {GoModCache, 0o755}, {NPMCache, 0o755},
}

// PlaywrightDir holds Playwright's browsers: in the image (owned by the agent user), not on the home volume, which
// is not mounted when the image is built.
const PlaywrightDir = "/opt/ms-playwright"

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
	// SessionEnvDir (root 0700, so not below Credentials, which is the agent user's) holds one EnvironmentFile per
	// session host unit (SessionEnvFile), root 0600, rewritten on every host start: the one Claude credential of the
	// session's mode, the git identity, the MCP token. systemd reads it as root.
	SessionEnvDir = Run + "/session-env"
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

// RepoDir is the directory of a repository (owner/name) in Work and in session directories: its name, lower case.
// ok is false for a name that is not a ValidName (GitHub also allows a leading dot, underscore or dash and up to 100
// characters): Flow refuses such repositories when a project is created, the one place both sides check. Two
// repositories of one project must not share a RepoDir; Flow refuses that at creation too.
func RepoDir(fullName string) (dir string, ok bool) {
	owner, name, found := strings.Cut(fullName, "/")
	if !found || owner == "" || strings.Contains(name, "/") {
		return "", false
	}
	dir = strings.ToLower(name)
	return dir, ValidName(dir)
}

// SessionDir is the agent's working directory: one sibling worktree per repository below it.
func SessionDir(name string) string { return path.Join(Sessions, name) }

// Worktree is a repository's worktree in a session.
func Worktree(session, repo string) string { return path.Join(Sessions, session, repo) }

// Checkout is a repository's main checkout, the source of its worktrees.
func Checkout(repo string) string { return path.Join(Work, repo) }

// EventLog is a session's append-only log.
func EventLog(sessionID string) string { return path.Join(Logs, sessionID, "events.jsonl") }

// SessionSpecFile is the session's spec as flowd last started its host with (host.Spec, JSON), beside the log: the
// host reads it on every start, and flowd serves the log from disk with no host running.
func SessionSpecFile(sessionID string) string { return path.Join(Logs, sessionID, "spec.json") }

// SDKRecording is the file name, beside a session's log, a recording sidecar (SessionSpec.Record) appends the raw
// SDK messages to, for contract fixtures.
const SDKRecording = "sdk.jsonl"

// SessionEnvFile is the EnvironmentFile of a session's host unit.
func SessionEnvFile(sessionID string) string { return path.Join(SessionEnvDir, sessionID+".env") }

// SessionClaim names the session id that owns a session directory: flowd never puts a second session's worktrees
// into a directory another session id claimed, and frees the claim when it removes the worktrees.
func SessionClaim(name string) string { return path.Join(State, "session-dirs", name) }

// HostSocket is where a session host takes commands and live subscriptions.
func HostSocket(sessionID string) string { return path.Join(Run, "sessions", sessionID, "host.sock") }

// TerminalSocket is where a terminal unit serves its PTY.
func TerminalSocket(id string) string { return path.Join(Run, "terminals", id+".sock") }

// TerminalExitFile is where a terminal records its shell's exit status (JSON {"code":N}) before its unit ends, for a
// client attaching after that; tmpfs, gone at reboot like the terminal itself.
func TerminalExitFile(id string) string { return path.Join(Run, "terminals", id+".exit") }

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
