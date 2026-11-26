// Package flowdproto is the Flow↔flowd protocol: HTTP/1.1 with JSON bodies and server-sent events, Flow always the
// client, over the Incus bridge to the sandbox's flowd on Port. API is the one interface both ends implement: the
// HTTP client in Flow (flowd runtime area), flowd's core behind its HTTP server, and flowdfake for Flow's tests and
// the UI demo. docs/architecture.md explains the rules; this file holds the wire names.
//
// Authentication: every request carries "Authorization: Bearer <token>", the per-sandbox token Flow generated and
// pushed to /etc/flow/flowd.json through the Incus file API. Flow addresses flowd by the bridge IP Incus reports,
// and the NIC's MAC/IPv4 filtering keeps another sandbox from answering on it; nothing flowd sends is trusted
// beyond the sandbox it came from.
//
// Versioning: both ends send HeaderProtocol. flowd serves the range [MinProtocol, Protocol] it reports in Hello;
// Flow refuses a flowd outside its own range with ErrProtocol and redeploys flowd. Within a protocol version a
// side only adds optional fields and new control kinds, and ignores unknown ones.
package flowdproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
)

// Protocol versions this build speaks.
const (
	Protocol    = 1
	MinProtocol = 1
)

// Port is flowd's listen port on the sandbox's bridge address.
const Port = 7433

// HeaderProtocol carries the sender's protocol version on every request and response.
const HeaderProtocol = "Flow-Protocol"

// Routes, as net/http patterns. The SSE routes send "id: <seq>" on durable events (Events) or the request id
// (Control) and honour Last-Event-ID.
const (
	RouteHello          = "GET /v1/hello"
	RouteEnv            = "PUT /v1/env"
	RouteRepos          = "POST /v1/repos"
	RouteSessionPut     = "PUT /v1/sessions/{id}"
	RouteSessionGet     = "GET /v1/sessions/{id}"
	RouteSessionDelete  = "DELETE /v1/sessions/{id}"
	RouteCommands       = "POST /v1/sessions/{id}/commands"
	RouteEvents         = "GET /v1/sessions/{id}/events"
	RouteWorktrees      = "POST /v1/sessions/{id}/worktrees"
	RouteDiff           = "GET /v1/sessions/{id}/diff"
	RouteControl        = "GET /v1/control"
	RouteControlReply   = "POST /v1/control/{id}"
	RouteTerminalPut    = "PUT /v1/terminals/{id}"
	RouteTerminalAttach = "GET /v1/terminals/{id}/attach"
	RouteTerminalDelete = "DELETE /v1/terminals/{id}"
	RoutePort           = "/v1/ports/{port}/"
	RouteJobPut         = "PUT /v1/jobs/{id}"
	RouteJobGet         = "GET /v1/jobs/{id}"
	RouteJobLog         = "GET /v1/jobs/{id}/log"
)

// Errors, as the HTTP client maps flowd's error codes; test with errors.Is.
var (
	ErrNotFound     = errors.New("flowd: not found")
	ErrProtocol     = errors.New("flowd: protocol version outside this build's range")
	ErrUnauthorized = errors.New("flowd: token refused")
	ErrUnavailable  = errors.New("flowd: not reachable")
)

// ErrorBody is every non-2xx response body; Code is one of the Code* constants.
type ErrorBody struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Error codes flowd answers with.
const (
	CodeNotFound     = "not_found"
	CodeProtocol     = "protocol"
	CodeUnauthorized = "unauthorized"
	CodeInvalid      = "invalid"
	CodeBusy         = "busy"
	CodeInternal     = "internal"
)

// UnitState is a systemd unit's state as flowd sees it.
type UnitState string

// UnitState values.
const (
	UnitActive     UnitState = "active"
	UnitActivating UnitState = "activating"
	UnitInactive   UnitState = "inactive"
	UnitFailed     UnitState = "failed"
	UnitMissing    UnitState = "missing"
)

// Hello is flowd's self-description, the first call after every (re)connect.
type Hello struct {
	Protocol    int    `json:"protocol"`
	MinProtocol int    `json:"minProtocol"`
	Version     string `json:"version"`
	Sandbox     string `json:"sandbox"`
	Arch        string `json:"arch"`
	// BootID changes on every sandbox boot: a change tells Flow that every unit is gone.
	BootID string `json:"bootId"`
	// EnvRevision is the last Env applied; Flow pushes again when its own is newer.
	EnvRevision int64 `json:"envRevision"`
	// ClaudeSignedIn: the home volume holds a Claude login. flowd checks the file's presence, never reads it.
	ClaudeSignedIn bool            `json:"claudeSignedIn"`
	Sessions       []SessionState  `json:"sessions"`
	Terminals      []TerminalState `json:"terminals"`
}

// Env is everything sandbox-wide that flowd delivers to git, gh and the agents: the one place credentials enter a
// sandbox. A push replaces the whole set; flowd ignores a Revision not above the one it has. Nothing here that
// rotates reaches a process environment: the GitHub token is served to git by the credential helper on every use
// and written to gh's hosts.yml (layout.GHHosts) atomically on every push.
type Env struct {
	Revision int64  `json:"revision"`
	GitName  string `json:"gitName"`
	GitEmail string `json:"gitEmail"`
	// GitHubToken is the GitHub App user token (8 hours). Empty: not connected, or the refresh token is no longer
	// valid; git and gh then fail with GitHubState's reason instead of hanging.
	GitHubToken          string    `json:"githubToken,omitempty"`
	GitHubTokenExpiresAt time.Time `json:"githubTokenExpiresAt,omitzero"`
	// GitHubState is connected, expired (Flow could not renew in time) or disconnected (reconnect needed).
	GitHubState string `json:"githubState"`
	// ClaudeOAuthToken reaches only the hosts of sessions with ClaudeAuth subscription_token, as
	// CLAUDE_CODE_OAUTH_TOKEN; AnthropicAPIKey only those with api_key, as ANTHROPIC_API_KEY. A subscription_login
	// session gets neither. Claude Code prefers an API key over a token over a stored login, so setting more than
	// the chosen one would silently switch who pays.
	ClaudeOAuthToken string `json:"claudeOAuthToken,omitempty"`
	AnthropicAPIKey  string `json:"anthropicApiKey,omitempty"`
	// ProviderKeys maps OpenCode provider environment variable names (OPENROUTER_API_KEY, ...) to values, for the
	// opencode serve unit.
	ProviderKeys map[string]string `json:"providerKeys,omitempty"`
	// ConfigRepo is owner/name of the agent config repository flowd syncs into the home volume.
	ConfigRepo string `json:"configRepo,omitempty"`
}

// GitHub states in Env.
const (
	GitHubConnected    = "connected"
	GitHubExpired      = "expired"
	GitHubDisconnected = "disconnected"
)

// RepoSpec asks for a main checkout under ~/work/<name>, cloned or fetched.
type RepoSpec struct {
	Repo          string `json:"repo"` // owner/name
	DefaultBranch string `json:"defaultBranch"`
}

// RepoState is a main checkout after EnsureRepo.
type RepoState struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	HeadSHA string `json:"headSha"`
	// SetupScript: the repository has .flow/setup.sh.
	SetupScript bool `json:"setupScript"`
}

// Limits bound one unit (MemoryMax, CPUQuota; MemorySwapMax=0 always).
type Limits struct {
	MemoryMaxBytes  int64 `json:"memoryMaxBytes"`
	CPUQuotaPercent int   `json:"cpuQuotaPercent"`
}

// WorktreeSpec asks for a worktree of Repo on Branch, cut from BaseRef (origin/<default branch>) after a fetch.
type WorktreeSpec struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	BaseRef string `json:"baseRef"`
}

// WorktreeState is a created worktree.
type WorktreeState struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	BaseRef string `json:"baseRef"`
	BaseSHA string `json:"baseSha"`
}

// SessionSpec asks flowd to make sure a session's host unit runs. Idempotent: a running host with the same ID is
// left as it is (settings changes go as commands); a stopped one is started with resume.
type SessionSpec struct {
	ID                string               `json:"id"`
	Name              string               `json:"name"` // directory under ~/work/.sessions
	Provider          event.Provider       `json:"provider"`
	ClaudeAuth        event.ClaudeAuth     `json:"claudeAuth,omitempty"`
	Model             string               `json:"model"`
	Mode              event.PermissionMode `json:"mode"`
	ModeCap           event.PermissionMode `json:"modeCap,omitempty"`
	ProviderSessionID string               `json:"providerSessionId,omitempty"`
	Worktrees         []WorktreeSpec       `json:"worktrees"`
	Limits            Limits               `json:"limits"`
	// ParentID is set for a started session; flowd passes it to the MCP server's session table.
	ParentID string `json:"parentId,omitempty"`
}

// SessionState is a session's host as flowd sees it.
type SessionState struct {
	ID           string              `json:"id"`
	Unit         UnitState           `json:"unit"`
	HostVersion  string              `json:"hostVersion,omitempty"`
	HostProtocol int                 `json:"hostProtocol,omitempty"`
	LastSeq      int64               `json:"lastSeq"`
	Status       event.SessionStatus `json:"status"`
	Dir          string              `json:"dir"`
	Worktrees    []WorktreeState     `json:"worktrees"`
}

// CommandResult acknowledges a command once the host has accepted it (not when its effect is done).
type CommandResult struct {
	ID string `json:"id"`
	// Duplicate: the host had this command id already; nothing was applied again.
	Duplicate bool `json:"duplicate,omitempty"`
}

// ControlKind is a request flowd raises for Flow to decide: the child-session tools and the gate queue.
type ControlKind string

// ControlKind values.
const (
	ControlChildCreate ControlKind = "child.create"
	ControlChildSend   ControlKind = "child.send"
	ControlChildStatus ControlKind = "child.status"
	ControlChildList   ControlKind = "child.list"
	ControlChildWait   ControlKind = "child.wait"
	ControlGateAcquire ControlKind = "gate.acquire"
	ControlGateRelease ControlKind = "gate.release"
)

// ControlRequest is one item of the control stream. flowd keeps unanswered requests and sends them again to every
// new control stream until replied to; Flow treats ID as an idempotency key.
type ControlRequest struct {
	ID string `json:"id"`
	// Session is the calling session as flowd identified it (MCP token, gate shim's session directory). Flow checks
	// it belongs to this sandbox's project and applies the child-session rules itself.
	Session string          `json:"session"`
	Kind    ControlKind     `json:"kind"`
	Data    json.RawMessage `json:"data"`
	Created time.Time       `json:"created"`
}

// ControlReply answers a ControlRequest; Error is an apitypes.ErrorCode when refused.
type ControlReply struct {
	ID    string          `json:"id"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// ChildCreate is the data of child.create: the create_session tool's input.
type ChildCreate struct {
	Title    string               `json:"title"`
	Prompt   string               `json:"prompt"`
	Repos    []string             `json:"repos"`
	Provider event.Provider       `json:"provider,omitempty"`
	Model    string               `json:"model,omitempty"`
	Mode     event.PermissionMode `json:"mode,omitempty"`
}

// ChildSend is the data of child.send.
type ChildSend struct {
	Session string `json:"session"`
	Text    string `json:"text"`
}

// ChildWait is the data of child.wait; Flow answers when one (Any) or all of the sessions are done, failed or need
// the owner, or after TimeoutSeconds (at most 600).
type ChildWait struct {
	Sessions       []string `json:"sessions"`
	Any            bool     `json:"any,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

// ChildRef is the data of child.status.
type ChildRef struct {
	Session string `json:"session"`
}

// ChildInfo is what child.create, child.status, child.list and child.wait answer, one per session.
type ChildInfo struct {
	Session  string               `json:"session"`
	Title    string               `json:"title"`
	Repos    []string             `json:"repos"`
	Mode     event.PermissionMode `json:"mode"`
	Status   event.SessionStatus  `json:"status"`
	LastText string               `json:"lastText,omitempty"`
}

// GateAcquire is the data of gate.acquire, raised by the make shim; Flow replies once the run may start, and sends
// gate.update commands to the session while it waits.
type GateAcquire struct {
	Repo string `json:"repo"`
	Dir  string `json:"dir"`
}

// GateRelease is the data of gate.release.
type GateRelease struct {
	ExitCode   int   `json:"exitCode"`
	DurationMs int64 `json:"durationMs"`
}

// JobKind is a long sandbox-wide task flowd runs as its own unit (layout.JobUnit), logging to layout.JobLog: it
// outlives flowd restarts like every other unit, and runs for minutes, which rules out sandbox.Runtime.Exec.
type JobKind string

// JobKind values.
const (
	// JobSetup runs a repository's .flow/setup.sh as the agent user.
	JobSetup JobKind = "setup"
	// JobConfigSync clones or pulls the owner's agent config repository into the home volume.
	JobConfigSync JobKind = "config_sync"
)

// JobSpec starts a job; idempotent by ID (a running or finished job with the ID is reported, not started again).
type JobSpec struct {
	ID   string  `json:"id"`
	Kind JobKind `json:"kind"`
	// Repo is owner/name for JobSetup, the config repository for JobConfigSync.
	Repo string `json:"repo"`
}

// JobState is a job as flowd sees it.
type JobState struct {
	ID       string    `json:"id"`
	Kind     JobKind   `json:"kind"`
	Unit     UnitState `json:"unit"`
	Done     bool      `json:"done"`
	ExitCode int       `json:"exitCode"`
	// Lines is the number of log lines so far.
	Lines int `json:"lines"`
	// Commit and Files report a finished config sync.
	Commit string `json:"commit,omitempty"`
	Files  int    `json:"files,omitempty"`
}

// LogLine is one line of a job's output, numbered from 1.
type LogLine struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// TerminalSpec asks for a terminal unit; idempotent by ID.
type TerminalSpec struct {
	ID      string `json:"id"`
	Session string `json:"session,omitempty"`
	Dir     string `json:"dir"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// TerminalState is a terminal unit as flowd sees it.
type TerminalState struct {
	ID      string    `json:"id"`
	Session string    `json:"session,omitempty"`
	Unit    UnitState `json:"unit"`
	Dir     string    `json:"dir"`
}

// TerminalConn is an attached terminal: bytes both ways, plus resizes. The scrollback arrives first.
type TerminalConn interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// API is the protocol, one method per route.
type API interface {
	Hello(ctx context.Context) (Hello, error)
	PutEnv(ctx context.Context, e Env) error
	EnsureRepo(ctx context.Context, r RepoSpec) (RepoState, error)
	EnsureSession(ctx context.Context, s SessionSpec) (SessionState, error)
	Session(ctx context.Context, id string) (SessionState, error)
	// StopSession stops the host unit; remove also deletes the worktrees (the log stays).
	StopSession(ctx context.Context, id string, remove bool) error
	AddWorktree(ctx context.Context, sessionID string, w WorktreeSpec) (WorktreeState, error)
	Command(ctx context.Context, c event.Command) (CommandResult, error)
	// Events yields the durable events after seq after, then follows live (ephemeral ones included) until ctx
	// ends or the stream fails; a failure is the last item.
	Events(ctx context.Context, sessionID string, after int64) iter.Seq2[event.Event, error]
	Diff(ctx context.Context, sessionID, repo string) (apitypes.Diff, error)
	// Control yields flowd's pending requests, then new ones, until ctx ends.
	Control(ctx context.Context) iter.Seq2[ControlRequest, error]
	ReplyControl(ctx context.Context, r ControlReply) error
	StartJob(ctx context.Context, j JobSpec) (JobState, error)
	Job(ctx context.Context, id string) (JobState, error)
	// JobLog returns the job's log lines after line after.
	JobLog(ctx context.Context, id string, after int) ([]LogLine, error)
	EnsureTerminal(ctx context.Context, t TerminalSpec) (TerminalState, error)
	AttachTerminal(ctx context.Context, id string) (TerminalConn, error)
	CloseTerminal(ctx context.Context, id string) error
	// Port forwards HTTP and WebSocket requests to the port inside the sandbox (localhost there), for previews.
	Port(port int) http.Handler
}

// Endpoint is where and how Flow reaches one sandbox's flowd.
type Endpoint struct {
	Sandbox string
	// Address is host:port on the bridge.
	Address string
	Token   string
}

// Dialer gives Flow an API for a sandbox. Implementations are cheap to call; connections are pooled inside.
type Dialer interface {
	Dial(e Endpoint) API
}

// Compatible reports whether a flowd's Hello falls in this build's protocol range.
func Compatible(h Hello) bool {
	return h.Protocol >= MinProtocol && h.MinProtocol <= Protocol
}
