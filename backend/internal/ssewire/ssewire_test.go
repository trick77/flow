package ssewire

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s, err := Open(w)
		if err != nil {
			t.Error(err)
			return
		}
		defer s.Close()
		_ = s.Send(Frame{ID: "1", Event: "flow", Data: `{"a":1}`}, Frame{Event: "flow", Data: "two\nlines"})
		_ = s.Comment()
		_ = s.Send()
		_ = s.Send(Frame{ID: "3\n", Data: "x"})
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	var got []Frame
	if err := Parse(resp.Body, func(f Frame) bool { got = append(got, f); return true }); err != nil {
		t.Fatal(err)
	}
	want := []Frame{{ID: "1", Event: "flow", Data: `{"a":1}`}, {Event: "flow", Data: "two\nlines"}, {ID: "3", Data: "x"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frame %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParse_stopsWhenAsked(t *testing.T) {
	n := 0
	err := Parse(strings.NewReader("data: a\n\ndata: b\n\n"), func(Frame) bool { n++; return false })
	if err != nil || n != 1 {
		t.Errorf("n = %d, err = %v", n, err)
	}
}

type failing struct{ http.ResponseWriter }

func (failing) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (failing) Flush()                    {}

func TestWriter_closesOnFailure(t *testing.T) {
	s, err := Open(httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	s.w = failing{s.w}
	if err := s.Send(Frame{Data: "x"}); err == nil {
		t.Fatal("write to a failing writer succeeded")
	}
	if err := s.Comment(); !errors.Is(err, ErrClosed) {
		t.Errorf("after failure: %v, want ErrClosed", err)
	}
	s.Close()
}
