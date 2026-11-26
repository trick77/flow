// Package mcp is the child-session tool server (flowd mcp, an MCP stdio server each agent starts): create_session,
// send_message, get_status, list_sessions, wait_for_sessions. It identifies the calling session by the token in its
// environment and relays every call as a control request through flowd to Flow, which enforces the rules.
// Owner: flowd runtime area (docs/ownership.md).
package mcp
