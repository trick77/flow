package httpapi

import "net/http"

// One interface per x-flow-area of api/openapi.json, one method per operation, named after its operationId (Go's
// initialisms: createApiKey is CreateAPIKey). An area
// implementer writes a type in its own package (internal/httpapi/<area>api) that implements its interface and
// reads path values with r.PathValue; cmd/flow passes it in Areas. Nobody edits routes.go for that.

// Auth is the auth area: sign-in through kit/auth.
type Auth interface {
	GetAuthSession(w http.ResponseWriter, r *http.Request)
	AuthLogin(w http.ResponseWriter, r *http.Request)
	AuthCallback(w http.ResponseWriter, r *http.Request)
	AuthLogout(w http.ResponseWriter, r *http.Request)
}

// Profile is the profile area.
type Profile interface {
	GetProfile(w http.ResponseWriter, r *http.Request)
	UpdateProfile(w http.ResponseWriter, r *http.Request)
	SyncConfigRepo(w http.ResponseWriter, r *http.Request)
}

// Connections is the connections area: GitHub App, API keys, Claude sign-in state.
type Connections interface {
	GetConnections(w http.ResponseWriter, r *http.Request)
	StartGitHubDeviceFlow(w http.ResponseWriter, r *http.Request)
	PollGitHubDeviceFlow(w http.ResponseWriter, r *http.Request)
	DisconnectGitHub(w http.ResponseWriter, r *http.Request)
	CreateAPIKey(w http.ResponseWriter, r *http.Request)
	DeleteAPIKey(w http.ResponseWriter, r *http.Request)
	ListGitHubRepos(w http.ResponseWriter, r *http.Request)
}

// Projects is the projects area.
type Projects interface {
	ListProjects(w http.ResponseWriter, r *http.Request)
	CreateProject(w http.ResponseWriter, r *http.Request)
	GetProject(w http.ResponseWriter, r *http.Request)
	UpdateProject(w http.ResponseWriter, r *http.Request)
	DeleteProject(w http.ResponseWriter, r *http.Request)
	AddProjectRepo(w http.ResponseWriter, r *http.Request)
	RemoveProjectRepo(w http.ResponseWriter, r *http.Request)
}

// Sandboxes is the sandboxes area.
type Sandboxes interface {
	GetSandbox(w http.ResponseWriter, r *http.Request)
	StartSandbox(w http.ResponseWriter, r *http.Request)
	StopSandbox(w http.ResponseWriter, r *http.Request)
	RebuildSandbox(w http.ResponseWriter, r *http.Request)
	GetSetupLog(w http.ResponseWriter, r *http.Request)
}

// Sessions is the sessions area.
type Sessions interface {
	ListAgents(w http.ResponseWriter, r *http.Request)
	ListSessions(w http.ResponseWriter, r *http.Request)
	CreateSession(w http.ResponseWriter, r *http.Request)
	GetSession(w http.ResponseWriter, r *http.Request)
	UpdateSession(w http.ResponseWriter, r *http.Request)
	ArchiveSession(w http.ResponseWriter, r *http.Request)
	MarkSessionSeen(w http.ResponseWriter, r *http.Request)
	AddSessionRepo(w http.ResponseWriter, r *http.Request)
}

// Timeline is the timeline area: event streams, messages, commands, replies.
type Timeline interface {
	StreamAppEvents(w http.ResponseWriter, r *http.Request)
	StreamSessionEvents(w http.ResponseWriter, r *http.Request)
	ReadSessionLog(w http.ResponseWriter, r *http.Request)
	SendMessage(w http.ResponseWriter, r *http.Request)
	SendQueuedNow(w http.ResponseWriter, r *http.Request)
	RemoveQueued(w http.ResponseWriter, r *http.Request)
	InterruptTurn(w http.ResponseWriter, r *http.Request)
	ReplyPermission(w http.ResponseWriter, r *http.Request)
	ReplyQuestion(w http.ResponseWriter, r *http.Request)
	ReplyPlan(w http.ResponseWriter, r *http.Request)
}

// Review is the review area: diffs and pull requests.
type Review interface {
	GetSessionDiff(w http.ResponseWriter, r *http.Request)
	ListPullRequests(w http.ResponseWriter, r *http.Request)
	RefreshPullRequests(w http.ResponseWriter, r *http.Request)
}

// Terminals is the terminals area.
type Terminals interface {
	CreateTerminal(w http.ResponseWriter, r *http.Request)
	AttachTerminal(w http.ResponseWriter, r *http.Request)
	CloseTerminal(w http.ResponseWriter, r *http.Request)
}

// Previews is the previews area (the API side; the preview hosts are Options.Preview).
type Previews interface {
	ListPreviews(w http.ResponseWriter, r *http.Request)
	OpenPreview(w http.ResponseWriter, r *http.Request)
	CreatePreviewTicket(w http.ResponseWriter, r *http.Request)
}

// Areas holds one implementation per area; a nil one answers not_implemented.
type Areas struct {
	Auth        Auth
	Profile     Profile
	Connections Connections
	Projects    Projects
	Sandboxes   Sandboxes
	Sessions    Sessions
	Timeline    Timeline
	Review      Review
	Terminals   Terminals
	Previews    Previews
}

// Guard admits the owner: it wraps every route the spec does not mark public (security: []). The auth area
// implements it with kit/auth's session cookie.
type Guard interface {
	Require(next http.Handler) http.Handler
}

// NotImplemented answers every operation with 501 and code not_implemented; it is every area's default, and the
// default Guard, so an unwired server fails closed.
type NotImplemented struct{}

func (NotImplemented) answer(w http.ResponseWriter) { WriteError(w, errNotImplemented) }

// Require implements Guard: nothing passes.
func (n NotImplemented) Require(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { n.answer(w) })
}

// GetAuthSession answers not_implemented.
func (n NotImplemented) GetAuthSession(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// AuthLogin answers not_implemented.
func (n NotImplemented) AuthLogin(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// AuthCallback answers not_implemented.
func (n NotImplemented) AuthCallback(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// AuthLogout answers not_implemented.
func (n NotImplemented) AuthLogout(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetProfile answers not_implemented.
func (n NotImplemented) GetProfile(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// UpdateProfile answers not_implemented.
func (n NotImplemented) UpdateProfile(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// SyncConfigRepo answers not_implemented.
func (n NotImplemented) SyncConfigRepo(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetConnections answers not_implemented.
func (n NotImplemented) GetConnections(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// StartGitHubDeviceFlow answers not_implemented.
func (n NotImplemented) StartGitHubDeviceFlow(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// PollGitHubDeviceFlow answers not_implemented.
func (n NotImplemented) PollGitHubDeviceFlow(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// DisconnectGitHub answers not_implemented.
func (n NotImplemented) DisconnectGitHub(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// CreateAPIKey answers not_implemented.
func (n NotImplemented) CreateAPIKey(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// DeleteAPIKey answers not_implemented.
func (n NotImplemented) DeleteAPIKey(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ListGitHubRepos answers not_implemented.
func (n NotImplemented) ListGitHubRepos(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ListProjects answers not_implemented.
func (n NotImplemented) ListProjects(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// CreateProject answers not_implemented.
func (n NotImplemented) CreateProject(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetProject answers not_implemented.
func (n NotImplemented) GetProject(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// UpdateProject answers not_implemented.
func (n NotImplemented) UpdateProject(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// DeleteProject answers not_implemented.
func (n NotImplemented) DeleteProject(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// AddProjectRepo answers not_implemented.
func (n NotImplemented) AddProjectRepo(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// RemoveProjectRepo answers not_implemented.
func (n NotImplemented) RemoveProjectRepo(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetSandbox answers not_implemented.
func (n NotImplemented) GetSandbox(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// StartSandbox answers not_implemented.
func (n NotImplemented) StartSandbox(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// StopSandbox answers not_implemented.
func (n NotImplemented) StopSandbox(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// RebuildSandbox answers not_implemented.
func (n NotImplemented) RebuildSandbox(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetSetupLog answers not_implemented.
func (n NotImplemented) GetSetupLog(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ListAgents answers not_implemented.
func (n NotImplemented) ListAgents(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ListSessions answers not_implemented.
func (n NotImplemented) ListSessions(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// CreateSession answers not_implemented.
func (n NotImplemented) CreateSession(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetSession answers not_implemented.
func (n NotImplemented) GetSession(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// UpdateSession answers not_implemented.
func (n NotImplemented) UpdateSession(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ArchiveSession answers not_implemented.
func (n NotImplemented) ArchiveSession(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// MarkSessionSeen answers not_implemented.
func (n NotImplemented) MarkSessionSeen(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// AddSessionRepo answers not_implemented.
func (n NotImplemented) AddSessionRepo(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// StreamAppEvents answers not_implemented.
func (n NotImplemented) StreamAppEvents(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// StreamSessionEvents answers not_implemented.
func (n NotImplemented) StreamSessionEvents(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ReadSessionLog answers not_implemented.
func (n NotImplemented) ReadSessionLog(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// SendMessage answers not_implemented.
func (n NotImplemented) SendMessage(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// SendQueuedNow answers not_implemented.
func (n NotImplemented) SendQueuedNow(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// RemoveQueued answers not_implemented.
func (n NotImplemented) RemoveQueued(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// InterruptTurn answers not_implemented.
func (n NotImplemented) InterruptTurn(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ReplyPermission answers not_implemented.
func (n NotImplemented) ReplyPermission(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ReplyQuestion answers not_implemented.
func (n NotImplemented) ReplyQuestion(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ReplyPlan answers not_implemented.
func (n NotImplemented) ReplyPlan(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// GetSessionDiff answers not_implemented.
func (n NotImplemented) GetSessionDiff(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ListPullRequests answers not_implemented.
func (n NotImplemented) ListPullRequests(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// RefreshPullRequests answers not_implemented.
func (n NotImplemented) RefreshPullRequests(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// CreateTerminal answers not_implemented.
func (n NotImplemented) CreateTerminal(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// AttachTerminal answers not_implemented.
func (n NotImplemented) AttachTerminal(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// CloseTerminal answers not_implemented.
func (n NotImplemented) CloseTerminal(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// ListPreviews answers not_implemented.
func (n NotImplemented) ListPreviews(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// OpenPreview answers not_implemented.
func (n NotImplemented) OpenPreview(w http.ResponseWriter, _ *http.Request) { n.answer(w) }

// CreatePreviewTicket answers not_implemented.
func (n NotImplemented) CreatePreviewTicket(w http.ResponseWriter, _ *http.Request) { n.answer(w) }
