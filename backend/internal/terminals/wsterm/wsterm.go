// Package wsterm carries an attached terminal over a WebSocket, the same way on both legs: flowd serves Flow, and
// Flow serves the browser. Binary messages are the terminal's bytes, both ways. Text messages are
// flowdproto.TerminalMessage JSON: {"resize":{"cols":C,"rows":R}} towards the terminal, {"exit":{"code":N}} from
// it as its last message before a normal close. Close codes tell the browser what to do next:
//
//   - 1000 after an exit message: the shell ended; the drawer shows the status and offers a restart.
//   - 1012 (service restart), 1011, 1001 or a dropped connection: Flow or flowd went away, the terminal did not;
//     the browser attaches again and gets the scrollback.
//   - CloseGone (4404): the terminal is not running and left no exit status (closed, or the sandbox restarted).
//   - CloseNoSandbox (4409): the sandbox is not running; attaching never starts it.
//
// Nothing here logs or keeps what passes through: terminal bytes are the owner's keystrokes and their output.
package wsterm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/trick77/flow/internal/flowdproto"
)

// Close codes of the application range (4000-4999).
const (
	CloseGone      websocket.StatusCode = 4404
	CloseNoSandbox websocket.StatusCode = 4409
)

// ReadLimit bounds one message: a paste of a large block arrives as one binary message.
const ReadLimit = 1 << 20

// pingEvery (nanoseconds) keeps idle connections alive through proxies and finds dead peers; atomic for tests.
var pingEvery atomic.Int64

func init() { pingEvery.Store(int64(30 * time.Second)) }

const writeWait = 10 * time.Second

// Accept upgrades a request. The origin was checked before (Flow: the Guard's same-origin rule, which applies to
// the handshake's GET; flowd: Flow's bearer token, and Flow sends no Origin), so Accept's own check, which compares
// the Origin with the Host header and would refuse a correct request behind a proxy rewriting Host, is off.
func Accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true,
		CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(ReadLimit)
	return c, nil
}

// Refuse upgrades the request only to close it at once with code: a browser cannot read why a handshake failed,
// but it can read a close code.
func Refuse(w http.ResponseWriter, r *http.Request, code websocket.StatusCode, reason string) {
	c, err := Accept(w, r)
	if err != nil {
		return
	}
	_ = c.Close(code, reason)
}

// Exit sends a terminal's exit status and closes normally.
func Exit(ctx context.Context, c *websocket.Conn, code int) {
	wctx, cancel := context.WithTimeout(ctx, writeWait)
	defer cancel()
	if b, err := json.Marshal(flowdproto.TerminalMessage{Exit: &flowdproto.TerminalEnd{Code: code}}); err == nil {
		_ = c.Write(wctx, websocket.MessageText, b)
	}
	_ = c.Close(websocket.StatusNormalClosure, "exited")
}

// Serve bridges c and tc until either ends; tc is closed (detached) when it returns. A shutdown (ctx ends) closes c
// with 1012, so the browser attaches again once the server is back.
func Serve(parent context.Context, c *websocket.Conn, tc flowdproto.TerminalConn) {
	// The connection's own context: a cancelled read context would drop the socket before the close code is out.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	shutdown := context.AfterFunc(parent, func() {
		_ = c.Close(websocket.StatusServiceRestart, "server restart")
		cancel()
	})
	defer shutdown()
	var closeOnce sync.Once
	closeTC := func() { closeOnce.Do(func() { _ = tc.Close() }) }
	defer closeTC()
	stop := context.AfterFunc(ctx, closeTC) // unblocks tc.Read below
	defer stop()
	// The read context outlives ctx by nothing; a broken browser leg cancels ctx.
	go func() {
		defer cancel()
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				if _, err := tc.Write(b); err != nil {
					return
				}
			case websocket.MessageText:
				var m flowdproto.TerminalMessage
				if json.Unmarshal(b, &m) == nil && m.Resize != nil &&
					flowdproto.ValidTerminalSize(m.Resize.Cols, m.Resize.Rows) {
					if err := tc.Resize(m.Resize.Cols, m.Resize.Rows); err != nil {
						return
					}
				}
			}
		}
	}()
	go ping(ctx, c, cancel)
	buf := make([]byte, 32<<10)
	for {
		n, err := tc.Read(buf)
		if n > 0 {
			wctx, wcancel := context.WithTimeout(ctx, writeWait)
			werr := c.Write(wctx, websocket.MessageBinary, buf[:n])
			wcancel()
			if werr != nil {
				_ = c.CloseNow()
				return
			}
		}
		if err == nil {
			continue
		}
		var exit *flowdproto.TerminalExit
		switch {
		case errors.As(err, &exit):
			Exit(context.WithoutCancel(ctx), c, exit.Code)
		case errors.Is(err, flowdproto.ErrNotFound):
			_ = c.Close(CloseGone, "terminal gone")
		case ctx.Err() != nil: // shutting down, or the other leg is gone already
			_ = c.Close(websocket.StatusServiceRestart, "")
		default:
			_ = c.Close(websocket.StatusInternalError, "terminal connection lost")
		}
		return
	}
}

func ping(ctx context.Context, c *websocket.Conn, cancel context.CancelFunc) {
	t := time.NewTicker(time.Duration(pingEvery.Load()))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, writeWait)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// Conn is the client side of a terminal WebSocket (Flow dialling flowd): a flowdproto.TerminalConn.
type Conn struct {
	c       *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	pending []byte
}

var _ flowdproto.TerminalConn = (*Conn)(nil)

// Wrap makes a dialled WebSocket a TerminalConn that lives until Close or a broken connection.
func Wrap(c *websocket.Conn) *Conn {
	c.SetReadLimit(ReadLimit)
	ctx, cancel := context.WithCancel(context.Background())
	go ping(ctx, c, cancel)
	return &Conn{c: c, ctx: ctx, cancel: cancel}
}

// Read implements io.Reader; *flowdproto.TerminalExit after an exit message, flowdproto.ErrNotFound when the
// terminal is gone, io.EOF when the connection ended otherwise.
func (t *Conn) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		typ, b, err := t.c.Read(t.ctx)
		if err != nil {
			if websocket.CloseStatus(err) == CloseGone {
				return 0, flowdproto.ErrNotFound
			}
			return 0, io.EOF
		}
		switch typ {
		case websocket.MessageBinary:
			t.pending = b
		case websocket.MessageText:
			var m flowdproto.TerminalMessage
			if json.Unmarshal(b, &m) == nil && m.Exit != nil {
				return 0, &flowdproto.TerminalExit{Code: m.Exit.Code}
			}
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	return n, nil
}

// Write implements io.Writer.
func (t *Conn) Write(p []byte) (int, error) {
	ctx, cancel := context.WithTimeout(t.ctx, writeWait)
	defer cancel()
	if err := t.c.Write(ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize implements flowdproto.TerminalConn.
func (t *Conn) Resize(cols, rows int) error {
	b, err := json.Marshal(flowdproto.TerminalMessage{Resize: &flowdproto.TerminalSize{Cols: cols, Rows: rows}})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(t.ctx, writeWait)
	defer cancel()
	return t.c.Write(ctx, websocket.MessageText, b)
}

// Close implements io.Closer: detaches; the terminal keeps running.
func (t *Conn) Close() error {
	t.cancel()
	return t.c.CloseNow()
}
