package flowdfake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
)

const sid = "0193a8f2-5d1e-7c3a-9b2e-6f1d2a3b4c5d"

func draft(t *testing.T, kind event.EventKind, payload any) event.EventDraft {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return event.EventDraft{Kind: kind, Data: b}
}

func cmd(t *testing.T, id string, kind event.CommandKind, payload any) event.Command {
	t.Helper()
	c, err := event.NewCommand(id, sid, kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func send(t *testing.T, f *Fake, c event.Command) flowdproto.CommandResult {
	t.Helper()
	r, err := f.Command(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func kinds(t *testing.T, f *Fake) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, _ := f.Session(ctx, sid)
	var out []string
	for e, err := range f.Events(ctx, sid, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != event.KindSessionStatus {
			out = append(out, string(e.Kind))
		} else {
			p, _ := e.Payload()
			out = append(out, "status:"+string(p.(*event.SessionStatusChanged).Status))
		}
		if e.Seq == st.LastSeq {
			break
		}
	}
	return out
}

func newSession(t *testing.T) *Fake {
	t.Helper()
	f := New("sb1")
	f.Now = func() time.Time { return time.Date(2026, 11, 26, 14, 0, 0, 0, time.UTC) }
	st, err := f.EnsureSession(context.Background(), flowdproto.SessionSpec{ID: sid, Name: "flaky", Provider: event.ProviderClaude,
		Model: "m", Mode: event.PermissionModeAsk, Worktrees: []flowdproto.WorktreeSpec{{Repo: "dev/riptide",
			Branch: "fix/x", BaseRef: "origin/master"}}})
	if err != nil || st.Unit != flowdproto.UnitActive || len(st.Worktrees) != 1 {
		t.Fatalf("EnsureSession = %+v, %v", st, err)
	}
	return f
}

func TestTurnWithPermissionAndQueue(t *testing.T) {
	f := newSession(t)
	f.Script = func(string) []event.EventDraft {
		return []event.EventDraft{
			draft(t, event.KindPermissionRequested, event.PermissionRequested{RequestID: "p1", Tool: "Bash",
				Kind: event.PermissionKindCommand, Mode: event.PermissionModeAsk}),
			draft(t, event.KindAssistantTextDelta, event.AssistantTextDelta{BlockID: "b", Text: "x"}),
			draft(t, event.KindAssistantText, event.AssistantText{BlockID: "b", Text: "done"}),
		}
	}
	send(t, f, cmd(t, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "go", Origin: event.MessageOriginUser}))
	if r := send(t, f, cmd(t, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "go"})); !r.Duplicate {
		t.Error("a repeated command id was applied again")
	}
	send(t, f, cmd(t, "m2", event.CmdMessageSend, event.MessageSend{MessageID: "m2", Text: "then", Origin: event.MessageOriginUser}))
	send(t, f, cmd(t, "m3", event.CmdMessageSend, event.MessageSend{MessageID: "m3", Text: "drop", Origin: event.MessageOriginUser}))
	send(t, f, cmd(t, "r3", event.CmdMessageRemove, event.MessageRemove{MessageID: "m3"}))
	send(t, f, cmd(t, "wrong", event.CmdPermissionReply, event.PermissionReply{RequestID: "nope", Decision: event.ReplyDecisionDeny}))
	send(t, f, cmd(t, "p1", event.CmdPermissionReply, event.PermissionReply{RequestID: "p1", Decision: event.ReplyDecisionAllowOnce}))
	got := strings.Join(kinds(t, f), " ")
	want := "session.started worktree.added message.user turn.started status:working permission.requested " +
		"status:approval message.queued message.queued message.dequeued permission.resolved status:working " +
		"assistant.text turn.ended status:done message.dequeued message.user turn.started status:working " +
		"permission.requested status:approval"
	if got != want {
		t.Errorf("log:\n got %s\nwant %s", got, want)
	}
}

func TestQuestionPlanInterruptAndStop(t *testing.T) {
	f := newSession(t)
	f.Script = func(text string) []event.EventDraft {
		switch text {
		case "ask":
			return []event.EventDraft{draft(t, event.KindQuestionAsked, event.QuestionAsked{RequestID: "q1"})}
		case "plan":
			return []event.EventDraft{draft(t, event.KindPlanProposed, event.PlanProposed{RequestID: "pl1", Plan: "# p"})}
		}
		return []event.EventDraft{draft(t, event.KindPermissionRequested, event.PermissionRequested{RequestID: "p9"})}
	}
	msg := func(id, text string, busy event.WhenBusy) {
		send(t, f, cmd(t, id, event.CmdMessageSend, event.MessageSend{MessageID: id, Text: text,
			Origin: event.MessageOriginUser, WhenBusy: busy}))
	}
	msg("a", "ask", "")
	send(t, f, cmd(t, "qa", event.CmdQuestionReply, event.QuestionReply{RequestID: "q1", Text: "yes"}))
	msg("b", "plan", "")
	send(t, f, cmd(t, "pa", event.CmdPlanReply, event.PlanReply{RequestID: "pl1", Decision: event.PlanReplyDecisionApproved}))
	msg("c", "ask", "")
	send(t, f, cmd(t, "int", event.CmdTurnInterrupt, event.TurnInterrupt{}))
	msg("d", "plan", "")
	msg("e", "x", event.WhenBusyInterrupt)
	msg("f", "ask", "")
	send(t, f, cmd(t, "now", event.CmdMessageSendNow, event.MessageSendNow{MessageID: "f"}))
	send(t, f, cmd(t, "now2", event.CmdMessageSendNow, event.MessageSendNow{MessageID: "missing"}))
	send(t, f, cmd(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: event.ShutdownReasonUpgrade}))
	msg("g", "x", "")
	if err := f.StopSession(context.Background(), sid, true); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(kinds(t, f), " ")
	for _, want := range []string{
		"question.asked status:question question.resolved status:working turn.ended status:done",
		"plan.proposed status:plan plan.resolved status:working turn.ended",
		"question.asked status:question question.resolved turn.ended status:done",
		"plan.proposed status:plan plan.resolved turn.ended status:done message.user",
		"permission.requested status:approval message.queued permission.resolved turn.ended status:done message.dequeued message.user",
		"permission.resolved turn.ended status:failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	st, _ := f.Session(context.Background(), sid)
	if st.Unit != flowdproto.UnitInactive || st.Worktrees != nil || st.Status != event.SessionStatusFailed {
		t.Errorf("after stop: %+v", st)
	}
	st, _ = f.EnsureSession(context.Background(), flowdproto.SessionSpec{ID: sid})
	if st.Unit != flowdproto.UnitActive || !strings.HasSuffix(strings.Join(kinds(t, f), " "), "session.resumed") {
		t.Errorf("resume: %+v", st)
	}
}

func TestInjectedCommands(t *testing.T) {
	f := newSession(t)
	for i, c := range []event.Command{
		cmd(t, "1", event.CmdSettingsSet, event.SettingsSet{Mode: event.PermissionModeAuto}),
		cmd(t, "2", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateQueued, Position: 2}),
		cmd(t, "3", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateStarted}),
		cmd(t, "4", event.CmdGateUpdate, event.GateUpdate{State: event.GateStateFinished, ExitCode: 2}),
		cmd(t, "5", event.CmdChildStart, event.ChildStart{Session: "c1", Title: "t", Mode: event.PermissionModeAsk}),
		cmd(t, "6", event.CmdChildReport, event.ChildReport{Session: "c1", Outcome: event.ReportOutcomeDone}),
		cmd(t, "7", event.CmdReportSent, event.ReportSent{ParentSession: "p", Outcome: event.ReportOutcomeDone}),
		cmd(t, "8", event.CmdWorktreeAdd, event.WorktreeAdd{Repo: "dev/loom", Branch: "b", Path: "/p", BaseRef: "origin/master"}),
		cmd(t, "9", event.CmdPullRequestsSet, event.PullRequestsSet{Items: []event.PullRequestItem{{Repo: "dev/loom", Order: 1, State: event.PullRequestStateOpen}}}),
	} {
		if r := send(t, f, c); r.Duplicate || r.ID != c.ID {
			t.Errorf("command %d: %+v", i, r)
		}
	}
	got := strings.Join(kinds(t, f), " ")
	want := "session.started worktree.added session.settings gate.queued status:queued gate.started status:working " +
		"gate.finished child.started child.reported session.reported worktree.added pull_requests.updated"
	if got != want {
		t.Errorf("log:\n got %s\nwant %s", got, want)
	}
	if _, err := f.Command(context.Background(), event.Command{V: 1, ID: "x", Session: sid, Kind: "nope"}); err == nil {
		t.Error("an unknown command kind was accepted")
	}
	if _, err := f.Command(context.Background(), event.Command{ID: "x", Session: "missing"}); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("command to a missing session: %v", err)
	}
}

// The demo recording replays as a turn that waits for the permission, then finishes as recorded.
func TestReplayDemo(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contract", "fixtures", "demo.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec []event.Event
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		e, err := event.ParseLine([]byte(line + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		rec = append(rec, e)
	}
	f := newSession(t)
	f.Script = Replay(rec)
	send(t, f, cmd(t, "m", event.CmdMessageSend, event.MessageSend{MessageID: "m", Text: "go", Origin: event.MessageOriginUser}))
	if st, _ := f.Session(context.Background(), sid); st.Status != event.SessionStatusApproval {
		t.Fatalf("status before the reply = %s", st.Status)
	}
	send(t, f, cmd(t, "p", event.CmdPermissionReply, event.PermissionReply{RequestID: "perm-d1", Decision: event.ReplyDecisionAllowOnce}))
	got := strings.Join(kinds(t, f), " ")
	if !strings.HasSuffix(got, "permission.resolved status:working tool.started gate.started gate.finished tool.finished "+
		"assistant.text files.changed turn.ended status:done") {
		t.Errorf("demo log: %s", got)
	}
}

func TestReplayAndEcho(t *testing.T) {
	f := newSession(t)
	rec := []event.Event{
		{Kind: event.KindTurnStarted},
		{Kind: event.KindAssistantText, Data: json.RawMessage(`{"blockId":"b","text":"hi"}`)},
		{Kind: event.KindTurnEnded},
	}
	f.Script = Replay(rec)
	send(t, f, cmd(t, "m", event.CmdMessageSend, event.MessageSend{MessageID: "m", Text: "x", Origin: event.MessageOriginUser}))
	if got := strings.Join(kinds(t, f), " "); !strings.Contains(got, "turn.started status:working assistant.text turn.ended") {
		t.Errorf("replay log: %s", got)
	}
	if d := Echo("hi"); len(d) != 1 || d[0].Kind != event.KindAssistantText {
		t.Errorf("Echo = %+v", d)
	}
}

func TestJobs(t *testing.T) {
	ctx := context.Background()
	f := New("sb1")
	st, err := f.StartJob(ctx, flowdproto.JobSpec{ID: "j1", Kind: flowdproto.JobConfigSync, Repo: "dev/agent-config"})
	if err != nil || !st.Done || st.Commit == "" || st.Lines != 1 {
		t.Errorf("config sync = %+v, %v", st, err)
	}
	f.JobScript = func(flowdproto.JobSpec) ([]string, int) { return []string{"go mod download", "failed"}, 2 }
	_, _ = f.StartJob(ctx, flowdproto.JobSpec{ID: "j2", Kind: flowdproto.JobSetup, Repo: "dev/ship"})
	if again, _ := f.StartJob(ctx, flowdproto.JobSpec{ID: "j2"}); again.ExitCode != 2 || again.Commit != "" {
		t.Errorf("a repeated start = %+v, want the first job", again)
	}
	if st, _ := f.Job(ctx, "j2"); st.ExitCode != 2 || st.Kind != flowdproto.JobSetup {
		t.Errorf("job = %+v", st)
	}
	lines, err := f.JobLog(ctx, "j2", 1)
	if err != nil || len(lines) != 1 || lines[0].N != 2 || lines[0].Text != "failed" {
		t.Errorf("log after 1 = %+v, %v", lines, err)
	}
	if _, err := f.Job(ctx, "x"); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("missing job: %v", err)
	}
	if _, err := f.JobLog(ctx, "x", 0); !errors.Is(err, flowdproto.ErrNotFound) {
		t.Errorf("missing job log: %v", err)
	}
}

func TestEventsFollowsAndStops(t *testing.T) {
	f := newSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan event.Event, 16)
	go func() {
		for e, err := range f.Events(ctx, sid, 2) {
			if err != nil {
				return
			}
			got <- e
		}
		close(got)
	}()
	send(t, f, cmd(t, "s", event.CmdSettingsSet, event.SettingsSet{Model: "x"}))
	select {
	case e := <-got:
		if e.Seq != 3 || e.Kind != event.KindSessionSettings {
			t.Errorf("first followed event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live event")
	}
	cancel()
	for range got {
	}
	for _, err := range f.Events(context.Background(), "missing", 0) {
		if !errors.Is(err, flowdproto.ErrNotFound) {
			t.Errorf("events of a missing session: %v", err)
		}
	}
}

func TestControl(t *testing.T) {
	f := New("sb1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply := make(chan flowdproto.ControlReply, 1)
	go func() {
		r, err := f.Request(ctx, flowdproto.ControlRequest{ID: "c1", Session: sid, Kind: flowdproto.ControlChildList})
		if err == nil {
			reply <- r
		}
	}()
	for r, err := range f.Control(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != "c1" || r.Created.IsZero() {
			t.Fatalf("control request = %+v", r)
		}
		_ = f.ReplyControl(ctx, flowdproto.ControlReply{ID: "c1", Data: json.RawMessage(`[]`)})
		_ = f.ReplyControl(ctx, flowdproto.ControlReply{ID: "c1", Error: "ignored"})
		break
	}
	if r := <-reply; string(r.Data) != "[]" || r.Error != "" {
		t.Errorf("reply = %+v", r)
	}
	short, stop := context.WithCancel(context.Background())
	stop()
	if _, err := f.Request(short, flowdproto.ControlRequest{ID: "c2"}); !errors.Is(err, context.Canceled) {
		t.Errorf("request with a dead context: %v", err)
	}
	for range f.Control(short) {
		t.Error("control yielded after its context ended")
	}
}

func TestSandboxWideCalls(t *testing.T) {
	ctx := context.Background()
	f := newSession(t)
	f.SetClaudeSignedIn(true)
	_ = f.PutEnv(ctx, flowdproto.Env{Revision: 2, GitName: "dev"})
	_ = f.PutEnv(ctx, flowdproto.Env{Revision: 1, GitName: "old"})
	if f.Env().GitName != "dev" {
		t.Errorf("env = %+v, want revision 2 kept", f.Env())
	}
	if r, _ := f.EnsureRepo(ctx, flowdproto.RepoSpec{Repo: "dev/loom"}); r.Path != "/home/flow/work/loom" {
		t.Errorf("repo = %+v", r)
	}
	if _, err := f.EnsureTerminal(ctx, flowdproto.TerminalSpec{ID: "t1", Dir: "/home/flow"}); err != nil {
		t.Fatal(err)
	}
	h, _ := f.Hello(ctx)
	if !flowdproto.Compatible(h) || !h.ClaudeSignedIn || h.EnvRevision != 2 || len(h.Sessions) != 1 || len(h.Terminals) != 1 {
		t.Errorf("hello = %+v", h)
	}
	ws, err := f.AddWorktree(ctx, sid, flowdproto.WorktreeSpec{Repo: "loom", Branch: "b"})
	if err != nil || ws.Path != "/home/flow/work/.sessions/flaky/loom" {
		t.Errorf("worktree = %+v, %v", ws, err)
	}
	f.SetDiff(sid, "dev/loom", apitypes.Diff{Repo: "dev/loom"})
	if d, err := f.Diff(ctx, sid, "dev/loom"); err != nil || d.Repo != "dev/loom" {
		t.Errorf("diff = %+v, %v", d, err)
	}
	for name, err := range map[string]error{
		"diff":      func() error { _, err := f.Diff(ctx, sid, "x"); return err }(),
		"session":   func() error { _, err := f.Session(ctx, "x"); return err }(),
		"stop":      f.StopSession(ctx, "x", false),
		"worktree":  func() error { _, err := f.AddWorktree(ctx, "x", flowdproto.WorktreeSpec{}); return err }(),
		"attach":    func() error { _, err := f.AttachTerminal(ctx, "x"); return err }(),
		"terminals": nil,
	} {
		if name != "terminals" && !errors.Is(err, flowdproto.ErrNotFound) {
			t.Errorf("%s of a missing target: %v", name, err)
		}
	}
	conn, err := f.AttachTerminal(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = conn.Write([]byte("ls\n")); _ = conn.Close() }()
	if b, _ := io.ReadAll(conn); string(b) != "ls\n" || conn.Resize(80, 24) != nil {
		t.Errorf("terminal echoed %q", b)
	}
	_ = f.CloseTerminal(ctx, "t1")
	rec := httptest.NewRecorder()
	f.Port(5173).ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Body.String() != "preview of port 5173" {
		t.Errorf("port page = %q", rec.Body.String())
	}
}
