package host

import (
	"path/filepath"
	"testing"

	"github.com/trick77/flow/internal/event"
)

func payloads[T any](t *testing.T, evs []event.Event, kind event.EventKind) []*T {
	t.Helper()
	var out []*T
	for _, e := range evs {
		if e.Kind != kind {
			continue
		}
		p, err := e.Payload()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.(*T))
	}
	return out
}

// Every turn.started carries the provider and the model the session is set to as the turn starts, also after a
// settings change and after a host restart; the replay's turn.ended carries the recording's.
func TestTurnsCarryTheirModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	r := start(t, path, replay(t))
	auto := func(id string) {
		evs := r.waitKind(event.KindPermissionRequested, 1)
		reqs := payloads[event.PermissionRequested](t, evs, event.KindPermissionRequested)
		if _, err := r.h.Command(cmd(t, id, event.CmdPermissionReply, event.PermissionReply{
			RequestID: reqs[len(reqs)-1].RequestID, Decision: event.ReplyDecisionAllowOnce})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.h.Command(send(t, "m1", "go")); err != nil {
		t.Fatal(err)
	}
	auto("r1")
	r.waitKind(event.KindTurnEnded, 1)
	if _, err := r.h.Command(cmd(t, "s1", event.CmdSettingsSet, event.SettingsSet{Model: "claude-sonnet-4-5"})); err != nil {
		t.Fatal(err)
	}
	r.stop()
	r = start(t, path, replay(t)) // the model set before the restart holds after it
	if _, err := r.h.Command(send(t, "m2", "again")); err != nil {
		t.Fatal(err)
	}
	r.waitKind(event.KindTurnStarted, 2)
	evs := r.events()
	starts := payloads[event.TurnStarted](t, evs, event.KindTurnStarted)
	if len(starts) != 2 || starts[0].Provider != event.ProviderClaude || starts[0].Model != "claude-opus-5-5" ||
		starts[1].Provider != event.ProviderClaude || starts[1].Model != "claude-sonnet-4-5" {
		t.Errorf("turn.started: %+v", starts)
	}
	ends := payloads[event.TurnEnded](t, evs, event.KindTurnEnded)
	if len(ends) < 1 || ends[0].Provider != event.ProviderClaude || ends[0].Model != "claude-opus-5-5" ||
		ends[0].CostUSD == nil || *ends[0].CostUSD != 0.3121 {
		t.Errorf("replayed turn.ended: %+v", ends[0])
	}
}
