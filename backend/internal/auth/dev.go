package auth

import (
	"net"
	"net/http"
	"strings"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// Dev is the dev mode: every request from the machine is the owner's. A loopback listener does not keep browsers
// out (any page open on the machine can send to 127.0.0.1, and DNS rebinding lets it read too), so the guard admits
// only a loopback Host and refuses requests whose Origin is another one (sameOrigin; a client sending neither
// Origin nor Referer, curl, may write).
type Dev struct{ httpapi.NotImplemented }

// Require implements httpapi.Guard.
func (Dev) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) || !sameOrigin(r, hostOrigin(r.Host), true) {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeForbidden, ""))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether a Host header names this machine's loopback: a rebound name does not.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// GetAuthSession implements httpapi.Auth.
func (Dev) GetAuthSession(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, apitypes.AuthSession{SignedIn: true, Mode: apitypes.AuthModeDev, Name: "dev"})
}

// AuthLogin implements httpapi.Auth: signed in already, back to the app.
func (Dev) AuthLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusFound)
}

// AuthLogout implements httpapi.Auth: there is no session to clear; the UI shows its sign-in screen, and signing in
// comes straight back.
func (Dev) AuthLogout(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
