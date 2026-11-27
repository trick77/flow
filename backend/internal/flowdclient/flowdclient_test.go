package flowdclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
)

func TestUnsupportedAndErrors(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/hello":
			_, _ = w.Write([]byte(`{"protocol":9,"minProtocol":9}`))
		case "/v1/sessions/busy/commands":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"busy"}`))
		case "/v1/sessions/s/events":
			_, _ = w.Write([]byte("data: not json\n\nid: 1\ndata: {\"v\":1,\"seq\":1,\"kind\":\"notice\"}\n\n"))
		default:
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`garbage`))
		}
	}))
	defer srv.Close()
	c := NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(srv.URL, "http://"), Token: "t"})
	if _, err := c.Hello(ctx); !errors.Is(err, flowdproto.ErrProtocol) {
		t.Errorf("newer flowd: %v", err)
	}
	if _, err := c.Command(ctx, flowdEventCommand("busy")); !errors.Is(err, flowdproto.ErrUnavailable) {
		t.Errorf("busy: %v", err)
	}
	var fe *Error
	if _, err := c.Session(ctx, "x"); !errors.As(err, &fe) || fe.Body.Code != flowdproto.CodeInternal || fe.Error() == "" {
		t.Errorf("garbage error body: %v", err)
	}
	var got []int64
	var last error
	for e, err := range c.Events(ctx, "s", 0) {
		if err != nil {
			last = err
			break
		}
		got = append(got, e.Seq)
	}
	if len(got) != 1 || !errors.Is(last, flowdproto.ErrUnavailable) {
		t.Errorf("events %v, end %v", got, last)
	}
	for range c.Events(ctx, "s", 0) {
		break // a consumer that stops early ends the stream quietly
	}
	for _, err := range c.Events(ctx, "x", 0) {
		if err == nil {
			t.Error("a 500 stream yielded an event")
		}
	}
	dead := NewDialer().Dial(flowdproto.Endpoint{Address: "127.0.0.1:1", Token: "t"})
	if _, err := dead.Hello(ctx); !errors.Is(err, flowdproto.ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
	for _, err := range dead.Events(ctx, "s", 0) {
		if !errors.Is(err, flowdproto.ErrUnavailable) {
			t.Errorf("unreachable stream: %v", err)
		}
	}
	unsupported := []error{
		c.ReplyControl(ctx, flowdproto.ControlReply{}),
		func() error { _, err := c.StartJob(ctx, flowdproto.JobSpec{}); return err }(),
		func() error { _, err := c.Job(ctx, "j"); return err }(),
		func() error { _, err := c.JobLog(ctx, "j", 0); return err }(),
	}
	for _, err := range c.Control(ctx) {
		unsupported = append(unsupported, err)
	}
	for i, err := range unsupported {
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("call %d: %v", i, err)
		}
	}
	w := httptest.NewRecorder()
	c.Port(3000).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("port: %d", w.Code)
	}
	if !errors.Is(&Error{Body: flowdproto.ErrorBody{Code: flowdproto.CodeInvalid}}, flowdproto.ErrInvalid) ||
		!errors.Is(&Error{Body: flowdproto.ErrorBody{Code: flowdproto.CodeRepoBroken}}, flowdproto.ErrRepoBroken) ||
		!errors.Is(&Error{Body: flowdproto.ErrorBody{Code: flowdproto.CodeNotImplemented}}, flowdproto.ErrNotImplemented) {
		t.Error("invalid or repo_broken not mapped")
	}
	if (&Error{Body: flowdproto.ErrorBody{Code: "other"}}).Is(flowdproto.ErrNotFound) {
		t.Error("unknown code matched")
	}
}

// The calls that clone or fetch run under the bring-up's deadline, not the short one of every other call: a clone
// longer than the short one would be cut off and start from zero on every try.
func TestCloneCallsOutliveTheCallTimeout(t *testing.T) {
	short, long := callTimeout, cloneTimeout
	callTimeout, cloneTimeout = 50*time.Millisecond, 10*time.Second
	t.Cleanup(func() { callTimeout, cloneTimeout = short, long })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond): // a slow clone
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ctx := t.Context()
	c := NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(srv.URL, "http://"), Token: "t"})
	if _, err := c.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "o/r"}); err != nil {
		t.Errorf("EnsureRepo: %v", err)
	}
	if _, err := c.EnsureSession(ctx, flowdproto.SessionSpec{ID: "s"}); err != nil {
		t.Errorf("EnsureSession: %v", err)
	}
	if _, err := c.AddWorktree(ctx, "s", flowdproto.WorktreeSpec{Repo: "o/r"}); err != nil {
		t.Errorf("AddWorktree: %v", err)
	}
	if _, err := c.Session(ctx, "s"); !errors.Is(err, flowdproto.ErrUnavailable) {
		t.Errorf("a short call outlived its timeout: %v", err)
	}
}

func flowdEventCommand(session string) event.Command {
	return event.Command{V: 1, ID: "c", Session: session, Kind: event.CmdTurnInterrupt, Data: []byte(`{}`)}
}
