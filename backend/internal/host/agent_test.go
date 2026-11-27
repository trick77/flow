package host

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/hostproto"
)

// writeLog writes events (kind, turn, payload) as a previous host would have.
func writeLog(t *testing.T, path string, evs ...event.Event) {
	t.Helper()
	w, _, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, e := range evs {
		if _, err := w.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func ev(t *testing.T, kind event.EventKind, turn string, payload any) event.Event {
	t.Helper()
	e, err := event.New(sid, kind, time.Now(), payload)
	if err != nil {
		t.Fatal(err)
	}
	e.Turn = turn
	return e
}

func cost(v float64) *float64 { return &v }

// The cost a resumed agent continues from is the sum of the costs the log's provider turns reported; turns without
// one (lost, zeroed, interrupted before a result) add nothing, and neither do turns before the provider marker
// (replay turns of a session from before the marker existed, which carry the recording's cost).
func TestAgentState_fromTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	turn := func(n string, ended event.TurnEnded, init bool) []event.Event {
		out := []event.Event{ev(t, event.KindTurnStarted, n, event.TurnStarted{MessageID: n, Provider: "claude"}),
			ev(t, event.KindAssistantText, n, event.AssistantText{BlockID: n + ":0", Text: "x"})}
		if init {
			out = append(out, ev(t, event.KindNotice, n, event.Notice{Code: NoticeProviderSession, Level: "info"}))
		}
		return append(out, ev(t, event.KindTurnEnded, n, ended))
	}
	evs := []event.Event{ev(t, event.KindSessionStarted, "", event.SessionStarted{Provider: "claude", Model: "m0",
		Mode: event.PermissionModeAsk, Cwd: "/"})}
	evs = append(evs, turn("t-0", event.TurnEnded{Outcome: "completed", CostUSD: cost(0.7), Model: "m"}, false)...)
	writeLog(t, path, evs...)
	r := start(t, path, newFake())
	if st := r.h.AgentState(); st.Resume || st.PriorCostUSD != 0 {
		t.Errorf("a replay turn counted as the provider's: %+v", st)
	}
	r.stop()

	evs = nil
	evs = append(evs, turn("t-1", event.TurnEnded{Outcome: "completed", CostUSD: cost(0.1)}, true)...)
	evs = append(evs, turn("t-2", event.TurnEnded{Outcome: "lost"}, false)...)
	evs = append(evs, turn("t-3", event.TurnEnded{Outcome: "completed", CostUSD: cost(0.25),
		Usage: &event.Usage{InputTokens: 1}}, false)...)
	evs = append(evs, turn("t-4", event.TurnEnded{Outcome: "interrupted"}, false)...)
	evs = append(evs, turn("t-5", event.TurnEnded{Outcome: "error", ErrorCode: "adapter_exited"}, false)...)
	evs = append(evs,
		ev(t, event.KindSessionSettings, "", event.SessionSettings{Model: "m1", ModeCap: event.PermissionModeAuto}),
		ev(t, event.KindPlanResolved, "t-4", event.PlanResolved{RequestID: "p", Decision: "approved",
			Mode: event.PermissionModeAcceptEdits}),
		ev(t, event.KindPlanResolved, "t-4", event.PlanResolved{RequestID: "q", Decision: "rejected",
			Mode: event.PermissionModePlan}))
	writeLog(t, path, evs...)

	r = start(t, path, newFake())
	got := r.h.AgentState()
	want := AgentState{Resume: true, PriorCostUSD: 0.35, Model: "m1", Mode: event.PermissionModeAcceptEdits,
		ModeCap: event.PermissionModeAuto}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AgentState = %+v, want %+v", got, want)
	}

	// A turn this host ends adds its cost; a settings.set moves model and mode.
	f := newFake()
	r.stop()
	r = start(t, path, f)
	_, _ = r.h.Command(send(t, "m9", "go"))
	<-f.got
	f.out <- draft(event.KindTurnEnded, event.TurnEnded{Outcome: "completed", CostUSD: cost(0.05)})
	r.waitKind(event.KindTurnEnded, 7)
	_, _ = r.h.Command(cmd(t, "s1", event.CmdSettingsSet, event.SettingsSet{Model: "m2", Mode: event.PermissionModePlan}))
	got = r.h.AgentState()
	if got.PriorCostUSD != 0.4 || got.Model != "m2" || got.Mode != event.PermissionModePlan {
		t.Errorf("after a turn and a settings change: %+v", got)
	}
}

// The worktrees a restarted agent is told about are the log's: the spec's (logged at the first start) and one added
// while the host ran, which the host's spec never sees; also after a restart.
func TestAgentState_worktreesFromTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	r := start(t, path, newFake())
	added := event.WorktreeAdd{Repo: "trick77/web", Branch: "flow/demo", Path: "/home/flow/work/.sessions/demo/web",
		BaseRef: "origin/main", BaseSHA: "def"}
	if _, err := r.h.Command(cmd(t, "w1", event.CmdWorktreeAdd, added)); err != nil {
		t.Fatal(err)
	}
	want := []event.WorktreeAdded{{Repo: "trick77/toolkit", Branch: "flow/demo",
		Path: "/home/flow/work/.sessions/demo/toolkit", BaseRef: "origin/master", BaseSHA: "abc"}, event.WorktreeAdded(added)}
	if got := r.h.AgentState().Worktrees; !reflect.DeepEqual(got, want) {
		t.Errorf("running: worktrees = %+v, want %+v", got, want)
	}
	r.stop()
	r = start(t, path, newFake())
	if got := r.h.AgentState().Worktrees; !reflect.DeepEqual(got, want) {
		t.Errorf("after a restart: worktrees = %+v, want %+v", got, want)
	}
}

// session.started names the agent's working directory: the spec's Cwd, the session directory for a spec without one.
func TestSessionStarted_cwd(t *testing.T) {
	for _, tc := range []struct{ cwd, want string }{
		{"", "/home/flow/work/.sessions/demo"},
		{"/home/flow/work/.sessions/demo/toolkit", "/home/flow/work/.sessions/demo/toolkit"},
	} {
		sp := spec()
		sp.Cwd = tc.cwd
		path := filepath.Join(t.TempDir(), "events.jsonl")
		h, err := New(Config{Spec: sp, LogPath: path, Adapter: newFake(), Version: "test"})
		if err != nil {
			t.Fatal(err)
		}
		_ = h.Close()
		evs, err := eventlog.NewReader(path).Read(0, 0)
		if err != nil || len(evs) == 0 {
			t.Fatal(err)
		}
		p, err := evs[0].Payload()
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := p.(*event.SessionStarted); !ok || s.Cwd != tc.want {
			t.Errorf("cwd %q: session.started = %+v, want cwd %q", tc.cwd, p, tc.want)
		}
	}
}

// session.started is the host's own: a session that never ran a turn on the provider is started, not resumed, also
// after a host restart; neither a zeroed result nor agent output proves a session, only the provider's init does.
func TestAgentState_noProviderSessionYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	r := start(t, path, newFake())
	r.stop()
	r = start(t, path, newFake())
	if st := r.h.AgentState(); st.Resume || st.PriorCostUSD != 0 || st.Model != "claude-opus-5-5" ||
		st.Mode != event.PermissionModeAcceptEdits {
		t.Errorf("fresh session after a restart: %+v", st)
	}
	f := newFake()
	r.stop()
	r = start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "go"))
	<-f.got
	f.out <- draft(event.KindNotice, event.Notice{Code: "x", Level: "warning"})
	// A zeroed result, as the sidecar maps a Claude Code start failure: usage all zero, no cost, no model.
	f.out <- draft(event.KindTurnEnded, event.TurnEnded{Outcome: "error", ErrorCode: "provider_exited",
		Usage: &event.Usage{}})
	r.waitKind(event.KindTurnEnded, 1)
	if r.h.AgentState().Resume {
		t.Error("a turn without provider output counted as a provider session")
	}
	f.out <- draft(event.KindSessionStarted, event.SessionStarted{Provider: "claude", Model: "m", Mode: "ask", Cwd: "/",
		ProviderSessionID: sid})
	f.out <- draft(event.KindToolStarted, event.ToolStarted{ToolID: "late", Kind: "bash", Name: "Bash"})
	r.waitKind(event.KindToolStarted, 1)
	if r.h.AgentState().Resume {
		t.Error("an init outside a turn counted as a provider session")
	}
}

// The provider's init inside a turn (the adapter's session.started) is logged once as the resume marker, also when
// the turn is interrupted before any output, and a restarted host resumes from it.
func TestAgentState_initMarksTheProviderSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	_, _ = r.h.Command(send(t, "m1", "go"))
	<-f.got
	init := draft(event.KindSessionStarted, event.SessionStarted{Provider: "claude", Model: "m", Mode: "ask", Cwd: "/",
		ProviderSessionID: sid})
	f.out <- init
	f.out <- init // every query start reports one
	_, _ = r.h.Command(cmd(t, "i1", event.CmdTurnInterrupt, event.TurnInterrupt{}))
	f.out <- draft(event.KindTurnEnded, event.TurnEnded{Outcome: "interrupted"})
	evs := r.waitKind(event.KindTurnEnded, 1)
	if n := count(evs, event.KindNotice); n != 1 {
		t.Errorf("%d markers", n)
	}
	if !r.h.AgentState().Resume {
		t.Error("the init did not mark the session")
	}
	r.stop()
	r = start(t, path, newFake())
	if !r.h.AgentState().Resume {
		t.Error("a restarted host lost the marker")
	}
}

// A lower cap pulls the mode in force down: logged and sent to the agent with that mode, and a restarted agent
// starts with it.
func TestSettings_modeHeldToTheCap(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f) // spec mode accept_edits
	if _, err := r.h.Command(cmd(t, "s1", event.CmdSettingsSet, event.SettingsSet{ModeCap: event.PermissionModeAsk})); err != nil {
		t.Fatal(err)
	}
	c := <-f.got
	var sent event.SettingsSet
	_ = json.Unmarshal(c.Data, &sent)
	if sent.Mode != event.PermissionModeAsk || sent.ModeCap != event.PermissionModeAsk {
		t.Errorf("agent got %+v", sent)
	}
	evs := r.waitKind(event.KindSessionSettings, 1)
	var logged event.SessionSettings
	_ = json.Unmarshal(evs[len(evs)-1].Data, &logged)
	if logged.Mode != event.PermissionModeAsk {
		t.Errorf("logged %+v", logged)
	}
	// A mode asked above the cap is held to it as well.
	_, _ = r.h.Command(cmd(t, "s2", event.CmdSettingsSet, event.SettingsSet{Mode: event.PermissionModeFullAccess}))
	<-f.got
	if st := r.h.AgentState(); st.Mode != event.PermissionModeAsk {
		t.Errorf("agent state %+v", st)
	}
}

// AgentState holds a mode above the cap to the cap, also for a log written before the host clamped.
func TestAgentState_clampsAnOldLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeLog(t, path, ev(t, event.KindSessionStarted, "", event.SessionStarted{Provider: "claude", Model: "m",
		Mode: event.PermissionModeFullAccess, ModeCap: event.PermissionModeFullAccess, Cwd: "/"}),
		ev(t, event.KindSessionSettings, "", event.SessionSettings{Mode: event.PermissionModeFullAccess}),
		ev(t, event.KindSessionSettings, "", event.SessionSettings{ModeCap: event.PermissionModeAcceptEdits}))
	r := start(t, path, newFake())
	if st := r.h.AgentState(); st.Mode != event.PermissionModeAcceptEdits {
		t.Errorf("mode %s above the cap %s", st.Mode, st.ModeCap)
	}
}

// A settings change the log cannot hold is refused and never reaches the agent.
func TestSettings_writtenBeforeSent(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	_, err := r.h.Command(cmd(t, "s1", event.CmdSettingsSet, event.SettingsSet{Model: strings.Repeat("<", event.MaxLine/5)}))
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != hostproto.CodeInvalid {
		t.Errorf("oversized settings: %v", err)
	}
	select {
	case c := <-f.got:
		t.Errorf("the agent got %s", c.Kind)
	default:
	}
}

// host.shutdown reaches the adapter in the host's own mode.
func TestShutdown_isForwarded(t *testing.T) {
	for _, whenIdle := range []bool{true, false} {
		f := newFake()
		r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
		_, _ = r.h.Command(send(t, "m1", "go"))
		<-f.got
		_, _ = r.h.Command(cmd(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: "stop", WhenIdle: whenIdle}))
		var got []event.CommandKind
		for len(f.got) > 0 {
			got = append(got, (<-f.got).Kind)
		}
		last := got[len(got)-1]
		if last != event.CmdHostShutdown {
			t.Errorf("whenIdle=%v: adapter got %v", whenIdle, got)
		}
		r.stop()
	}
}
