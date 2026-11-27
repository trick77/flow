package claude_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host"
	"github.com/trick77/flow/internal/host/claude"
	"github.com/trick77/flow/internal/layout"
)

const sid = "0193a8f2-9000-7000-8000-0000000000c1"

// syncBuffer is the journal: what the adapter logs.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type opts struct {
	flags     []string
	auth      event.ClaudeAuth
	environ   []string
	logPath   string
	dump      string
	argv      []string
	record    string
	shutdown  time.Duration
	kill      time.Duration
	stderrCap int64
}

type rig struct {
	t       *testing.T
	h       *host.Host
	logPath string
	dump    string
	journal *syncBuffer
	cancel  context.CancelFunc
	done    chan error
}

func baseEnviron() []string {
	// The image's default unit environment (deploy/image), then what flowd's EnvironmentFile adds, then strays.
	return []string{"PATH=/home/flow/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/home/flow", "LANG=C.UTF-8",
		"GOMODCACHE=/srv/flow-home/cache/go-mod", "npm_config_cache=/srv/flow-home/cache/npm",
		"PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright", "DISABLE_AUTOUPDATER=1", "OPENCODE_DISABLE_AUTOUPDATE=1",
		"GIT_AUTHOR_NAME=Jan",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-token", "ANTHROPIC_API_KEY=stray-api-key", "ANTHROPIC_AUTH_TOKEN=stray-auth",
		"CLAUDE_CODE_USE_BEDROCK=1", "AWS_SECRET_ACCESS_KEY=stray-aws", "ANTHROPIC_BASE_URL=http://stray",
		"GH_TOKEN=stray-gh", "BACKEND_MCP_TOKEN=mcp-token", "BACKEND_LOG_LEVEL=debug"}
}

func newRig(t *testing.T, o opts) *rig {
	t.Helper()
	dir := t.TempDir()
	if o.logPath == "" {
		o.logPath = filepath.Join(dir, "events.jsonl")
	}
	if o.dump == "" {
		o.dump = filepath.Join(dir, "dump.jsonl")
	}
	if o.environ == nil {
		o.environ = baseEnviron()
	}
	if o.argv == nil {
		o.argv = append([]string{os.Args[0], "-test.run=^TestFakeSidecar$", "--", fakeMarker, o.dump}, o.flags...)
	}
	env, _ := claude.Env(o.environ, o.auth)
	journal := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(journal, nil))
	var h *host.Host
	a := claude.New(claude.Config{
		Argv: o.argv, Dir: dir, Env: env, RecordPath: o.record,
		Session: claude.Session{ID: sid, Cwd: dir, ClaudeAuth: o.auth, ClaudeExecutable: layout.ClaudeCLI,
			SettingsPath: layout.Settings, Version: "1.2.3", MCPCommand: "/opt/flow/releases/1.2.3/flowd",
			MCPToken: "mcp-token"},
		State: func() claude.State {
			s := h.AgentState()
			return claude.State{Resume: s.Resume, PriorCostUSD: s.PriorCostUSD, Model: s.Model, Mode: s.Mode,
				ModeCap: s.ModeCap}
		},
		Log: log, ShutdownWait: o.shutdown, KillWait: o.kill, StderrCap: o.stderrCap,
	})
	spec := host.Spec{Session: flowdproto.SessionSpec{ID: sid, Name: "demo", Provider: event.ProviderClaude,
		ClaudeAuth: o.auth, Model: "claude-opus-5-5", Mode: event.PermissionModeAsk, ModeCap: event.PermissionModeAuto},
		Dir: dir}
	var err error
	h, err = host.New(host.Config{Spec: spec, LogPath: o.logPath, Adapter: a, Version: "1.2.3", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{t: t, h: h, logPath: o.logPath, dump: o.dump, journal: journal, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- h.Pump(ctx) }()
	t.Cleanup(func() { r.stop() })
	return r
}

// stop ends the host as Run does on ctx; it returns how long the adapter took.
func (r *rig) stop() time.Duration {
	if r.cancel == nil {
		return 0
	}
	t0 := time.Now()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
		r.t.Fatal("the adapter did not stop")
	}
	_ = r.h.Close()
	r.cancel = nil
	return time.Since(t0)
}

func (r *rig) events() []event.Event {
	evs, err := eventlog.NewReader(r.logPath).Read(0, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return evs
}

func (r *rig) waitKind(kind event.EventKind, n int) []event.Event {
	r.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		evs := r.events()
		if count(evs, kind) >= n {
			return evs
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("no %d× %s in %v", n, kind, kinds(r.events()))
	return nil
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

func kinds(evs []event.Event) []event.EventKind {
	var out []event.EventKind
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func gapless(t *testing.T, evs []event.Event) {
	t.Helper()
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
}

func command(t *testing.T, id string, kind event.CommandKind, payload any) event.Command {
	t.Helper()
	c, err := event.NewCommand(id, sid, kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *rig) send(id, text string) {
	r.t.Helper()
	if _, err := r.h.Command(command(r.t, id, event.CmdMessageSend, event.MessageSend{MessageID: id, Text: text,
		Origin: event.MessageOriginUser})); err != nil {
		r.t.Fatal(err)
	}
}

func lastTurnEnded(t *testing.T, evs []event.Event) (event.TurnEnded, event.Event) {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == event.KindTurnEnded {
			var te event.TurnEnded
			_ = json.Unmarshal(evs[i].Data, &te)
			return te, evs[i]
		}
	}
	t.Fatal("no turn.ended")
	return event.TurnEnded{}, event.Event{}
}

// dumpEntry is one line the fake wrote.
type dumpEntry struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
	Env  []string        `json:"env"`
	Args []string        `json:"args"`
}

func (r *rig) dumped() []dumpEntry {
	b, err := os.ReadFile(r.dump)
	if err != nil {
		return nil
	}
	var out []dumpEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var d dumpEntry
		if json.Unmarshal(sc.Bytes(), &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

func (r *rig) starts() []claude.StartSpec {
	var out []claude.StartSpec
	for _, d := range r.dumped() {
		if d.Type == "start" {
			var s claude.StartSpec
			if err := json.Unmarshal(d.Data, &s); err != nil {
				r.t.Fatal(err)
			}
			out = append(out, s)
		}
	}
	return out
}

func (r *rig) cmds(kind string) []dumpEntry {
	var out []dumpEntry
	for _, d := range r.dumped() {
		if d.Type == "cmd" && d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

func (r *rig) waitCmds(kind string, n int) []dumpEntry {
	r.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c := r.cmds(kind); len(c) >= n {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("the sidecar got %d× %s, want %d", len(r.cmds(kind)), kind, n)
	return nil
}

func raw(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The start line on the first start creates the session; after the sidecar ended between turns, and after a host
// restart, it resumes, with the current settings and the cost of the earlier turns.
func TestStartLine_firstStartAndResume(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "hello")
	r.waitKind(event.KindTurnEnded, 1)
	starts := r.starts()
	if len(starts) != 1 {
		t.Fatalf("%d starts", len(starts))
	}
	dir := filepath.Dir(r.logPath)
	want := claude.StartSpec{SessionID: sid, Resume: false, Cwd: dir, Mode: event.PermissionModeAsk,
		ModeCap: event.PermissionModeAuto, Model: "claude-opus-5-5", ClaudeExecutable: layout.ClaudeCLI,
		SettingsPath: layout.Settings, Version: "1.2.3", MCPServers: map[string]claude.MCPServer{"flow": {Type: "stdio",
			Command: "/opt/flow/releases/1.2.3/flowd", Args: []string{"mcp"},
			Env: map[string]string{claude.MCPTokenVar: "mcp-token"}}}}
	if raw(t, starts[0]) != raw(t, want) {
		t.Errorf("first start\n got %s\nwant %s", raw(t, starts[0]), raw(t, want))
	}
	for _, d := range r.dumped() {
		if d.Type == "start" && (!strings.Contains(string(d.Data), `"sessionId"`) ||
			strings.Contains(string(d.Data), "priorCostUsd")) {
			t.Errorf("first start line carries a prior cost or no sessionId: %s", d.Data)
		}
	}

	_, _ = r.h.Command(command(t, "s1", event.CmdSettingsSet, event.SettingsSet{Model: "claude-sonnet-5",
		Mode: event.PermissionModePlan}))
	r.send("m2", "exit") // completes, then the sidecar exits between turns
	r.waitKind(event.KindTurnEnded, 2)
	r.waitJournal("sidecar ended") // a message sent while it is still going would fail, never be re-sent
	r.send("m3", "hello")
	r.waitKind(event.KindTurnEnded, 3)
	starts = r.starts()
	if len(starts) != 2 {
		t.Fatalf("%d starts after the exit between turns", len(starts))
	}
	s := starts[1]
	if !s.Resume || s.PriorCostUSD == nil || *s.PriorCostUSD != 1.0 || s.Model != "claude-sonnet-5" ||
		s.Mode != event.PermissionModePlan || s.SessionID != sid {
		t.Errorf("restart start line %s", raw(t, s))
	}
	evs := r.events()
	gapless(t, evs)
	for _, e := range evs {
		if e.Kind == event.KindTurnEnded {
			var te event.TurnEnded
			_ = json.Unmarshal(e.Data, &te)
			if te.Outcome != event.TurnOutcomeCompleted {
				t.Errorf("turn %s ended %s %s", e.Turn, te.Outcome, te.ErrorCode)
			}
		}
	}
	r.stop()

	// A new host on the same log: resume, and the cost from the log.
	r2 := newRig(t, opts{logPath: r.logPath, dump: r.dump})
	r2.send("m4", "hello")
	r2.waitKind(event.KindTurnEnded, 4)
	starts = r2.starts()
	s = starts[len(starts)-1]
	if !s.Resume || s.PriorCostUSD == nil || *s.PriorCostUSD != 1.5 || s.Model != "claude-sonnet-5" {
		t.Errorf("start line after a host restart %s", raw(t, s))
	}
}

// A turn with a tool call and a permission request answered through the host.
func TestTurn_toolAndPermission(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "tool")
	r.waitKind(event.KindPermissionRequested, 1)
	time.Sleep(50 * time.Millisecond)
	if st := r.h.Hello(0).Status; st != event.SessionStatusApproval {
		t.Fatalf("status %s", st)
	}
	if _, err := r.h.Command(command(t, "reply-req-m1", event.CmdPermissionReply, event.PermissionReply{
		RequestID: "req-m1", Decision: event.ReplyDecisionAllowOnce})); err != nil {
		t.Fatal(err)
	}
	r.waitKind(event.KindTurnEnded, 1)
	time.Sleep(30 * time.Millisecond)
	evs := r.events()
	gapless(t, evs)
	var got []event.EventKind
	for _, e := range evs {
		if e.Turn == "t-1" && e.Kind != event.KindSessionStatus {
			got = append(got, e.Kind)
		}
	}
	want := []event.EventKind{event.KindMessageUser, event.KindTurnStarted, event.KindNotice, event.KindToolStarted,
		event.KindPermissionRequested, event.KindPermissionResolved, event.KindToolFinished, event.KindAssistantText,
		event.KindTurnEnded}
	if !slices.Equal(got, want) {
		t.Errorf("turn events %v\nwant %v", got, want)
	}
	te, _ := lastTurnEnded(t, evs)
	if te.Outcome != event.TurnOutcomeCompleted || te.CostUSD == nil || *te.CostUSD != 0.25 {
		t.Errorf("turn.ended %+v", te)
	}
	if st := r.h.Hello(0).Status; st != event.SessionStatusDone {
		t.Errorf("status %s", st)
	}
	if c := r.cmds("permission.reply"); len(c) != 1 || c[0].ID != "reply-req-m1" {
		t.Errorf("the sidecar got replies %+v", c)
	}
}

func TestTurn_interrupt(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "wait")
	r.waitCmds("message.send", 1)
	if _, err := r.h.Command(command(t, "i1", event.CmdTurnInterrupt, event.TurnInterrupt{})); err != nil {
		t.Fatal(err)
	}
	evs := r.waitKind(event.KindTurnEnded, 1)
	te, e := lastTurnEnded(t, evs)
	if te.Outcome != event.TurnOutcomeInterrupted || e.Cmd != "i1" {
		t.Errorf("turn ended %s by %q", te.Outcome, e.Cmd)
	}
}

// The sidecar dies mid-turn: the turn fails as adapter_exited, its open request expires, and the next message
// starts a new sidecar that resumes.
func TestCrash_midTurn(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "crash")
	evs := r.waitKind(event.KindTurnEnded, 1)
	te, _ := lastTurnEnded(t, evs)
	if te.Outcome != event.TurnOutcomeError || te.ErrorCode != claude.CodeExited {
		t.Errorf("turn ended %+v", te)
	}
	var res event.PermissionResolved
	for _, e := range evs {
		if e.Kind == event.KindPermissionResolved {
			_ = json.Unmarshal(e.Data, &res)
		}
	}
	if res.RequestID != "req-m1" || res.Decision != event.PermissionDecisionExpired {
		t.Errorf("open request resolved %+v", res)
	}
	time.Sleep(30 * time.Millisecond)
	if st := r.h.Hello(0).Status; st != event.SessionStatusFailed {
		t.Errorf("status %s", st)
	}
	if !strings.Contains(r.journal.String(), "sidecar ended") {
		t.Error("the exit is not in the journal")
	}
	// The owner is told the message may not have run, and it is not sent again by itself.
	if got := notices(evs); !slices.Contains(got, claude.NoticeNotRun+" messageId=m1") {
		t.Errorf("notices %v", got)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(r.starts()); n != 1 {
		t.Errorf("%d sidecar starts before the next message", n)
	}
	r.send("m2", "hello")
	evs = r.waitKind(event.KindTurnEnded, 2)
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeCompleted {
		t.Errorf("turn after the crash %+v", te)
	}
	starts := r.starts()
	if len(starts) != 2 || !starts[1].Resume || starts[1].PriorCostUSD == nil || *starts[1].PriorCostUSD != 0 {
		t.Errorf("start after the crash %s", raw(t, starts))
	}
	gapless(t, evs)
}

// A sidecar that cannot start fails the turn; the host lives on.
func TestStart_failureFailsTheTurn(t *testing.T) {
	r := newRig(t, opts{argv: []string{filepath.Join(t.TempDir(), "no-node")}})
	r.send("m1", "hello")
	evs := r.waitKind(event.KindTurnEnded, 1)
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeError || te.ErrorCode != claude.CodeStart {
		t.Errorf("turn ended %+v", te)
	}
	r.send("m2", "hello")
	r.waitKind(event.KindTurnEnded, 2)
}

// A sidecar that refuses its start line is stopped; the turn fails instead of hanging.
func TestStart_refusedStartLine(t *testing.T) {
	r := newRig(t, opts{flags: []string{"refuse-start"}})
	r.send("m1", "wait")
	evs := r.waitKind(event.KindTurnEnded, 1)
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeError || te.ErrorCode != claude.CodeExited {
		t.Errorf("turn ended %+v", te)
	}
	if got := notices(evs); !slices.Contains(got, "sidecar_bad_command") {
		t.Errorf("the sidecar's notice is missing: %v", got)
	}
}

// notices are the logged notices as "code k=v".
func notices(evs []event.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == event.KindNotice {
			var n event.Notice
			_ = json.Unmarshal(e.Data, &n)
			s := n.Code
			for k, v := range n.Params {
				s += " " + k + "=" + v
			}
			out = append(out, s)
		}
	}
	return out
}

func (r *rig) waitJournal(s string) {
	r.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(r.journal.String(), s) {
			return
		}
	}
	r.t.Fatalf("no %q in the journal", s)
}

// The provider's init is logged once as the resume marker: a turn interrupted before any output still resumes.
func TestInit_marksResume(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "wait")
	r.waitKind(event.KindNotice, 1)
	_, _ = r.h.Command(command(t, "i1", event.CmdTurnInterrupt, event.TurnInterrupt{}))
	r.waitKind(event.KindTurnEnded, 1)
	r.stop()
	r2 := newRig(t, opts{logPath: r.logPath, dump: r.dump})
	r2.send("m2", "hello")
	evs := r2.waitKind(event.KindTurnEnded, 2)
	if s := r2.starts(); len(s) != 2 || !s[1].Resume {
		t.Errorf("start after the interrupted first turn %s", raw(t, s))
	}
	if got := notices(evs); !slices.Equal(got, []string{host.NoticeProviderSession + " providerSessionId=sid"}) {
		t.Errorf("notices %v", got)
	}
}

// An exiting sidecar's last stderr lines still reach the journal.
func TestStderr_lastWordsKept(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "lastwords")
	r.waitKind(event.KindTurnEnded, 1)
	r.waitJournal("sidecar ended")
	r.waitJournal("last-words-marker")
}

// The stop bounds let the sidecar's own orderly shutdown (its interrupt grace) finish first.
func TestStopBounds(t *testing.T) {
	cfg := claude.New(claude.Config{}).Config()
	if cfg.ShutdownWait <= claude.SidecarInterruptGrace || cfg.ShutdownWait+cfg.KillWait != claude.StopLimit ||
		claude.StopLimit >= 90*time.Second {
		t.Errorf("bounds %s + %s", cfg.ShutdownWait, cfg.KillWait)
	}
}

// The sidecar floods stdout and reads no stdin while the host writes commands: nothing blocks, nothing is lost.
func TestFlood_noDeadlock(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "flood")
	const n = 1500
	done := make(chan error, 1)
	go func() {
		for i := range n {
			c := command(t, fmt.Sprintf("s%d", i), event.CmdSettingsSet, event.SettingsSet{Model: strings.Repeat("m", 100)})
			if _, err := r.h.Command(c); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("commands blocked while the sidecar flooded")
	}
	evs := r.waitKind(event.KindTurnEnded, 1)
	r.waitCmds("settings.set", n)
	if c := count(evs, event.KindAssistantText); c != 300 {
		t.Errorf("%d assistant.text, want 300", c)
	}
	gapless(t, evs)
}

// Lines that are no draft are refused and journalled; the log stays readable and gapless.
func TestMalformedDrafts(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "malformed")
	evs := r.waitKind(event.KindTurnEnded, 1)
	gapless(t, evs)
	if c := count(evs, event.KindAssistantText); c != 1 {
		t.Errorf("%d assistant.text, want only the valid one", c)
	}
	for _, e := range evs {
		if _, ok := e.Kind.Info(); !ok {
			t.Errorf("unknown kind %s logged", e.Kind)
		}
	}
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeCompleted {
		t.Errorf("turn %+v", te)
	}
	j := r.journal.String()
	if n := strings.Count(j, "sidecar draft refused"); n != 7 {
		t.Errorf("%d refusals journalled, want 7:\n%s", n, j)
	}
	if strings.Contains(j, "aaaaaaaa") {
		t.Error("a refused line's content reached the journal")
	}
}

// A request draft or a turn.ended that cannot be read would leave the session waiting for good: the request is
// answered no, the turn ends as failed.
func TestMalformedDrafts_requestAndTurnEnd(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "badend")
	evs := r.waitKind(event.KindTurnEnded, 1)
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeError || te.ErrorCode != claude.CodeStart {
		t.Errorf("turn %+v", te)
	}
	reply := r.cmds("permission.reply")
	if len(reply) != 1 || reply[0].ID != "refuse-req-m1" || !strings.Contains(string(reply[0].Data), `"deny"`) {
		t.Errorf("the sidecar got %+v", reply)
	}
	if n := count(evs, event.KindPermissionRequested); n != 0 {
		t.Errorf("%d unreadable requests logged", n)
	}
	r.send("m2", "hello") // the session goes on
	r.waitKind(event.KindTurnEnded, 2)
}

// host.shutdown when idle: the turn finishes, the sidecar is told so and ends.
func TestShutdown_whenIdle(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "slow")
	r.waitCmds("message.send", 1)
	if _, err := r.h.Command(command(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: "upgrade",
		WhenIdle: true})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.h.Stopped():
	case <-time.After(10 * time.Second):
		t.Fatal("no stop after the turn")
	}
	if took := r.stop(); took > 5*time.Second {
		t.Errorf("stop took %s", took)
	}
	evs := r.events()
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeCompleted {
		t.Errorf("turn %+v", te)
	}
	sd := r.cmds("host.shutdown")
	if len(sd) != 1 || !strings.Contains(string(sd[0].Data), `"whenIdle":true`) {
		t.Errorf("the sidecar got shutdowns %+v", sd)
	}
}

// host.shutdown now: the host ends the turn itself, the sidecar is interrupted and told to stop now.
func TestShutdown_immediate(t *testing.T) {
	r := newRig(t, opts{})
	r.send("m1", "wait")
	r.waitCmds("message.send", 1)
	if _, err := r.h.Command(command(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: "stop"})); err != nil {
		t.Fatal(err)
	}
	<-r.h.Stopped()
	r.waitCmds("host.shutdown", 1)
	if took := r.stop(); took > 5*time.Second {
		t.Errorf("stop took %s", took)
	}
	evs := r.events()
	if n := count(evs, event.KindTurnEnded); n != 1 {
		t.Errorf("%d turn.ended", n)
	}
	if te, e := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeInterrupted || e.Cmd != "sd" {
		t.Errorf("turn %+v by %q", te, e.Cmd)
	}
	if sd := r.cmds("host.shutdown"); strings.Contains(string(sd[0].Data), "whenIdle") {
		t.Errorf("shutdown %s", sd[0].Data)
	}
	if len(r.cmds("turn.interrupt")) != 1 {
		t.Error("the sidecar was not interrupted")
	}
}

// A sidecar that ignores the shutdown, stdin's end and SIGTERM is killed after the bounds.
func TestShutdown_bounded(t *testing.T) {
	r := newRig(t, opts{flags: []string{"ignore-shutdown"}, shutdown: 300 * time.Millisecond, kill: 300 * time.Millisecond})
	r.send("m1", "hello")
	r.waitKind(event.KindTurnEnded, 1)
	took := r.stop()
	if took > 3*time.Second {
		t.Errorf("stop took %s", took)
	}
	if sd := r.cmds("host.shutdown"); len(sd) != 1 || sd[0].ID != "host-stop" {
		t.Errorf("the sidecar got %+v", sd)
	}
	if !strings.Contains(r.journal.String(), "did not stop in time") {
		t.Error("no escalation journalled")
	}
}

// Only the chosen mode's credential reaches the sidecar; stray provider variables of the host never do.
func TestEnvironment_allowList(t *testing.T) {
	r := newRig(t, opts{auth: event.ClaudeAuthSubscriptionToken})
	r.send("m1", "hello")
	r.waitKind(event.KindTurnEnded, 1)
	var env []string
	for _, d := range r.dumped() {
		if d.Type == "env" {
			env = d.Env
		}
	}
	has := func(kv string) bool { return slices.Contains(env, kv) }
	for _, kv := range []string{"CLAUDE_CODE_OAUTH_TOKEN=oauth-token",
		"PATH=/home/flow/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/home/flow", "LANG=C.UTF-8",
		"GIT_AUTHOR_NAME=Jan", "CLAUDE_CONFIG_DIR=" + layout.ClaudeConfig, "GH_CONFIG_DIR=" + layout.GHConfigDir,
		"DISABLE_AUTOUPDATER=1", "OPENCODE_DISABLE_AUTOUPDATE=1", "GOMODCACHE=/srv/flow-home/cache/go-mod",
		"npm_config_cache=/srv/flow-home/cache/npm", "PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright"} {
		if !has(kv) {
			t.Errorf("missing %s in %v", kv, env)
		}
	}
	// A command the agent runs finds its tools on that PATH.
	var found string
	for _, d := range r.dumped() {
		if d.Type == "lookpath" {
			found = string(d.Data)
		}
	}
	if !strings.Contains(found, "/sh") {
		t.Errorf("the sidecar's environment finds no sh: %s", found)
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		for _, bad := range []string{"ANTHROPIC_", "AWS_", "CLAUDE_CODE_USE_", "GH_TOKEN", "BACKEND_"} {
			if strings.HasPrefix(name, bad) {
				t.Errorf("%s reached the sidecar", name)
			}
		}
	}
}

func TestEnv_perMode(t *testing.T) {
	for _, tc := range []struct {
		auth event.ClaudeAuth
		want string
		ok   bool
	}{
		{"", "CLAUDE_CODE_OAUTH_TOKEN=oauth-token", true},
		{event.ClaudeAuthSubscriptionToken, "CLAUDE_CODE_OAUTH_TOKEN=oauth-token", true},
		{event.ClaudeAuthAPIKey, "ANTHROPIC_API_KEY=stray-api-key", true},
		{event.ClaudeAuthSubscriptionLogin, "", true},
	} {
		env, ok := claude.Env(baseEnviron(), tc.auth)
		var creds []string
		for _, kv := range env {
			if strings.HasPrefix(kv, "ANTHROPIC_") || strings.HasPrefix(kv, "CLAUDE_CODE_") {
				creds = append(creds, kv)
			}
		}
		if ok != tc.ok || (tc.want == "" && len(creds) != 0) || (tc.want != "" && !slices.Equal(creds, []string{tc.want})) {
			t.Errorf("%q: credentials %v ok %v", tc.auth, creds, ok)
		}
	}
	if _, ok := claude.Env([]string{"PATH=/bin"}, event.ClaudeAuthAPIKey); ok {
		t.Error("a missing key reported as present")
	}
	env, _ := claude.Env(nil, event.ClaudeAuthSubscriptionLogin)
	if !slices.Contains(env, "HOME="+layout.Home) || !slices.ContainsFunc(env, func(s string) bool {
		return strings.HasPrefix(s, "PATH=/")
	}) {
		t.Errorf("defaults missing: %v", env)
	}
}

func TestRecord_flag(t *testing.T) {
	rec := filepath.Join(t.TempDir(), layout.SDKRecording)
	r := newRig(t, opts{record: rec})
	r.send("m1", "hello")
	r.waitKind(event.KindTurnEnded, 1)
	var args []string
	for _, d := range r.dumped() {
		if d.Type == "args" {
			args = d.Args
		}
	}
	if len(args) < 2 || args[len(args)-2] != "--record" || args[len(args)-1] != rec {
		t.Errorf("args %v", args)
	}
	r2 := newRig(t, opts{})
	r2.send("m1", "hello")
	r2.waitKind(event.KindTurnEnded, 1)
	for _, d := range r2.dumped() {
		if d.Type == "args" && slices.Contains(d.Args, "--record") {
			t.Error("recording without the switch")
		}
	}
}

// stderr goes to the journal up to its cap and is drained beyond it.
func TestStderr_capped(t *testing.T) {
	r := newRig(t, opts{stderrCap: 64 << 10})
	r.send("m1", "noisy")
	evs := r.waitKind(event.KindTurnEnded, 1)
	if te, _ := lastTurnEnded(t, evs); te.Outcome != event.TurnOutcomeCompleted {
		t.Errorf("turn %+v", te)
	}
	r.stop()
	j := r.journal.String()
	if n := strings.Count(j, "sidecar stderr\" line=eeee"); n == 0 || n > (64<<10)/100+1 {
		t.Errorf("%d stderr lines journalled", n)
	}
	if !strings.Contains(j, "over its cap") || !strings.Contains(j, "sidecar stderr dropped") {
		t.Error("the cap is not journalled")
	}
	for _, e := range evs {
		if strings.Contains(string(e.Data), "eeee") {
			t.Fatal("stderr reached the event log")
		}
	}
}

// The agent hears nothing about its worktrees when it works in the session's one worktree. Working in the session
// directory, it gets a note naming each repository's directory; working in its first worktree after another was
// added, also that one as an additional directory (it lies outside the working directory).
func TestStartSpec_worktreeLayout(t *testing.T) {
	const dir = "/home/flow/work/.sessions/fix"
	api := event.WorktreeAdded{Repo: "acme/api", Branch: "flow/fix", Path: dir + "/api", BaseRef: "origin/main",
		BaseSHA: "abc"}
	web := event.WorktreeAdded{Repo: "acme/web", Branch: "flow/fix", Path: dir + "/web", BaseRef: "origin/master"}
	note := "The session's repositories, each a git worktree in its own directory " +
		"(run git commands inside the repository's directory):\n" +
		"- " + dir + "/api: acme/api, branch flow/fix, based on origin/main\n" +
		"- " + dir + "/web: acme/web, branch flow/fix, based on origin/master"
	for _, tc := range []struct {
		name    string
		cwd     string
		wts     []event.WorktreeAdded
		outside []string
		note    string
	}{
		{"one repository, cwd is its worktree", dir + "/api", []event.WorktreeAdded{api}, nil, ""},
		{"no worktree", dir, nil, nil, ""},
		{"several repositories, cwd is the session directory", dir, []event.WorktreeAdded{api, web}, nil, note},
		{"a repository added to a one-repository session", dir + "/api", []event.WorktreeAdded{api, web},
			[]string{dir + "/web"}, note},
		{"one repository, cwd is the session directory (a session from before)", dir, []event.WorktreeAdded{api}, nil,
			"The session's repositories, each a git worktree in its own directory " +
				"(run git commands inside the repository's directory):\n" +
				"- " + dir + "/api: acme/api, branch flow/fix, based on origin/main"},
	} {
		s := claude.NewStartSpec(claude.Session{ID: sid, Cwd: tc.cwd}, claude.State{Worktrees: tc.wts})
		if !slices.Equal(s.AdditionalDirectories, tc.outside) || s.AppendSystemPrompt != tc.note {
			t.Errorf("%s: additional %q, note\n%s\nwant %q\n%s", tc.name, s.AdditionalDirectories,
				s.AppendSystemPrompt, tc.outside, tc.note)
		}
	}
}

// The real sidecar (sidecar/dist) accepts the start line and speaks the protocol. No message is sent, so the SDK's
// query never runs and Claude Code is never started; claudeExecutable points nowhere besides.
func TestRealSidecar_acceptsTheStartLine(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "sidecar"))
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node:", err)
	}
	for _, p := range []string{"dist/main.js", "node_modules/@anthropic-ai/claude-agent-sdk"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Skip("sidecar not built:", err)
		}
	}
	env, _ := claude.Env([]string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin"}, event.ClaudeAuthSubscriptionLogin)
	spec := claude.NewStartSpec(claude.Session{ID: sid, Cwd: t.TempDir(), ClaudeExecutable: "/nonexistent/claude",
		SettingsPath: layout.Settings, Version: "test", MCPCommand: "/nonexistent/flowd", MCPToken: "x"},
		claude.State{Resume: true, PriorCostUSD: 1.25, Model: "m", Mode: event.PermissionModeAsk,
			Worktrees: []event.WorktreeAdded{{Repo: "a/api", Branch: "flow/x", Path: "/nonexistent/x/api",
				BaseRef: "origin/main"}}})
	if spec.AppendSystemPrompt == "" || len(spec.AdditionalDirectories) != 1 {
		t.Fatalf("start spec without the layout: %+v", spec)
	}
	start, err := claude.StartLine("start-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	var in bytes.Buffer
	in.Write(start)
	for _, c := range []event.Command{
		command(t, "x1", event.CmdMessageRemove, event.MessageRemove{MessageID: "m"}), // not a sidecar kind
		command(t, "s1", event.CmdSettingsSet, event.SettingsSet{Mode: event.PermissionModePlan}),
		command(t, "sd", event.CmdHostShutdown, event.HostShutdown{Reason: "stop"}),
	} {
		b, _ := json.Marshal(c)
		in.Write(append(b, '\n'))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(dir, "dist", "main.js"))
	cmd.Env, cmd.Dir, cmd.Stdin = env, t.TempDir(), &in
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sidecar: %v\n%s", err, stderr.String())
	}
	var notices []string
	for line := range strings.Lines(out.String()) {
		d, err := claude.ParseDraft([]byte(strings.TrimSuffix(line, "\n")))
		if err != nil {
			t.Errorf("%v: %s", err, line)
			continue
		}
		if d.Kind == event.KindNotice {
			var n event.Notice
			_ = json.Unmarshal(d.Data, &n)
			notices = append(notices, n.Code+"/"+d.Cmd)
		}
	}
	if !slices.Equal(notices, []string{"sidecar_command_skipped/x1"}) {
		t.Errorf("notices %v (the start line must be accepted)\n%s", notices, stderr.String())
	}
}
