package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/metering"
	"github.com/trick77/flow/internal/sandboxes"
)

// The spec carries the server's session limits and the replay switch to every host start.
func TestSpec_limitsAndReplay(t *testing.T) {
	r := newRig(t)
	r.svc.Limits = flowdproto.Limits{MemoryMaxBytes: 5 << 30, CPUQuotaPercent: 300, TasksMax: 4096}
	r.svc.Replay = true
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"plan",
		"claudeAuth":"api_key"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	spec, ok := r.sb.api.Spec(s.Summary.ID)
	if !ok || spec.Limits != r.svc.Limits || !spec.Replay || spec.ClaudeAuth != "api_key" || spec.Model != "m" ||
		spec.Mode != "plan" || spec.Provider != "claude" {
		t.Errorf("spec %+v", spec)
	}
}

// Archiving stops the host with its worktrees and closes the session's terminal, which would otherwise keep a shell
// with the session's credential running.
func TestArchive_closesTheTerminal(t *testing.T) {
	r := newRig(t)
	var closed []string
	r.svc.CloseTerminals = func(ctx context.Context, api flowdproto.API, id string) error {
		closed = append(closed, id)
		return api.CloseTerminal(ctx, "s-"+id+"-1")
	}
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	id := s.Summary.ID
	ctx := t.Context()
	if _, err := r.sb.api.EnsureTerminal(ctx, flowdproto.TerminalSpec{ID: "s-" + id + "-1", Session: id, Dir: s.Dir,
		Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	archive := func(id string) int {
		req := httptest.NewRequest("DELETE", "/", nil)
		req.SetPathValue("sessionId", id)
		w := httptest.NewRecorder()
		(&API{Service: r.svc}).ArchiveSession(w, req)
		return w.Code
	}
	if c := archive(id); c != http.StatusNoContent {
		t.Fatalf("archive: %d", c)
	}
	h, _ := r.sb.api.Hello(ctx)
	if len(h.Terminals) != 0 || len(closed) != 1 {
		t.Errorf("terminal left: %+v (closed %v)", h.Terminals, closed)
	}
	st, _ := r.sb.api.Session(ctx, id)
	if st.Unit != flowdproto.UnitInactive || len(st.Worktrees) != 0 {
		t.Errorf("host %+v", st)
	}
	if se, _ := r.svc.Store.Get(ctx, id); se.ArchivedAt == nil {
		t.Error("row not archived")
	}
	if c := archive(id); c != http.StatusNoContent || len(closed) != 1 {
		t.Errorf("second archive: %d, closed %v", c, closed)
	}
	if c := archive("nope"); c != http.StatusNotFound {
		t.Errorf("unknown session: %d", c)
	}
	// A sandbox that cannot be reached leaves the session as it is.
	w = create(t, r, `{"projectId":"p1","repoIds":["r2"],"provider":"claude","model":"m","mode":"ask"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	r.svc.CloseTerminals = func(context.Context, flowdproto.API, string) error { return errors.New("flowd gone") }
	if c := archive(s.Summary.ID); c != http.StatusServiceUnavailable && c/100 != 5 {
		t.Errorf("archive with flowd failing: %d", c)
	}
	if se, _ := r.svc.Store.Get(ctx, s.Summary.ID); se.ArchivedAt != nil {
		t.Error("archived although its terminal was not closed")
	}
	// The terminal's failure does not keep the host running: it is stopped all the same.
	if st, _ := r.sb.api.Session(ctx, s.Summary.ID); st.Unit != flowdproto.UnitInactive {
		t.Errorf("host after a failed terminal close: %+v", st)
	}
}

// meterLog is a Meter that records whose follow ended.
type meterLog struct{ forgot chan string }

func (m meterLog) Observe(context.Context, metering.Session, event.Event) error { return nil }
func (m meterLog) Forget(id string)                                             { m.forgot <- id }

// Archiving takes the session out of every sidebar (session.removed, not an update the UI upserts) and ends its
// follower, which would otherwise keep a stream to flowd open for as long as the sandbox runs.
func TestArchive_removesAndUnfollows(t *testing.T) {
	r := newRig(t)
	meter := meterLog{forgot: make(chan string, 4)}
	r.svc.Meter = meter
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	id := s.Summary.ID
	events, unsub, _ := r.hub.Subscribe("")
	defer unsub()
	if err := r.svc.Archive(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(5 * time.Second)
	for removed := false; !removed; {
		select {
		case e := <-events: // updates from before the archive may come first
			removed = e.Kind == apitypes.AppEventKindSessionRemoved && e.SessionID == id
		case <-timeout:
			t.Fatal("no session.removed")
		}
	}
	select {
	case got := <-meter.forgot:
		if got != id {
			t.Errorf("forgot %s", got)
		}
	case <-timeout:
		t.Fatal("the follower of the archived session still runs")
	}
	r.svc.mu.Lock()
	left := r.svc.follows[id]
	r.svc.mu.Unlock()
	if left != nil {
		t.Error("the follower's place is still taken")
	}
	// Nothing re-adds it: a publish for an archived row is a removal too.
	r.svc.Publish(t.Context(), id)
	for {
		select {
		case e := <-events:
			if e.SessionID == id && e.Kind != apitypes.AppEventKindSessionRemoved {
				t.Fatalf("archived session published as %s", e.Kind)
			}
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

// Archiving never starts a stopped sandbox: nothing of the session runs there, so it is marked archived with its
// cleanup pending, and the sandbox's next bring-up removes host and worktrees (Sweep). A failure that proves nothing
// (not "not warm") still fails the archive: a host with the session's credential may be running.
func TestArchive_stoppedSandbox(t *testing.T) {
	r := newRig(t)
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	id := s.Summary.ID
	ctx := t.Context()
	r.sb.warmErr = errors.New("store locked")
	if err := r.svc.Archive(ctx, id); err == nil {
		t.Fatal("archived on an error that proves nothing")
	}
	if se, _ := r.svc.Store.Get(ctx, id); se.ArchivedAt != nil {
		t.Fatal("archived on an error that proves nothing")
	}
	r.sb.warmErr = sandboxes.ErrNotWarm
	ensures := r.sb.ensures.Load()
	if err := r.svc.Archive(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := r.sb.ensures.Load(); n != ensures {
		t.Errorf("archive started the sandbox (%d ensures)", n-ensures)
	}
	se, _ := r.svc.Store.Get(ctx, id)
	if se.ArchivedAt == nil || !se.CleanupPending {
		t.Fatalf("row %+v", se)
	}
	// The sandbox is up again.
	r.sb.warmErr = nil
	r.svc.Sweep(ctx, "p1", r.sb.api)
	if st, _ := r.sb.api.Session(ctx, id); st.Unit != flowdproto.UnitInactive || len(st.Worktrees) != 0 {
		t.Errorf("host after the sweep %+v", st)
	}
	if se, _ := r.svc.Store.Get(ctx, id); se.CleanupPending {
		t.Error("still pending after the sweep")
	}
}

// A host start in flight (waiting for the sandbox) and an archive are serialized: the archive waits for it and then
// stops what it started, instead of marking the session archived while the start recreates host and worktrees.
func TestArchive_waitsForAHostStart(t *testing.T) {
	r := newRig(t)
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	id := s.Summary.ID
	ctx := t.Context()
	if err := r.sb.api.StopSession(ctx, id, false); err != nil { // idle and stopped: the next message starts it
		t.Fatal(err)
	}
	r.sb.ensuring, r.sb.proceed = make(chan struct{}), make(chan struct{})
	hosted := make(chan error, 1)
	go func() { _, err := r.svc.Host(ctx, id); hosted <- err }()
	<-r.sb.ensuring // the send waits for its sandbox
	archived := make(chan error, 1)
	go func() { archived <- r.svc.Archive(ctx, id) }()
	early := false
	select {
	case err := <-archived: // without the lock the archive runs right through
		early = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
	}
	close(r.sb.proceed)
	if err := <-hosted; err != nil {
		t.Fatal(err)
	}
	if !early {
		if err := <-archived; err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := r.sb.api.Session(ctx, id); st.Unit == flowdproto.UnitActive || len(st.Worktrees) != 0 {
		t.Errorf("an archived session's host runs: %+v", st)
	}
}
