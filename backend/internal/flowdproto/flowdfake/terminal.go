package flowdfake

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/trick77/flow/internal/flowdproto"
)

// fakeRing bounds the fake terminal's scrollback like the real one's ring (smaller: it is a test double).
const fakeRing = 64 << 10

// terminal is an echoing terminal: input comes back as output to every attached connection; the scrollback is
// replayed on attach; ExitTerminal ends it like an exiting shell.
type terminal struct {
	state flowdproto.TerminalState
	size  flowdproto.TerminalSize
	ring  []byte
	conns map[*termConn]struct{}
}

// EnsureTerminal implements flowdproto.API: idempotent while running; an exited one starts afresh.
func (f *Fake) EnsureTerminal(_ context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.terminals[t.ID]; ok && !old.state.Exited {
		return old.state, nil
	}
	st := flowdproto.TerminalState{ID: t.ID, Session: t.Session, Unit: flowdproto.UnitActive, Dir: t.Dir}
	f.terminals[t.ID] = &terminal{state: st, size: flowdproto.TerminalSize{Cols: t.Cols, Rows: t.Rows},
		conns: map[*termConn]struct{}{}}
	return st, nil
}

// AttachTerminal implements flowdproto.API: the scrollback first, then the echo.
func (f *Fake) AttachTerminal(_ context.Context, id string, size flowdproto.TerminalSize) (flowdproto.TerminalConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.terminals[id]
	if !ok {
		return nil, fmt.Errorf("%w: terminal %s", flowdproto.ErrNotFound, id)
	}
	if t.state.Exited {
		return nil, &flowdproto.TerminalExit{Code: t.state.ExitCode}
	}
	if flowdproto.ValidTerminalSize(size.Cols, size.Rows) {
		t.size = size
	}
	c := &termConn{f: f, t: t}
	c.cond = sync.NewCond(&c.mu)
	c.out = append([]byte(nil), t.ring...)
	t.conns[c] = struct{}{}
	return c, nil
}

// CloseTerminal implements flowdproto.API.
func (f *Fake) CloseTerminal(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.terminals[id]; ok {
		for c := range t.conns {
			c.end(nil)
		}
		delete(f.terminals, id)
	}
	return nil
}

// ExitTerminal ends a terminal's shell with code: attached connections read the rest of the output, then
// *flowdproto.TerminalExit.
func (f *Fake) ExitTerminal(id string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.terminals[id]
	if !ok {
		return
	}
	t.state.Exited, t.state.ExitCode, t.state.Unit = true, code, flowdproto.UnitMissing
	for c := range t.conns {
		c.end(&flowdproto.TerminalExit{Code: code})
	}
	t.conns = map[*termConn]struct{}{}
}

// DropTerminalConns breaks every attached connection without ending the terminal, as a flowd restart does.
func (f *Fake) DropTerminalConns(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.terminals[id]; ok {
		for c := range t.conns {
			c.end(io.ErrUnexpectedEOF)
		}
		t.conns = map[*termConn]struct{}{}
	}
}

// TerminalSize is the terminal's current window size.
func (f *Fake) TerminalSize(id string) flowdproto.TerminalSize {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.terminals[id]; ok {
		return t.size
	}
	return flowdproto.TerminalSize{}
}

type termConn struct {
	f    *Fake
	t    *terminal
	mu   sync.Mutex
	cond *sync.Cond
	out  []byte
	err  error // after out is read; io.EOF once closed
}

// end makes the connection's reads end with err after its pending output (nil: io.EOF); callers hold f.mu.
func (c *termConn) end(err error) {
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *termConn) push(p []byte) {
	c.mu.Lock()
	if c.err == nil {
		c.out = append(c.out, p...)
	}
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *termConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.out) == 0 && c.err == nil {
		c.cond.Wait()
	}
	if len(c.out) == 0 {
		return 0, c.err
	}
	n := copy(p, c.out)
	c.out = c.out[n:]
	return n, nil
}

func (c *termConn) Write(p []byte) (int, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if c.t.state.Exited {
		return 0, io.ErrClosedPipe
	}
	c.t.ring = append(c.t.ring, p...)
	if len(c.t.ring) > fakeRing {
		c.t.ring = c.t.ring[len(c.t.ring)-fakeRing:]
	}
	for o := range c.t.conns {
		o.push(p)
	}
	return len(p), nil
}

func (c *termConn) Resize(cols, rows int) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if flowdproto.ValidTerminalSize(cols, rows) {
		c.t.size = flowdproto.TerminalSize{Cols: cols, Rows: rows}
	}
	return nil
}

// Close detaches: output written before it can still be read, then io.EOF.
func (c *termConn) Close() error {
	c.f.mu.Lock()
	delete(c.t.conns, c)
	c.f.mu.Unlock()
	c.end(nil)
	return nil
}
