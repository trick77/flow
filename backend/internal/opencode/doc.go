// Package opencode is the OpenCode adapter inside a session host: it drives the sandbox's one opencode serve (V2
// API under /api, HTTP Basic user opencode, password from flowd) for one session, translates /api/event frames of
// that session into event drafts, and resyncs from the message list after a reconnect, since the event stream has
// no replay. Owner: flowd OpenCode adapter area (docs/ownership.md).
package opencode
