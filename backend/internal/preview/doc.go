// Package preview is the reverse proxy for preview hosts (flow-<session>-<port>.<preview domain>) to
// flowdproto.API.Port, its one-use sign-in tickets and preview-host cookie, and httpapi.Previews. It strips every
// cookie and the Authorization header before forwarding. Owner: preview area (docs/ownership.md).
package preview
