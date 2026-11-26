// Package ssewire is server-sent events with ids, both ends: a Writer for flowd's and Flow's event streams and a
// Parse for flowdclient. kit/sse frames only event and data fields; the timeline contract needs "id: <seq>" on every
// durable event so EventSource and Flow resume with Last-Event-ID. The Writer keeps kit's discipline: headers and a
// flush at Open, a deadline on every write, a failed write closes the stream.
package ssewire

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WriteTimeout bounds each write and its flush, as kit/sse does.
const WriteTimeout = 10 * time.Second

// ErrClosed is a write after Close or after a failed write.
var ErrClosed = errors.New("ssewire: stream closed")

// Frame is one event. ID empty: no id field (ephemeral events). Event empty: the client's "message".
type Frame struct {
	ID    string
	Event string
	Data  string
}

// Writer is one open stream; its writes are serialized.
type Writer struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	mu     sync.Mutex
	closed bool
}

// Open sends the event-stream headers and the 200 and flushes them.
func Open(w http.ResponseWriter) (*Writer, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	s := &Writer{w: w, rc: http.NewResponseController(w)}
	w.WriteHeader(http.StatusOK)
	if err := s.flush(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Writer) flush() error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(WriteTimeout))
	defer func() { _ = s.rc.SetWriteDeadline(time.Time{}) }()
	return s.rc.Flush()
}

// Encode is the wire form of frames.
func Encode(frames ...Frame) string {
	var b strings.Builder
	for _, f := range frames {
		if f.ID != "" {
			b.WriteString("id: " + oneLine(f.ID) + "\n")
		}
		if f.Event != "" {
			b.WriteString("event: " + oneLine(f.Event) + "\n")
		}
		data := strings.ReplaceAll(f.Data, "\r\n", "\n")
		for line := range strings.SplitSeq(data, "\n") {
			b.WriteString("data: " + line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func oneLine(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

// Send writes frames in one write and one flush.
func (s *Writer) Send(frames ...Frame) error {
	if len(frames) == 0 {
		return nil
	}
	return s.write(Encode(frames...))
}

// Comment writes a keepalive comment line.
func (s *Writer) Comment() error { return s.write(": keepalive\n\n") }

func (s *Writer) write(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	_ = s.rc.SetWriteDeadline(time.Now().Add(WriteTimeout))
	_, err := io.WriteString(s.w, text)
	if err == nil {
		err = s.rc.Flush()
	}
	_ = s.rc.SetWriteDeadline(time.Time{})
	if err != nil {
		s.closed = true
	}
	return err
}

// Close refuses further writes.
func (s *Writer) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Parse reads frames from r until it ends or yield returns false. Comment lines are skipped; a frame without data
// is not dispatched, as in EventSource.
func Parse(r io.Reader, yield func(Frame) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 2<<20)
	var f Frame
	var data []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if data != nil {
				f.Data = strings.Join(data, "\n")
				if !yield(f) {
					return nil
				}
			}
			f, data = Frame{}, nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			f.ID = value
		case "event":
			f.Event = value
		case "data":
			data = append(data, value)
		}
	}
	return sc.Err()
}
