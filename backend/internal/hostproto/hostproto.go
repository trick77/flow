// Package hostproto is the protocol between flowd and a session host (flowd host, one systemd unit per session):
// JSON lines over the host's unix socket (layout.HostSocket). flowd and the host are both Go but not the same
// binary version: a host keeps running the release it was started from while flowd is updated, so this protocol
// is versioned on its own and flowd speaks every version from MinProtocol up.
//
// One connection carries requests and their responses in order. After a subscribe request the connection carries
// only Frames until either side closes it. The event log file stays the source of truth: a Frame announces new
// durable events by seq and flowd reads them from the file; only ephemeral events travel in the Frame itself.
package hostproto

import "github.com/trick77/flow/internal/event"

// Protocol versions this build speaks.
const (
	Protocol    = 1
	MinProtocol = 1
)

// Op is a request's operation.
type Op string

// Op values.
const (
	// OpHello returns the host's Hello; the first request on every connection.
	OpHello Op = "hello"
	// OpCommand hands the host a command; the response comes once it is accepted (logged or queued).
	OpCommand Op = "command"
	// OpSubscribe turns the connection into a Frame stream: first a live.snapshot of open blocks, then frames.
	OpSubscribe Op = "subscribe"
	// OpNotice has the host log a notice flowd raised for the session (git asked for a GitHub token that is
	// missing). Additive within protocol 1: a host from before answers invalid, and flowd drops the notice.
	OpNotice Op = "notice"
)

// Request is one line from flowd.
type Request struct {
	ID      string         `json:"id"`
	Op      Op             `json:"op"`
	Command *event.Command `json:"command,omitempty"`
	Notice  *event.Notice  `json:"notice,omitempty"`
}

// Response is the host's answer to the request with the same ID.
type Response struct {
	ID string `json:"id"`
	// Error is empty on success, else one of the Code* values.
	Error     string `json:"error,omitempty"`
	Hello     *Hello `json:"hello,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
	// Queued: the command was a message.send that waits in the queue behind the running turn.
	Queued bool `json:"queued,omitempty"`
}

// Error codes.
const (
	CodeInvalid  = "invalid"
	CodeUnknown  = "unknown_kind"
	CodeShutdown = "shutting_down"
	// CodeNotPending: a reply names a permission, question or plan request that is not open (answered, expired,
	// or never asked).
	CodeNotPending = "not_pending"
)

// Hello describes a running host.
type Hello struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	Session  string `json:"session"`
	PID      int    `json:"pid"`
	// LastSeq is the last durable seq written.
	LastSeq int64               `json:"lastSeq"`
	Status  event.SessionStatus `json:"status"`
	// Busy: a turn runs; flowd upgrades an old host only when it is not.
	Busy bool `json:"busy"`
}

// Frame is one item of a subscription.
type Frame struct {
	// Appended: durable events up to this seq are in the log.
	Appended int64 `json:"appended,omitempty"`
	// Live is an ephemeral event (text delta, tool progress, live snapshot).
	Live *event.Event `json:"live,omitempty"`
}
