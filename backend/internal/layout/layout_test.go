package layout

import (
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	id := "0193a8f2-5d1e-7c3a-9b2e-6f1d2a3b4c5d"
	for got, want := range map[string]string{
		SessionDir("flaky"):          "/home/flow/work/.sessions/flaky",
		Worktree("flaky", "riptide"): "/home/flow/work/.sessions/flaky/riptide",
		Checkout("riptide"):          "/home/flow/work/riptide",
		EventLog(id):                 "/var/lib/flow/sessions/" + id + "/events.jsonl",
		HostSocket(id):               "/run/flow/sessions/" + id + "/host.sock",
		TerminalSocket("t1"):         "/run/flow/terminals/t1.sock",
		Release("0.3.0"):             "/opt/flow/releases/0.3.0",
		HostUnit(id):                 "flow-session-" + id + ".service",
		TerminalUnit("t1"):           "flow-term-t1.service",
		JobUnit("j1"):                "flow-job-j1.service",
		JobLog("j1"):                 "/var/lib/flow/jobs/j1.log",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	// Nothing per project may live on the shared home volume.
	for _, p := range []string{Work, Logs, Run, State} {
		if strings.HasPrefix(p, HomeVolume) {
			t.Errorf("%s is on the shared home volume", p)
		}
	}
}

func TestValidName(t *testing.T) {
	for s, want := range map[string]bool{
		"flaky-migration-test": true, "riptide": true, "a.b_c": true,
		"": false, ".sessions": false, "../x": false, "A": false, "a/b": false, strings.Repeat("a", 64): false,
	} {
		if ValidName(s) != want {
			t.Errorf("ValidName(%q) = %v", s, !want)
		}
	}
}
