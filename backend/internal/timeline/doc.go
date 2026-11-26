// Package timeline serves the browser's event streams (SSE by seq, with catch-up and live snapshot), the log pages,
// messages, commands and replies: httpapi.Timeline. It relays to flowd through flowdproto.API and turns browser
// requests into commands with ids the browser chose. Owner: timeline API area (docs/ownership.md).
package timeline
