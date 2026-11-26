// Package auth signs the owner in (kit/auth: OIDC against the configured provider, admitting only the configured
// subject or group; BACKEND_AUTH_MODE=dev on loopback only) and implements httpapi.Guard and httpapi.Auth.
// Owner: auth area (docs/ownership.md). The walking skeleton has dev mode only; config refuses it unless the server
// listens on a loopback address, so whoever reaches it is on the machine.
package auth

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// Dev is the dev mode: every request from the machine is the owner's. A loopback listener does not keep browsers
// out (any page open on the machine can send to 127.0.0.1, and DNS rebinding lets it read too), so the guard admits
// only a loopback Host and refuses state changes whose Origin is another one.
type Dev struct{ httpapi.NotImplemented }

// Require implements httpapi.Guard.
func (Dev) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) || (changes(r.Method) && !sameOrigin(r)) {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeForbidden, ""))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func changes(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
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

// sameOrigin: no Origin (a non-browser client), or one naming exactly the host the request was sent to.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
}

// GetAuthSession implements httpapi.Auth.
func (Dev) GetAuthSession(w http.ResponseWriter, _ *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, apitypes.AuthSession{SignedIn: true, Mode: apitypes.AuthModeDev, Name: "dev"})
}

// AuthLogin implements httpapi.Auth: signed in already, back to the app.
func (Dev) AuthLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusFound)
}
