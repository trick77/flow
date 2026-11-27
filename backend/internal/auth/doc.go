// Package auth signs the owner in and implements httpapi.Guard and httpapi.Auth. Owner: auth area
// (docs/ownership.md); settings and the admission rule: docs/auth.md.
//
// Two modes. oidc: kit/auth's authorization code flow with PKCE against the configured provider, admitting only the
// configured subject or group, and kit's HMAC-signed session cookie (no session table). dev: config refuses it
// unless the server listens on loopback, so whoever reaches it is on the machine.
//
// CSRF in oidc mode: the session is a cookie a browser attaches by itself, and SameSite=Lax keeps it off cross-site
// POSTs but not off requests from sibling hosts of the same site (other apps on the owner's domain, preview hosts),
// nor off WebSocket handshakes, which have no CORS. So the Guard admits a request only when its Origin, if it sends
// one, is Flow's public origin, and a state change (anything but GET, HEAD, OPTIONS) only when its Origin, or its
// Referer when Origin is absent, is; a state change with neither is refused. httpapi.Decode refuses bodies that are
// not application/json, which an HTML form cannot send and a cross-site fetch only after a CORS preflight Flow never
// grants. Preview origins are refused before routing (httpapi). Dev mode applies the same rule (sameOrigin) against
// the Host the request was sent to, and lets a client that sends neither header (curl) write.
//
// Sibling hosts also share the parent domain's cookie jar: every cookie Flow sets is __Host- prefixed, so none can
// be planted or shadowed from there.
package auth
