package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/hostproto"
)

const sid = "0193a8f2-9000-7000-8000-0000000000d1"

func spec() Spec {
	return Spec{Session: flowdproto.SessionSpec{ID: sid, Name: "demo", Provider: event.ProviderClaude,
		Model: "claude-opus-5-5", Mode: event.PermissionModeAcceptEdits, Replay: true},
		Dir: "/home/flow/work/.sessions/demo",
		Worktrees: []flowdproto.WorktreeState{{Repo: "trick77/toolkit", Branch: "flow/demo",
			Path: "/home/flow/work/.sessions/demo/toolkit", BaseRef: "origin/master", BaseSHA: "abc"}}}
}

type rig struct {
	t      *testing.T
	h      *Host
	path   string
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, path string, a Adapter) *rig {
	t.Helper()
	return startFor(t, path, a, "")
}

// startFor is start with the session.resumed reason the host is started for.
func startFor(t *testing.T, path string, a Adapter, reason event.ResumeReason) *rig {
	t.Helper()
	h, err := New(Config{Spec: spec(), LogPath: path, Adapter: a, Version: "test", ResumeReason: reason,
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{t: t, h: h, path: path, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- h.Pump(ctx) }()
	t.Cleanup(r.stop)
	return r
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	_ = r.h.Close()
	r.cancel = nil
}

func (r *rig) events() []event.Event {
	evs, err := eventlog.NewReader(r.path).Read(0, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return evs
}

// waitKind waits until the log holds n events of kind.
func (r *rig) waitKind(kind event.EventKind, n int) []event.Event {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs := r.events()
		c := 0
		for _, e := range evs {
			if e.Kind == kind {
				c++
			}
		}
		if c >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("no %d× %s in %v", n, kind, kinds(r.events()))
	return nil
}

func kinds(evs []event.Event) []event.EventKind {
	var out []event.EventKind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func cmd(t *testing.T, id string, kind event.CommandKind, payload any) event.Command {
	t.Helper()
	c, err := event.NewCommand(id, sid, kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, id, text string) event.Command {
	return cmd(t, id, event.CmdMessageSend, event.MessageSend{MessageID: id, Text: text, Origin: event.MessageOriginUser})
}

func replay(t *testing.T) *Replay {
	t.Helper()
	r, err := NewDemoReplay(Pace{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func gapless(t *testing.T, evs []event.Event) {
	t.Helper()
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
}

func TestReplayFixtureIsTheContractOne(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "fixtures", "demo.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(recording) {
		t.Error("internal/host/replay.jsonl differs from contract/fixtures/demo.jsonl: copy it again")
	}
}

func TestTurn_permissionBlocksUntilTheReply(t *testing.T) {
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), replay(t))
	evs := r.events()
	if len(evs) != 2 || evs[0].Kind != event.KindSessionStarted || evs[1].Kind != event.KindWorktreeAdded {
		t.Fatalf("first start wrote %v", kinds(evs))
	}
	if dup, err := r.h.Command(send(t, "m1", "go")); dup || err != nil {
		t.Fatal(dup, err)
	}
	r.waitKind(event.KindPermissionRequested, 1)
	time.Sleep(50 * time.Millisecond)
	evs = r.events()
	if last := evs[len(evs)-1]; last.Kind != event.KindSessionStatus || string(last.Data) != `{"status":"approval"}` {
		t.Fatalf("the turn went on without a reply: %v", kinds(evs))
	}
	evs = evs[:len(evs)-1]
	if r.h.Hello(1).Status != event.SessionStatusApproval || !r.h.Hello(1).Busy {
		t.Errorf("hello = %+v", r.h.Hello(1))
	}
	var req event.PermissionRequested
	_ = json.Unmarshal(evs[len(evs)-1].Data, &req)
	if req.RequestID != "perm-d1-m1" {
		t.Errorf("request id %q", req.RequestID)
	}
	// A reply to another request is refused; a repeat of a command is a duplicate.
	_, err := r.h.Command(cmd(t, "r0", event.CmdPermissionReply, event.PermissionReply{RequestID: "nope", Decision: "allow_once"}))
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != hostproto.CodeNotPending {
		t.Errorf("reply to an unknown request: %v", err)
	}
	if dup, _ := r.h.Command(send(t, "m1", "go")); !dup {
		t.Error("repeated command not reported as duplicate")
	}
	if _, err := r.h.Command(cmd(t, "r1", event.CmdPermissionReply, event.PermissionReply{RequestID: req.RequestID,
		Decision: "allow_once"})); err != nil {
		t.Fatal(err)
	}
	r.waitKind(event.KindTurnEnded, 1)
	time.Sleep(20 * time.Millisecond) // the status follows turn.ended under the same lock, but the read can fall between
	evs = r.events()
	gapless(t, evs)
	last := evs[len(evs)-1]
	if last.Kind != event.KindSessionStatus || string(last.Data) != `{"status":"done"}` {
		t.Errorf("last event %s %s", last.Kind, last.Data)
	}
	var cmds []string
	for _, e := range evs {
		if e.Cmd != "" {
			cmds = append(cmds, string(e.Kind)+"="+e.Cmd)
		}
	}
	if len(cmds) != 3 {
		t.Errorf("events with cmd: %v", cmds)
	}
}

func TestRestart_closesTheLostTurnAndRemembersCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	r := start(t, path, replay(t))
	_, _ = r.h.Command(send(t, "m1", "go"))
	r.waitKind(event.KindPermissionRequested, 1)
	r.stop()

	r = start(t, path, replay(t))
	evs := r.events()
	tail := kinds(evs[len(evs)-5:])
	want := []event.EventKind{event.KindPermissionResolved, event.KindTurnEnded, event.KindSessionStatus,
		event.KindSessionResumed}
	for i, k := range want {
		if tail[i+1] != k {
			t.Fatalf("after restart: %v", tail)
		}
	}
	gapless(t, evs)
	if dup, _ := r.h.Command(send(t, "m1", "go")); !dup {
		t.Error("a command from before the restart was applied again")
	}
	if r.h.Hello(0).Status != event.SessionStatusFailed {
		t.Errorf("status %s", r.h.Hello(0).Status)
	}
}

func TestQueueInterruptAndRemove(t *testing.T) {
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), replay(t))
	_, _ = r.h.Command(send(t, "m1", "first"))
	r.waitKind(event.KindPermissionRequested, 1)
	_, _ = r.h.Command(send(t, "m2", "second"))
	_, _ = r.h.Command(send(t, "m3", "third"))
	if _, err := r.h.Command(cmd(t, "x1", event.CmdMessageRemove, event.MessageRemove{MessageID: "m3"})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Command(cmd(t, "x2", event.CmdMessageRemove, event.MessageRemove{MessageID: "m3"})); err == nil {
		t.Error("removed a message twice")
	}
	if _, err := r.h.Command(cmd(t, "i1", event.CmdTurnInterrupt, event.TurnInterrupt{})); err != nil {
		t.Fatal(err)
	}
	evs := r.waitKind(event.KindPermissionRequested, 2) // m2's turn reached its request
	var ended event.TurnEnded
	for _, e := range evs {
		if e.Kind == event.KindTurnEnded {
			_ = json.Unmarshal(e.Data, &ended)
			if e.Cmd != "i1" {
				t.Errorf("turn.ended cmd %q, want the interrupt's", e.Cmd)
			}
			break
		}
	}
	if ended.Outcome != event.TurnOutcomeInterrupted {
		t.Errorf("outcome %s", ended.Outcome)
	}
	if n := count(evs, event.KindPermissionResolved); n != 1 {
		t.Errorf("%d permission.resolved (the interrupted request must expire)", n)
	}
	if n := count(evs, event.KindMessageDequeued); n != 2 {
		t.Errorf("%d message.dequeued", n)
	}
	// Deny ends the second turn without running the command.
	var req event.PermissionRequested
	for _, e := range evs {
		if e.Kind == event.KindPermissionRequested {
			_ = json.Unmarshal(e.Data, &req)
		}
	}
	_, err := r.h.Command(cmd(t, "d1", event.CmdPermissionReply, event.PermissionReply{RequestID: req.RequestID, Decision: "deny"}))
	if err != nil {
		t.Fatal(err)
	}
	evs = r.waitKind(event.KindTurnEnded, 2)
	gapless(t, evs)
}

func count(evs []event.Event, k event.EventKind) int {
	n := 0
	for _, e := range evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// fakeAdapter records what it gets and emits what the test pushes.
type fakeAdapter struct {
	got chan event.Command
	out chan event.EventDraft
}

func newFake() *fakeAdapter {
	return &fakeAdapter{got: make(chan event.Command, 16), out: make(chan event.EventDraft, 16)}
}

func (f *fakeAdapter) Send(c event.Command) error { f.got <- c; return nil }

func (f *fakeAdapter) Run(ctx context.Context, out chan<- event.EventDraft) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-f.out:
			if !ok {
				return errors.New("gone")
			}
			out <- d
		}
	}
}

func TestOtherCommandsAndDrafts(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	for i, c := range []event.Command{
		cmd(t, "g1", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateQueued, Position: 2}),
		cmd(t, "g2", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateStarted}),
		cmd(t, "g3", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateFinished}),
		cmd(t, "s1", event.CmdSettingsSet, event.SettingsSet{Model: "m"}),
		cmd(t, "w1", event.CmdWorktreeAdd, event.WorktreeAdd{Repo: "a/b", Branch: "x", Path: "/p", BaseRef: "origin/main"}),
		cmd(t, "p1", event.CmdPullRequestsSet, event.PullRequestsSet{Items: []event.PullRequestItem{}}),
		cmd(t, "c1", event.CmdChildStart, event.ChildStart{Session: "c", Title: "t", Repos: []string{}, Mode: "ask"}),
		cmd(t, "c2", event.CmdChildReport, event.ChildReport{Session: "c", Outcome: "done"}),
		cmd(t, "c3", event.CmdReportSent, event.ReportSent{ParentSession: "p", Outcome: "done"}),
	} {
		if _, err := r.h.Command(c); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}
	if _, err := r.h.Command(event.Command{V: 1, ID: "bad", Session: sid, Kind: "nope", Data: []byte(`{}`)}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := r.h.Command(event.Command{V: 1, ID: "x", Session: "other", Kind: event.CmdTurnInterrupt}); err == nil {
		t.Error("command for another session accepted")
	}
	_, _ = r.h.Command(send(t, "m1", "hi"))
	if c := <-f.got; c.Kind != event.CmdSettingsSet {
		t.Errorf("adapter got %+v first", c)
	}
	if c := <-f.got; c.Kind != event.CmdMessageSend || c.ID != "m1" {
		t.Errorf("adapter got %+v", c)
	}
	frames, cancel := r.h.Subscribe()
	defer cancel()
	if f := <-frames; f.Live == nil || f.Live.Kind != event.KindLiveSnapshot {
		t.Fatalf("first frame %+v", f)
	}
	f.out <- draft(event.KindAssistantTextDelta, event.AssistantTextDelta{BlockID: "b", Text: "Hel"})
	f.out <- draft(event.KindToolProgress, event.ToolProgress{ToolID: "t", ElapsedMs: 5})
	f.out <- draft(event.KindSessionStarted, event.SessionStarted{Provider: "claude", Model: "m", Mode: "ask", Cwd: "/"})
	f.out <- draft(event.KindQuestionAsked, event.QuestionAsked{RequestID: "q1", Questions: []event.Question{}})
	for f := range frames {
		if f.Live != nil && f.Live.Kind == event.KindToolProgress {
			break
		}
	}
	_, snapCancel := r.h.Subscribe()
	snapCancel()
	snapCancel()
	r.waitKind(event.KindQuestionAsked, 1)
	if r.h.Hello(0).Status != event.SessionStatusQuestion {
		t.Errorf("status %s", r.h.Hello(0).Status)
	}
	_, _ = r.h.Command(cmd(t, "q", event.CmdQuestionReply, event.QuestionReply{RequestID: "q1", Text: "yes"}))
	f.out <- draft(event.KindPlanProposed, event.PlanProposed{RequestID: "pl", Plan: "x"})
	r.waitKind(event.KindPlanProposed, 1)
	_, _ = r.h.Command(cmd(t, "pl", event.CmdPlanReply, event.PlanReply{RequestID: "pl", Decision: "approved"}))
	f.out <- draft(event.KindAssistantText, event.AssistantText{BlockID: "b", Text: "Hello"})
	_, _ = r.h.Command(cmd(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: "stop", WhenIdle: true}))
	f.out <- event.EventDraft{Kind: event.KindTurnEnded, Data: []byte(`{"outcome":"error","durationMs":1}`)}
	select {
	case <-r.h.Stopped():
	case <-time.After(5 * time.Second):
		t.Fatal("no stop after the turn ended")
	}
	if _, err := r.h.Command(send(t, "late", "x")); err == nil {
		t.Error("a stopping host took a message")
	}
	evs := r.waitKind(event.KindTurnEnded, 1)
	gapless(t, evs)
	if count(evs, event.KindSessionStarted) != 1 {
		t.Error("the adapter's session.started was logged twice")
	}
	if r.h.Hello(0).Status != event.SessionStatusFailed {
		t.Errorf("status after an error %s", r.h.Hello(0).Status)
	}
}

// Status after check runs follows docs/events.md: a run finished while the turn waited in the queue puts it back to
// working, and a turn whose last run failed ends failed although the turn itself completed.
func TestGate_statusFollowsTheDocs(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	_, _ = r.h.Command(send(t, "m1", "hi"))
	<-f.got
	for _, c := range []event.Command{
		cmd(t, "g1", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateQueued, Position: 1}),
		cmd(t, "g2", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateFinished, ExitCode: 2}),
	} {
		if _, err := r.h.Command(c); err != nil {
			t.Fatal(err)
		}
	}
	if st := r.h.Hello(0).Status; st != event.SessionStatusWorking {
		t.Errorf("status after a withdrawn wait %s, want working", st)
	}
	f.out <- event.EventDraft{Kind: event.KindTurnEnded, Data: []byte(`{"outcome":"completed","durationMs":1}`)}
	r.waitKind(event.KindTurnEnded, 1)
	if st := r.h.Hello(0).Status; st != event.SessionStatusFailed {
		t.Errorf("status after a turn whose check failed %s, want failed", st)
	}
	_, _ = r.h.Command(send(t, "m2", "again"))
	<-f.got
	_, _ = r.h.Command(cmd(t, "g3", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateFinished}))
	f.out <- event.EventDraft{Kind: event.KindTurnEnded, Data: []byte(`{"outcome":"completed","durationMs":1}`)}
	r.waitKind(event.KindTurnEnded, 2)
	if st := r.h.Hello(0).Status; st != event.SessionStatusDone {
		t.Errorf("status after a passing check %s, want done", st)
	}
}

// Request ids never repeat in a log, also across host restarts: Flow's reply command id is "reply-<requestId>", so a
// repeated request id would make the new request's reply a duplicate of the old one, never answered.
func TestReplay_idsStayUniqueAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	reqOf := func(r *rig, n int) string {
		evs := r.waitKind(event.KindPermissionRequested, n)
		var p event.PermissionRequested
		for _, e := range evs {
			if e.Kind == event.KindPermissionRequested {
				_ = json.Unmarshal(e.Data, &p)
			}
		}
		return p.RequestID
	}
	r := start(t, path, replay(t))
	_, _ = r.h.Command(send(t, "0193a8f2-0000-7000-8000-00000000aa01", "one"))
	req1 := reqOf(r, 1)
	_, _ = r.h.Command(cmd(t, "reply-"+req1, event.CmdPermissionReply, event.PermissionReply{RequestID: req1, Decision: "allow_once"}))
	r.waitKind(event.KindTurnEnded, 1)
	r.stop()

	r = start(t, path, replay(t)) // a new process: the adapter starts from scratch
	_, _ = r.h.Command(send(t, "0193a8f2-0000-7000-8000-00000000aa02", "two"))
	req2 := reqOf(r, 2)
	if req2 == req1 {
		t.Fatalf("request id %s reused after the restart", req2)
	}
	dup, err := r.h.Command(cmd(t, "reply-"+req2, event.CmdPermissionReply, event.PermissionReply{RequestID: req2, Decision: "allow_once"}))
	if dup || err != nil {
		t.Errorf("reply to the new request: duplicate %v, %v", dup, err)
	}
}

// A message whose event cannot be written (over event.MaxLine after JSON escaping) is refused; no turn starts
// without its user message.
func TestCommand_refusesAMessageItCannotLog(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	huge := strings.Repeat("<", event.MaxLine/5) // each < becomes <: six bytes
	_, err := r.h.Command(send(t, "big", huge))
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != hostproto.CodeInvalid {
		t.Errorf("oversized message: %v", err)
	}
	if r.h.Hello(0).Busy || count(r.events(), event.KindTurnStarted) != 0 {
		t.Error("a turn started without its user message")
	}
	select {
	case c := <-f.got:
		t.Errorf("the adapter got %s", c.Kind)
	default:
	}
	if dup, err := r.h.Command(send(t, "big", "now small")); dup || err != nil {
		t.Errorf("the refused id was remembered: duplicate %v, %v", dup, err)
	}
}

// Messages queued when the host died run after its restart, in order, before any newer message.
func TestRestart_drainsTheRecoveredQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "first"))
	_, _ = r.h.Command(send(t, "m2", "queued before the crash"))
	<-f.got
	r.stop() // the host dies mid-turn with m2 queued

	f = newFake()
	r = start(t, path, f)
	select {
	case c := <-f.got:
		if c.ID != "m2" {
			t.Fatalf("after restart the adapter got %s first, want the queued m2", c.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the recovered queue was never drained")
	}
	_, _ = r.h.Command(send(t, "m3", "newer"))
	evs := r.waitKind(event.KindMessageQueued, 2)
	if last := evs[len(evs)-1]; last.Kind != event.KindMessageQueued || last.Cmd != "m3" {
		t.Errorf("m3 not queued behind m2: %s %s", last.Kind, last.Cmd)
	}
}

// A host started after a sandbox start only to log the cut-off turn lost starts no turn: the queued message waits
// for the owner, and the next start for a message drains it.
func TestRestart_sandboxStartKeepsTheQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "first"))
	_, _ = r.h.Command(send(t, "m2", "queued before the stop"))
	<-f.got
	r.stop() // the sandbox stops mid-turn with m2 queued

	f = newFake()
	r = startFor(t, path, f, event.ResumeReasonSandboxStart)
	select {
	case c := <-f.got:
		t.Fatalf("the adapter got %s, nobody asked for a turn", c.ID)
	case <-time.After(300 * time.Millisecond):
	}
	evs := r.events()
	var lost, resumed bool
	for _, e := range evs {
		p, _ := e.Payload()
		switch p := p.(type) {
		case *event.TurnEnded:
			lost = lost || p.Outcome == event.TurnOutcomeLost
		case *event.SessionResumed:
			resumed = resumed || p.Reason == event.ResumeReasonSandboxStart
		}
	}
	if !lost || !resumed || kinds(evs)[len(evs)-1] != event.KindSessionResumed {
		t.Errorf("lost %v, resumed for sandbox_start %v: %v", lost, resumed, kinds(evs))
	}
	if !r.h.Queued("m2") {
		t.Error("m2 left the queue")
	}
	r.stop()

	f = newFake()
	start(t, path, f)
	select {
	case c := <-f.got:
		if c.ID != "m2" {
			t.Fatalf("the next start sent %s, want the queued m2", c.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the queue was never drained after the owner's start")
	}
}

// A host started held with messages queued runs a new message after them, not ahead: it is queued and the queue
// drains in order.
func TestIdleWithAQueue_newMessageGoesBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "first"))
	_, _ = r.h.Command(send(t, "m2", "queued before the stop"))
	<-f.got
	r.stop()

	f = newFake()
	r = startFor(t, path, f, event.ResumeReasonSandboxStart)
	if _, err := r.h.Command(send(t, "m3", "newer")); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-f.got:
		if c.ID != "m2" {
			t.Fatalf("the adapter got %s first, want the queued m2", c.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing started")
	}
	if !r.h.Queued("m3") {
		t.Error("m3 is not queued behind m2")
	}
	f.out <- draft(event.KindTurnEnded, event.TurnEnded{Outcome: event.TurnOutcomeCompleted})
	select {
	case c := <-f.got:
		if c.ID != "m3" {
			t.Fatalf("after m2 the adapter got %s, want m3", c.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("m3 never ran")
	}
}

// A host started held to take a command (session.resumed command) starts no queued turn either: the command
// arrives, the queue waits for the owner's next start.
func TestRestart_commandKeepsTheQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "first"))
	_, _ = r.h.Command(send(t, "m2", "queued before the stop"))
	<-f.got
	r.stop()

	f = newFake()
	r = startFor(t, path, f, event.ResumeReasonCommand)
	select {
	case c := <-f.got:
		t.Fatalf("the adapter got %s, nobody asked for a turn", c.ID)
	case <-time.After(300 * time.Millisecond):
	}
	if !r.h.Queued("m2") {
		t.Error("m2 left the queue")
	}
	resumed := false
	for _, e := range r.events() {
		if p, _ := e.Payload(); p != nil {
			if sr, ok := p.(*event.SessionResumed); ok {
				resumed = resumed || sr.Reason == event.ResumeReasonCommand
			}
		}
	}
	if !resumed {
		t.Error("no session.resumed command")
	}
	r.stop()
}

// A question reply naming a question or option the request does not have is refused and leaves the request open;
// a known option and free text are taken.
func TestQuestionReply_refusesUnknownIds(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	_, _ = r.h.Command(send(t, "m1", "ask me"))
	<-f.got
	f.out <- draft(event.KindQuestionAsked, event.QuestionAsked{RequestID: "q1", Questions: []event.Question{
		{ID: "q0", Text: "Which?", Options: []event.QuestionOption{{ID: "o0", Label: "A"}, {ID: "o1", Label: "B"}}}}})
	r.waitKind(event.KindQuestionAsked, 1)
	for i, answers := range [][]event.Answer{
		{{QuestionID: "q9", OptionIds: []string{"o0"}}},
		{{QuestionID: "q0", OptionIds: []string{"o7"}}},
	} {
		_, err := r.h.Command(cmd(t, "bad"+strconv.Itoa(i), event.CmdQuestionReply,
			event.QuestionReply{RequestID: "q1", Answers: answers}))
		var ce *CommandError
		if !errors.As(err, &ce) || ce.Code != hostproto.CodeInvalid {
			t.Errorf("answers %+v: %v, want invalid", answers, err)
		}
	}
	if n := count(r.events(), event.KindQuestionResolved); n != 0 {
		t.Fatalf("%d question.resolved after refused replies", n)
	}
	if _, err := r.h.Command(cmd(t, "ok", event.CmdQuestionReply, event.QuestionReply{RequestID: "q1",
		Answers: []event.Answer{{QuestionID: "q0", OptionIds: []string{"o1"}}}})); err != nil {
		t.Fatal(err)
	}
	r.waitKind(event.KindQuestionResolved, 1)
}

// The queue's order survives a restart: a message sent with interrupt and a queued one sent now go to the head,
// and the log says so.
func TestRestart_keepsTheQueueOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "first"))
	<-f.got
	for _, id := range []string{"m2", "m3", "m4"} {
		_, _ = r.h.Command(send(t, id, id))
	}
	if _, err := r.h.Command(cmd(t, "n1", event.CmdMessageSendNow, event.MessageSendNow{MessageID: "m3"})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.h.Command(cmd(t, "m5", event.CmdMessageSend, event.MessageSend{MessageID: "m5", Text: "now",
		Origin: event.MessageOriginUser, WhenBusy: event.WhenBusyInterrupt})); err != nil {
		t.Fatal(err)
	}
	ids := func(h *Host) []string {
		h.mu.Lock()
		defer h.mu.Unlock()
		var out []string
		for _, m := range h.queue {
			out = append(out, m.MessageID)
		}
		return out
	}
	before := ids(r.h)
	if strings.Join(before, " ") != "m5 m3 m2 m4" {
		t.Fatalf("live queue %v", before)
	}
	r.stop() // the host dies mid-turn: the adapter never ended it

	f = newFake()
	r = start(t, path, f)
	select {
	case c := <-f.got:
		if c.ID != "m5" {
			t.Fatalf("after restart the adapter got %s first, want m5", c.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the recovered queue was never drained")
	}
	if got := ids(r.h); strings.Join(got, " ") != "m3 m2 m4" {
		t.Errorf("recovered queue %v, want m3 m2 m4", got)
	}
}

func TestPump_failsTheTurnWhenTheAdapterDies(t *testing.T) {
	f := newFake()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	h, err := New(Config{Spec: spec(), LogPath: path, Adapter: f, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	_, _ = h.Command(send(t, "m1", "hi"))
	close(f.out)
	if err := h.Pump(context.Background()); err == nil {
		t.Fatal("Pump returned nil for a dead adapter")
	}
	if h.Hello(0).Busy {
		t.Error("turn still open")
	}
}

func TestServe(t *testing.T) {
	dir, err := os.MkdirTemp("", "host") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	r := start(t, filepath.Join(dir, "events.jsonl"), replay(t))
	l, err := net.Listen("unix", filepath.Join(dir, "h.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- r.h.Serve(ctx, l) }()

	conn, err := net.Dial("unix", filepath.Join(dir, "h.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(bufio.NewReader(conn))
	c := send(t, "m1", "go")
	for _, req := range []hostproto.Request{{ID: "1", Op: hostproto.OpHello}, {ID: "2", Op: hostproto.OpCommand, Command: &c},
		{ID: "3", Op: hostproto.OpCommand}, {ID: "4", Op: "nope"}} {
		_ = enc.Encode(req)
		var resp hostproto.Response
		if err := dec.Decode(&resp); err != nil {
			t.Fatal(err)
		}
		switch req.ID {
		case "1":
			if resp.Hello == nil || resp.Hello.Session != sid {
				t.Errorf("hello %+v", resp)
			}
		case "2":
			if resp.Error != "" {
				t.Errorf("command: %+v", resp)
			}
		default:
			if resp.Error != hostproto.CodeInvalid {
				t.Errorf("request %s: %+v", req.ID, resp)
			}
		}
	}
	sub, _ := net.Dial("unix", filepath.Join(dir, "h.sock"))
	_ = json.NewEncoder(sub).Encode(hostproto.Request{ID: "s", Op: hostproto.OpSubscribe})
	sdec := json.NewDecoder(sub)
	var resp hostproto.Response
	_ = sdec.Decode(&resp)
	var fr hostproto.Frame
	if err := sdec.Decode(&fr); err != nil || fr.Live == nil {
		t.Fatalf("first frame %+v %v", fr, err)
	}
	_ = sub.Close()
	_, _ = conn.Write([]byte("garbage\n"))
	cancel()
	if err := <-served; err != nil {
		t.Error(err)
	}
}

func TestRun(t *testing.T) {
	dir, err := os.MkdirTemp("", "run")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	b, _ := json.Marshal(spec())
	specPath := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(specPath, b, 0o600)
	sock := filepath.Join(dir, "h.sock")
	args := []string{"--session", sid, "--spec", specPath, "--log", filepath.Join(dir, "events.jsonl"), "--socket", sock,
		"--pace", "0"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, args, "test", slog.New(slog.DiscardHandler)) }()
	var conn net.Conn
	for i := 0; i < 200 && conn == nil; i++ {
		conn, _ = net.Dial("unix", sock)
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("no socket")
	}
	c := cmd(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: event.ShutdownReasonStop})
	_ = json.NewEncoder(conn).Encode(hostproto.Request{ID: "1", Op: hostproto.OpCommand, Command: &c})
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host.shutdown did not stop Run")
	}
	cancel()
	_ = conn.Close()
	for _, bad := range [][]string{{"--session", "../x"}, {"--nope"}, {"--session", "s", "--spec", filepath.Join(dir, "missing")},
		{"--session", "other", "--spec", specPath}} {
		if err := Run(context.Background(), bad, "t", slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("Run(%v) succeeded", bad)
		}
	}
}

func TestPaceAndRecordingErrors(t *testing.T) {
	p := DefaultPace
	if p.wait(time.Hour) != p.Max || p.wait(0) != p.Min || (Pace{}).wait(time.Hour) != 0 {
		t.Error("pace clamps wrong")
	}
	if _, err := NewReplay([]byte("not json\n"), Pace{}); err == nil {
		t.Error("garbage recording accepted")
	}
	if _, err := NewReplay(nil, Pace{}); err == nil {
		t.Error("empty recording accepted")
	}
	if got := suffixIDs([]byte(`[1]`), "2"); string(got) != `[1]` {
		t.Errorf("suffixIDs on a non-object: %s", got)
	}
	if got := idTail("0193a8f2-9100-7000-8000-00000000b001"); got != "0000b001" {
		t.Errorf("idTail = %q", got)
	}
}
