// Package eventlog writes and reads a session's append-only event log (layout.EventLog): one event.Event per line,
// seq from 1 without gaps, a single writer holding an exclusive flock, a torn last line truncated by the writer on
// open, readers that seek by a sparse seq index and follow by inotify (polling elsewhere). docs/events.md has the
// format. Owner: flowd runtime area.
package eventlog
