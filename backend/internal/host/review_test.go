package host

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/hostproto"
)

// Finding 3: an immediate host.shutdown during a turn ends the turn as interrupted, carrying the shutdown's id; the
// next start finds nothing to call lost.
func TestShutdown_endsTheTurnAsInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "work"))
	<-f.got
	if _, err := r.h.Command(cmd(t, "sd1", event.CmdHostShutdown, event.HostShutdown{Reason: event.ShutdownReasonStop})); err != nil {
		t.Fatal(err)
	}
	evs := r.events()
	var ended *event.Event
	for i := range evs {
		if evs[i].Kind == event.KindTurnEnded {
			ended = &evs[i]
		}
	}
	if ended == nil {
		t.Fatal("the shutdown left the turn open")
	}
	var te event.TurnEnded
	_ = json.Unmarshal(ended.Data, &te)
	if te.Outcome != event.TurnOutcomeInterrupted || ended.Cmd != "sd1" {
		t.Errorf("turn ended %s by %q, want interrupted by sd1", te.Outcome, ended.Cmd)
	}
	r.stop()
	r = start(t, path, newFake())
	for _, e := range r.events() {
		if e.Kind == event.KindTurnEnded && string(e.Data) != "" {
			var te event.TurnEnded
			_ = json.Unmarshal(e.Data, &te)
			if te.Outcome == event.TurnOutcomeLost {
				t.Error("the next start recorded the turn as lost")
			}
		}
	}
	if r.h.Hello(0).Status == event.SessionStatusFailed {
		t.Error("status failed after a clean shutdown")
	}
}

// Finding 3: the shutdown command's own reply reaches the caller before the host goes.
func TestShutdown_replyIsDelivered(t *testing.T) {
	for i := range 20 {
		dir, err := os.MkdirTemp("", "sd")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(spec())
		specPath := filepath.Join(dir, "spec.json")
		_ = os.WriteFile(specPath, b, 0o600)
		sock := filepath.Join(dir, "h.sock")
		done := make(chan error, 1)
		go func() {
			done <- Run(context.Background(), []string{"--session", sid, "--spec", specPath, "--log",
				filepath.Join(dir, "events.jsonl"), "--socket", sock, "--pace", "0"}, "test", slog.New(slog.DiscardHandler))
		}()
		var conn net.Conn
		for j := 0; j < 200 && conn == nil; j++ {
			conn, _ = net.Dial("unix", sock)
			time.Sleep(5 * time.Millisecond)
		}
		if conn == nil {
			t.Fatal("no socket")
		}
		c := cmd(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: event.ShutdownReasonStop})
		_ = json.NewEncoder(conn).Encode(hostproto.Request{ID: "1", Op: hostproto.OpCommand, Command: &c})
		var resp hostproto.Response
		if err := json.NewDecoder(conn).Decode(&resp); err != nil || resp.ID != "1" {
			t.Fatalf("run %d: shutdown reply lost: %+v, %v", i, resp, err)
		}
		_ = conn.Close()
		<-done
		_ = os.RemoveAll(dir)
	}
}

// Finding 8: a request whose event could not be written, or that arrives outside a turn, never leaves the session
// waiting: no pending request, no approval status, and the adapter is told to deny it.
func TestRequestDrafts_onlyWhenLoggedInATurn(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	// Outside a turn.
	r.h.Draft(draft(event.KindPermissionRequested, event.PermissionRequested{RequestID: "p0", Tool: "Bash",
		Kind: "command", Mode: "ask"}))
	if h := r.h.Hello(0); h.Status == event.SessionStatusApproval {
		t.Error("a request outside a turn set the status to approval")
	}
	if c := next(t, f); c.Kind != event.CmdPermissionReply || !jsonHas(c.Data, `"decision":"deny"`) {
		t.Errorf("the adapter was not told to deny the request outside a turn: %s %s", c.Kind, c.Data)
	}
	// Inside a turn, but the write fails (too large).
	_, _ = r.h.Command(send(t, "m1", "work"))
	<-f.got
	huge := make([]byte, event.MaxLine)
	for i := range huge {
		huge[i] = 'a'
	}
	r.h.Draft(draft(event.KindPermissionRequested, event.PermissionRequested{RequestID: "p1", Tool: "Bash",
		Kind: "command", Command: string(huge), Mode: "ask"}))
	if h := r.h.Hello(0); h.Status == event.SessionStatusApproval {
		t.Error("an unwritten request set the status to approval")
	}
	if c := next(t, f); c.Kind != event.CmdPermissionReply || !jsonHas(c.Data, `"requestId":"p1"`) {
		t.Errorf("the adapter was not told to deny the unwritten request: %s %s", c.Kind, c.Data)
	}
	// A request that is logged in a turn still waits for the owner.
	r.h.Draft(draft(event.KindPermissionRequested, event.PermissionRequested{RequestID: "p2", Tool: "Bash",
		Kind: "command", Mode: "ask"}))
	if h := r.h.Hello(0); h.Status != event.SessionStatusApproval {
		t.Errorf("a logged request: status %s", h.Status)
	}
	if _, err := r.h.Command(cmd(t, "r", event.CmdPermissionReply, event.PermissionReply{RequestID: "p2", Decision: "allow_once"})); err != nil {
		t.Errorf("reply to the logged request: %v", err)
	}
	var ce *CommandError
	if _, err := r.h.Command(cmd(t, "r1", event.CmdPermissionReply, event.PermissionReply{RequestID: "p1", Decision: "allow_once"})); !errors.As(err, &ce) {
		t.Errorf("the unwritten request became answerable: %v", err)
	}
}

func next(t *testing.T, f *fakeAdapter) event.Command {
	t.Helper()
	select {
	case c := <-f.got:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("nothing sent to the adapter")
	}
	return event.Command{}
}

func jsonHas(b []byte, s string) bool {
	return len(b) > 0 && contains(string(b), s)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
