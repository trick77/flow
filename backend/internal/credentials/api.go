package credentials

import (
	"errors"
	"net/http"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// API implements httpapi.Connections: the API keys here (the Claude subscription token, the Anthropic API key and
// OpenCode provider keys), the GitHub App routes through GitHub (package github). Without GitHub the GitHub routes
// answer not_implemented and the connection is reported unavailable. The Claude sign-in is reported as not signed in
// (nothing checks the home volume's login yet).
type API struct {
	httpapi.NotImplemented
	Service *Service
	GitHub  GitHub
}

var _ httpapi.Connections = (*API)(nil)

// GetConnections implements httpapi.Connections: which keys are stored and since when, never a value.
func (a *API) GetConnections(w http.ResponseWriter, r *http.Request) {
	keys, err := a.Service.Keys(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	gh := apitypes.GitHubConnection{State: apitypes.GitHubStateDisconnected}
	if a.GitHub != nil {
		gh = a.GitHub.Connection(r.Context())
	}
	httpapi.WriteJSON(w, http.StatusOK, apitypes.Connections{
		Github:  gh,
		Claude:  apitypes.ClaudeSignIn{SignedIn: false},
		APIKeys: keys})
}

// StartGitHubDeviceFlow implements httpapi.Connections.
func (a *API) StartGitHubDeviceFlow(w http.ResponseWriter, r *http.Request) {
	if a.GitHub == nil {
		a.NotImplemented.StartGitHubDeviceFlow(w, r)
		return
	}
	a.GitHub.StartDeviceFlow(w, r)
}

// PollGitHubDeviceFlow implements httpapi.Connections.
func (a *API) PollGitHubDeviceFlow(w http.ResponseWriter, r *http.Request) {
	if a.GitHub == nil {
		a.NotImplemented.PollGitHubDeviceFlow(w, r)
		return
	}
	a.GitHub.PollDeviceFlow(w, r)
}

// DisconnectGitHub implements httpapi.Connections.
func (a *API) DisconnectGitHub(w http.ResponseWriter, r *http.Request) {
	if a.GitHub == nil {
		a.NotImplemented.DisconnectGitHub(w, r)
		return
	}
	a.GitHub.Disconnect(w, r)
}

// ListGitHubRepos implements httpapi.Connections.
func (a *API) ListGitHubRepos(w http.ResponseWriter, r *http.Request) {
	if a.GitHub == nil {
		a.NotImplemented.ListGitHubRepos(w, r)
		return
	}
	a.GitHub.ListRepos(w, r)
}

// CreateAPIKey implements httpapi.Connections: store or replace.
func (a *API) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var in apitypes.ApiKeyCreate
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	k, err := a.Service.Set(r.Context(), in)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, k)
}

// DeleteAPIKey implements httpapi.Connections.
func (a *API) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	err := a.Service.Delete(r.Context(), r.PathValue("keyId"))
	if errors.Is(err, ErrNotFound) {
		err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
