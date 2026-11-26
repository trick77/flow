package hostproto

import (
	"encoding/json"
	"testing"

	"github.com/trick77/flow/internal/event"
)

// The wire shapes, one line each, as flowd and a host of the same protocol exchange them.
func TestWire(t *testing.T) {
	cmd := event.Command{V: 1, ID: "c1", Session: "s", Kind: event.CmdTurnInterrupt, Data: json.RawMessage(`{}`)}
	live := event.Event{V: 1, Session: "s", Kind: event.KindAssistantTextDelta,
		Data: json.RawMessage(`{"blockId":"b","text":"x"}`)}
	for _, c := range []struct {
		v    any
		want string
	}{
		{Request{ID: "1", Op: OpHello}, `{"id":"1","op":"hello"}`},
		{Request{ID: "2", Op: OpCommand, Command: &cmd}, `{"id":"2","op":"command","command":{"v":1,"id":"c1","session":"s","kind":"turn.interrupt","data":{}}}`},
		{Response{ID: "2", Duplicate: true}, `{"id":"2","duplicate":true}`},
		{Response{ID: "3", Error: CodeShutdown}, `{"id":"3","error":"shutting_down"}`},
		{Response{ID: "1", Hello: &Hello{Protocol: Protocol, Version: "0.3.0", Session: "s", PID: 42, LastSeq: 7,
			Status: event.SessionStatusWorking, Busy: true}},
			`{"id":"1","hello":{"protocol":1,"version":"0.3.0","session":"s","pid":42,"lastSeq":7,"status":"working","busy":true}}`},
		{Frame{Appended: 8}, `{"appended":8}`},
	} {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("got  %s\nwant %s", b, c.want)
		}
	}
	b, _ := json.Marshal(Frame{Live: &live})
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil || f.Live == nil || f.Live.Kind != event.KindAssistantTextDelta {
		t.Errorf("live frame = %s, %v", b, err)
	}
	if MinProtocol > Protocol {
		t.Error("MinProtocol above Protocol")
	}
}
