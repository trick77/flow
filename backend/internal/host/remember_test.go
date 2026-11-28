package host

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
)

func bash(id, command string, extra map[string]any) event.EventDraft {
	in := map[string]any{"command": command, "description": "label " + id}
	for k, v := range extra {
		in[k] = v
	}
	b, _ := json.Marshal(in)
	return draft(event.KindPermissionRequested, event.PermissionRequested{RequestID: id, Tool: "Bash",
		Kind: event.PermissionKindCommand, Command: command, Input: b, Mode: event.PermissionModeAsk})
}

// nextReply waits for the adapter's next command, which must be a permission reply: its id and payload.
func nextReply(t *testing.T, f *fakeAdapter) (string, event.PermissionReply) {
	t.Helper()
	select {
	case c := <-f.got:
		var p event.PermissionReply
		if c.Kind != event.CmdPermissionReply || json.Unmarshal(c.Data, &p) != nil {
			t.Fatalf("adapter got %s %s", c.Kind, c.Data)
		}
		return c.ID, p
	case <-time.After(5 * time.Second):
		t.Fatal("no reply reached the adapter")
	}
	return "", event.PermissionReply{}
}

func resolutions(t *testing.T, evs []event.Event) map[string]event.PermissionResolved {
	t.Helper()
	out := map[string]event.PermissionResolved{}
	for _, e := range evs {
		if e.Kind == event.KindPermissionResolved {
			p, err := e.Payload()
			if err != nil {
				t.Fatal(err)
			}
			out[p.(*event.PermissionResolved).RequestID] = *p.(*event.PermissionResolved)
		}
	}
	return out
}

// The owner allows a command for the session that the provider offered no rule for: the host remembers that exact
// request, answers the next identical one itself (logged as remembered, the session never waits), asks again for any
// other, and rebuilds the list from the log after a restart.
func TestPermission_sessionAllowWithoutProviderRule(t *testing.T) {
	const command = "echo effort=$CLAUDE_EFFORT"
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f := newFake()
	r := start(t, path, f)
	if _, err := r.h.Command(send(t, "m1", "go")); err != nil {
		t.Fatal(err)
	}
	<-f.got
	f.out <- bash("p1", command, nil)
	r.waitKind(event.KindPermissionRequested, 1)
	if _, err := r.h.Command(cmd(t, "reply-p1", event.CmdPermissionReply, event.PermissionReply{RequestID: "p1",
		Decision: event.ReplyDecisionAllowAlways})); err != nil {
		t.Fatal(err)
	}
	// The agent offers no rule: it hears allow once, the log says what the owner chose.
	if id, p := nextReply(t, f); id != "reply-p1" || p.Decision != event.ReplyDecisionAllowOnce {
		t.Errorf("adapter got %s %+v", id, p)
	}

	// The same command again, another label: answered by the host.
	f.out <- bash("p2", command, nil)
	if id, p := nextReply(t, f); id != "remembered-p2" || p.RequestID != "p2" || p.Decision != event.ReplyDecisionAllowOnce {
		t.Errorf("adapter got %s %+v", id, p)
	}
	evs := r.waitKind(event.KindPermissionResolved, 2)
	res := resolutions(t, evs)
	if got := res["p1"]; got.Decision != event.PermissionDecisionAllowAlways || got.Remembered {
		t.Errorf("owner's choice logged %+v", got)
	}
	if got := res["p2"]; got.Decision != event.PermissionDecisionAllowAlways || !got.Remembered {
		t.Errorf("remembered answer logged %+v", got)
	}
	if count(evs, event.KindPermissionRequested) != 2 {
		t.Errorf("the remembered request is not in the log: %v", kinds(evs))
	}
	if s := r.h.Hello(0).Status; s != event.SessionStatusWorking {
		t.Errorf("status %s: the session waited for a remembered request", s)
	}

	// Anything not exactly that request waits for the owner: a longer command, the same one with another flag.
	for i, d := range []event.EventDraft{bash("p3", command+" && true", nil),
		bash("p4", command, map[string]any{"run_in_background": true}),
		bash("p5", "echo effort=", nil)} {
		f.out <- d
		r.waitKind(event.KindPermissionRequested, 3+i)
		if s := r.h.Hello(0).Status; s != event.SessionStatusApproval {
			t.Fatalf("request %d: status %s, want it waiting for the owner", i, s)
		}
		id := []string{"p3", "p4", "p5"}[i]
		if _, err := r.h.Command(cmd(t, "reply-"+id, event.CmdPermissionReply, event.PermissionReply{RequestID: id,
			Decision: event.ReplyDecisionDeny})); err != nil {
			t.Fatal(err)
		}
		nextReply(t, f)
	}

	// After a restart the owner's choice is read back from the log.
	r.stop()
	f2 := newFake()
	r2 := start(t, path, f2)
	if _, err := r2.h.Command(send(t, "m2", "again")); err != nil {
		t.Fatal(err)
	}
	<-f2.got
	f2.out <- bash("p6", command, map[string]any{"timeout": 5000})
	if id, p := nextReply(t, f2); id != "remembered-p6" || p.Decision != event.ReplyDecisionAllowOnce {
		t.Errorf("after the restart: adapter got %s %+v", id, p)
	}
	gapless(t, r2.events())
}

// A rule the provider offers stays the provider's: the reply goes to the agent as allow_always, the host remembers
// nothing, and the next request is the agent's to ask or not.
func TestPermission_providerRuleUnchanged(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	if _, err := r.h.Command(send(t, "m1", "go")); err != nil {
		t.Fatal(err)
	}
	<-f.got
	d := bash("p1", "make check", nil)
	var p event.PermissionRequested
	_ = json.Unmarshal(d.Data, &p)
	p.CanAlwaysAllow = true
	f.out <- draft(event.KindPermissionRequested, p)
	r.waitKind(event.KindPermissionRequested, 1)
	if _, err := r.h.Command(cmd(t, "reply-p1", event.CmdPermissionReply, event.PermissionReply{RequestID: "p1",
		Decision: event.ReplyDecisionAllowAlways})); err != nil {
		t.Fatal(err)
	}
	if _, got := nextReply(t, f); got.Decision != event.ReplyDecisionAllowAlways {
		t.Errorf("adapter got %+v", got)
	}
	r.h.mu.Lock()
	n := len(r.h.allowed)
	r.h.mu.Unlock()
	if n != 0 {
		t.Errorf("host remembered %d requests the provider keeps itself", n)
	}
}

func TestRememberKey(t *testing.T) {
	req := func(tool string, kind event.PermissionKind, input string) *event.PermissionRequested {
		return &event.PermissionRequested{Tool: tool, Kind: kind, Input: json.RawMessage(input)}
	}
	key := func(r *event.PermissionRequested) string {
		k, ok := rememberKey(r)
		if !ok {
			t.Fatalf("no key for %s", r.Input)
		}
		return k
	}
	same := func(a, b *event.PermissionRequested, want bool) {
		t.Helper()
		if got := key(a) == key(b); got != want {
			t.Errorf("%s %s vs %s %s: same %v, want %v", a.Tool, a.Input, b.Tool, b.Input, got, want)
		}
	}
	cmd := func(in string) *event.PermissionRequested { return req("Bash", event.PermissionKindCommand, in) }
	same(cmd(`{"command":"ls","description":"a","timeout":1}`), cmd(`{"command":"ls","description":"b"}`), true)
	same(cmd(`{"command":"ls"}`), cmd(`{"command":"ls "}`), false)
	same(cmd(`{"command":"ls"}`), cmd(`{"command":"ls","dangerouslyDisableSandbox":true}`), false)
	same(cmd(`{"command":"ls"}`), req("Shell", event.PermissionKindCommand, `{"command":"ls"}`), false)
	// A file by tool and exact path.
	w := func(tool, in string) *event.PermissionRequested { return req(tool, event.PermissionKindWrite, in) }
	same(w("Write", `{"file_path":"/w/a.go","content":"x"}`), w("Write", `{"file_path":"/w/a.go","content":"y"}`), true)
	same(w("Write", `{"file_path":"/w/a.go"}`), w("Write", `{"file_path":"/w/./a.go"}`), false)
	same(w("Write", `{"file_path":"/w/a.go"}`), req("Edit", event.PermissionKindEdit, `{"file_path":"/w/a.go"}`), false)
	// Anything else: the whole input, a description included (an argument there).
	m := func(in string) *event.PermissionRequested { return req("mcp__x__create", event.PermissionKindMCP, in) }
	same(m(`{"a":1,"description":"x"}`), m(`{"description":"x","a":1}`), true)
	same(m(`{"a":1,"description":"x"}`), m(`{"a":1,"description":"y"}`), false)
	// Shortened by the sidecar to fit the log line: two long commands could read the same.
	for _, r := range []*event.PermissionRequested{cmd(``), cmd(`{"description":"no command"}`), cmd(`[1]`),
		req("", event.PermissionKindOther, `{"a":1}`), cmd(`{"command":"echo aaa\n…\nzzz"}`),
		m(`{"a":["x","head\n…\ntail"]}`), w("Write", `{"file_path":"/w/a.go","content":"h\n…\nt"}`)} {
		if k, ok := rememberKey(r); ok {
			t.Errorf("%s: key %q for a request with nothing to match", r.Input, k)
		}
	}
}
