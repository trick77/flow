package flowdfake

import (
	"testing"

	"github.com/trick77/flow/internal/event"
)

// turnStarts are the provider and model of every turn.started in the session's log.
func turnStarts(t *testing.T, f *Fake) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.sessions[sid].log {
		if e.Kind == event.KindTurnStarted {
			p, err := e.Payload()
			if err != nil {
				t.Fatal(err)
			}
			ts := p.(*event.TurnStarted)
			out = append(out, string(ts.Provider)+"/"+ts.Model)
		}
	}
	return out
}

// Like the host, the fake stamps each turn with the model the session is set to as it starts.
func TestTurnStartedCarriesTheModel(t *testing.T) {
	f := newSession(t)
	send(t, f, cmd(t, "m1", event.CmdMessageSend, event.MessageSend{MessageID: "m1", Text: "a", Origin: event.MessageOriginUser}))
	send(t, f, cmd(t, "s1", event.CmdSettingsSet, event.SettingsSet{Model: "m2"}))
	send(t, f, cmd(t, "m2", event.CmdMessageSend, event.MessageSend{MessageID: "m2", Text: "b", Origin: event.MessageOriginUser}))
	send(t, f, cmd(t, "s2", event.CmdSettingsSet, event.SettingsSet{Mode: event.PermissionModeAuto}))
	send(t, f, cmd(t, "m3", event.CmdMessageSend, event.MessageSend{MessageID: "m3", Text: "c", Origin: event.MessageOriginUser}))
	got := turnStarts(t, f)
	if len(got) != 3 || got[0] != "claude/m" || got[1] != "claude/m2" || got[2] != "claude/m2" {
		t.Errorf("turn models %v", got)
	}
}
