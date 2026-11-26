// Package gate is the check queue: it grants gate.acquire control requests from all sandboxes in order, at most
// BACKEND_GATE_CONCURRENCY at a time (default 1), and tells waiting sessions their position with gate.update
// commands. In memory: after a Flow restart flowd raises the pending requests again. Owner: sessions area.
package gate
