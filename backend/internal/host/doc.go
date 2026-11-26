// Package host is the session host (flowd host, one transient systemd unit per session): the log's only writer,
// the command socket (hostproto), the message queue, status derivation, turn bookkeeping (files.changed, expiring
// open requests), and the agent adapter it drives: the Node sidecar over stdio for Claude, or the OpenCode bridge.
// Owner: flowd Claude adapter host area (docs/ownership.md).
package host
