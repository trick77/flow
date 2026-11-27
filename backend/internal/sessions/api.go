package sessions

import (
	"context"
	"errors"
	"net/http"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// API implements httpapi.Sessions: list, create, get, update, mark seen, archive and the agent catalog (the rest
// answer not_implemented).
type API struct {
	httpapi.NotImplemented
	Service *Service
	// Keys tells the catalog which Claude credential modes are stored; nil lists none.
	Keys Keys
	// ClaudeSignedIn tells the catalog whether the home volume holds a Claude login (sandboxes.Manager, from the
	// last flowd Hello); nil: never listed.
	ClaudeSignedIn func(context.Context) bool
}

// ListSessions implements httpapi.Sessions.
func (a *API) ListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := a.Service.Store.List(r.Context(), ListFilter{ProjectID: r.URL.Query().Get("projectId")})
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	out := apitypes.SessionList{Items: []apitypes.SessionSummary{}}
	for _, se := range list {
		sum, err := a.Service.Summary(r.Context(), se)
		if err != nil {
			httpapi.WriteError(w, err)
			return
		}
		out.Items = append(out.Items, sum)
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// GetSession implements httpapi.Sessions.
func (a *API) GetSession(w http.ResponseWriter, r *http.Request) {
	se, err := a.Service.Store.Get(r.Context(), r.PathValue("sessionId"))
	if errors.Is(err, ErrNotFound) {
		err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	out, err := a.Service.Describe(r.Context(), se)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// CreateSession implements httpapi.Sessions.
func (a *API) CreateSession(w http.ResponseWriter, r *http.Request) {
	var in apitypes.SessionCreate
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	out, err := a.Service.Create(r.Context(), in)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	a.Service.Publish(r.Context(), out.Summary.ID)
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

// UpdateSession implements httpapi.Sessions.
func (a *API) UpdateSession(w http.ResponseWriter, r *http.Request) {
	var in apitypes.SessionUpdate
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	out, err := a.Service.Update(r.Context(), r.PathValue("sessionId"), in)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// MarkSessionSeen implements httpapi.Sessions.
func (a *API) MarkSessionSeen(w http.ResponseWriter, r *http.Request) {
	var in apitypes.SeenMark
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if err := a.Service.MarkSeen(r.Context(), r.PathValue("sessionId"), in.Seq); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ArchiveSession implements httpapi.Sessions.
func (a *API) ArchiveSession(w http.ResponseWriter, r *http.Request) {
	if err := a.Service.Archive(r.Context(), r.PathValue("sessionId")); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
