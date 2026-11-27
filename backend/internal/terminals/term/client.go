package term

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/trick77/flow/internal/flowdproto"
)

// Query asks the terminal on socket for its Info, within 3 s or ctx's deadline, whichever comes first.
func Query(ctx context.Context, socket string) (Info, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if err := WriteJSON(conn, FrameHello, Hello{Protocol: Protocol, Op: OpInfo}); err != nil {
		return Info{}, err
	}
	return readInfo(bufio.NewReader(conn))
}

func readInfo(r io.Reader) (Info, error) {
	typ, p, err := ReadFrame(r)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if typ != FrameInfo || json.Unmarshal(p, &info) != nil {
		return Info{}, fmt.Errorf("term: no info frame (%q)", typ)
	}
	return info, nil
}

// Conn is an attached terminal; it implements flowdproto.TerminalConn.
type Conn struct {
	conn net.Conn
	r    *bufio.Reader
	Info Info

	wmu     sync.Mutex
	pending []byte
}

var _ flowdproto.TerminalConn = (*Conn)(nil)

// Attach connects to the terminal on socket; size, when valid, resizes it before the ring arrives.
func Attach(ctx context.Context, socket string, size *Size) (*Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := WriteJSON(conn, FrameHello, Hello{Protocol: Protocol, Op: OpAttach, Size: size}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	r := bufio.NewReaderSize(conn, chunk+5)
	info, err := readInfo(r)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &Conn{conn: conn, r: r, Info: info}, nil
}

// Read implements io.Reader: the terminal's output; *flowdproto.TerminalExit once the shell has exited, io.EOF
// when the terminal went away without saying so.
func (c *Conn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		typ, b, err := ReadFrame(c.r)
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.EOF
			}
			return 0, err
		}
		switch typ {
		case FrameOutput:
			c.pending = b
		case FrameExit:
			var e Exit
			if json.Unmarshal(b, &e) != nil {
				return 0, io.EOF
			}
			return 0, &flowdproto.TerminalExit{Code: e.Code}
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// Write implements io.Writer: input for the shell.
func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	for off := 0; off < len(p); off += chunk {
		if err := WriteFrame(c.conn, FrameInput, p[off:min(off+chunk, len(p))]); err != nil {
			return off, err
		}
	}
	return len(p), nil
}

// Resize implements flowdproto.TerminalConn; the terminal ignores sizes outside flowdproto's bounds.
func (c *Conn) Resize(cols, rows int) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return WriteJSON(c.conn, FrameResize, Size{Cols: cols, Rows: rows})
}

// Close implements io.Closer: detaches; the terminal keeps running.
func (c *Conn) Close() error { return c.conn.Close() }
