package flowdclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		func() error { _, err := c.Diff(ctx, "s", "r"); return err }(),
		c.ReplyControl(ctx, flowdproto.ControlReply{}),
		func() error { _, err := c.StartJob(ctx, flowdproto.JobSpec{}); return err }(),
		func() error { _, err := c.Job(ctx, "j"); return err }(),
		func() error { _, err := c.JobLog(ctx, "j", 0); return err }(),
		func() error { _, err := c.EnsureTerminal(ctx, flowdproto.TerminalSpec{}); return err }(),
		func() error { _, err := c.AttachTerminal(ctx, "t"); return err }(),
		c.CloseTerminal(ctx, "t"),
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
		!errors.Is(&Error{Body: flowdproto.ErrorBody{Code: flowdproto.CodeRepoBroken}}, flowdproto.ErrRepoBroken) {
		t.Error("invalid or repo_broken not mapped")
	}
	if (&Error{Body: flowdproto.ErrorBody{Code: "other"}}).Is(flowdproto.ErrNotFound) {
		t.Error("unknown code matched")
	}
}

func flowdEventCommand(session string) event.Command {
	return event.Command{V: 1, ID: "c", Session: session, Kind: event.CmdTurnInterrupt, Data: []byte(`{}`)}
}
