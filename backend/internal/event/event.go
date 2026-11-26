// Package event is Flow's event model in Go: the bindings generated from contract/events.schema.json
// (event.gen.go) and the rules every reader and writer follows (docs/events.md).
//
// Forward compatibility: a reader skips an event whose v is above Version or whose kind it does not know
// (ErrUnknownKind), and ignores unknown fields. A writer never changes a field's meaning or type inside a major
// version; it only adds kinds and optional fields.
package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Version is the contract's major version, the v of every event and command this build writes.
const Version = 1

// MaxLine is the longest log line or stream frame a reader must accept. A writer shortens tool output and inputs
// so an event stays below it.
const MaxLine = 1 << 20

// MaxText bounds a message's text, measured as encoded JSON: escaping makes it longer than its bytes (Go writes <, >
// and & as six-byte \u escapes), and the text travels inside a command body and an event line, both bounded by
// MaxLine. A quarter of MaxLine leaves the envelope and any re-encoding far from that bound.
const MaxText = MaxLine / 4

// TextFits reports whether text is within MaxText once encoded.
func TextFits(text string) bool {
	b, err := json.Marshal(text)
	return err == nil && len(b) <= MaxText
}

// ErrUnknownKind is a kind outside this build's contract. Readers skip such events.
var ErrUnknownKind = errors.New("event: unknown kind")

// ErrNewerVersion is an event whose major version is above Version. Readers skip such events.
var ErrNewerVersion = errors.New("event: newer major version")

var (
	eventInfo   = map[EventKind]EventKindInfo{}
	commandInfo = map[CommandKind]CommandKindInfo{}
)

func init() {
	for _, k := range EventKindInfos {
		eventInfo[k.Kind] = k
	}
	for _, k := range CommandKindInfos {
		commandInfo[k.Kind] = k
	}
}

// Info returns the contract's description of an event kind.
func (k EventKind) Info() (EventKindInfo, bool) {
	i, ok := eventInfo[k]
	return i, ok
}

// Info returns the contract's description of a command kind.
func (k CommandKind) Info() (CommandKindInfo, bool) {
	i, ok := commandInfo[k]
	return i, ok
}

// Durable reports whether events of the kind are written to the log and numbered. Unknown kinds count as durable:
// a newer writer only logs what it numbers.
func (k EventKind) Durable() bool {
	i, ok := eventInfo[k]
	return !ok || !i.Ephemeral
}

// New builds an event of kind around payload; seq, turn and the other envelope fields are the caller's.
func New(session string, kind EventKind, ts time.Time, payload any) (Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("event %s: %w", kind, err)
	}
	return Event{V: Version, Session: session, Kind: kind, TS: ts.UTC(), Data: data}, nil
}

// Payload decodes the event's data into the kind's payload type, a pointer such as *ToolStarted.
func (e Event) Payload() (any, error) {
	if e.V > Version {
		return nil, ErrNewerVersion
	}
	i, ok := eventInfo[e.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, e.Kind)
	}
	p := i.New()
	if err := json.Unmarshal(e.Data, p); err != nil {
		return nil, fmt.Errorf("event %s: %w", e.Kind, err)
	}
	return p, nil
}

// Payload decodes the command's data into the kind's payload type.
func (c Command) Payload() (any, error) {
	if c.V > Version {
		return nil, ErrNewerVersion
	}
	i, ok := commandInfo[c.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: command %q", ErrUnknownKind, c.Kind)
	}
	p := i.New()
	if err := json.Unmarshal(c.Data, p); err != nil {
		return nil, fmt.Errorf("command %s: %w", c.Kind, err)
	}
	return p, nil
}

// NewCommand builds a command of kind around payload.
func NewCommand(id, session string, kind CommandKind, payload any) (Command, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Command{}, fmt.Errorf("command %s: %w", kind, err)
	}
	return Command{V: Version, ID: id, Session: session, Kind: kind, Data: data}, nil
}

// Line is the event as one log line or JSONL frame: compact JSON and a newline.
func (e Event) Line() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b) >= MaxLine {
		return nil, fmt.Errorf("event %s: %d bytes, over the %d line limit", e.Kind, len(b), MaxLine)
	}
	return append(b, '\n'), nil
}

// ParseLine reads one log line. A line without its newline is a torn write and fails: the writer had not
// finished it.
func ParseLine(line []byte) (Event, error) {
	body, ok := bytes.CutSuffix(line, []byte("\n"))
	if !ok {
		return Event{}, errors.New("event: torn line (no newline)")
	}
	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return Event{}, fmt.Errorf("event: %w", err)
	}
	return e, nil
}
