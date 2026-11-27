package term

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trick77/flow/internal/flowdproto"
)

// Shell is a started shell on a PTY: Read is its output, Write its input. The PTY code is Linux-only (pty_linux.go);
// tests use a fake.
type Shell interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	// Wait blocks until the shell has exited and returns its exit status (code, or 128 + signal).
	Wait() int
	// Hangup tells the shell's session the terminal is going away (SIGHUP to its process group).
	Hangup()
	// Kill ends the shell's process group with SIGKILL: for a shell that ignores the hangup.
	Kill()
	Pid() int
}

// Output waiting for one client is bounded by bytes (maxQueued) and frames (queueFrames). A client at the bound
// holds the PTY reader up, which stalls the shell's writes as a real terminal does when its screen is slow (a
// flood of output is throttled to the slowest reader, not lost). One that stays at the bound for slowWait is
// dropped instead, so a stuck client cannot freeze the terminal for the others; it attaches again and gets the ring.
const (
	maxQueued   = 4 << 20
	queueFrames = 4096
)

// slowWait (nanoseconds; atomic for tests).
var slowWait atomic.Int64

func init() { slowWait.Store(int64(5 * time.Second)) }

// drainWait is how long output keeps being read after the shell exited: a background job may hold the PTY open.
const drainWait = time.Second

// writeWait bounds one frame's write to a client: one that takes no frame for this long is dropped.
const writeWait = 10 * time.Second

// Server serves one shell to any number of clients.
type Server struct {
	shell  Shell
	info   Info
	inMu   sync.Mutex // one writer of input at a time; a write may block while the program reads no stdin
	sizeMu sync.Mutex // one window-size change at a time, never behind a blocked input write

	mu       sync.Mutex
	ring     *Ring
	clients  map[*client]struct{}
	exited   bool
	code     int
	flushing bool // set by Flush: no new writer is counted in wg after it
	done     chan struct{}
	wg       sync.WaitGroup
}

type client struct {
	conn    net.Conn
	mu      sync.Mutex // guards out against its close
	closed  bool
	out     chan frame
	queued  int // bytes in out, under mu
	drained chan struct{}
	gone    chan struct{} // closed with out: a pump waiting for room stops waiting
}

func newClient(conn net.Conn) *client {
	return &client{conn: conn, out: make(chan frame, queueFrames), drained: make(chan struct{}, 1),
		gone: make(chan struct{})}
}

// offer queues f unless the queue is at its bound (ok false) or closed (closed true). An empty queue takes any
// frame, so one larger than maxQueued still goes through.
func (c *client) offer(f frame) (ok, closed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false, true
	}
	if len(c.out) == cap(c.out) || c.queued > 0 && c.queued+len(f.payload) > maxQueued {
		return false, false
	}
	c.queued += len(f.payload)
	c.out <- f
	return true, false
}

// sent is the writer's receipt for a frame written out.
func (c *client) sent(f frame) {
	c.mu.Lock()
	c.queued -= len(f.payload)
	c.mu.Unlock()
	select {
	case c.drained <- struct{}{}:
	default:
	}
}

func (c *client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.out)
		close(c.gone)
	}
}

// deliver queues f for c, waiting up to slowWait for room; a client that never makes room is dropped.
func (s *Server) deliver(c *client, f frame) {
	timer := time.NewTimer(time.Duration(slowWait.Load()))
	defer timer.Stop()
	for {
		ok, closed := c.offer(f)
		if ok || closed {
			return
		}
		select {
		case <-c.drained:
		case <-c.gone: // dropped meanwhile: the next offer says closed
		case <-timer.C:
			s.mu.Lock()
			s.drop(c)
			s.mu.Unlock()
			return
		}
	}
}

type frame struct {
	typ     byte
	payload []byte
}

// NewServer starts pumping the shell's output into a ring of ringSize bytes. info describes the terminal to clients.
func NewServer(shell Shell, info Info, ringSize int) *Server {
	info.Protocol, info.Pid = Protocol, shell.Pid()
	s := &Server{shell: shell, info: info, ring: NewRing(ringSize), clients: map[*client]struct{}{},
		done: make(chan struct{})}
	read := make(chan struct{})
	go s.pump(read)
	go s.wait(read)
	return s
}

// Done is closed once the shell has exited and every client was told.
func (s *Server) Done() <-chan struct{} { return s.done }

// Code is the shell's exit status, valid after Done.
func (s *Server) Code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Flush waits up to d for the clients' queues to be written out after Done. Attaches arriving from now on are
// closed unanswered (the client reads the exit status the terminal left beside its socket), so no writer is
// counted after the wait began.
func (s *Server) Flush(d time.Duration) {
	s.mu.Lock()
	s.flushing = true
	s.mu.Unlock()
	ch := make(chan struct{})
	go func() { s.wg.Wait(); close(ch) }()
	select {
	case <-ch:
	case <-time.After(d):
	}
}

func (s *Server) pump(read chan<- struct{}) {
	defer close(read)
	buf := make([]byte, chunk)
	for {
		n, err := s.shell.Read(buf)
		if n > 0 {
			p := append([]byte(nil), buf[:n]...)
			// The ring and the list of receivers under one lock (an attach in between gets p with the ring, never
			// twice); the delivery outside it, since it may wait for a slow client.
			s.mu.Lock()
			s.ring.Write(p)
			targets := make([]*client, 0, len(s.clients))
			for c := range s.clients {
				targets = append(targets, c)
			}
			s.mu.Unlock()
			for _, c := range targets {
				s.deliver(c, frame{FrameOutput, p})
			}
		}
		if err != nil {
			return
		}
	}
}

// wait records the exit once the shell ended and its output is read (or drainWait passed), and tells every client.
func (s *Server) wait(read <-chan struct{}) {
	code := s.shell.Wait()
	select {
	case <-read:
	case <-time.After(drainWait):
	}
	_ = s.shell.Close()
	// The pump ends now (its Read fails) once its last delivery is queued: the exit goes behind all output.
	<-read
	s.mu.Lock()
	s.exited, s.code = true, code
	s.info.Exited, s.info.Code = true, code
	targets := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		targets = append(targets, c)
	}
	s.mu.Unlock()
	for _, c := range targets {
		// One too far behind to take even this is dropped, and learns the exit when it attaches again.
		s.deliver(c, exitFrame(code))
		s.mu.Lock()
		s.drop(c)
		s.mu.Unlock()
	}
	close(s.done)
}

func exitFrame(code int) frame {
	b, _ := json.Marshal(Exit{Code: code})
	return frame{FrameExit, b}
}

// drop ends a client: its queue is written out, then its connection closed. Callers hold mu.
func (s *Server) drop(c *client) {
	delete(s.clients, c)
	c.close()
}

// Serve accepts clients on l until it is closed.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	typ, p, err := ReadFrame(conn)
	var h Hello
	if err != nil || typ != FrameHello || json.Unmarshal(p, &h) != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	if h.Op != OpAttach {
		s.mu.Lock()
		info := s.info
		s.mu.Unlock()
		_ = WriteJSON(conn, FrameInfo, info)
		_ = conn.Close()
		return
	}
	c := newClient(conn)
	s.mu.Lock()
	if s.flushing {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	// An empty queue takes the info, the ring (at most RingSize, below maxQueued) and the exit without waiting. The
	// ring and the registration happen under one lock: nothing written in between is lost or sent twice.
	c.offer(frame{FrameInfo, mustJSON(s.info)})
	ring := s.ring.Bytes()
	for len(ring) > 0 {
		n := min(len(ring), chunk)
		c.offer(frame{FrameOutput, ring[:n]})
		ring = ring[n:]
	}
	if s.exited {
		c.offer(exitFrame(s.code))
		c.close()
	} else {
		s.clients[c] = struct{}{}
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go s.write(c)
	// After the info is queued: a resize never delays an attach (the shell redraws on SIGWINCH anyway).
	if h.Size != nil {
		s.resize(*h.Size)
	}
	s.read(c)
}

// write sends a client's queue; a closed queue (exit, or dropped) closes the connection.
func (s *Server) write(c *client) {
	defer s.wg.Done()
	defer func() { _ = c.conn.Close() }()
	for f := range c.out {
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if WriteFrame(c.conn, f.typ, f.payload) != nil {
			s.mu.Lock()
			s.drop(c)
			s.mu.Unlock()
			for range c.out { //nolint:revive // drain until the queue is closed
			}
			return
		}
		c.sent(f)
	}
}

// read takes a client's input and resizes until its connection ends.
func (s *Server) read(c *client) {
	defer func() {
		s.mu.Lock()
		s.drop(c)
		s.mu.Unlock()
	}()
	for {
		typ, p, err := ReadFrame(c.conn)
		if err != nil {
			return
		}
		switch typ {
		case FrameInput:
			s.inMu.Lock()
			_, err = s.shell.Write(p)
			s.inMu.Unlock()
			if err != nil {
				return
			}
		case FrameResize:
			var sz Size
			if json.Unmarshal(p, &sz) == nil {
				s.resize(sz)
			}
		}
	}
}

// resize applies a size within flowdproto's bounds; others are ignored.
func (s *Server) resize(sz Size) {
	if !flowdproto.ValidTerminalSize(sz.Cols, sz.Rows) {
		return
	}
	s.sizeMu.Lock()
	err := s.shell.Resize(sz.Cols, sz.Rows)
	s.sizeMu.Unlock()
	if err == nil {
		s.mu.Lock()
		s.info.Size = sz
		s.mu.Unlock()
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
