// Package auth signs the owner in (kit/auth: OIDC against the configured provider, admitting only the configured
// subject or group; BACKEND_AUTH_MODE=dev on loopback only) and implements httpapi.Guard and httpapi.Auth.
// Owner: auth area (docs/ownership.md).
package auth
