// Package eventlog writes and reads a session's append-only event log (layout.EventLog): one event.Event per line,
// seq from 1 without gaps, a single writer holding an exclusive flock, a torn last line truncated by the writer on
// open, readers that seek by a sparse seq index and follow by polling, woken early by the host's appended frames.
// docs/events.md has the format. Owner: flowd runtime area.
package eventlog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/trick77/flow/internal/event"
)

// ErrLocked is Open's refusal when another writer holds the log.
var ErrLocked = errors.New("eventlog: held by another writer")

// SyncInterval bounds how long an appended line may wait for its fsync while a turn runs.
const SyncInterval = time.Second

// Writer is the one writer of a log.
type Writer struct {
	mu   sync.Mutex
	f    logFile
	last int64
	// end is the offset after the last complete line: a failed write is cut back to it.
	end    int64
	dirty  bool
	synced time.Time
}

// logFile is what the Writer needs of its file (an *os.File; tests put a failing one in).
type logFile interface {
	io.Writer
	Truncate(size int64) error
	Seek(offset int64, whence int) (int64, error)
	Sync() error
	Close() error
}

// Open opens or creates the log at path for appending, takes the exclusive lock, cuts a torn last line and returns
// every complete event in it, so the caller can rebuild its state. Directories are created as needed.
func Open(path string) (*Writer, []event.Event, error) {
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, nil, err
	}
	// 0644: flowd (root) and Flow's readers only read it; the log is the conversation, not a secret beyond the
	// sandbox.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644) //nolint:gosec // see above
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, nil, err
	}
	events, good, err := scan(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if err := f.Truncate(good); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	w := &Writer{f: f, end: good, synced: time.Now()}
	if n := len(events); n > 0 {
		w.last = events[n-1].Seq
	}
	return w, events, nil
}

// scan reads complete lines from the start; good is the offset after the last complete, parseable line.
func scan(r io.ReadSeeker) ([]event.Event, int64, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	var events []event.Event
	var good int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			e, perr := event.ParseLine(line)
			if perr != nil {
				// Garbage from a crash is cut, not an error: Open truncates to good.
				return events, good, nil //nolint:nilerr // see above
			}
			events = append(events, e)
			good += int64(len(line))
		}
		if err == io.EOF {
			return events, good, nil
		}
		if err != nil {
			return nil, 0, err
		}
	}
}

// LastSeq is the seq of the last event written.
func (w *Writer) LastSeq() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// Append numbers e with the next seq and writes it as one line. A turn.ended is synced at once; other lines within
// SyncInterval (Sync is called by the writer's owner on a ticker).
func (w *Writer) Append(e event.Event) (event.Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return event.Event{}, os.ErrClosed
	}
	e.Seq = w.last + 1
	line, err := e.Line()
	if err != nil {
		return event.Event{}, err
	}
	if n, err := w.f.Write(line); err != nil || n != len(line) {
		if err == nil {
			err = io.ErrShortWrite
		}
		// A fragment left in the file would glue the next line onto itself and stop every reader there. Cut it;
		// if even that fails, close: the next Open truncates the torn tail.
		if terr := w.cut(); terr != nil {
			_ = w.f.Close()
			w.f = nil
			return event.Event{}, errors.Join(err, terr)
		}
		return event.Event{}, err
	}
	w.end += int64(len(line))
	w.last = e.Seq
	w.dirty = true
	if e.Kind == event.KindTurnEnded || time.Since(w.synced) >= SyncInterval {
		w.syncLocked()
	}
	return e, nil
}

// cut drops whatever follows the last complete line; callers hold mu.
func (w *Writer) cut() error {
	if err := w.f.Truncate(w.end); err != nil {
		return err
	}
	_, err := w.f.Seek(w.end, io.SeekStart)
	return err
}

// Sync flushes appended lines to disk if any are pending.
func (w *Writer) Sync() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.syncLocked()
}

func (w *Writer) syncLocked() {
	if w.dirty && w.f != nil {
		_ = w.f.Sync()
		w.dirty = false
	}
	w.synced = time.Now()
}

// Close syncs and releases the lock.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	w.syncLocked()
	err := w.f.Close()
	w.f = nil
	return err
}

// indexEvery is the sparse index's stride: one offset per this many lines.
const indexEvery = 256

// Reader reads one log by seq. It keeps a sparse index (seq -> offset) built as it reads, so a later read from a
// high seq seeks instead of rescanning.
type Reader struct {
	path  string
	mu    sync.Mutex
	index []int64 // index[i] is the offset of the line with seq i*indexEvery+1
}

// NewReader returns a reader of the log at path.
func NewReader(path string) *Reader { return &Reader{path: path, index: []int64{0}} }

// PollInterval is how often Follow looks for new lines when nothing wakes it.
const PollInterval = 250 * time.Millisecond

// Follow yields the events after seq after in order, then every new one as it is appended, until ctx ends or
// yield returns false. wake (may be nil) makes it look at once. A missing file is waited for.
func (r *Reader) Follow(ctx context.Context, after int64, wake <-chan struct{}, yield func(event.Event) bool) error {
	var f *os.File
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	var offset int64
	next := after + 1
	for {
		if f == nil {
			var err error
			f, err = os.Open(r.path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if f != nil {
				offset, next = r.seek(after)
			}
		}
		if f != nil {
			var stop bool
			var err error
			offset, next, stop, err = r.readFrom(f, offset, next, after, yield)
			if err != nil || stop {
				return err
			}
		}
		t := time.NewTimer(PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// Read returns the events after seq after that are in the log now (at most limit, 0 for all).
func (r *Reader) Read(after int64, limit int) ([]event.Event, error) {
	f, err := os.Open(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	offset, next := r.seek(after)
	var out []event.Event
	_, _, _, err = r.readFrom(f, offset, next, after, func(e event.Event) bool {
		out = append(out, e)
		return limit <= 0 || len(out) < limit
	})
	return out, err
}

// seek returns the indexed offset at or before the line after seq after, and the seq found there.
func (r *Reader) seek(after int64) (offset, seq int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := min(int(after/indexEvery), len(r.index)-1)
	return r.index[i], int64(i)*indexEvery + 1
}

// readFrom reads complete lines from offset, the line there having seq next; it yields those after after.
func (r *Reader) readFrom(f *os.File, offset, next, after int64, yield func(event.Event) bool) (int64, int64, bool, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, next, false, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return offset, next, false, nil // end, or a line still being written
		}
		e, perr := event.ParseLine(bytes.Clone(line))
		if perr != nil {
			return offset, next, false, perr
		}
		r.note(e.Seq, offset)
		offset += int64(len(line))
		next = e.Seq + 1
		if e.Seq > after && !yield(e) {
			return offset, next, true, nil
		}
		if err != nil {
			// EOF after a complete line: the caller follows on from offset.
			return offset, next, false, nil //nolint:nilerr // see above
		}
	}
}

func (r *Reader) note(seq, offset int64) {
	if (seq-1)%indexEvery != 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := int((seq - 1) / indexEvery); i == len(r.index) {
		r.index = append(r.index, offset)
	}
}
