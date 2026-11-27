package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/sandboxes"
)

func patch(t *testing.T, r *rig, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/sessions/"+id, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("sessionId", id)
	(&API{Service: r.svc}).UpdateSession(w, req)
	return w
}

func newSession(t *testing.T, r *rig) string {
	t.Helper()
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"t"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	return s.Summary.ID
}

// settingsOf is the session.settings events in the fake host's log.
func settingsOf(t *testing.T, r *rig, id string) []event.SessionSettings {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var out []event.SessionSettings
	for e, err := range r.sb.api.Events(ctx, id, 0) {
		if err != nil {
			break
		}
		if e.Kind == event.KindSessionSettings {
			var s event.SessionSettings
			_ = json.Unmarshal(e.Data, &s)
			out = append(out, s)
		}
	}
	return out
}

// PATCH changes the title in the row only, and a model or mode change also reaches the agent as settings.set
// (logged as session.settings); a mode above the cap and malformed values are refused.
func TestUpdateSession(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil { // the follower mirrors the settings into the row
		t.Fatal(err)
	}
	id := newSession(t, r)
	w := patch(t, r, id, `{"title":"  New title  "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("title: %d %s", w.Code, w.Body)
	}
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	se, _ := r.svc.Store.Get(t.Context(), id)
	if s.Summary.Title != "New title" || se.TitleSource != "owner" || len(settingsOf(t, r, id)) != 0 {
		t.Errorf("title change: %+v, source %s, settings %v", s.Summary, se.TitleSource, settingsOf(t, r, id))
	}

	w = patch(t, r, id, `{"model":"claude-sonnet-5-5","mode":"accept_edits"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("model and mode: %d %s", w.Code, w.Body)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if s.Summary.Model != "claude-sonnet-5-5" || s.Summary.Mode != "accept_edits" {
		t.Errorf("answer %+v", s.Summary)
	}
	if got := settingsOf(t, r, id); len(got) != 1 || got[0].Model != "claude-sonnet-5-5" || got[0].Mode != "accept_edits" {
		t.Errorf("session.settings %+v", got)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Model == "claude-sonnet-5-5" && se.Mode == "accept_edits" })
	// The same values again change nothing.
	if w := patch(t, r, id, `{"model":"claude-sonnet-5-5"}`); w.Code != http.StatusOK || len(settingsOf(t, r, id)) != 1 {
		t.Errorf("unchanged model sent again: %d, %v", w.Code, settingsOf(t, r, id))
	}

	// An effort goes the model's way, and default (the agent's own) is a change back, not an absent field.
	if se, _ := r.svc.Store.Get(t.Context(), id); se.Effort != "default" {
		t.Errorf("a session created without an effort holds %q", se.Effort)
	}
	if w := patch(t, r, id, `{"effort":"default"}`); w.Code != http.StatusOK || len(settingsOf(t, r, id)) != 1 {
		t.Errorf("effort default on a default session: %d, %v", w.Code, settingsOf(t, r, id))
	}
	w = patch(t, r, id, `{"effort":"max"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != http.StatusOK || s.Summary.Effort != "max" {
		t.Errorf("effort max: %d %+v", w.Code, s.Summary)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Effort == "max" && se.Model == "claude-sonnet-5-5" })
	w = patch(t, r, id, `{"effort":"default"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != http.StatusOK || s.Summary.Effort != "default" {
		t.Errorf("effort back to default: %d %+v", w.Code, s.Summary)
	}
	if got := settingsOf(t, r, id); len(got) != 3 || got[1].Effort != "max" || got[2].Effort != "default" ||
		got[2].Model != "" {
		t.Errorf("session.settings %+v", got)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Effort == "default" })

	_ = r.svc.Store.ApplyMirror(t.Context(), id, Mirror{ModeCap: "accept_edits"})
	for body, code := range map[string]string{
		`{"effort":"huge"}`: "invalid_request", `{"effort":"Max"}`: "invalid_request",
		`{"mode":"full_access"}`: "mode_above_cap", `{"mode":"root"}`: "invalid_request",
		`{"model":"a b"}`: "invalid_request", `{"title":"   "}`: "invalid_request", `{"title":"a\u0007b"}`: "invalid_request",
	} {
		if w := patch(t, r, id, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), code) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := patch(t, r, "nope", `{"title":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown session: %d", w.Code)
	}
	if err := r.svc.Archive(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if w := patch(t, r, id, `{"title":"x"}`); w.Code != http.StatusConflict {
		t.Errorf("archived session: %d %s", w.Code, w.Body)
	}
}

// The follower mirrors the log's settings into the row: a session.settings (the owner's change as applied, or the
// agent's correction) and an approved plan's mode.
func TestFollow_mirrorsSettingsAndPlanMode(t *testing.T) {
	r := newRig(t)
	r.sb.api.Script = func(string) []event.EventDraft {
		d, _ := json.Marshal(event.PlanProposed{RequestID: "pl1", Plan: "do it"})
		return []event.EventDraft{{Kind: event.KindPlanProposed, Data: d}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := newSession(t, r)
	c, _ := event.NewCommand("s1", id, event.CmdSettingsSet, event.SettingsSet{Model: "x-model", Mode: "auto"})
	if _, err := r.sb.api.Command(ctx, c); err != nil {
		t.Fatal(err)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Model == "x-model" && se.Mode == "auto" })
	if _, err := r.svc.Command(ctx, id, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "plan",
		Origin: event.MessageOriginUser}); err != nil {
		t.Fatal(err)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Status == "plan" })
	if _, err := r.svc.Command(ctx, id, "reply-pl1", event.CmdPlanReply, event.PlanReply{RequestID: "pl1",
		Decision: event.PlanReplyDecisionApproved, Mode: "accept_edits"}); err != nil {
		t.Fatal(err)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Mode == "accept_edits" })
}

func waitRow(t *testing.T, r *rig, id string, ok func(Session) bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		se, err := r.svc.Store.Get(t.Context(), id)
		if err == nil && ok(se) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("row never matched: %+v", se)
		}
	}
}

// A held command (queue removal, send-now, settings) starts a host that is down as for a lost turn, so it does not
// send its queue's head first, and with stopAfter stops it again; a running host is left running.
func TestCommandHeld_keepsTheQueueStill(t *testing.T) {
	r := newRig(t)
	id := newSession(t, r)
	if err := r.sb.api.StopSession(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := r.svc.CommandHeld(t.Context(), id, "remove-m1", event.CmdMessageRemove,
		event.MessageRemove{MessageID: "m1"}, false, true); err != nil || !ran {
		t.Fatalf("held command: %v, ran %v", err, ran)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var reason event.ResumeReason
	for e, err := range r.sb.api.Events(ctx, id, 0) {
		if err != nil {
			break
		}
		if e.Kind == event.KindSessionResumed {
			var p event.SessionResumed
			_ = json.Unmarshal(e.Data, &p)
			reason = p.Reason
		}
	}
	if reason != event.ResumeReasonCommand {
		t.Errorf("host started with resume reason %q, want command (queue left as it is)", reason)
	}
	if st, _ := r.sb.api.Session(t.Context(), id); st.Unit == "active" {
		t.Error("the host started for the command keeps running")
	}
	// Running: neither restarted nor stopped.
	if _, err := r.svc.Host(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.CommandHeld(t.Context(), id, "remove-m2", event.CmdMessageRemove,
		event.MessageRemove{MessageID: "m2"}, false, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.sb.api.Session(t.Context(), id); st.Unit != "active" {
		t.Errorf("a running host was stopped: %s", st.Unit)
	}
	// A stopped sandbox is not started without mayStart.
	r.sb.warmErr = sandboxes.ErrNotWarm
	if _, ran, err := r.svc.CommandHeld(t.Context(), id, "remove-m3", event.CmdMessageRemove,
		event.MessageRemove{MessageID: "m3"}, false, true); err != nil || ran {
		t.Errorf("stopped sandbox: ran %v, %v", ran, err)
	}
}

// mirrorOnGet is a Store whose second Get is followed at once by a mirror of the given settings: the follower writing
// while a rename is under way.
type mirrorOnGet struct {
	Store
	id   string
	m    Mirror
	gets int
}

func (s *mirrorOnGet) Get(ctx context.Context, id string) (Session, error) {
	se, err := s.Store.Get(ctx, id)
	if id == s.id {
		if s.gets++; s.gets == 2 {
			_ = s.ApplyMirror(ctx, id, s.m)
		}
	}
	return se, err
}

// A rename writes the title only: a mode, model or provider session id mirrored meanwhile stays.
func TestUpdate_renameKeepsAConcurrentMirror(t *testing.T) {
	r := newRig(t)
	id := newSession(t, r)
	r.svc.Store = &mirrorOnGet{Store: r.svc.Store, id: id,
		m: Mirror{Mode: "auto", Model: "x-model", ProviderSessionID: "prov-2"}}
	if w := patch(t, r, id, `{"title":"Renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	se, _ := r.svc.Store.Get(t.Context(), id)
	if se.Title != "Renamed" || se.TitleSource != "owner" || se.Mode != "auto" || se.Model != "x-model" ||
		se.ProviderSessionID != "prov-2" {
		t.Errorf("row after the rename: %+v", se)
	}
}

// Create refuses the model and title values Update refuses.
func TestCreate_sharesUpdatesValidation(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"x\ny","mode":"ask"}`,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"a b","mode":"ask"}`,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"` + strings.Repeat("m", 201) + `","mode":"ask"}`,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"a\u0007b"}`,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","title":"   "}`,
		`{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","effort":"huge"}`,
	} {
		if w := create(t, r, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	// A title derived from the first message may hold a tab or a CR: never refused for it.
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask",`+
		`"message":{"clientId":"c1","text":"fix\tit\r\nmore"}}`)
	if w.Code != http.StatusCreated {
		t.Errorf("derived title: %d %s", w.Code, w.Body)
	}
}

// An effort given at create is the row's, the summary's and the host spec's (the host's session.started carries it).
func TestCreate_effort(t *testing.T) {
	r := newRig(t)
	w := create(t, r, `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask","effort":"high"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	se, _ := r.svc.Store.Get(t.Context(), s.Summary.ID)
	if s.Summary.Effort != "high" || se.Effort != "high" {
		t.Errorf("summary %q, row %q", s.Summary.Effort, se.Effort)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var started *event.SessionStarted
	for e, err := range r.sb.api.Events(ctx, s.Summary.ID, 0) {
		if err != nil {
			break
		}
		if e.Kind == event.KindSessionStarted {
			started = &event.SessionStarted{}
			_ = json.Unmarshal(e.Data, started)
		}
	}
	if started == nil || started.Effort != event.EffortHigh {
		t.Errorf("session.started %+v", started)
	}
}

// A Stop or a stale reply on a session whose host is down starts it held: the cut-off turn is closed, the queue is
// left as it is (resume reason sandbox_start, not message) and the host started for it is stopped again.
func TestCommandIfRunning_startsTheHostHeld(t *testing.T) {
	r := newRig(t)
	id := newSession(t, r)
	if err := r.sb.api.StopSession(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := r.svc.CommandIfRunning(t.Context(), id, "interrupt-1", event.CmdTurnInterrupt,
		event.TurnInterrupt{}); err != nil || !ran {
		t.Fatalf("interrupt: %v, ran %v", err, ran)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var reasons []event.ResumeReason
	for e, err := range r.sb.api.Events(ctx, id, 0) {
		if err != nil {
			break
		}
		if e.Kind == event.KindSessionResumed {
			var p event.SessionResumed
			_ = json.Unmarshal(e.Data, &p)
			reasons = append(reasons, p.Reason)
		}
	}
	if len(reasons) != 1 || reasons[0] != event.ResumeReasonCommand {
		t.Errorf("host started with resume reasons %v, want one command (queue left as it is)", reasons)
	}
	if st, _ := r.sb.api.Session(t.Context(), id); st.Unit == "active" {
		t.Error("the host started for the interrupt keeps running")
	}
}

// A Stop that waited for the session's lock while the sandbox stopped does not start it again: the sandbox is
// asked under the lock.
func TestCommandIfRunning_sandboxStoppedWhileWaiting(t *testing.T) {
	r := newRig(t)
	id := newSession(t, r)
	before := r.sb.ensures.Load()
	unlock := r.svc.Lock(id) // a closeLostTurn or a Create holds it
	type answer struct {
		ran bool
		err error
	}
	done := make(chan answer, 1)
	go func() {
		_, ran, err := r.svc.CommandIfRunning(t.Context(), id, "interrupt-1", event.CmdTurnInterrupt, event.TurnInterrupt{})
		done <- answer{ran, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		r.svc.mu.Lock()
		waiting := r.svc.locks[id].users == 2
		r.svc.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stop never waited for the lock")
		}
	}
	r.sb.warmErr = sandboxes.ErrNotWarm // the sandbox stops meanwhile
	unlock()
	a := <-done
	if a.err != nil || a.ran {
		t.Errorf("stop on a stopped sandbox: ran %v, %v", a.ran, a.err)
	}
	if n := r.sb.ensures.Load(); n != before {
		t.Errorf("the stopped sandbox was started (%d Ensure calls)", n-before)
	}
}

// correctingAPI is a host whose agent corrects a settings change at once: the follower mirrors the correction
// right after the command, before Update answers.
type correctingAPI struct {
	flowdproto.API
	correct func()
}

func (c correctingAPI) Command(ctx context.Context, cmd event.Command) (flowdproto.CommandResult, error) {
	res, err := c.API.Command(ctx, cmd)
	if cmd.Kind == event.CmdSettingsSet {
		c.correct()
	}
	return res, err
}

// A mode change the agent corrected stays corrected in the row: Update leaves model and mode to the mirror.
func TestUpdate_keepsAMirroredCorrection(t *testing.T) {
	r := newRig(t)
	id := newSession(t, r)
	r.svc.Sandboxes = &fakeSandboxesAPI{api: correctingAPI{API: r.sb.api, correct: func() {
		_ = r.svc.Store.ApplyMirror(t.Context(), id, Mirror{Mode: "plan"})
	}}}
	if w := patch(t, r, id, `{"mode":"accept_edits"}`); w.Code != http.StatusOK {
		t.Fatalf("mode change: %d %s", w.Code, w.Body)
	}
	if se, _ := r.svc.Store.Get(t.Context(), id); se.Mode != "plan" {
		t.Errorf("row mode %s after the agent put plan in force", se.Mode)
	}
}

// A status, mode or mode cap this contract does not list is left out of the mirror, which goes on: the row still
// takes the valid fields and every later event.
func TestFollow_skipsValuesNotInTheContract(t *testing.T) {
	r := newRig(t)
	r.sb.api.Script = func(string) []event.EventDraft {
		return []event.EventDraft{
			{Kind: event.KindSessionStatus, Data: json.RawMessage(`{"status":"paused"}`)},
			{Kind: event.KindSessionSettings, Data: json.RawMessage(`{"model":"x-model","mode":"root","modeCap":"sudo",` +
				`"effort":"huge"}`)},
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := newSession(t, r)
	if _, err := r.svc.Command(ctx, id, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "go",
		Origin: event.MessageOriginUser}); err != nil {
		t.Fatal(err)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Status == "done" })
	if se, _ := r.svc.Store.Get(t.Context(), id); se.Model != "x-model" || se.Mode != "ask" || se.ModeCap != "" ||
		se.Effort != "default" {
		t.Errorf("row %+v", se)
	}
}

// The row's turn start and last activity are the events' times, not when Flow mirrored them (a restart catching
// up on a running turn).
func TestFollow_mirrorsTheEventsTime(t *testing.T) {
	r := newRig(t)
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	r.sb.api.Now = func() time.Time { return at }
	r.sb.api.Script = func(string) []event.EventDraft {
		d, _ := json.Marshal(event.PlanProposed{RequestID: "pl1", Plan: "do it"})
		return []event.EventDraft{{Kind: event.KindPlanProposed, Data: d}}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := r.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id := newSession(t, r)
	if _, err := r.svc.Command(ctx, id, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "plan",
		Origin: event.MessageOriginUser}); err != nil {
		t.Fatal(err)
	}
	waitRow(t, r, id, func(se Session) bool { return se.Status == "plan" })
	se, _ := r.svc.Store.Get(t.Context(), id)
	if se.TurnStartedAt == nil || !se.TurnStartedAt.Equal(at) || !se.LastActivityAt.Equal(at) {
		t.Errorf("turn started %v, last activity %v, want %v", se.TurnStartedAt, se.LastActivityAt, at)
	}
}

// The database holds a message clientId once (the backstop behind the in-process lock); sessions without one are
// not affected by the index.
func TestSQL_clientIDUnique(t *testing.T) {
	r := newRig(t)
	st := r.svc.Store
	mk := func(id, name, client string) error {
		return st.Create(t.Context(), Session{ID: id, ProjectID: "p1", Name: name, Provider: "claude", Model: "m",
			Mode: "ask", Branch: "flow/" + name, ClientID: client}, nil)
	}
	if err := mk("a", "a", "c-1"); err != nil {
		t.Fatal(err)
	}
	if err := mk("b", "b", "c-1"); !errors.Is(err, ErrClientIDTaken) {
		t.Errorf("second session with the clientId: %v", err)
	}
	if err := mk("c", "c", ""); err != nil {
		t.Fatal(err)
	}
	if err := mk("d", "d", ""); err != nil {
		t.Errorf("two sessions without a clientId: %v", err)
	}
	if se, err := st.ByClientID(t.Context(), "c-1"); err != nil || se.ID != "a" {
		t.Errorf("ByClientID = %+v, %v", se, err)
	}
	if _, err := st.ByClientID(t.Context(), "c-9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown clientId: %v", err)
	}
}

// POST .../seen clears unseen once the owner saw the last seq.
func TestMarkSessionSeen(t *testing.T) {
	r := newRig(t)
	id := newSession(t, r)
	if err := r.svc.Store.ApplyMirror(t.Context(), id, Mirror{LastSeq: 7, Unseen: true}); err != nil {
		t.Fatal(err)
	}
	// The summary names the mirrored last seq: what the UI marks with while the log cannot be read.
	row, _ := r.svc.Store.Get(t.Context(), id)
	if sum, err := r.svc.Summary(t.Context(), row); err != nil || sum.LastSeq != 7 {
		t.Errorf("summary lastSeq %d, %v", sum.LastSeq, err)
	}
	seen := func(id, body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/sessions/"+id+"/seen", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetPathValue("sessionId", id)
		(&API{Service: r.svc}).MarkSessionSeen(w, req)
		return w.Code
	}
	if c := seen(id, `{"seq":5}`); c != http.StatusNoContent {
		t.Fatalf("seen: %d", c)
	}
	if se, _ := r.svc.Store.Get(t.Context(), id); !se.Unseen || se.SeenSeq != 5 {
		t.Errorf("seen short of the end: %+v", se)
	}
	if c := seen(id, `{"seq":7}`); c != http.StatusNoContent {
		t.Fatalf("seen: %d", c)
	}
	if se, _ := r.svc.Store.Get(t.Context(), id); se.Unseen || se.SeenSeq != 7 {
		t.Errorf("seen to the end: %+v", se)
	}
	if c := seen(id, `{"seq":-1}`); c != http.StatusBadRequest {
		t.Errorf("negative seq: %d", c)
	}
	if c := seen("nope", `{"seq":1}`); c != http.StatusNotFound {
		t.Errorf("unknown session: %d", c)
	}
}

// A create repeated with the same message clientId, also at the same moment, makes one session and answers it to
// both; a create with another clientId makes another.
func TestCreate_idempotentByClientID(t *testing.T) {
	r := newRig(t)
	body := `{"projectId":"p1","repoIds":["r1"],"provider":"claude","model":"m","mode":"ask",
		"message":{"clientId":"c-1","text":"Fix the flaky test"}}`
	ids := make([]string, 4)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			w := create(t, r, body)
			if w.Code != http.StatusCreated {
				t.Errorf("create %d: %d %s", i, w.Code, w.Body)
				return
			}
			var s apitypes.Session
			_ = json.Unmarshal(w.Body.Bytes(), &s)
			ids[i] = s.Summary.ID
		})
	}
	wg.Wait()
	for _, id := range ids[1:] {
		if id != ids[0] || id == "" {
			t.Fatalf("repeated creates answered %v", ids)
		}
	}
	list, _ := r.svc.Store.List(t.Context(), ListFilter{ProjectID: "p1"})
	if len(list) != 1 {
		t.Fatalf("%d sessions for one clientId", len(list))
	}
	// The message ran once.
	n := 0
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	for e, err := range r.sb.api.Events(ctx, ids[0], 0) {
		if err != nil {
			break
		}
		if e.Kind == event.KindMessageUser {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the first message ran %d times", n)
	}
	w := create(t, r, strings.Replace(body, "c-1", "c-2", 1))
	var s apitypes.Session
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != http.StatusCreated || s.Summary.ID == ids[0] {
		t.Errorf("another clientId: %d, %s", w.Code, s.Summary.ID)
	}
}
