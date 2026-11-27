package term

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host/claude"
)

func TestRing(t *testing.T) {
	r := NewRing(8)
	r.Write([]byte("abc"))
	if got := string(r.Bytes()); got != "abc" {
		t.Fatalf("ring = %q", got)
	}
	r.Write([]byte("defgh"))
	if got := string(r.Bytes()); got != "abcdefgh" {
		t.Fatalf("full ring = %q", got)
	}
	r.Write([]byte("ij"))
	if got := string(r.Bytes()); got != "cdefghij" || r.Len() != 8 {
		t.Fatalf("wrapped ring = %q", got)
	}
	r.Write([]byte("0123456789"))
	if got := string(r.Bytes()); got != "23456789" {
		t.Fatalf("oversize write = %q", got)
	}
	if NewRing(0).Bytes() == nil {
		t.Fatal("an empty ring gives an empty slice")
	}
}

func TestRingStartsOnACharacter(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte("aé")) // 3 bytes
	r.Write([]byte("bcd"))
	// Held: the second byte of é, then "bcd": the replay starts at 'b'.
	if got := string(r.Bytes()); got != "bcd" {
		t.Fatalf("ring after cut = %q", got)
	}
	r = NewRing(5)
	r.Write([]byte("aé"))
	r.Write([]byte("bcd"))
	if got := string(r.Bytes()); got != "ébcd" {
		t.Fatalf("ring cut before a character = %q", got)
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestFrames(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, FrameOutput, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&b, FrameExit, Exit{Code: 3}); err != nil {
		t.Fatal(err)
	}
	typ, p, err := ReadFrame(&b)
	if err != nil || typ != FrameOutput || string(p) != "hi" {
		t.Fatalf("frame = %q %q %v", typ, p, err)
	}
	typ, p, err = ReadFrame(&b)
	if err != nil || typ != FrameExit || string(p) != `{"code":3}` {
		t.Fatalf("frame = %q %q %v", typ, p, err)
	}
	if _, _, err := ReadFrame(&b); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v", err)
	}
	if WriteFrame(&b, FrameInput, make([]byte, MaxFrame+1)) == nil {
		t.Fatal("an oversize frame was written")
	}
	b.Reset()
	b.Write([]byte{FrameInput, 0xff, 0xff, 0xff, 0xff})
	if _, _, err := ReadFrame(&b); !errors.Is(err, ErrFrame) {
		t.Fatalf("oversize header = %v", err)
	}
	b.Reset()
	b.Write([]byte{FrameInput, 0, 0, 0, 5, 'a'})
	if _, _, err := ReadFrame(&b); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short frame = %v", err)
	}
}

// fakeShell echoes its input as output and exits with the code sent on exit.
type fakeShell struct {
	out    *io.PipeReader
	outW   *io.PipeWriter
	mu     sync.Mutex
	input  bytes.Buffer
	sizes  []Size
	exit   chan int
	hungUp chan struct{}
	once   sync.Once
}

func newFakeShell() *fakeShell {
	r, w := io.Pipe()
	return &fakeShell{out: r, outW: w, exit: make(chan int, 1), hungUp: make(chan struct{})}
}

func (f *fakeShell) Read(p []byte) (int, error) { return f.out.Read(p) }
func (f *fakeShell) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.input.Write(p)
	f.mu.Unlock()
	return f.outW.Write(p)
}
func (f *fakeShell) Close() error { return f.out.Close() }
func (f *fakeShell) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes = append(f.sizes, Size{cols, rows})
	return nil
}
func (f *fakeShell) Wait() int { return <-f.exit }
func (f *fakeShell) Pid() int  { return 42 }
func (f *fakeShell) Hangup() {
	f.once.Do(func() { close(f.hungUp); f.end(129) })
}

// end makes the shell exit: output ends, then the process.
func (f *fakeShell) end(code int) {
	_ = f.outW.Close()
	f.exit <- code
}

func (f *fakeShell) emit(t *testing.T, s string) {
	t.Helper()
	if _, err := f.outW.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeShell) lastSize() (Size, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sizes) == 0 {
		return Size{}, 0
	}
	return f.sizes[len(f.sizes)-1], len(f.sizes)
}

func serve(t *testing.T, sh Shell, ring int) (*Server, string) {
	t.Helper()
	sock := filepath.Join(shortTemp(t), "t.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(sh, Info{ID: "t1", Session: "s1", Dir: "/home/flow", Size: Size{80, 24}}, ring)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = l.Close() })
	return srv, sock
}

// shortTemp is a temporary directory with a path short enough for a unix socket on macOS (104 bytes).
func shortTemp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "term")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// readUntil reads from c until the output holds want.
func readUntil(t *testing.T, c io.Reader, want string) string {
	t.Helper()
	var got []byte
	buf := make([]byte, 4096)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(got), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output %q never held %q", got, want)
		}
		n, err := c.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("read after %q: %v", got, err)
		}
	}
	return string(got)
}

func TestAttachReplaysTheRingThenLive(t *testing.T) {
	sh := newFakeShell()
	_, sock := serve(t, sh, RingSize)
	ctx := context.Background()
	sh.emit(t, "before\r\n")
	info, err := Query(ctx, sock)
	if err != nil || info.ID != "t1" || info.Session != "s1" || info.Pid != 42 || info.Protocol != Protocol {
		t.Fatalf("info = %+v, %v", info, err)
	}
	a, err := Attach(ctx, sock, &Size{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	waitFor(t, func() bool { info, _ := Query(ctx, sock); return info.Size == Size{120, 40} })
	readUntil(t, a, "before")
	b, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	readUntil(t, b, "before")
	// Input from one client reaches the shell; its echo reaches both.
	if _, err := a.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, a, "ls")
	readUntil(t, b, "ls")
	if err := b.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err := b.Resize(10000, 30); err != nil { // out of bounds: ignored
		t.Fatal(err)
	}
	waitFor(t, func() bool { s, n := sh.lastSize(); return s == Size{100, 30} && n == 2 })
	if info, _ := Query(ctx, sock); info.Size != (Size{100, 30}) {
		t.Fatalf("size after resize = %+v", info.Size)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExitReachesEveryClientAndLateOnes(t *testing.T) {
	sh := newFakeShell()
	srv, sock := serve(t, sh, RingSize)
	ctx := context.Background()
	a, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh.emit(t, "bye\r\n")
	readUntil(t, a, "bye")
	sh.end(3)
	buf := make([]byte, 64)
	var exit *flowdproto.TerminalExit
	for {
		_, err := a.Read(buf)
		if errors.As(err, &exit) {
			break
		}
		if err != nil {
			t.Fatalf("read = %v", err)
		}
	}
	if exit.Code != 3 {
		t.Fatalf("exit = %d", exit.Code)
	}
	<-srv.Done()
	if srv.Code() != 3 {
		t.Fatalf("code = %d", srv.Code())
	}
	// Attaching after the exit (before the unit is gone) gives the ring and the exit.
	b, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Info.Exited || b.Info.Code != 3 {
		t.Fatalf("late info = %+v", b.Info)
	}
	readUntil(t, b, "bye")
	if _, err := b.Read(buf); !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("late read = %v", err)
	}
	srv.Flush(time.Second)
}

// A flood reaches a client that reads slowly completely: the shell is throttled, nothing is dropped.
func TestFloodToASlowReaderArrivesWhole(t *testing.T) {
	sh := newFakeShell()
	_, sock := serve(t, sh, 1024)
	c, err := Attach(context.Background(), sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	const total = 8 << 20 // twice maxQueued, far beyond the socket buffers
	go func() {
		chunk := []byte(strings.Repeat("y", 4096))
		for sent := 0; sent < total; sent += len(chunk) {
			if _, err := sh.outW.Write(chunk); err != nil {
				return
			}
		}
		_, _ = sh.outW.Write([]byte("END"))
	}()
	got := 0
	buf := make([]byte, 8<<10)
	for got < total+3 {
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("dropped after %d of %d bytes: %v", got, total, err)
		}
		got += n
		if got%(1<<20) < n {
			time.Sleep(20 * time.Millisecond) // a browser drawing slowly
		}
	}
	sh.end(0)
}

func TestStuckClientIsDroppedNotBlocking(t *testing.T) {
	old := slowWait.Load()
	slowWait.Store(int64(200 * time.Millisecond))
	t.Cleanup(func() { slowWait.Store(old) })
	sh := newFakeShell()
	_, sock := serve(t, sh, 1024)
	ctx := context.Background()
	slow, err := Attach(ctx, sock, nil) // never reads
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slow.Close() }()
	fast, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fast.Close() }()
	line := strings.Repeat("x", 1000) + "\n"
	done := make(chan string)
	go func() {
		buf := make([]byte, 64<<10)
		var tail []byte
		for {
			n, err := fast.Read(buf)
			tail = append(tail, buf[:n]...)
			if len(tail) > 16 {
				tail = tail[len(tail)-16:]
			}
			if err != nil || strings.HasSuffix(string(tail), "END") {
				done <- string(tail)
				return
			}
		}
	}()
	// Far more than the socket buffers and the stuck client's queue hold: after slowWait the stuck one is dropped
	// and the other gets everything.
	for range 8000 {
		sh.emit(t, line)
	}
	sh.emit(t, "END")
	if tail := <-done; !strings.HasSuffix(tail, "END") {
		t.Fatalf("the reading client lost output: %q", tail)
	}
	// The slow client was dropped: after its buffered frames its connection ends.
	buf := make([]byte, 64<<10)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("slow client was never dropped")
		}
		if _, err := slow.Read(buf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				var ne net.Error
				if !errors.As(err, &ne) {
					t.Fatalf("slow read = %v", err)
				}
			}
			break
		}
	}
	sh.end(0)
}

func TestConcurrentWritersAndAttaches(t *testing.T) {
	sh := newFakeShell()
	srv, sock := serve(t, sh, 4096)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 500 {
			_, _ = sh.outW.Write([]byte{byte('a' + i%26)})
		}
	}()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Attach(ctx, sock, &Size{Cols: 90, Rows: 20})
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = c.Write([]byte("x"))
			_ = c.Close()
		}()
	}
	wg.Wait()
	sh.Hangup()
	<-sh.hungUp
	<-srv.Done()
	if srv.Code() != 129 {
		t.Fatalf("code = %d", srv.Code())
	}
}

func TestBadHelloIsClosed(t *testing.T) {
	sh := newFakeShell()
	_, sock := serve(t, sh, 64)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = WriteFrame(conn, FrameInput, []byte("no hello"))
	if _, _, err := ReadFrame(conn); err == nil {
		t.Fatal("a connection without hello got an answer")
	}
	sh.end(0)
}

func TestLoginShellAndEnv(t *testing.T) {
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	sh, _ := os.Executable()
	content := "root:x:0:0:root:/root:/bin/bash\nflow:x:65532:65532::/home/flow:" + sh + "\nnoshell:x:7:7::/:/nope\n"
	if err := os.WriteFile(passwd, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoginShell(passwd, 65532); got != sh {
		t.Fatalf("shell = %q", got)
	}
	if got := LoginShell(passwd, 7); got != "/bin/bash" && got != "/bin/sh" {
		t.Fatalf("fallback shell = %q", got)
	}
	if got := LoginShell(filepath.Join(dir, "missing"), 1); got == "" {
		t.Fatal("no shell")
	}
	// The unit's environment as flowd starts it: the EnvironmentFile's credential and MCP token among the rest.
	unit := []string{"HOME=/home/flow", "TERM=dumb", "COLORTERM=x", "GIT_AUTHOR_NAME=Jan",
		"CLAUDE_CODE_OAUTH_TOKEN=tok", "ANTHROPIC_API_KEY=key", claude.MCPTokenVar + "=mcp", "AWS_SECRET_ACCESS_KEY=aws"}
	for auth, want := range map[event.ClaudeAuth]string{
		event.ClaudeAuthSubscriptionLogin: "",
		event.ClaudeAuthSubscriptionToken: "CLAUDE_CODE_OAUTH_TOKEN=tok",
		event.ClaudeAuthAPIKey:            "ANTHROPIC_API_KEY=key",
	} {
		env := strings.Join(ShellEnv(unit, auth), " ")
		for _, must := range []string{"HOME=/home/flow", "TERM=xterm-256color", "COLORTERM=truecolor",
			"GIT_AUTHOR_NAME=Jan", "CLAUDE_CONFIG_DIR=/srv/flow-home/claude", "GH_CONFIG_DIR=/run/flow/credentials/gh"} {
			if !strings.Contains(env, must) {
				t.Errorf("%s: env lacks %s: %s", auth, must, env)
			}
		}
		for _, never := range []string{"TERM=dumb", "COLORTERM=x", claude.MCPTokenVar, "AWS_SECRET"} {
			if strings.Contains(env, never) {
				t.Errorf("%s: env has %s: %s", auth, never, env)
			}
		}
		creds := 0
		for _, c := range []string{"CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY="} {
			if strings.Contains(env, c) {
				creds++
			}
		}
		if want == "" && creds != 0 || want != "" && (creds != 1 || !strings.Contains(env, want)) {
			t.Errorf("%s: credentials in %s, want only %q", auth, env, want)
		}
	}
}

func TestExitFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "t1.exit")
	if _, ok := ReadExit(file); ok {
		t.Fatal("exit without a file")
	}
	if err := writeExit(file, 7); err != nil {
		t.Fatal(err)
	}
	if e, ok := ReadExit(file); !ok || e.Code != 7 {
		t.Fatalf("exit = %+v %v", e, ok)
	}
}

func TestRunWithAFakeShell(t *testing.T) {
	sh := newFakeShell()
	old := StartShell
	StartShell = func(s ShellSpec) (Shell, error) {
		if s.Argv0[0] != '-' || s.Dir != "/work" || s.Size != (Size{100, 30}) {
			t.Errorf("spec = %+v", s)
		}
		return sh, nil
	}
	t.Cleanup(func() { StartShell = old })
	sock := filepath.Join(shortTemp(t), "t1.sock")
	exitFile := strings.TrimSuffix(sock, ".sock") + ".exit"
	_ = writeExit(exitFile, 99) // a stale one is removed at start
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"--id", "t1", "--dir", "/work", "--socket", sock, "--exit-file", exitFile,
			"--cols", "100", "--rows", "30"}, discard())
	}()
	waitFor(t, func() bool { _, err := Query(context.Background(), sock); return err == nil })
	if _, ok := ReadExit(exitFile); ok {
		t.Fatal("stale exit file kept")
	}
	cancel() // systemd's stop: the shell is hung up
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e, ok := ReadExit(exitFile); !ok || e.Code != 129 {
		t.Fatalf("exit = %+v %v", e, ok)
	}
	if Run(context.Background(), []string{"--id", "x"}, discard()) == nil {
		t.Fatal("missing flags accepted")
	}
	if Run(context.Background(), []string{"--bogus"}, discard()) == nil {
		t.Fatal("unknown flag accepted")
	}
}
