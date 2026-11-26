package httpapi

import "net/http"

// Route is one operation of api/openapi.json, registered here and nowhere else. TestRoutesMatchTheSpec keeps the
// table and the spec equal: method, path, operationId, area and whether it skips the Guard.
type Route struct {
	Method      string
	Path        string
	OperationID string
	Area        string
	// Public routes skip the Guard (security: [] in the spec).
	Public  bool
	handler func(a *Areas) http.HandlerFunc
}

// Routes is the registration table, in spec order.
var Routes = []Route{
	{"GET", "/api/auth/session", "getAuthSession", "auth", true, func(a *Areas) http.HandlerFunc { return a.Auth.GetAuthSession }},
	{"GET", "/api/auth/login", "authLogin", "auth", true, func(a *Areas) http.HandlerFunc { return a.Auth.AuthLogin }},
	{"GET", "/api/auth/callback", "authCallback", "auth", true, func(a *Areas) http.HandlerFunc { return a.Auth.AuthCallback }},
	{"POST", "/api/auth/logout", "authLogout", "auth", false, func(a *Areas) http.HandlerFunc { return a.Auth.AuthLogout }},

	{"GET", "/api/profile", "getProfile", "profile", false, func(a *Areas) http.HandlerFunc { return a.Profile.GetProfile }},
	{"PATCH", "/api/profile", "updateProfile", "profile", false, func(a *Areas) http.HandlerFunc { return a.Profile.UpdateProfile }},
	{"POST", "/api/profile/config-sync", "syncConfigRepo", "profile", false, func(a *Areas) http.HandlerFunc { return a.Profile.SyncConfigRepo }},

	{"GET", "/api/connections", "getConnections", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.GetConnections }},
	{"POST", "/api/connections/github/device", "startGitHubDeviceFlow", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.StartGitHubDeviceFlow }},
	{"POST", "/api/connections/github/device/poll", "pollGitHubDeviceFlow", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.PollGitHubDeviceFlow }},
	{"DELETE", "/api/connections/github", "disconnectGitHub", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.DisconnectGitHub }},
	{"POST", "/api/connections/api-keys", "createApiKey", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.CreateAPIKey }},
	{"DELETE", "/api/connections/api-keys/{keyId}", "deleteApiKey", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.DeleteAPIKey }},
	{"GET", "/api/github/repos", "listGitHubRepos", "connections", false, func(a *Areas) http.HandlerFunc { return a.Connections.ListGitHubRepos }},

	{"GET", "/api/projects", "listProjects", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.ListProjects }},
	{"POST", "/api/projects", "createProject", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.CreateProject }},
	{"GET", "/api/projects/{projectId}", "getProject", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.GetProject }},
	{"PATCH", "/api/projects/{projectId}", "updateProject", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.UpdateProject }},
	{"DELETE", "/api/projects/{projectId}", "deleteProject", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.DeleteProject }},
	{"POST", "/api/projects/{projectId}/repos", "addProjectRepo", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.AddProjectRepo }},
	{"DELETE", "/api/projects/{projectId}/repos/{repoId}", "removeProjectRepo", "projects", false, func(a *Areas) http.HandlerFunc { return a.Projects.RemoveProjectRepo }},

	{"GET", "/api/projects/{projectId}/sandbox", "getSandbox", "sandboxes", false, func(a *Areas) http.HandlerFunc { return a.Sandboxes.GetSandbox }},
	{"POST", "/api/projects/{projectId}/sandbox/start", "startSandbox", "sandboxes", false, func(a *Areas) http.HandlerFunc { return a.Sandboxes.StartSandbox }},
	{"POST", "/api/projects/{projectId}/sandbox/stop", "stopSandbox", "sandboxes", false, func(a *Areas) http.HandlerFunc { return a.Sandboxes.StopSandbox }},
	{"POST", "/api/projects/{projectId}/sandbox/rebuild", "rebuildSandbox", "sandboxes", false, func(a *Areas) http.HandlerFunc { return a.Sandboxes.RebuildSandbox }},
	{"GET", "/api/projects/{projectId}/sandbox/setup-log", "getSetupLog", "sandboxes", false, func(a *Areas) http.HandlerFunc { return a.Sandboxes.GetSetupLog }},

	{"GET", "/api/agents", "listAgents", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.ListAgents }},
	{"GET", "/api/sessions", "listSessions", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.ListSessions }},
	{"POST", "/api/sessions", "createSession", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.CreateSession }},
	{"GET", "/api/sessions/{sessionId}", "getSession", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.GetSession }},
	{"PATCH", "/api/sessions/{sessionId}", "updateSession", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.UpdateSession }},
	{"DELETE", "/api/sessions/{sessionId}", "archiveSession", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.ArchiveSession }},
	{"POST", "/api/sessions/{sessionId}/seen", "markSessionSeen", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.MarkSessionSeen }},
	{"POST", "/api/sessions/{sessionId}/repos", "addSessionRepo", "sessions", false, func(a *Areas) http.HandlerFunc { return a.Sessions.AddSessionRepo }},

	{"GET", "/api/events", "streamAppEvents", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.StreamAppEvents }},
	{"GET", "/api/sessions/{sessionId}/events", "streamSessionEvents", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.StreamSessionEvents }},
	{"GET", "/api/sessions/{sessionId}/log", "readSessionLog", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.ReadSessionLog }},
	{"POST", "/api/sessions/{sessionId}/messages", "sendMessage", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.SendMessage }},
	{"POST", "/api/sessions/{sessionId}/queue/{messageId}/send-now", "sendQueuedNow", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.SendQueuedNow }},
	{"DELETE", "/api/sessions/{sessionId}/queue/{messageId}", "removeQueued", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.RemoveQueued }},
	{"POST", "/api/sessions/{sessionId}/interrupt", "interruptTurn", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.InterruptTurn }},
	{"POST", "/api/sessions/{sessionId}/permissions/{requestId}", "replyPermission", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.ReplyPermission }},
	{"POST", "/api/sessions/{sessionId}/questions/{requestId}", "replyQuestion", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.ReplyQuestion }},
	{"POST", "/api/sessions/{sessionId}/plans/{requestId}", "replyPlan", "timeline", false, func(a *Areas) http.HandlerFunc { return a.Timeline.ReplyPlan }},

	{"GET", "/api/sessions/{sessionId}/repos/{repoId}/diff", "getSessionDiff", "review", false, func(a *Areas) http.HandlerFunc { return a.Review.GetSessionDiff }},
	{"GET", "/api/sessions/{sessionId}/pull-requests", "listPullRequests", "review", false, func(a *Areas) http.HandlerFunc { return a.Review.ListPullRequests }},
	{"POST", "/api/sessions/{sessionId}/pull-requests/refresh", "refreshPullRequests", "review", false, func(a *Areas) http.HandlerFunc { return a.Review.RefreshPullRequests }},

	{"POST", "/api/terminals", "createTerminal", "terminals", false, func(a *Areas) http.HandlerFunc { return a.Terminals.CreateTerminal }},
	{"GET", "/api/terminals/{terminalId}/ws", "attachTerminal", "terminals", false, func(a *Areas) http.HandlerFunc { return a.Terminals.AttachTerminal }},
	{"DELETE", "/api/terminals/{terminalId}", "closeTerminal", "terminals", false, func(a *Areas) http.HandlerFunc { return a.Terminals.CloseTerminal }},

	{"GET", "/api/sessions/{sessionId}/previews", "listPreviews", "previews", false, func(a *Areas) http.HandlerFunc { return a.Previews.ListPreviews }},
	{"POST", "/api/sessions/{sessionId}/previews", "openPreview", "previews", false, func(a *Areas) http.HandlerFunc { return a.Previews.OpenPreview }},
	{"POST", "/api/sessions/{sessionId}/previews/{port}/ticket", "createPreviewTicket", "previews", false, func(a *Areas) http.HandlerFunc { return a.Previews.CreatePreviewTicket }},
}

// withDefaults fills every nil area with NotImplemented.
func withDefaults(a Areas) *Areas {
	var n NotImplemented
	if a.Auth == nil {
		a.Auth = n
	}
	if a.Profile == nil {
		a.Profile = n
	}
	if a.Connections == nil {
		a.Connections = n
	}
	if a.Projects == nil {
		a.Projects = n
	}
	if a.Sandboxes == nil {
		a.Sandboxes = n
	}
	if a.Sessions == nil {
		a.Sessions = n
	}
	if a.Timeline == nil {
		a.Timeline = n
	}
	if a.Review == nil {
		a.Review = n
	}
	if a.Terminals == nil {
		a.Terminals = n
	}
	if a.Previews == nil {
		a.Previews = n
	}
	return &a
}

// register mounts every route on mux; non-public ones behind guard.
func register(mux *http.ServeMux, a *Areas, guard Guard) {
	for _, r := range Routes {
		var h http.Handler = r.handler(a)
		if !r.Public {
			h = guard.Require(h)
		}
		mux.Handle(r.Method+" "+r.Path, h)
	}
}
