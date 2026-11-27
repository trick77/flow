// Package terminals bridges the browser's terminal WebSocket to flowdproto.API.AttachTerminal: httpapi.Terminals.
// Subpackages: term is `flowd term` (PTY, scrollback ring, local socket) and its client; wsterm carries an attached
// terminal over a WebSocket on both legs (flowd → Flow, Flow → browser). Nothing a terminal carries is logged or
// stored by Flow. Owner: terminal area (docs/ownership.md).
package terminals
