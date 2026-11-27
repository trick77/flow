package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	kitauth "github.com/trick77/ship/kit/auth"

	"github.com/trick77/flow/internal/config"
	"github.com/trick77/flow/internal/httpapi"
)

const (
	publicURL = "https://flow.example.com"
	secret    = "0123456789abcdef0123456789abcdef"
)

// timeline stands in for an area behind the Guard: it answers 200 with the identity the Guard put on the context.
type timeline struct{ httpapi.NotImplemented }

func (timeline) StreamAppEvents(w http.ResponseWriter, r *http.Request) {
	c, _ := kitauth.FromContext(r.Context())
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"sub": c.Subject})
}

type terminals struct{ httpapi.NotImplemented }

func (terminals) AttachTerminal(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

type harness struct {
	t        *testing.T
	provider *fakeProvider
	oidc     *OIDC
	handler  http.Handler
}

// newHarness is Flow's real handler (httpapi.New: routes, Guard, preview rule) in oidc mode against a fake provider.
func newHarness(t *testing.T, subject, group string) *harness {
	t.Helper()
	p := newFakeProvider(t)
	o, err := NewOIDC(t.Context(), config.OIDC{PublicURL: publicURL, Issuer: p.srv.URL, ClientID: p.clientID,
		ClientSecret: p.clientSecret, Subject: subject, Group: group, SessionSecret: secret, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(httpapi.Options{Static: http.NotFoundHandler(), Guard: o,
		Areas:         httpapi.Areas{Auth: o, Timeline: timeline{}, Terminals: terminals{}},
		IsPreviewHost: func(host string) bool { return strings.HasSuffix(host, ".flow.example.com") }})
	return &harness{t: t, provider: p, oidc: o, handler: h}
}

// do sends one request; header pairs are name, value.
func (h *harness) do(method, path string, cookies []*http.Cookie, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://flow.example.com"+path, nil)
	for _, c := range cookies {
		r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

// login starts a sign-in and plays the provider: the callback URL it sends the browser back to, and the transient
// cookies the browser holds for it.
func (h *harness) login() (callback string, transient []*http.Cookie) {
	h.t.Helper()
	w := h.do("GET", "/api/auth/login", nil)
	if w.Code != http.StatusFound {
		h.t.Fatalf("login %d %s", w.Code, w.Body)
	}
	code, state := h.provider.authorize(h.t, w.Header().Get("Location"))
	return CallbackPath + "?" + url.Values{"code": {code}, "state": {state}}.Encode(), w.Result().Cookies()
}

// signIn runs the whole flow and returns the session cookie.
func (h *harness) signIn() *http.Cookie {
	h.t.Helper()
	callback, transient := h.login()
	w := h.do("GET", callback, transient)
	c := cookie(w, SessionCookie)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" || c == nil || c.Value == "" {
		h.t.Fatalf("callback %d %s %v", w.Code, w.Body, w.Result().Cookies())
	}
	return c
}

func cookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// errorCode is the code of a JSON error answer; it fails the test on anything else (a redirect, HTML).
func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Code string }
	if w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("not a JSON error: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if w.Header().Get("Location") != "" {
		t.Errorf("error answer redirects to %s", w.Header().Get("Location"))
	}
	return body.Code
}

func session(t *testing.T, h *harness, cookies ...*http.Cookie) (signedIn bool, mode, name string) {
	t.Helper()
	w := h.do("GET", "/api/auth/session", cookies)
	var s struct {
		SignedIn bool   `json:"signedIn"`
		Mode     string `json:"mode"`
		Name     string `json:"name"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatalf("session %d %s", w.Code, w.Body)
	}
	return s.SignedIn, s.Mode, s.Name
}

func TestOIDCSignIn(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	if in, mode, _ := session(t, h); in || mode != "oidc" {
		t.Errorf("before sign-in: signedIn %v mode %q", in, mode)
	}

	w := h.do("GET", "/api/auth/login", nil)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if !strings.HasPrefix(loc.String(), h.provider.srv.URL+"/authorize") ||
		loc.Query().Get("redirect_uri") != publicURL+"/api/auth/callback" ||
		loc.Query().Get("scope") != "openid profile email" {
		t.Errorf("authorization request %s", loc)
	}

	c := h.signIn()
	// Host-only (no Domain: never sent to preview hosts), Secure, HttpOnly, Lax, the configured lifetime.
	if c.Domain != "" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		time.Until(c.Expires) > time.Hour || time.Until(c.Expires) < 59*time.Minute {
		t.Errorf("session cookie %+v", c)
	}
	if in, mode, name := session(t, h, c); !in || mode != "oidc" || name != "Owner Name" {
		t.Errorf("signed in: %v %q %q", in, mode, name)
	}
	w = h.do("GET", "/api/events", []*http.Cookie{c})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sub":"owner-sub"`) {
		t.Errorf("guarded route %d %s", w.Code, w.Body)
	}
	// Signed in already: login goes straight back to the app.
	if w := h.do("GET", "/api/auth/login", []*http.Cookie{c}); w.Code != http.StatusFound ||
		w.Header().Get("Location") != "/" {
		t.Errorf("login while signed in %d %s", w.Code, w.Header().Get("Location"))
	}
}

// A sibling host under the parent domain (a preview, another app) can set a cookie for the whole domain. Under a
// plain name it would shadow Flow's (the first of a name wins): every request 401, every callback failing. A
// browser accepts a __Host- cookie only Secure, Path=/ and without Domain, so no sibling can plant one.
func TestOIDCCookiesAreHostPrefixed(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	hostOnly := func(c *http.Cookie) {
		t.Helper()
		if !strings.HasPrefix(c.Name, "__Host-") || !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Errorf("cookie %s: not a valid __Host- cookie: %+v", c.Name, c)
		}
	}
	callback, transient := h.login()
	if len(transient) != 3 {
		t.Fatalf("transient cookies %v", transient)
	}
	for _, c := range transient {
		hostOnly(c)
	}
	w := h.do("GET", callback, transient)
	for _, c := range w.Result().Cookies() {
		hostOnly(c)
	}
	hostOnly(cookie(w, SessionCookie))
	logout := h.do("POST", "/api/auth/logout", nil, "Origin", publicURL)
	hostOnly(cookie(logout, SessionCookie))
}

func TestOIDCDisplayNameFallsBack(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	h.provider.signInAs(map[string]any{"sub": "owner-sub", "preferred_username": "owner"})
	if _, _, name := session(t, h, h.signIn()); name != "owner" {
		t.Errorf("name %q", name)
	}
}

// Every refusal at the callback sends the browser back to the app with the same generic marker and no session: it
// learns nothing about which check failed, and it is not left on a raw error page.
func refused(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/?signin=failed" {
		t.Errorf("callback %d %q %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	if c := cookie(w, SessionCookie); c != nil {
		t.Errorf("refused callback set a session %+v", c)
	}
}

func TestOIDCCallbackWrongState(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	callback, transient := h.login()
	u, _ := url.Parse(callback)
	q := u.Query()
	q.Set("state", "forged")
	refused(t, h.do("GET", CallbackPath+"?"+q.Encode(), transient))
	// No state cookie at all (a callback opened in another browser).
	callback, _ = h.login()
	refused(t, h.do("GET", callback, nil))
}

func TestOIDCCallbackReplay(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	callback, transient := h.login()
	w := h.do("GET", callback, transient)
	if w.Code != http.StatusFound {
		t.Fatalf("first callback %d", w.Code)
	}
	// The callback cleared its transient cookies, so the browser replays without them...
	for _, c := range transient {
		if cl := cookie(w, c.Name); cl == nil || cl.MaxAge >= 0 {
			t.Errorf("transient cookie %s not cleared: %+v", c.Name, cl)
		}
	}
	refused(t, h.do("GET", callback, nil))
	// ...and a replay with captured cookies fails at the provider: the code is single-use.
	refused(t, h.do("GET", callback, transient))
}

// captureLog points the harness's logger at a buffer, every level included.
func captureLog(h *harness) *bytes.Buffer {
	var buf bytes.Buffer
	h.oidc.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &buf
}

// The callback is public: whoever calls it chooses the provider error text and makes it fail. Nothing of that text
// reaches the log, and no attempt logs above info.
func TestOIDCCallbackLogging(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	log := captureLog(h)
	_, transient := h.login()
	planted := "FORGED-LOG-LINE " + strings.Repeat("x", 500)
	refused(t, h.do("GET", CallbackPath+"?"+url.Values{"error": {planted}}.Encode(), transient))
	refused(t, h.do("GET", CallbackPath+"?state=nope", transient))
	h.provider.signInAs(map[string]any{"sub": "someone-else-" + strings.Repeat("y", 500)})
	callback, transient := h.login()
	refused(t, h.do("GET", callback, transient))

	out := log.String()
	if strings.Contains(out, "FORGED") || strings.Contains(out, strings.Repeat("y", maxLogText)) {
		t.Errorf("attacker text in the log:\n%s", out)
	}
	if strings.Contains(out, "level=WARN") || strings.Contains(out, "level=ERROR") {
		t.Errorf("a failed sign-in logged above info:\n%s", out)
	}
	for _, want := range []string{"provider answered with an error", "invalid oidc state", "sub=someone-else-"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// A provider that does not answer the token request holds the callback no longer than the timeout.
func TestOIDCCallbackTimeout(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	h.oidc.callbackTimeout = 100 * time.Millisecond
	h.provider.mu.Lock()
	h.provider.delay = 5 * time.Second
	h.provider.mu.Unlock()
	callback, transient := h.login()
	start := time.Now()
	refused(t, h.do("GET", callback, transient))
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("callback took %s", d)
	}
}

func TestOIDCNotAdmittedBySubject(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	// Authenticated at the provider, in a group even, but not the configured subject.
	h.provider.signInAs(map[string]any{"sub": "someone-else", "name": "Someone", "groups": []string{"flow-owner"}})
	callback, transient := h.login()
	refused(t, h.do("GET", callback, transient))
}

func TestOIDCAdmittedByGroup(t *testing.T) {
	h := newHarness(t, "", "flow-owner")
	w := h.do("GET", "/api/auth/login", nil)
	if loc, _ := url.Parse(w.Header().Get("Location")); loc.Query().Get("scope") != "openid profile email groups" {
		t.Errorf("scope %q", loc.Query().Get("scope"))
	}
	// Case differs from the setting (IdPs disagree), and a long list is cut to fit the cookie: the group survives.
	groups := []string{}
	for i := range 20 {
		groups = append(groups, "g"+string(rune('a'+i)))
	}
	h.provider.signInAs(map[string]any{"sub": "any-sub", "name": "Member", "groups": append(groups, "Flow-Owner")})
	c := h.signIn()
	if w := h.do("GET", "/api/events", []*http.Cookie{c}); w.Code != http.StatusOK {
		t.Errorf("group member refused %d %s", w.Code, w.Body)
	}

	h.provider.signInAs(map[string]any{"sub": "owner-sub", "groups": []string{"other"}})
	callback, transient := h.login()
	refused(t, h.do("GET", callback, transient))
}

// Admission is checked per request: a session from before a settings change no longer passes.
func TestOIDCAdmissionAppliesToLiveSessions(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := h.signIn()
	h.oidc.subject = "new-owner"
	if w := h.do("GET", "/api/events", []*http.Cookie{c}); w.Code != http.StatusUnauthorized {
		t.Errorf("stale admission %d", w.Code)
	}
	if in, _, name := session(t, h, c); in || name != "" {
		t.Errorf("session %v %q", in, name)
	}
}

func TestOIDCExpiredSession(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	// The same key, a lifetime already over (kit's expiry is in whole seconds).
	past, err := kitauth.NewSessions(kitauth.SessionConfig{Name: SessionCookie,
		Key: kitauth.DeriveKey(secret, "flow-session"), TTL: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := past.Issue(w, kitauth.Claims{Subject: "owner-sub"}); err != nil {
		t.Fatal(err)
	}
	c := cookie(w, SessionCookie)
	if got := h.do("GET", "/api/events", []*http.Cookie{c}); got.Code != http.StatusUnauthorized ||
		errorCode(t, got) != "unauthenticated" {
		t.Errorf("expired session %d %s", got.Code, got.Body)
	}
	if in, _, _ := session(t, h, c); in {
		t.Error("expired session reads as signed in")
	}
}

func TestOIDCTamperedCookie(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := h.signIn()
	payload, sig, _ := strings.Cut(c.Value, ".")
	// Flip one payload character; then a payload for another subject signed with another key.
	flipped := []byte(payload)
	flipped[len(flipped)/2] ^= 1
	badSig := []byte(sig)
	badSig[0] ^= 1
	other, _ := kitauth.NewSessions(kitauth.SessionConfig{Name: SessionCookie,
		Key: kitauth.DeriveKey("another-secret-another-secret-xx", "flow-session")})
	w := httptest.NewRecorder()
	_ = other.Issue(w, kitauth.Claims{Subject: "owner-sub"})
	for name, value := range map[string]string{
		"payload": string(flipped) + "." + sig, "signature": payload + "." + string(badSig),
		"foreign key": cookie(w, SessionCookie).Value, "garbage": "nonsense",
	} {
		got := h.do("GET", "/api/events", []*http.Cookie{{Name: SessionCookie, Value: value}})
		if got.Code != http.StatusUnauthorized || errorCode(t, got) != "unauthenticated" {
			t.Errorf("%s: %d %s", name, got.Code, got.Body)
		}
	}
}

// The UI decides where the browser goes: an unauthenticated API call is JSON, never a redirect; streams and the
// WebSocket upgrade included.
func TestOIDCUnauthenticatedAPI(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/events"}, {"GET", "/api/sessions/s1/events"}, {"GET", "/api/terminals/t1/ws"},
		{"GET", "/api/projects"}, {"GET", "/api/sessions"},
	} {
		w := h.do(c.method, c.path, nil, "Upgrade", "websocket", "Origin", publicURL)
		if w.Code != http.StatusUnauthorized || errorCode(t, w) != "unauthenticated" ||
			w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	// The auth endpoints and the probes stay public.
	probe := httptest.NewRequest("GET", "/healthz", nil)
	probe.RemoteAddr = "127.0.0.1:40000" // kit answers probes from loopback only
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, probe)
	if w.Code != http.StatusOK {
		t.Errorf("healthz %d", w.Code)
	}
	if w := h.do("GET", "/api/auth/login", nil); w.Code != http.StatusFound {
		t.Errorf("login %d", w.Code)
	}
}

func TestOIDCCrossOrigin(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := []*http.Cookie{h.signIn()}
	for _, tc := range []struct {
		name, method, path string
		header             []string
		want               int
	}{
		{"same-origin write", "POST", "/api/projects", []string{"Origin", publicURL}, http.StatusNotImplemented},
		{"same-origin write by Referer", "POST", "/api/projects", []string{"Referer", publicURL + "/x?y"}, http.StatusNotImplemented},
		{"read without Origin", "GET", "/api/events", nil, http.StatusOK},
		{"same-origin websocket", "GET", "/api/terminals/t1/ws", []string{"Origin", publicURL, "Upgrade", "websocket"}, http.StatusOK},
		{"cross-site write", "POST", "/api/projects", []string{"Origin", "https://evil.example"}, http.StatusForbidden},
		{"sibling host write", "DELETE", "/api/sessions/s1", []string{"Origin", "https://other.example.com"}, http.StatusForbidden},
		{"plain http origin", "POST", "/api/projects", []string{"Origin", "http://flow.example.com"}, http.StatusForbidden},
		{"opaque origin", "POST", "/api/projects", []string{"Origin", "null"}, http.StatusForbidden},
		{"cross-site Referer", "PATCH", "/api/profile", []string{"Referer", "https://evil.example/"}, http.StatusForbidden},
		{"write with neither", "POST", "/api/projects", nil, http.StatusForbidden},
		{"cross-site websocket", "GET", "/api/terminals/t1/ws", []string{"Origin", "https://evil.example", "Upgrade", "websocket"}, http.StatusForbidden},
		{"cross-site read", "GET", "/api/events", []string{"Origin", "https://evil.example"}, http.StatusForbidden},
	} {
		w := h.do(tc.method, tc.path, c, tc.header...)
		if w.Code != tc.want {
			t.Errorf("%s: %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body)
		}
		if tc.want == http.StatusForbidden && errorCode(t, w) != "forbidden" {
			t.Errorf("%s: %s", tc.name, w.Body)
		}
	}
}

// httpapi refuses an unreadable Referer before routing; the Guard does not rely on that.
func TestOIDCGuardUnreadableReferer(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := h.signIn()
	for _, referer := range []string{"%zz", "/relative"} {
		r := httptest.NewRequest("POST", "/api/projects", nil)
		r.AddCookie(c)
		r.Header.Set("Referer", referer)
		w := httptest.NewRecorder()
		h.oidc.Require(http.NotFoundHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%q: %d", referer, w.Code)
		}
	}
}

// A preview page shares the site (SameSite does not stop it); Flow's API refuses it even with the owner's cookie.
func TestOIDCPreviewOrigin(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := []*http.Cookie{h.signIn()}
	for _, header := range [][]string{
		{"Origin", "https://s1-5173.flow.example.com"},
		{"Referer", "https://s1-5173.flow.example.com/app"},
	} {
		for _, method := range []string{"GET", "POST"} {
			w := h.do(method, "/api/events", c, header...)
			if w.Code != http.StatusForbidden || errorCode(t, w) != "preview_origin" {
				t.Errorf("%s %v: %d %s", method, header, w.Code, w.Body)
			}
		}
	}
	if w := h.do("GET", "/api/auth/session", c, "Origin", "https://s1-5173.flow.example.com"); w.Code != http.StatusForbidden {
		t.Errorf("session from a preview %d", w.Code)
	}
}

func TestOIDCLogout(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := h.signIn()
	w := h.do("POST", "/api/auth/logout", []*http.Cookie{c}, "Origin", publicURL)
	cleared := cookie(w, SessionCookie)
	if w.Code != http.StatusNoContent || cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 ||
		cleared.Path != "/" || cleared.Domain != "" || !cleared.Secure || !cleared.HttpOnly ||
		cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("logout %d %+v", w.Code, cleared)
	}
	if in, _, _ := session(t, h, cleared); in {
		t.Error("signed in after logout")
	}
	// A cross-site page cannot sign the owner out either: the route is public, so this is the handler's own check.
	if w := h.do("POST", "/api/auth/logout", []*http.Cookie{c}, "Origin", "https://evil.example"); w.Code != http.StatusForbidden ||
		cookie(w, SessionCookie) != nil {
		t.Errorf("cross-site logout %d", w.Code)
	}
	// Also the handler on its own.
	r := httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.Header.Set("Origin", "https://evil.example")
	direct := httptest.NewRecorder()
	h.oidc.AuthLogout(direct, r)
	if direct.Code != http.StatusForbidden || cookie(direct, SessionCookie) != nil {
		t.Errorf("direct cross-site logout %d", direct.Code)
	}
}

// Sign-out clears whatever cookie the browser has: a session no longer admitted, a tampered one, none at all.
func TestOIDCLogoutWithoutValidSession(t *testing.T) {
	h := newHarness(t, "owner-sub", "")
	c := h.signIn()
	h.oidc.subject = "new-owner" // the session is valid but no longer admitted
	for name, cookies := range map[string][]*http.Cookie{
		"not admitted": {c},
		"tampered":     {{Name: SessionCookie, Value: "x" + c.Value}},
		"none":         nil,
	} {
		w := h.do("POST", "/api/auth/logout", cookies, "Origin", publicURL)
		if cl := cookie(w, SessionCookie); w.Code != http.StatusNoContent || cl == nil || cl.MaxAge >= 0 {
			t.Errorf("%s: %d %+v", name, w.Code, cl)
		}
	}
}

func TestNewOIDCUnreachableProvider(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := NewOIDC(ctx, config.OIDC{PublicURL: publicURL, Issuer: srv.URL, ClientID: "flow",
		ClientSecret: "x", Subject: "s", SessionSecret: secret, SessionTTL: time.Hour}); err == nil {
		t.Fatal("NewOIDC without a provider succeeded")
	}
}
