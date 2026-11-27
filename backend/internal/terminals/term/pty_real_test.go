//go:build incus && linux

package term

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
)

// TestRealPTY runs a real shell on a real PTY (Linux, the incus tag for the real machine): output, input, resize as
// the shell sees it (stty size), the ring after detaching, an exit status, and a hangup. Build with -tags incus.
func TestRealPTY(t *testing.T) {
	dir := t.TempDir()
	sh := LoginShell("/etc/passwd", os.Getuid())
	shell, err := StartShell(ShellSpec{Path: sh, Argv0: "-" + filepath.Base(sh), Dir: dir,
		Env:  ShellEnv([]string{"HOME=" + dir, "PATH=/usr/bin:/bin"}, event.ClaudeAuthSubscriptionLogin),
		Size: Size{Cols: 80, Rows: 24}})
	if err != nil {
		t.Fatal(err)
	}
	srv, sock := serve(t, shell, RingSize)
	ctx := context.Background()
	a, err := Attach(ctx, sock, &Size{Cols: 101, Rows: 33})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write([]byte("stty size; pwd; echo ma''rker-$((6*7))\r")); err != nil {
		t.Fatal(err)
	}
	out := readUntil(t, a, "marker-42")
	if !strings.Contains(out, "33 101") || !strings.Contains(out, dir) {
		t.Fatalf("output %q lacks the size or the directory", out)
	}
	_ = a.Close()
	b, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	readUntil(t, b, "marker-42") // the ring after a detach
	_ = b.Resize(120, 40)
	_, _ = b.Write([]byte("stty size\r"))
	readUntil(t, b, "40 120")
	_, _ = b.Write([]byte("exit 7\r"))
	buf := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := b.Read(buf)
		var exit *flowdproto.TerminalExit
		if errors.As(err, &exit) {
			if exit.Code != 7 {
				t.Fatalf("exit = %d", exit.Code)
			}
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("no exit: %v", err)
		}
	}
	<-srv.Done()

	// A hangup (the unit stopped) ends the shell with SIGHUP: 128 + 1.
	shell, err = StartShell(ShellSpec{Path: sh, Argv0: "-" + filepath.Base(sh), Dir: dir,
		Env:  ShellEnv([]string{"HOME=" + dir, "PATH=/usr/bin:/bin"}, event.ClaudeAuthSubscriptionLogin),
		Size: Size{Cols: 80, Rows: 24}})
	if err != nil {
		t.Fatal(err)
	}
	srv2 := NewServer(shell, Info{ID: "t2"}, RingSize)
	time.Sleep(200 * time.Millisecond)
	shell.Hangup()
	select {
	case <-srv2.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("hangup did not end the shell")
	}
	if srv2.Code() != 129 {
		t.Fatalf("hangup code = %d", srv2.Code())
	}
}
