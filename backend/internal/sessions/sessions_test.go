package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/trick77/ship/kit/sse"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/flowdproto/flowdfake"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/store"
)

type fakeSandboxes struct {
	api *flowdfake.Fake
	err error
	// warmErr, when set, is Warm's answer instead of err (a stopped sandbox: sandboxes.ErrNotWarm).
	warmErr error
	forgets int
	ensures atomic.Int32
	// ensuring, when set, is told on every Ensure, which then waits for proceed (a bring-up in progress).
	ensuring chan struct{}
	proceed  chan struct{}
}

func (f *fakeSandboxes) Ensure(context.Context, string) (flowdproto.API, error) {
	f.ensures.Add(1)
	if f.ensuring != nil {
		f.ensuring <- struct{}{}
		<-f.proceed
	}
	return f.api, f.err
}

func (f *fakeSandboxes) Warm(context.Context, string) (flowdproto.API, error) {
	if f.warmErr != nil {
		return nil, f.warmErr
	}
	return f.api, f.err
}
func (f *fakeSandboxes) Forget(string) { f.forgets++ }

type rig struct {
	svc *Service
	sb  *fakeSandboxes
	hub *sse.Hub[apitypes.AppEvent]
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := projects.SQL{DB: db}
	if err := ps.Create(t.Context(), projects.Project{ID: "p1", Name: "demo"}, []projects.Repo{
		{ID: "r1", FullName: "trick77/toolkit", DefaultBranch: "master"},
		{ID: "r2", FullName: "trick77/ship", DefaultBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	hub := sse.NewHub[apitypes.AppEvent](sse.HubOptions{})
	t.Cleanup(hub.Close)
	sb := &fakeSandboxes{api: flowdfake.New("sb")}
	return &rig{svc: &Service{Store: SQL{DB: db}, Projects: ps, Sandboxes: sb, Hub: hub, Log: slog.New(slog.DiscardHandler),
		Retry: 10 * time.Millisecond}, sb: sb, hub: hub}
}

func create(t *testing.T, r *rig, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	(&API{Service: r.svc}).CreateSession(w, req)
	return w
}

func TestCreateFollowAndList(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	updates, unsub, _ := r.hub.Subscribe("")
	defer unsub()
	w := create(t, r, `{"projectId":"p1","repoIds":["r1","r2"],"provider":"claude","model":"m","mode":"accept_edits",
		"message":{"clientId":"c1","text":"Persist the sidebar width\nper user"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if s.Summary.Title != "Persist the sidebar width" || s.Summary.Branch != "flow/persist-the-sidebar-width" ||
		s.Dir != "/home/flow/work/.sessions/persist-the-sidebar-width" || len(s.Worktrees) != 2 ||
		s.Worktrees[1].BaseRef != "origin/main" || s.Summary.ClaudeAuth != "subscription_token" {
		t.Fatalf("session %+v", s)
	}
	// The follower mirrors the host's status and announces it.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-updates:
			if e.Session != nil && e.Session.Status == apitypes.SessionStatusDone {
				goto done
			}
		case <-deadline:
			t.Fatal("no session.updated with status done")
		}
	}
done:
	se, _ := r.svc.Store.Get(t.Context(), s.Summary.ID)
	if se.LastSeq == 0 || se.Status != "done" || !se.Unseen || se.ProviderSessionID != s.Summary.ID {
		t.Errorf("row %+v", se)
	}
	// A second session with the same title gets its own directory.
	w = create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"opencode","model":"m","mode":"ask","title":"Persist the sidebar width"}`)
	s = apitypes.Session{}
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if s.Summary.Branch != "flow/persist-the-sidebar-width-2" || s.Summary.ClaudeAuth != "" {
		t.Errorf("second session %+v", s.Summary)
	}
	lw := httptest.NewRecorder()
	(&API{Service: r.svc}).ListSessions(lw, httptest.NewRequest("GET", "/api/sessions?projectId=p1", nil))
	var list apitypes.SessionList
	_ = json.Unmarshal(lw.Body.Bytes(), &list)
	if len(list.Items) != 2 {
		t.Errorf("list %d", len(list.Items))
	}
	gw := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("sessionId", "nope")
	(&API{Service: r.svc}).GetSession(gw, req)
	if gw.Code != 404 {
		t.Errorf("get missing: %d", gw.Code)
	}
	req.SetPathValue("sessionId", s.Summary.ID)
	gw = httptest.NewRecorder()
	(&API{Service: r.svc}).GetSession(gw, req)
	if gw.Code != 200 {
		t.Errorf("get: %d", gw.Code)
	}
}

// flakyGet fails Get a few times, as a busy or briefly unavailable database does.
type flakyGet struct {
	SQL
	mu    sync.Mutex
	fails int
}

func (f *flakyGet) Get(ctx context.Context, id string) (Session, error) {
	f.mu.Lock()
	if f.fails > 0 {
		f.fails--
		f.mu.Unlock()
		return Session{}, errors.New("database is locked")
	}
	f.mu.Unlock()
	return f.SQL.Get(ctx, id)
}

// A transient store error does not stop following a session: the sidebar status keeps being mirrored. A follower
// that ends frees its place, so Follow can start one again.
func TestFollow_survivesTransientStoreErrors(t *testing.T) {
	r := newRig(t)
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"t"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	flaky := &flakyGet{SQL: r.svc.Store.(SQL), fails: 2}
	r.svc.Store = flaky
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := event.NewCommand("m1", s.Summary.ID, event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "hi",
		Origin: event.MessageOriginUser})
	if _, err := r.sb.api.Command(ctx, c); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		se, _ := flaky.SQL.Get(ctx, s.Summary.ID)
		if se.Status == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never mirrored after transient errors: %s", se.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel() // the follower ends
	deadline = time.Now().Add(5 * time.Second)
	for {
		r.svc.mu.Lock()
		following := r.svc.follows[s.Summary.ID]
		r.svc.mu.Unlock()
		if following == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ended follower kept its entry")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A sandbox stopped mid-turn ends every unit without a turn.ended in the log: the follower, finding it stopped,
// ends the mirrored turn (failed, as the host logs it lost on its next start) and announces it. A sandbox that may
// still run (ErrMayRun) proves nothing and changes nothing.
func TestFollow_stoppedSandboxEndsTheTurn(t *testing.T) {
	for _, tc := range []struct {
		warmErr error
		want    string
	}{{sandboxes.ErrNotWarm, "failed"}, {fmt.Errorf("%w (recorded failed)", sandboxes.ErrMayRun), "working"}} {
		r := newRig(t)
		w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"t"}`)
		var s apitypes.Session
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		id := s.Summary.ID
		if err := r.svc.Store.ApplyMirror(t.Context(), id, Mirror{Status: "working", TurnStarted: true}); err != nil {
			t.Fatal(err)
		}
		r.sb.warmErr = tc.warmErr
		updates, unsub, _ := r.hub.Subscribe("")
		ctx, cancel := context.WithCancel(t.Context())
		if err := r.svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		var announced *apitypes.SessionSummary
		select {
		case e := <-updates:
			announced = e.Session
		case <-time.After(300 * time.Millisecond):
		}
		cancel()
		unsub()
		se, _ := r.svc.Store.Get(t.Context(), id)
		if se.Status != tc.want {
			t.Errorf("%v: status %s, want %s", tc.warmErr, se.Status, tc.want)
		}
		if tc.want == "failed" {
			if se.TurnStartedAt != nil || !se.Unseen {
				t.Errorf("turn still open or not unseen: %+v", se)
			}
			if announced == nil || announced.Status != apitypes.SessionStatusFailed || announced.TurnStartedAt != nil {
				t.Errorf("announced %+v", announced)
			}
		} else if announced != nil || se.TurnStartedAt == nil {
			t.Errorf("changed on ErrMayRun: %+v, announced %+v", se, announced)
		}
	}
}

// blindList does not see a concurrent creation that has not committed when the name is chosen.
type blindList struct{ SQL }

func (blindList) List(context.Context, ListFilter) ([]Session, error) { return nil, nil }

// Two creations with the same title at once: the one that loses the insert takes the next name, not a 500.
func TestCreate_concurrentSameTitle(t *testing.T) {
	r := newRig(t)
	sq := r.svc.Store.(SQL)
	if err := sq.Create(t.Context(), Session{ID: "other", ProjectID: "p1", Name: "same-title", Provider: "claude",
		Model: "m", Mode: "ask", Branch: "flow/same-title"}, nil); err != nil {
		t.Fatal(err)
	}
	r.svc.Store = blindList{sq}
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"Same title"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != 201 || s.Summary.Branch != "flow/same-title-2" {
		t.Errorf("create racing on a name: %d %s", w.Code, w.Body)
	}
}

// hiding does not see one name, as a concurrent creation that took it after the names were listed.
type hiding struct {
	SQL
	name string
}

func (h hiding) List(ctx context.Context, f ListFilter) ([]Session, error) {
	list, err := h.SQL.List(ctx, f)
	return slices.DeleteFunc(list, func(se Session) bool { return se.Name == h.name }), err
}

// The retry after losing a name continues after the chosen suffix instead of walking the taken names from -2 again.
func TestCreate_raceRetryContinuesAfterTheChosenName(t *testing.T) {
	r := newRig(t)
	sq := r.svc.Store.(SQL)
	for i := 1; i <= 13; i++ {
		name := "fix"
		if i > 1 {
			name = fmt.Sprintf("fix-%d", i)
		}
		if err := sq.Create(t.Context(), Session{ID: "s" + strconv.Itoa(i), ProjectID: "p1", Name: name,
			Provider: "claude", Model: "m", Mode: "ask", Branch: "flow/" + name}, nil); err != nil {
			t.Fatal(err)
		}
	}
	r.svc.Store = hiding{SQL: sq, name: "fix-13"}
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"fix"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != 201 || s.Summary.Branch != "flow/fix-14" {
		t.Errorf("create after losing fix-13: %d %s", w.Code, w.Body)
	}
}

func TestCreate_refusals(t *testing.T) {
	r := newRig(t)
	for body, code := range map[string]int{
		`{"projectId":"p1","repoIds":["r1"],"provider":"x","model":"m","mode":"ask"}`:                                400,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"x"}`:                             400,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","claudeAuth":"apikey"}`:     400,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":" ","mode":"ask"}`:                           400,
		`{"projectId":"px","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`:                           400,
		`{"projectId":"p1","repoIds":[],"provider":"claude","model":"m","mode":"ask"}`:                               400,
		`{"projectId":"p1","repoIds":["r1","r1"],"provider":"claude","model":"m","mode":"ask"}`:                      400,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","message":{"clientId":""}}`: 400,
		`nope`: 400,
	} {
		if w := create(t, r, body); w.Code != code {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	big, _ := json.Marshal(strings.Repeat(">", 60_000))
	if w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask",
		"message":{"clientId":"c","text":`+string(big)+`}}`); w.Code != 400 {
		t.Errorf("first message over the encoded limit: %d", w.Code)
	}
	r.sb.err = apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, "down")
	if w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`); w.Code != 503 {
		t.Errorf("sandbox down: %d", w.Code)
	}
	// A failed creation leaves nothing behind: a retry must not find a half-made session.
	if list, _ := r.svc.Store.List(t.Context(), ListFilter{}); len(list) != 0 {
		t.Errorf("failed creation left %d sessions", len(list))
	}
	r.sb.err = nil
	if w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"t",
		"message":{"clientId":"c","text":"x"}}`); w.Code != 201 {
		t.Errorf("after recovery: %d", w.Code)
	}
}

func TestCreate_failedFirstMessageRemovesTheSession(t *testing.T) {
	r := newRig(t)
	f := &failingCommand{Fake: r.sb.api}
	r.svc.Sandboxes = &fakeSandboxesAPI{api: f}
	if w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask",
		"message":{"clientId":"c","text":"x"}}`); w.Code != 503 {
		t.Errorf("failing first message: %d %s", w.Code, w.Body)
	}
	if list, _ := r.svc.Store.List(t.Context(), ListFilter{}); len(list) != 0 {
		t.Errorf("failed first message left %d sessions", len(list))
	}
	if f.stopped != 1 {
		t.Errorf("host stopped %d times, want once", f.stopped)
	}
}

// The host never comes up (EnsureSession fails after flowd made worktrees): the undo still stops and removes the
// session in the sandbox, through the no-start path, before it deletes the row.
func TestCreate_failedHostRemovesTheSessionInTheSandbox(t *testing.T) {
	r := newRig(t)
	f := &failingEnsure{failingCommand: failingCommand{Fake: r.sb.api}}
	r.svc.Sandboxes = &fakeSandboxesAPI{api: f}
	if w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask"}`); w.Code != 503 {
		t.Errorf("failing host: %d %s", w.Code, w.Body)
	}
	if list, _ := r.svc.Store.List(t.Context(), ListFilter{}); len(list) != 0 {
		t.Errorf("failed host left %d sessions", len(list))
	}
	if f.stopped != 1 || !f.removed {
		t.Errorf("session stopped %d times (remove %v), want once with remove", f.stopped, f.removed)
	}
}

// A command the host refuses as invalid (an event it cannot write) reaches the client as invalid_request, not as an
// unavailable sandbox.
func TestCommand_hostRefusalIsInvalid(t *testing.T) {
	r := newRig(t)
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"t"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	r.svc.Sandboxes = &fakeSandboxesAPI{api: refusing{r.sb.api}}
	_, err := r.svc.Command(t.Context(), s.Summary.ID, "c", event.CmdTurnInterrupt, event.TurnInterrupt{})
	var ae *apitypes.Error
	if !errors.As(err, &ae) || ae.Code != apitypes.ErrorCodeInvalidRequest {
		t.Errorf("refused command: %v", err)
	}
}

type refusing struct{ *flowdfake.Fake }

func (refusing) Command(context.Context, event.Command) (flowdproto.CommandResult, error) {
	return flowdproto.CommandResult{}, flowdproto.ErrInvalid
}

type failingEnsure struct {
	failingCommand
	removed bool
}

func (f *failingEnsure) EnsureSession(ctx context.Context, s flowdproto.SessionSpec) (flowdproto.SessionState, error) {
	_, _ = f.Fake.EnsureSession(ctx, s)
	return flowdproto.SessionState{}, errors.New("host did not come up")
}

func (f *failingEnsure) StopSession(ctx context.Context, id string, remove bool) error {
	f.removed = remove
	return f.failingCommand.StopSession(ctx, id, remove)
}

type failingCommand struct {
	*flowdfake.Fake
	stopped int
}

func (f *failingCommand) Command(context.Context, event.Command) (flowdproto.CommandResult, error) {
	return flowdproto.CommandResult{}, errors.New("boom")
}

func (f *failingCommand) StopSession(ctx context.Context, id string, remove bool) error {
	f.stopped++
	return f.Fake.StopSession(ctx, id, remove)
}

type fakeSandboxesAPI struct{ api flowdproto.API }

func (f *fakeSandboxesAPI) Ensure(context.Context, string) (flowdproto.API, error) { return f.api, nil }
func (f *fakeSandboxesAPI) Warm(context.Context, string) (flowdproto.API, error)   { return f.api, nil }
func (f *fakeSandboxesAPI) Forget(string)                                          {}

func TestCreate_titleCutOnARune(t *testing.T) {
	r := newRig(t)
	title := strings.Repeat("a", 79) + "äöü and more"
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"`+title+`"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if !utf8.ValidString(s.Summary.Title) || len(s.Summary.Title) > 80 || !strings.HasPrefix(title, s.Summary.Title) {
		t.Errorf("title %q (%d bytes, valid %v)", s.Summary.Title, len(s.Summary.Title), utf8.ValidString(s.Summary.Title))
	}
}

func TestCommandAndHost(t *testing.T) {
	r := newRig(t)
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"t"}`)
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	ctx := t.Context()
	if _, err := r.svc.Command(ctx, s.Summary.ID, "r1", event.CmdPermissionReply,
		event.PermissionReply{RequestID: "x", Decision: "allow_once"}); err != nil {
		t.Errorf("reply on the fake (it ignores unknown requests): %v", err)
	}
	if _, err := r.svc.Command(ctx, "nope", "c", event.CmdTurnInterrupt, event.TurnInterrupt{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: %v", err)
	}
	if err := r.svc.Store.Archive(ctx, s.Summary.ID, false); err != nil {
		t.Fatal(err)
	}
	var ae *apitypes.Error
	if _, err := r.svc.Host(ctx, s.Summary.ID); !errors.As(err, &ae) || ae.Code != apitypes.ErrorCodeSessionArchived {
		t.Errorf("archived: %v", err)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"": "session", "Fix: the flaky test!": "fix-the-flaky-test",
		strings.Repeat("word ", 20): "word-word-word-word-word-word-word-word",
		strings.Repeat("x", 50):     strings.Repeat("x", 40)} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q", in, got)
		}
	}
}

func TestSQL(t *testing.T) {
	r := newRig(t)
	s := r.svc.Store.(SQL)
	ctx := t.Context()
	parent := "p"
	if err := s.Create(ctx, Session{ID: "p", ProjectID: "p1", Name: "p", Provider: "claude", Model: "m", Mode: "ask", Branch: "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, Session{ID: "c", ProjectID: "p1", ParentID: &parent, Name: "c", Provider: "claude", Model: "m",
		Mode: "ask", Branch: "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountChildren(ctx, "p"); err != nil || n != 1 {
		t.Errorf("CountChildren = %d, %v", n, err)
	}
	if list, _ := s.List(ctx, ListFilter{ParentID: "p"}); len(list) != 1 || *list[0].ParentID != "p" {
		t.Errorf("children %+v", list)
	}
	se, _ := s.Get(ctx, "c")
	se.Title = "new"
	if err := s.Update(ctx, se); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyMirror(ctx, "c", Mirror{LastSeq: 9, Status: "done", TurnStarted: true, Unseen: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyMirror(ctx, "c", Mirror{LastSeq: 3, TurnEnded: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen(ctx, "c", 9); err != nil {
		t.Fatal(err)
	}
	se, _ = s.Get(ctx, "c")
	if se.Title != "new" || se.LastSeq != 9 || se.Unseen || se.SeenSeq != 9 || se.TurnStartedAt != nil {
		t.Errorf("row %+v", se)
	}
	if err := s.AddWorktree(ctx, Worktree{ID: "w", SessionID: "c", RepoID: "r1", Branch: "b", Path: "/p", BaseRef: "origin/master"}); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{s.Update(ctx, Session{ID: "x"}), s.ApplyMirror(ctx, "x", Mirror{}), s.MarkSeen(ctx, "x", 1),
		s.Archive(ctx, "x", false), s.CleanedUp(ctx, "x"), s.UpdateWorktree(ctx, Worktree{ID: "x"})} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("missing row: %v", err)
		}
	}
	if err := s.Archive(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx, ListFilter{Archived: true}); len(list) != 1 {
		t.Errorf("archived list %d", len(list))
	}
	pending := ListFilter{ProjectID: "p1", Archived: true, CleanupPending: true}
	if list, _ := s.List(ctx, pending); len(list) != 1 || !list[0].CleanupPending {
		t.Errorf("pending cleanup %+v", list)
	}
	if err := s.CleanedUp(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx, pending); len(list) != 0 {
		t.Errorf("still pending after CleanedUp: %+v", list)
	}
}
