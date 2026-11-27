package term

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/flow/internal/flowdproto"
)

// stdinFull is a shell whose input never drains (a program not reading stdin with the PTY's buffer full).
type stdinFull struct {
	*fakeShell
	release chan struct{}
}

func (s *stdinFull) Write(p []byte) (int, error) {
	<-s.release
	return len(p), nil
}

// A write that blocks must not hold up resizes or new attaches.
func TestBlockedInputHoldsUpNoResizeOrAttach(t *testing.T) {
	sh := &stdinFull{fakeShell: newFakeShell(), release: make(chan struct{})}
	defer close(sh.release)
	_, sock := serve(t, sh, 1024)
	ctx := context.Background()
	a, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if _, err := a.Write([]byte("a large paste")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // the server is now stuck in the shell's Write
	done := make(chan error, 1)
	go func() {
		b, err := Attach(ctx, sock, &Size{Cols: 90, Rows: 20})
		if err == nil {
			_ = b.Resize(91, 21)
			defer func() { _ = b.Close() }()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attach while input is blocked: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attach hung behind a blocked input write")
	}
	waitFor(t, func() bool { s, _ := sh.lastSize(); return s == Size{91, 21} })
	sh.end(0)
}

// A client that goes away while the pump waits for room in its queue must wake the pump at once, not after
// slowWait.
func TestClosingAStuckClientWakesThePump(t *testing.T) {
	sh := newFakeShell()
	_, sock := serve(t, sh, 1024)
	ctx := context.Background()
	stuck, err := Attach(ctx, sock, nil) // never reads
	if err != nil {
		t.Fatal(err)
	}
	fast, err := Attach(ctx, sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fast.Close() }()
	got := make(chan struct{})
	go func() {
		buf := make([]byte, 64<<10)
		var tail []byte
		for {
			n, err := fast.Read(buf)
			tail = append(tail, buf[:n]...)
			if len(tail) > 8 {
				tail = tail[len(tail)-8:]
			}
			if err != nil {
				return
			}
			if strings.HasSuffix(string(tail), "END") {
				close(got)
				return
			}
		}
	}()
	emitted := make(chan struct{})
	go func() {
		line := []byte(strings.Repeat("z", 4000) + "\n")
		for range 3000 { // 12 MB: far beyond the stuck client's queue and socket buffers
			if _, err := sh.outW.Write(line); err != nil {
				return
			}
		}
		_, _ = sh.outW.Write([]byte("END"))
		close(emitted)
	}()
	time.Sleep(300 * time.Millisecond) // the pump now waits for the stuck client
	start := time.Now()
	_ = stuck.Close()
	select {
	case <-got:
	case <-time.After(4 * time.Second): // slowWait is 5 s
		t.Fatal("the pump waited out slowWait for a client that had gone")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	<-emitted
	sh.end(0)
}

// Once Flush has begun no attach is counted any more (a WaitGroup must not gain from zero while waited on): it is
// closed unanswered, and the client reads the exit file instead. Attaches racing the start of Flush stay safe.
func TestAttachDuringFlush(t *testing.T) {
	for range 20 {
		sh := newFakeShell()
		srv, sock := serve(t, sh, 1024)
		sh.end(0)
		<-srv.Done()
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if c, err := Attach(context.Background(), sock, nil); err == nil {
					_ = c.Close()
				}
			}()
		}
		srv.Flush(time.Second)
		wg.Wait()
		if c, err := Attach(context.Background(), sock, nil); err == nil {
			_ = c.Close()
			t.Fatal("an attach after Flush began was served")
		}
	}
}

// After the shell ends, Run serves a slow client its whole queue (and the exit) before the process ends.
func TestRunServesASlowClientToTheEnd(t *testing.T) {
	sh := newFakeShell()
	old := StartShell
	StartShell = func(ShellSpec) (Shell, error) { return sh, nil }
	t.Cleanup(func() { StartShell = old })
	sock := filepath.Join(shortTemp(t), "t1.sock")
	returned := make(chan struct{})
	go func() {
		_ = Run(context.Background(), []string{"--id", "t1", "--dir", "/work", "--socket", sock,
			"--exit-file", sock + ".exit"}, discard())
		close(returned)
	}()
	waitFor(t, func() bool { _, err := Query(context.Background(), sock); return err == nil })
	c, err := Attach(context.Background(), sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	const total = 4 << 20
	go func() {
		chunk := []byte(strings.Repeat("w", 32<<10))
		for sent := 0; sent < total; sent += len(chunk) {
			_, _ = sh.outW.Write(chunk)
		}
		sh.end(0)
	}()
	var got, atReturn atomic.Int64
	atReturn.Store(-1)
	go func() {
		<-returned
		atReturn.Store(got.Load())
	}()
	buf := make([]byte, 64<<10)
	for {
		n, err := c.Read(buf)
		got.Add(int64(n))
		var exit *flowdproto.TerminalExit
		if errors.As(err, &exit) {
			break
		}
		if err != nil {
			t.Fatalf("lost the end after %d of %d bytes: %v", got.Load(), total, err)
		}
		time.Sleep(40 * time.Millisecond) // a slow client: about 1.6 MB/s
	}
	<-returned
	if got.Load() < total {
		t.Fatalf("got %d of %d bytes", got.Load(), total)
	}
	// What was still unread when the process would have ended fits in the socket's buffers.
	if r := atReturn.Load(); r < 0 || total-r > 1<<20 {
		t.Fatalf("Run returned with %d bytes not yet delivered", total-r)
	}
}
