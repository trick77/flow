package eventlog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
)

func ev(t *testing.T, kind event.EventKind, payload any) event.Event {
	t.Helper()
	e, err := event.New("s1", kind, time.Unix(0, 0), payload)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestWriter_numbersLocksAndCutsATornLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1", "events.jsonl")
	w, evs, err := Open(path)
	if err != nil || len(evs) != 0 {
		t.Fatalf("Open = %v, %d events", err, len(evs))
	}
	for range 3 {
		if _, err := w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "working"})); err != nil {
			t.Fatal(err)
		}
	}
	e, err := w.Append(ev(t, event.KindTurnEnded, event.TurnEnded{Outcome: "completed"}))
	if err != nil || e.Seq != 4 || w.LastSeq() != 4 {
		t.Fatalf("Append = %d, %v; last %d", e.Seq, err, w.LastSeq())
	}
	if _, _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Errorf("second writer: %v, want ErrLocked", err)
	}
	w.Sync()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(e); err == nil {
		t.Error("append after close succeeded")
	}
	if err := w.Close(); err != nil {
		t.Error(err)
	}
	// A crash mid-write leaves half a line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"v":1,"seq":5,"ki`)
	_ = f.Close()

	w, evs, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if len(evs) != 4 || w.LastSeq() != 4 {
		t.Fatalf("reopen: %d events, last %d", len(evs), w.LastSeq())
	}
	e, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "done"}))
	if e.Seq != 5 {
		t.Errorf("seq after the torn line = %d, want 5", e.Seq)
	}
	got, err := NewReader(path).Read(0, 0)
	if err != nil || len(got) != 5 {
		t.Fatalf("Read = %d, %v", len(got), err)
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d", i, e.Seq)
		}
	}
}

// halfWriter writes half of the next line and fails, as a full disk does.
type halfWriter struct {
	logFile
	failed bool
}

func (h *halfWriter) Write(p []byte) (int, error) {
	if h.failed {
		return h.logFile.Write(p)
	}
	h.failed = true
	n, _ := h.logFile.Write(p[:len(p)/2])
	return n, errors.New("no space left on device")
}

// A failed write leaves no fragment: the next line starts clean and every reader keeps reading.
func TestWriter_cutsAFailedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "idle"}))
	w.f = &halfWriter{logFile: w.f}
	if _, err := w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "working"})); err == nil {
		t.Fatal("a failed write reported success")
	}
	e, err := w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "done"}))
	if err != nil || e.Seq != 2 {
		t.Fatalf("append after the failure: seq %d, %v (the failed one must not use a seq)", e.Seq, err)
	}
	got, err := NewReader(path).Read(0, 0)
	if err != nil || len(got) != 2 || got[1].Seq != 2 {
		t.Fatalf("read after a failed write: %d events, %v", len(got), err)
	}
}

func TestOpen_cutsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	w, _, _ := Open(path)
	_, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "idle"}))
	_ = w.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("not json\n")
	_ = f.Close()
	w, evs, err := Open(path)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Open = %d, %v", len(evs), err)
	}
	_ = w.Close()
	if _, _, err := Open(filepath.Join(path, "x")); err == nil {
		t.Error("opened a log below a file")
	}
}

func TestReader_seeksAndFollows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	r := NewReader(path)
	if got, err := r.Read(0, 0); err != nil || got != nil {
		t.Fatalf("missing log: %v, %v", got, err)
	}
	w, _, _ := Open(path)
	defer w.Close()
	for range 600 {
		_, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "working"}))
	}
	if got, _ := r.Read(0, 10); len(got) != 10 || got[9].Seq != 10 {
		t.Fatalf("limited read: %d", len(got))
	}
	if got, _ := r.Read(0, 0); len(got) != 600 {
		t.Fatalf("full read: %d", len(got))
	}
	if len(r.index) != 3 {
		t.Errorf("index has %d entries, want 3", len(r.index))
	}
	got, _ := r.Read(590, 0)
	if len(got) != 10 || got[0].Seq != 591 {
		t.Fatalf("read after 590: %d from %d", len(got), got[0].Seq)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wake := make(chan struct{}, 1)
	var seen []int64
	done := make(chan error, 1)
	go func() {
		done <- r.Follow(ctx, 598, wake, func(e event.Event) bool {
			seen = append(seen, e.Seq)
			return e.Seq < 602
		})
	}()
	time.Sleep(50 * time.Millisecond)
	_, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "done"}))
	wake <- struct{}{}
	_, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "done"}))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 || seen[0] != 599 || seen[3] != 602 {
		t.Errorf("followed %v", seen)
	}
}

func TestFollow_waitsForTheFileAndStopsWithTheContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan int64, 4)
	done := make(chan error, 1)
	go func() {
		done <- NewReader(path).Follow(ctx, 0, nil, func(e event.Event) bool { got <- e.Seq; return true })
	}()
	time.Sleep(30 * time.Millisecond)
	w, _, _ := Open(path)
	defer w.Close()
	_, _ = w.Append(ev(t, event.KindSessionStatus, event.SessionStatusChanged{Status: "idle"}))
	select {
	case s := <-got:
		if s != 1 {
			t.Errorf("seq %d", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event from a file created after Follow started")
	}
	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}
}
