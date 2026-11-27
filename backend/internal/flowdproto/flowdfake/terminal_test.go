package flowdfake

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/trick77/flow/internal/flowdproto"
)

func TestFakeTerminal(t *testing.T) {
	ctx := context.Background()
	f := New("sb")
	spec := flowdproto.TerminalSpec{ID: "t1", Session: "s1", Dir: "/home/flow", Cols: 80, Rows: 24}
	if _, err := f.EnsureTerminal(ctx, spec); err != nil {
		t.Fatal(err)
	}
	a, err := f.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if f.TerminalSize("t1") != (flowdproto.TerminalSize{Cols: 100, Rows: 30}) {
		t.Fatalf("size = %+v", f.TerminalSize("t1"))
	}
	_, _ = a.Write([]byte("one "))
	// A second attach gets the scrollback, then live output.
	b, _ := f.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{})
	_, _ = b.Write([]byte("two"))
	buf := make([]byte, 64)
	if n, _ := io.ReadAtLeast(b, buf, len("one two")); string(buf[:n]) != "one two" {
		t.Fatalf("second attach read %q", buf[:n])
	}
	_ = b.Resize(90, 20)
	if f.TerminalSize("t1") != (flowdproto.TerminalSize{Cols: 90, Rows: 20}) {
		t.Fatalf("size after resize = %+v", f.TerminalSize("t1"))
	}
	// Idempotent while running.
	if st, _ := f.EnsureTerminal(ctx, spec); st.Exited {
		t.Fatal("running terminal reported exited")
	}
	f.DropTerminalConns("t1")
	if _, err := io.ReadAll(b); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("dropped conn = %v", err)
	}
	c, _ := f.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{})
	f.ExitTerminal("t1", 2)
	var exit *flowdproto.TerminalExit
	if got, err := io.ReadAll(c); string(got) != "one two" || !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("exit read %q %v", got, err)
	}
	if _, err := c.Write([]byte("x")); err == nil {
		t.Fatal("write after exit")
	}
	if _, err := f.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{}); !errors.As(err, &exit) {
		t.Fatalf("attach after exit = %v", err)
	}
	if h, _ := f.Hello(ctx); len(h.Terminals) != 1 || !h.Terminals[0].Exited || h.Terminals[0].ExitCode != 2 {
		t.Fatalf("hello terminals = %+v", h.Terminals)
	}
	// Ensure again restarts it with an empty scrollback.
	if st, _ := f.EnsureTerminal(ctx, spec); st.Exited {
		t.Fatal("restart still exited")
	}
	d, _ := f.AttachTerminal(ctx, "t1", flowdproto.TerminalSize{})
	_ = f.CloseTerminal(ctx, "t1")
	if got, err := io.ReadAll(d); len(got) != 0 || err != nil {
		t.Fatalf("closed terminal read %q %v", got, err)
	}
	f.ExitTerminal("missing", 1)
	f.DropTerminalConns("missing")
	if f.TerminalSize("missing") != (flowdproto.TerminalSize{}) {
		t.Fatal("size of a missing terminal")
	}
}
