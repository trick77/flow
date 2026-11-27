package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	kitauth "github.com/trick77/ship/kit/auth"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/config"
	"github.com/trick77/flow/internal/httpapi"
)

// SessionCookie is the session cookie's name. The __Host- prefix makes a browser accept it only Secure, with
// Path=/ and without Domain (kit sets exactly that), so a sibling host under the same parent domain (preview hosts,
// other apps) can neither read it nor plant a same-named cookie for the parent domain that shadows it.
const SessionCookie = "__Host-flow_session"

// transientPrefix names kit's state, nonce and PKCE cookies (__Host-flow_oidc_state, ...): __Host- for the same
// reason, or a planted state cookie would fail every callback.
const transientPrefix = "__Host-flow_"

// CallbackPath is the redirect URI's path on the public URL; the provider's client registration names it.
const CallbackPath = "/api/auth/callback"

// SignInFailed is where a refused callback sends the browser: the app, whose sign-in screen shows one generic
// sentence for the marker. Which check failed is logged only.
const SignInFailed = "/?signin=failed"

// callbackTimeout bounds a callback's calls to the provider (token exchange, key fetch), like discovery at start.
const callbackTimeout = 15 * time.Second

// maxLogText caps text from a sign-in attempt that reaches the log (anyone can start one).
const maxLogText = 200

// OIDC is the oidc mode: kit/auth's code flow and session cookie, admitting only the configured owner.
type OIDC struct {
	flow     *kitauth.OIDC
	sessions *kitauth.Sessions
	// origin is the public URL's scheme://host, the one origin requests are accepted from.
	origin          string
	subject, group  string
	callbackTimeout time.Duration
	log             *slog.Logger
}

// NewOIDC reads the provider's discovery document (under ctx's deadline: an unreachable provider fails startup,
// not the first sign-in) and sets up the session cookie.
func NewOIDC(ctx context.Context, cfg config.OIDC) (*OIDC, error) {
	sessions, err := kitauth.NewSessions(kitauth.SessionConfig{
		Name: SessionCookie,
		Key:  kitauth.DeriveKey(cfg.SessionSecret, "flow-session"),
		TTL:  cfg.SessionTTL,
		// oidc mode is served over TLS only (config builds the public URL as https); __Host- requires it too.
		Secure: true,
		// A long group list is cut to fit the cookie; the admitting group must survive the cut.
		KeepGroup: cfg.Group,
	})
	if err != nil {
		return nil, err
	}
	// groups is no standard scope: a provider may refuse a client that asks for it without being allowed to.
	scopes := []string{"openid", "profile", "email"}
	if cfg.Group != "" {
		scopes = append(scopes, "groups")
	}
	flow, err := kitauth.NewOIDC(ctx, kitauth.OIDCConfig{
		Issuer: cfg.Issuer, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
		RedirectURL: cfg.PublicURL + CallbackPath, Scopes: scopes,
		// No Group: admission is Flow's (subject or group), checked in admits.
		CookiePrefix: transientPrefix, Secure: true,
	})
	if err != nil {
		return nil, err
	}
	return &OIDC{flow: flow, sessions: sessions, origin: strings.ToLower(cfg.PublicURL), subject: cfg.Subject,
		group: cfg.Group, callbackTimeout: callbackTimeout, log: slog.Default()}, nil
}

// admits is the single-owner rule: the configured subject, or a member of the configured group. Checked at the
// callback and again on every request against the cookie's claims, so a changed setting applies to live sessions.
// The groups are those of the sign-in: a removal at the provider reaches Flow only when the session ends
// (BACKEND_SESSION_TTL) or BACKEND_SESSION_SECRET is rotated, which ends every session.
func (o *OIDC) admits(c kitauth.Claims) bool {
	return (o.subject != "" && c.Subject == o.subject) || (o.group != "" && c.HasGroup(o.group))
}

// owner is the request's identity: a valid session cookie of an admitted identity.
func (o *OIDC) owner(r *http.Request) (kitauth.Claims, bool) {
	c, ok := o.sessions.Read(r)
	if !ok || !o.admits(c) {
		return kitauth.Claims{}, false
	}
	return c, true
}

func (o *OIDC) sameOrigin(r *http.Request) bool { return sameOrigin(r, isOrigin(o.origin), false) }

// Require implements httpapi.Guard: same origin (package comment), then a valid session; without one the answer is
// the JSON code unauthenticated, never a redirect, so the UI decides where the browser goes. Sign-out is a public
// route (it needs no valid session: a stale or shadowed cookie must still be clearable) and checks the origin itself.
func (o *OIDC) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !o.sameOrigin(r) {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeForbidden, ""))
			return
		}
		c, ok := o.owner(r)
		if !ok {
			httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeUnauthenticated, ""))
			return
		}
		next.ServeHTTP(w, r.WithContext(kitauth.NewContext(r.Context(), c)))
	})
}

// GetAuthSession implements httpapi.Auth: always 200, signed in or not.
func (o *OIDC) GetAuthSession(w http.ResponseWriter, r *http.Request) {
	c, ok := o.owner(r)
	s := apitypes.AuthSession{SignedIn: ok, Mode: apitypes.AuthModeOIDC}
	if ok {
		s.Name = displayName(c)
	}
	httpapi.WriteJSON(w, http.StatusOK, s)
}

// displayName is the provider's name for the owner, else kit's handle, email or subject.
func displayName(c kitauth.Claims) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Username()
}

// AuthLogin implements httpapi.Auth: to the provider, or straight back when signed in already.
func (o *OIDC) AuthLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := o.owner(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	o.flow.Start(w, r)
}

// AuthCallback implements httpapi.Auth. It is a top-level navigation, so every refusal sends the browser back to
// the app with the generic SignInFailed marker and no session; which check failed (state, nonce, exchange,
// admission) goes to the log, at info: anyone can make a callback fail.
func (o *OIDC) AuthCallback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), o.callbackTimeout)
	defer cancel()
	c, err := o.flow.Callback(w, r.WithContext(ctx))
	switch {
	case errors.Is(err, kitauth.ErrProviderDenied):
		// The provider's error text comes in on the callback URL: whoever sends it chooses it.
		o.log.Info("sign-in failed: the provider answered with an error")
	case err != nil:
		o.log.Info("sign-in failed", "err", capText(err.Error()))
	case !o.admits(c):
		o.log.Info("sign-in refused: not the configured owner", "sub", capText(c.Subject))
	default:
		if err := o.sessions.Issue(w, c); err != nil {
			httpapi.WriteError(w, err)
			return
		}
		o.log.Info("signed in", "sub", c.Subject)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Redirect(w, r, SignInFailed, http.StatusFound)
}

// capText keeps at most the first maxLogText bytes of s, cut on a rune boundary: the log line stays valid UTF-8.
func capText(s string) string {
	if len(s) <= maxLogText {
		return s
	}
	n := maxLogText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// AuthLogout implements httpapi.Auth: the session lives in the cookie alone, so clearing it is the sign-out. It
// clears whatever cookie there is, valid or not. The route is public (outside the Guard), so the origin check here
// is the only one it gets.
func (o *OIDC) AuthLogout(w http.ResponseWriter, r *http.Request) {
	if !o.sameOrigin(r) {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeForbidden, ""))
		return
	}
	o.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}
