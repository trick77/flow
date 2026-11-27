package claude_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
)

// The fake sidecar is this test binary run again with -test.run=^TestFakeSidecar$ -- fake-sidecar <dump> [flags]: it
// speaks the sidecar's stdio protocol, does what each message's text names, and appends what it saw (start line,
// environment, arguments, commands) to the dump file as JSON lines. Selected by arguments, not the environment: the
// adapter hands its child only the allow-listed variables.
const fakeMarker = "fake-sidecar"

// TestFakeSidecar is not a test: it is the fake when the marker is in the arguments, and returns at once otherwise.
func TestFakeSidecar(_ *testing.T) {
	i := slices.Index(os.Args, fakeMarker)
	if i < 0 {
		return
	}
	os.Exit(fakeMain(os.Args[i+1:]))
}

type fake struct {
	dump   *os.File
	flags  []string
	out    sync.Mutex
	lines  chan map[string]json.RawMessage
	inited bool
}

func (f *fake) has(flag string) bool { return slices.Contains(f.flags, flag) }

func (f *fake) record(v map[string]any) {
	b, _ := json.Marshal(v)
	_, _ = f.dump.Write(append(b, '\n'))
}

func (f *fake) raw(line string) {
	f.out.Lock()
	defer f.out.Unlock()
	_, _ = os.Stdout.WriteString(line + "\n")
}

func (f *fake) emit(kind event.EventKind, payload any) {
	b, _ := json.Marshal(event.EventDraft{Kind: kind, Data: mustJSON(payload)})
	f.raw(string(b))
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func str(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}

func fakeMain(args []string) int {
	if len(args) == 0 {
		return 2
	}
	dump, err := os.OpenFile(args[0], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	f := &fake{dump: dump, flags: args[1:], lines: make(chan map[string]json.RawMessage)}
	f.record(map[string]any{"type": "args", "args": args[1:]})
	f.record(map[string]any{"type": "env", "env": os.Environ()})
	sh, _ := exec.LookPath("sh") // resolved on the PATH the adapter gave this process
	f.record(map[string]any{"type": "lookpath", "data": sh})
	if f.has("ignore-shutdown") {
		signal.Ignore(syscall.SIGTERM)
	}
	go func() { // unbuffered: while the fake works on a line it reads no further, as a busy process would
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 64<<10), event.MaxLine)
		for sc.Scan() {
			var m map[string]json.RawMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				f.lines <- m
			}
		}
		close(f.lines)
	}()
	start, ok := <-f.lines
	if !ok || str(start, "kind") != "sidecar.start" {
		return 3
	}
	f.record(map[string]any{"type": "start", "id": str(start, "id"), "data": start["data"]})
	if f.has("refuse-start") { // as the sidecar answers a start line it cannot read
		b, _ := json.Marshal(event.EventDraft{Kind: event.KindNotice, Cmd: str(start, "id"),
			Data: mustJSON(event.Notice{Code: "sidecar_bad_command", Level: "error"})})
		f.raw(string(b))
	}
	for {
		m, ok := f.next()
		if !ok {
			return f.exit()
		}
		switch str(m, "kind") {
		case "message.send":
			var p event.MessageSend
			_ = json.Unmarshal(m["data"], &p)
			if !f.inited && !f.has("no-init") { // the SDK's system/init, once per query
				f.inited = true
				f.emit(event.KindSessionStarted, event.SessionStarted{Provider: event.ProviderClaude,
					Model: "claude-opus-5-5", Mode: event.PermissionModeAsk, ProviderSessionID: "sid", Cwd: "/"})
			}
			if code, stop := f.turn(str(m, "id"), p.Text); stop {
				return code
			}
		case "host.shutdown":
			if f.has("ignore-shutdown") {
				continue
			}
			return 0
		}
	}
}

// next takes the next command and records it.
func (f *fake) next() (map[string]json.RawMessage, bool) {
	m, ok := <-f.lines
	if ok {
		f.record(map[string]any{"type": "cmd", "id": str(m, "id"), "kind": str(m, "kind"), "data": m["data"]})
	}
	return m, ok
}

func (f *fake) exit() int {
	if f.has("ignore-shutdown") {
		time.Sleep(time.Hour)
	}
	return 0
}

func ended(outcome event.TurnOutcome, cost float64) event.TurnEnded {
	t := event.TurnEnded{Outcome: outcome, DurationMs: 5}
	if cost > 0 {
		t.CostUSD = &cost
		t.Usage = &event.Usage{InputTokens: 10, OutputTokens: 20}
		t.Provider, t.Model = event.ProviderClaude, "claude-opus-5-5"
	}
	return t
}

// turn plays one turn; stop reports that the process ends with code.
func (f *fake) turn(id, text string) (code int, stop bool) {
	switch text {
	case "hello", "exit", "slow":
		if text == "slow" {
			time.Sleep(300 * time.Millisecond)
		}
		f.emit(event.KindAssistantTextDelta, event.AssistantTextDelta{BlockID: id + ":0", Text: "Hel"})
		f.emit(event.KindAssistantText, event.AssistantText{BlockID: id + ":0", Text: "Hello"})
		f.emit(event.KindTurnEnded, ended(event.TurnOutcomeCompleted, 0.5))
		return 0, text == "exit"
	case "lastwords": // much stderr right before it exits: the tail must still reach the journal
		f.emit(event.KindTurnEnded, ended(event.TurnOutcomeCompleted, 0.5))
		line := strings.Repeat("w", 99) + "\n"
		for range 3000 {
			_, _ = os.Stderr.WriteString(line)
		}
		_, _ = os.Stderr.WriteString("last-words-marker\n")
		return 0, true
	case "noisy":
		line := strings.Repeat("e", 99) + "\n"
		for range (2 << 20) / 100 {
			_, _ = os.Stderr.WriteString(line)
		}
		f.emit(event.KindTurnEnded, ended(event.TurnOutcomeCompleted, 0.5))
	case "tool", "crash":
		f.emit(event.KindToolStarted, event.ToolStarted{ToolID: "tool-" + id, Name: "Bash", Kind: event.ToolKindBash,
			Command: "make check"})
		f.emit(event.KindPermissionRequested, event.PermissionRequested{RequestID: "req-" + id, ToolID: "tool-" + id,
			Tool: "Bash", Kind: event.PermissionKindCommand, Command: "make check", Mode: event.PermissionModeAsk})
		if text == "crash" {
			return 3, true
		}
		for {
			m, ok := f.next()
			if !ok {
				return f.exit(), true
			}
			switch str(m, "kind") {
			case "permission.reply":
				var r event.PermissionReply
				_ = json.Unmarshal(m["data"], &r)
				if r.RequestID != "req-"+id {
					continue
				}
				status := event.ToolStatusOk
				if r.Decision == event.ReplyDecisionDeny {
					status = event.ToolStatusDenied
				}
				f.emit(event.KindToolFinished, event.ToolFinished{ToolID: "tool-" + id, Status: status, Output: "ok"})
				f.emit(event.KindAssistantText, event.AssistantText{BlockID: id + ":1", Text: "Done."})
				f.emit(event.KindTurnEnded, ended(event.TurnOutcomeCompleted, 0.25))
				return 0, false
			case "turn.interrupt":
				f.emit(event.KindTurnEnded, ended(event.TurnOutcomeInterrupted, 0))
				return 0, false
			}
		}
	case "wait":
		for {
			m, ok := f.next()
			if !ok {
				return f.exit(), true
			}
			switch str(m, "kind") {
			case "turn.interrupt":
				f.emit(event.KindTurnEnded, ended(event.TurnOutcomeInterrupted, 0))
				return 0, false
			case "host.shutdown":
				if f.has("ignore-shutdown") {
					continue
				}
				var p event.HostShutdown
				_ = json.Unmarshal(m["data"], &p)
				if !p.WhenIdle {
					f.emit(event.KindTurnEnded, ended(event.TurnOutcomeInterrupted, 0))
					return 0, true
				}
			}
		}
	case "flood":
		big := strings.Repeat("x", 1<<10)
		for i := range 3000 {
			f.emit(event.KindAssistantTextDelta, event.AssistantTextDelta{BlockID: id + ":f", Text: big})
			if i%10 == 0 {
				f.emit(event.KindAssistantText, event.AssistantText{BlockID: id + ":" + itoa(i), Text: big})
			}
		}
		f.emit(event.KindTurnEnded, ended(event.TurnOutcomeCompleted, 0.5))
	case "badend": // an unreadable request, then an unreadable turn.ended
		f.raw(`{"kind":"permission.requested","data":{"requestId":"req-` + id + `","tool":5}}`)
		for {
			m, ok := f.next()
			if !ok {
				return f.exit(), true
			}
			if str(m, "kind") == "permission.reply" {
				break
			}
		}
		f.raw(`{"kind":"turn.ended","data":{"outcome":"completed","durationMs":1.5}}`)
	case "malformed":
		for _, l := range []string{
			"not json",
			`{"kind":"no.such.kind","data":{}}`,
			`{"kind":"assistant.text","data":{"blockId":5,"text":"x"}}`,
			`{"kind":"assistant.text"}`,
			`{"kind":"assistant.text","data":"text"}`,
			`{"kind":"assistant.text","data":{"blockId":"big","text":"` + strings.Repeat("a", event.MaxLine+10) + `"}}`,
			``,
			`[1,2]`,
		} {
			f.raw(l)
		}
		f.emit(event.KindAssistantText, event.AssistantText{BlockID: id + ":0", Text: "after"})
		f.emit(event.KindTurnEnded, ended(event.TurnOutcomeCompleted, 0.5))
	}
	return 0, false
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
