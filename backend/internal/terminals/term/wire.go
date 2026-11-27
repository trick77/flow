// Package term is `flowd term`: one PTY running the agent user's login shell, a scrollback ring, and a unix socket
// that flowd attaches to (several clients at once, each first given the ring, then live output). It also holds the
// socket's client side, which flowd uses. The terminal is its own systemd unit, so it outlives flowd's restarts and
// updates; a terminal started by an older release keeps running that release's binary, hence the versioned,
// additive framing below.
//
// Socket protocol: frames of [type byte][uint32 big-endian length][payload], at most MaxFrame bytes of payload.
// The client opens with FrameHello (JSON Hello); the terminal answers FrameInfo (JSON Info). For OpAttach it then
// sends the ring as FrameOutput frames and live output after it, and takes FrameInput and FrameResize; when the shell
// has exited it sends FrameExit (JSON Exit) and closes. Either side ignores frame types it does not know.
package term

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Protocol is the socket protocol version a client names in its Hello.
const Protocol = 1

// MaxFrame bounds one frame's payload.
const MaxFrame = 1 << 20

// chunk is the largest output frame the terminal sends (the ring goes out in pieces of it).
const chunk = 32 << 10

// Frame types.
const (
	FrameHello  byte = 'h' // client → terminal, JSON Hello
	FrameInfo   byte = 'n' // terminal → client, JSON Info
	FrameInput  byte = 'i' // client → terminal, bytes for the shell
	FrameResize byte = 'r' // client → terminal, JSON Size
	FrameOutput byte = 'o' // terminal → client, the PTY's bytes
	FrameExit   byte = 'x' // terminal → client, JSON Exit, then the terminal closes
)

// Ops a Hello asks for.
const (
	OpAttach = "attach"
	OpInfo   = "info"
)

// Hello opens a connection.
type Hello struct {
	Protocol int    `json:"protocol"`
	Op       string `json:"op"`
	// Size, when set and valid, resizes the PTY before the ring is sent.
	Size *Size `json:"size,omitempty"`
}

// Size is a PTY window size in character cells.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Info describes a running terminal.
type Info struct {
	Protocol int    `json:"protocol"`
	ID       string `json:"id"`
	Session  string `json:"session,omitempty"`
	Dir      string `json:"dir"`
	Pid      int    `json:"pid"`
	Size     Size   `json:"size"`
	Exited   bool   `json:"exited,omitempty"`
	Code     int    `json:"code,omitempty"`
}

// Exit is the shell's exit status: its exit code, or 128 + the signal that ended it.
type Exit struct {
	Code int `json:"code"`
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("term: frame of %d bytes", len(payload))
	}
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], uint32(len(payload))) //nolint:gosec // bounded by MaxFrame above
	copy(b[5:], payload)
	_, err := w.Write(b)
	return err
}

// WriteJSON writes one frame with v as its JSON payload.
func WriteJSON(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return WriteFrame(w, typ, b)
}

// ErrFrame is a frame longer than MaxFrame: the stream cannot be trusted after it.
var ErrFrame = errors.New("term: frame too long")

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > MaxFrame {
		return 0, nil, ErrFrame
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return h[0], p, nil
}
