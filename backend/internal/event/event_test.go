package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/contractgen"
)

var repoRoot = filepath.Join("..", "..", "..")

// fixtureLines reads one JSONL fixture of contract/fixtures.
func fixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot, "contract", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, MaxLine), MaxLine)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// sameJSON compares two JSON documents by value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(va, vb)
}

func strict(data []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// Every event fixture is valid under the language-neutral schema, decodes into the generated types without
// unknown fields, and encodes back to the same JSON; every kind has one.
func TestEventFixtures(t *testing.T) {
	doc, err := contractgen.Load(repoRoot, contractgen.EventsSchema)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[EventKind]bool{}
	for i, line := range fixtureLines(t, "events.jsonl") {
		if err := doc.ValidateUnion("Event", line); err != nil {
			t.Errorf("line %d: %v", i+1, err)
			continue
		}
		e, err := ParseLine(append(line, '\n'))
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		seen[e.Kind] = true
		info, _ := e.Kind.Info()
		if info.Ephemeral != (e.Seq == 0) {
			t.Errorf("line %d: %s has seq %d; ephemeral kinds have none, durable ones do", i+1, e.Kind, e.Seq)
		}
		p := info.New()
		if err := strict(e.Data, p); err != nil {
			t.Errorf("line %d: %s data: %v", i+1, e.Kind, err)
			continue
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, data, e.Data) {
			t.Errorf("line %d: %s data does not round-trip:\n got %s\nwant %s", i+1, e.Kind, data, e.Data)
		}
		out, err := e.Line()
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, out, line) {
			t.Errorf("line %d: envelope does not round-trip:\n got %s\nwant %s", i+1, out, line)
		}
	}
	for _, k := range EventKindInfos {
		if !seen[k.Kind] {
			t.Errorf("no fixture for event kind %s", k.Kind)
		}
	}
}

func TestCommandFixtures(t *testing.T) {
	doc, err := contractgen.Load(repoRoot, contractgen.EventsSchema)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[CommandKind]bool{}
	for i, line := range fixtureLines(t, "commands.jsonl") {
		if err := doc.ValidateUnion("Command", line); err != nil {
			t.Errorf("line %d: %v", i+1, err)
			continue
		}
		var c Command
		if err := strict(line, &c); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		seen[c.Kind] = true
		info, ok := c.Kind.Info()
		if !ok {
			t.Fatalf("line %d: kind %s has no info", i+1, c.Kind)
		}
		p := info.New()
		if err := strict(c.Data, p); err != nil {
			t.Errorf("line %d: %s data: %v", i+1, c.Kind, err)
			continue
		}
		again, err := NewCommand(c.ID, c.Session, c.Kind, p)
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(again)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, out, line) {
			t.Errorf("line %d: command does not round-trip:\n got %s\nwant %s", i+1, out, line)
		}
	}
	for _, k := range CommandKindInfos {
		if !seen[k.Kind] {
			t.Errorf("no fixture for command kind %s", k.Kind)
		}
	}
}

// demo.jsonl is a real stream, unlike events.jsonl: one session, seq gapless from 1, time never going backwards,
// every event valid. The walking skeleton and the UI demo replay it.
func TestDemoStream(t *testing.T) {
	doc, err := contractgen.Load(repoRoot, contractgen.EventsSchema)
	if err != nil {
		t.Fatal(err)
	}
	var prev Event
	for i, line := range fixtureLines(t, "demo.jsonl") {
		if err := doc.ValidateUnion("Event", line); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		e, _ := ParseLine(append(line, '\n'))
		if e.Seq != int64(i+1) || (i > 0 && (e.Session != prev.Session || e.TS.Before(prev.TS))) {
			t.Errorf("line %d: seq %d at %v of %s after seq %d at %v", i+1, e.Seq, e.TS, e.Session, prev.Seq, prev.TS)
		}
		prev = e
	}
	if prev.Kind != KindSessionStatus {
		t.Errorf("the demo ends with %s, want the final status", prev.Kind)
	}
}

// The mapping fixtures (provider input, expected drafts) are the adapters' contract tests until recorded streams
// replace them; every expected draft must be a valid EventDraft with a valid payload.
func TestMappingFixtures(t *testing.T) {
	doc, err := contractgen.Load(repoRoot, contractgen.EventsSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude.jsonl", "opencode.jsonl"} {
		lines := fixtureLines(t, filepath.Join("mapping", name))
		if len(lines) == 0 {
			t.Fatalf("%s is empty", name)
		}
		for i, line := range lines {
			var c struct {
				Case string            `json:"case"`
				In   json.RawMessage   `json:"in"`
				Out  []json.RawMessage `json:"out"`
			}
			if err := strict(line, &c); err != nil || c.Case == "" || len(c.In) == 0 || len(c.Out) == 0 {
				t.Errorf("%s:%d: want case, in and out: %v", name, i+1, err)
				continue
			}
			for _, d := range c.Out {
				if err := doc.Validate("EventDraft", d); err != nil {
					t.Errorf("%s:%d %s: %v", name, i+1, c.Case, err)
					continue
				}
				var draft EventDraft
				_ = json.Unmarshal(d, &draft)
				info, ok := draft.Kind.Info()
				if !ok {
					t.Errorf("%s:%d: unknown kind %s", name, i+1, draft.Kind)
					continue
				}
				kind, _ := doc.KindOf("Event", string(draft.Kind))
				if err := doc.Validate(kind.Payload, draft.Data); err != nil || strict(draft.Data, info.New()) != nil {
					t.Errorf("%s:%d %s: %s data: %v", name, i+1, c.Case, draft.Kind, err)
				}
			}
		}
	}
}

func TestNewAndPayload(t *testing.T) {
	ts := time.Date(2026, 11, 26, 14, 18, 3, 0, time.FixedZone("CET", 3600))
	e, err := New("s1", KindToolStarted, ts, ToolStarted{ToolID: "t1", Name: "Read", Kind: ToolKindRead})
	if err != nil {
		t.Fatal(err)
	}
	if e.V != Version || !e.TS.Equal(ts) || e.TS.Location() != time.UTC {
		t.Errorf("envelope = %+v, want v%d at %v in UTC", e, Version, ts)
	}
	p, err := e.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*ToolStarted); got.ToolID != "t1" || got.Kind != ToolKindRead {
		t.Errorf("payload = %+v", got)
	}

	if _, err := (Event{V: 1, Kind: "nope"}).Payload(); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := (Event{V: 2, Kind: KindNotice}).Payload(); !errors.Is(err, ErrNewerVersion) {
		t.Errorf("newer version: %v", err)
	}
	if _, err := (Event{V: 1, Kind: KindNotice, Data: []byte(`[]`)}).Payload(); err == nil {
		t.Error("bad data decoded")
	}
	if _, err := New("s1", KindNotice, ts, func() {}); err == nil {
		t.Error("an unencodable payload made an event")
	}
}

func TestCommandPayload(t *testing.T) {
	c, err := NewCommand("c1", "s1", CmdTurnInterrupt, TurnInterrupt{})
	if err != nil {
		t.Fatal(err)
	}
	if p, err := c.Payload(); err != nil || p.(*TurnInterrupt) == nil {
		t.Errorf("payload = %v, %v", p, err)
	}
	if _, err := (Command{V: 1, Kind: "nope"}).Payload(); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := (Command{V: 9, Kind: CmdTurnInterrupt}).Payload(); !errors.Is(err, ErrNewerVersion) {
		t.Errorf("newer version: %v", err)
	}
	if _, err := (Command{V: 1, Kind: CmdMessageSend, Data: []byte(`1`)}).Payload(); err == nil {
		t.Error("bad data decoded")
	}
	if _, err := NewCommand("c", "s", CmdTurnInterrupt, make(chan int)); err == nil {
		t.Error("an unencodable payload made a command")
	}
	if _, ok := CommandKind("nope").Info(); ok {
		t.Error("info for an unknown command kind")
	}
}

func TestDurable(t *testing.T) {
	if !KindAssistantText.Durable() || KindAssistantTextDelta.Durable() || !EventKind("future.kind").Durable() {
		t.Error("Durable: assistant.text logged, text_delta not, unknown kinds logged")
	}
}

func TestLines(t *testing.T) {
	if _, err := ParseLine([]byte(`{"v":1}`)); err == nil || !strings.Contains(err.Error(), "torn") {
		t.Errorf("a line without newline: %v", err)
	}
	if _, err := ParseLine([]byte("{\n")); err == nil {
		t.Error("broken JSON parsed")
	}
	big := Event{V: 1, Kind: KindNotice, Data: json.RawMessage(`"` + strings.Repeat("x", MaxLine) + `"`)}
	if _, err := big.Line(); err == nil {
		t.Error("an event over MaxLine was encoded")
	}
	if _, err := (Event{Data: json.RawMessage(`{`)}).Line(); err == nil {
		t.Error("broken data encoded")
	}
}
