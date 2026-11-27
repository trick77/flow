# Sign-in

Flow has one owner and no user management. `BACKEND_AUTH_MODE=oidc` (the default) signs the owner in through an
external OpenID Connect provider with `ship/kit/auth`; `dev` admits every request and boots only on a loopback
address. Nothing here is specific to one provider; the owner's is Authelia.

## Registering Flow at the provider

| Field | Value |
|---|---|
| Client type | confidential, authorization code flow with PKCE (S256) |
| Redirect URI | `https://<BACKEND_PUBLIC_FQDN>/api/auth/callback` (exactly; no other) |
| Token endpoint auth | `client_secret_basic` (`client_secret_post` also works) |
| Scopes | `openid profile email`, plus `groups` when `BACKEND_OIDC_ALLOWED_GROUP` is set |
| ID token signing | anything the provider's discovery and JWKS announce (RS256 is fine) |

Claims Flow reads from the ID token: `sub` (always), `groups` (a list of strings, only for group admission),
`name` or `given_name` + `family_name`, else `preferred_username`, else `email`, for the name shown in the UI.
Groups must be in the ID token itself: Flow does not call the userinfo endpoint. The group claim's name is fixed
to `groups` (kit/auth reads only that one).

## Settings

| Variable | Meaning |
|---|---|
| `BACKEND_PUBLIC_FQDN` | Host name Flow is served at, e.g. `flow.example.com`. Public URL `https://<it>`: redirect URI and the only origin state changes are accepted from. |
| `BACKEND_OIDC_ISSUER` | Issuer URL; Flow reads `<issuer>/.well-known/openid-configuration` at start and refuses to boot when it cannot. |
| `BACKEND_OIDC_CLIENT_ID`, `BACKEND_OIDC_CLIENT_SECRET` | The client registered above. |
| `BACKEND_OIDC_ALLOWED_SUBJECT` | The owner's `sub`. |
| `BACKEND_OIDC_ALLOWED_GROUP` | A group whose members are the owner (case-insensitive). |
| `BACKEND_SESSION_SECRET` | At least 32 characters (`openssl rand -hex 32`); signs the session cookie. Rotating it signs the owner out. |
| `BACKEND_SESSION_TTL` | Sign-in lifetime, default `12h` (`30m`, `1d`, ...). Nothing renews it. See "Ending access". |

All but the TTL are required in oidc mode, the subject and group as "at least one of": without either Flow refuses
to start. `BACKEND_PUBLIC_FQDN` must be a bare host name (no scheme, port, path or trailing dot). In the container,
`compose.yaml` passes nothing of this itself: every setting above goes into `.env` next to it, or Flow does not
boot (oidc is the default mode).

## Admission

Flow admits an identity when its `sub` equals `BACKEND_OIDC_ALLOWED_SUBJECT` **or** its `groups` contain
`BACKEND_OIDC_ALLOWED_GROUP`. Anyone else who signs in at the provider gets no session: the callback sends the
browser to `/?signin=failed`, and the sign-in screen says only that sign-in did not work. Every failed callback
(refused identity, bad state, provider error, timeout) ends there; the reason goes to the server log only, at info
level, with the provider's error text dropped (anyone can call the callback). Providers often issue opaque
subjects: to find the owner's, sign in once with only a group (or a placeholder subject) configured and read `sub`
from the log line `sign-in refused: not the configured owner` (or `signed in`).

## Ending access

Admission is checked on every request, but against the identity the session cookie carries: the `sub` and
`groups` of the sign-in. Changing `BACKEND_OIDC_ALLOWED_*` applies to a live session at once; removing the owner
from the group, or disabling the account, at the provider does **not** end a Flow session until it expires
(`BACKEND_SESSION_TTL`). Flow asks the provider nothing per request. Hence the short default of 12 hours: one
working day, so a change at the provider reaches Flow by the next morning, at the cost of one sign-in a day (usually
a redirect without a password prompt while the provider's own session lasts). To end access at once, rotate
`BACKEND_SESSION_SECRET` and restart: that ends every session.

## Session and requests

- The session is kit's HMAC-signed cookie `__Host-flow_session` carrying the identity (no session table); the
  sign-in's transient state, nonce and PKCE cookies are `__Host-flow_oidc_*`. `__Host-` means `Secure`, `Path=/`,
  no `Domain`: preview hosts (`flow-<id>-<port>.<domain>`) and other apps under the same parent domain never get
  these cookies and cannot plant same-named ones for the parent domain to shadow them. Also `HttpOnly`,
  `SameSite=Lax`.
- Sign-out (`POST /api/auth/logout`) is a public route that makes its own same-origin check, needs no valid
  session, and clears the cookie whatever its state; the provider's own session is left alone.
- Every `/api/*` route except `/api/auth/session|login|callback|logout` (and kit's `/healthz`, `/readyz`) needs it; without
  it the answer is `401 unauthenticated` as JSON, never a redirect: the UI shows its sign-in screen, whose button
  navigates to `/api/auth/login`. An event stream that ends makes the UI ask `/api/auth/session` (an EventSource
  never shows the status), so an expired session reaches the sign-in screen with only streams open.
- The callback's calls to the provider (token exchange, key fetch) are bounded at 15 seconds, like discovery at
  start.
- CSRF: a request with an `Origin` other than the public URL is refused (`403 forbidden`; this covers WebSocket
  upgrades), and a state change must name the public URL in `Origin`, or in `Referer` when `Origin` is absent.
  Bodies must be `application/json`. Requests from preview hosts are refused before that (`preview_origin`). Dev
  mode applies the same rule against the request's `Host`, except that a client sending neither header (curl) may
  write.
