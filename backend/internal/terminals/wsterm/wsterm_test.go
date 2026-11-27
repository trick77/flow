package wsterm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/trick77/flow/internal/flowdproto"
)

// pipeTerm echoes input; end makes Read return err after the pending output.
type pipeTerm struct {
	mu     sync.Mutex
	cond   *sync.Cond
	out    []byte
	err    error
	sizes  []flowdproto.TerminalSize
	closed bool
}

func newPipeTerm() *pipeTerm {
	p := &pipeTerm{}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *pipeTerm) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.out) == 0 && p.err == nil && !p.closed {
		p.cond.Wait()
	}
	if len(p.out) > 0 {
		n := copy(b, p.out)
		p.out = p.out[n:]
		return n, nil
	}
	if p.err != nil {
		return 0, p.err
	}
	return 0, io.EOF
}

func (p *pipeTerm) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.out = append(p.out, b...)
	p.mu.Unlock()
	p.cond.Broadcast()
	return len(b), nil
}

func (p *pipeTerm) Resize(cols, rows int) error {
	p.mu.Lock()
	p.sizes = append(p.sizes, flowdproto.TerminalSize{Cols: cols, Rows: rows})
	p.mu.Unlock()
	return nil
}

func (p *pipeTerm) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cond.Broadcast()
	return nil
}

func (p *pipeTerm) end(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
	p.cond.Broadcast()
}

func serve(t *testing.T, tc flowdproto.TerminalConn) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gone":
			Refuse(w, r, CloseGone, "gone")
		default:
			c, err := Accept(w, r)
			if err != nil {
				return
			}
			Serve(r.Context(), c, tc)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, srv *httptest.Server, path string) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Wrap(c)
}

func TestBridge(t *testing.T) {
	old := pingEvery.Load()
	pingEvery.Store(int64(20 * time.Millisecond)) // pings flow both ways while the test runs
	t.Cleanup(func() { pingEvery.Store(old) })
	pt := newPipeTerm()
	srv := serve(t, pt)
	c := dial(t, srv, "/")
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	var got []byte
	for len(got) < 5 {
		n, err := c.Read(buf) // smaller than a message: the rest stays pending
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != "hello" {
		t.Fatalf("echo = %q", got)
	}
	if err := c.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	_ = c.Resize(0, 30) // ignored on the far side
	time.Sleep(60 * time.Millisecond)
	pt.mu.Lock()
	sizes := pt.sizes
	pt.mu.Unlock()
	if len(sizes) != 1 || sizes[0] != (flowdproto.TerminalSize{Cols: 100, Rows: 30}) {
		t.Fatalf("sizes = %+v", sizes)
	}
	pt.end(&flowdproto.TerminalExit{Code: 5})
	var exit *flowdproto.TerminalExit
	if _, err := c.Read(buf); !errors.As(err, &exit) || exit.Code != 5 {
		t.Fatalf("exit = %v", err)
	}
	_ = c.Close()
}

func TestBridgeEnds(t *testing.T) {
	pt := newPipeTerm()
	srv := serve(t, pt)
	c := dial(t, srv, "/")
	pt.end(flowdproto.ErrNotFound)
	if _, err := c.Read(make([]byte, 8)); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Fatalf("gone = %v", err)
	}
	pt2 := newPipeTerm()
	c2 := dial(t, serve(t, pt2), "/")
	pt2.end(io.ErrUnexpectedEOF) // the terminal's connection broke: no exit, the client reconnects
	if _, err := c2.Read(make([]byte, 8)); !errors.Is(err, io.EOF) {
		t.Fatalf("broken = %v", err)
	}
	g := dial(t, srv, "/gone")
	if _, err := g.Read(make([]byte, 8)); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Fatalf("refused = %v", err)
	}
	// The client going away detaches the terminal.
	pt3 := newPipeTerm()
	c3 := dial(t, serve(t, pt3), "/")
	_ = c3.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pt3.mu.Lock()
		closed := pt3.closed
		pt3.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal not detached")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := c3.Write([]byte("x")); err == nil {
		t.Fatal("write after close")
	}
}
