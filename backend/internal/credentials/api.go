package credentials

import (
	"errors"
	"net/http"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// API implements the API-key part of httpapi.Connections: the Claude subscription token, the Anthropic API key and
// OpenCode provider keys. The GitHub App routes answer not_implemented until that area lands; the GitHub state is
// reported disconnected and the Claude sign-in as not signed in (nothing checks the home volume's login yet).
type API struct {
	httpapi.NotImplemented
	Service *Service
}

var _ httpapi.Connections = (*API)(nil)

// GetConnections implements httpapi.Connections: which keys are stored and since when, never a value.
func (a *API) GetConnections(w http.ResponseWriter, r *http.Request) {
	keys, err := a.Service.Keys(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, apitypes.Connections{
		Github:  apitypes.GitHubConnection{State: apitypes.GitHubStateDisconnected},
		Claude:  apitypes.ClaudeSignIn{SignedIn: false},
		APIKeys: keys})
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
