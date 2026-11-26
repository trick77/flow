package host

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/trick77/flow/internal/event"
)

// recording is contract/fixtures/demo.jsonl (TestReplayFixtureIsTheContractOne keeps the copy equal): go:embed
// cannot reach outside the module.
//
//go:embed replay.jsonl
var recording []byte

// Pace turns the gap between two recorded events into the wait before the second: Factor of the gap, held between
// Min and Max. The zero value plays without waiting.
type Pace struct {
	Factor   float64
	Min, Max time.Duration
	// Delta is the wait between the chunks of a streamed text block.
	Delta time.Duration
}

// DefaultPace plays the demo turn in about half a minute, the permission request in its middle.
var DefaultPace = Pace{Factor: 0.25, Min: 250 * time.Millisecond, Max: 2500 * time.Millisecond,
	Delta: 120 * time.Millisecond}

func (p Pace) wait(gap time.Duration) time.Duration {
	if p.Factor == 0 && p.Min == 0 {
		return 0
	}
	d := time.Duration(float64(gap) * p.Factor)
	return min(max(d, p.Min), p.Max)
}

// step is one recorded draft and the gap before it.
type step struct {
	draft event.EventDraft
	gap   time.Duration
}

// Replay is an Adapter that answers every message with a recorded turn: the turn content of the recording (what an
// agent emits: tools, todo, text, requests, gate rows, files.changed, turn.ended) at the recording's pace. A
// permission request waits for its reply; deny skips the rest of the work. turn.interrupt ends the turn at once.
// Ids of tools, requests and blocks get the tail of the message id appended, so no turn reuses one, not even
// after a host restart.
type Replay struct {
	steps []step
	pace  Pace
	cmds  chan event.Command
	now   func() time.Time
}

// NewReplay reads a recorded session (event lines) into a replay adapter.
func NewReplay(rec []byte, pace Pace) (*Replay, error) {
	r := &Replay{pace: pace, cmds: make(chan event.Command, 64), now: time.Now}
	sc := bufio.NewScanner(bytes.NewReader(rec))
	sc.Buffer(make([]byte, 64<<10), event.MaxLine)
	var prev time.Time
	for sc.Scan() {
		e, err := event.ParseLine(append(sc.Bytes(), '\n'))
		if err != nil {
			return nil, err
		}
		switch e.Kind {
		case event.KindSessionStarted, event.KindSessionResumed, event.KindSessionStatus, event.KindSessionSettings,
			event.KindMessageUser, event.KindMessageQueued, event.KindMessageDequeued, event.KindTurnStarted,
			event.KindPermissionResolved, event.KindQuestionResolved, event.KindPlanResolved, event.KindWorktreeAdded:
			if e.Kind == event.KindTurnStarted {
				prev = e.TS
			}
			continue
		}
		var gap time.Duration
		if !prev.IsZero() {
			gap = e.TS.Sub(prev)
		}
		prev = e.TS
		r.steps = append(r.steps, step{draft: event.EventDraft{Kind: e.Kind, ParentTool: e.ParentTool, Data: e.Data}, gap: gap})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(r.steps) == 0 || r.steps[len(r.steps)-1].draft.Kind != event.KindTurnEnded {
		return nil, errors.New("replay: the recording has no turn ending in turn.ended")
	}
	return r, nil
}

// NewDemoReplay is the replay of the embedded demo recording.
func NewDemoReplay(pace Pace) (*Replay, error) { return NewReplay(recording, pace) }

// Send implements Adapter.
func (r *Replay) Send(c event.Command) error {
	select {
	case r.cmds <- c:
		return nil
	default:
		return errors.New("replay: command queue full")
	}
}

// Run implements Adapter.
func (r *Replay) Run(ctx context.Context, out chan<- event.EventDraft) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-r.cmds:
			if c.Kind != event.CmdMessageSend {
				continue // replies and interrupts outside a turn
			}
			if !r.play(ctx, idTail(c.ID), out) {
				return nil
			}
		}
	}
}

// play runs one turn; false when ctx ended.
func (r *Replay) play(ctx context.Context, turn string, out chan<- event.EventDraft) bool {
	start := r.now()
	emit := func(d event.EventDraft) bool {
		select {
		case out <- d:
			return true
		case <-ctx.Done():
			return false
		}
	}
	end := func(outcome event.TurnOutcome, usage json.RawMessage) bool {
		t := event.TurnEnded{Outcome: outcome, DurationMs: r.now().Sub(start).Milliseconds()}
		if usage != nil {
			var rec event.TurnEnded
			if json.Unmarshal(usage, &rec) == nil {
				t.Usage, t.CostUSD = rec.Usage, rec.CostUSD
			}
		}
		return emit(draft(event.KindTurnEnded, t))
	}
	waiting := ""
	for i := 0; i < len(r.steps); i++ {
		s := r.steps[i]
		d := s.draft
		d.Data = suffixIDs(d.Data, turn)
		if d.Kind == event.KindTurnEnded {
			return end(event.TurnOutcomeCompleted, s.draft.Data)
		}
		// Wait the recorded gap, or for the reply the turn waits on.
		timer := time.NewTimer(r.pace.wait(s.gap))
		fired := timer.C
		for fired != nil || waiting != "" {
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case c := <-r.cmds:
				switch c.Kind {
				case event.CmdTurnInterrupt:
					timer.Stop()
					return end(event.TurnOutcomeInterrupted, nil)
				case event.CmdPermissionReply:
					var p event.PermissionReply
					if waiting == "" || json.Unmarshal(c.Data, &p) != nil || p.RequestID != waiting {
						continue
					}
					waiting = ""
					if p.Decision == event.ReplyDecisionDeny {
						timer.Stop()
						return r.denied(emit, end)
					}
				}
			case <-fired:
				fired = nil
			}
		}
		if d.Kind == event.KindAssistantText && !r.stream(ctx, d, emit) {
			return false
		}
		if !emit(d) {
			return false
		}
		if d.Kind == event.KindPermissionRequested {
			var p event.PermissionRequested
			_ = json.Unmarshal(d.Data, &p)
			waiting = p.RequestID
		}
	}
	return end(event.TurnOutcomeCompleted, nil)
}

func (r *Replay) denied(emit func(event.EventDraft) bool, end func(event.TurnOutcome, json.RawMessage) bool) bool {
	if !emit(draft(event.KindAssistantText, event.AssistantText{BlockID: "denied-" + strconv.FormatInt(r.now().UnixNano(), 36),
		Text: "Understood: I did not run it. The changes are in place; run the check yourself when you are ready."})) {
		return false
	}
	return end(event.TurnOutcomeCompleted, nil)
}

// stream sends a text block as deltas before the durable block, as a live agent does.
func (r *Replay) stream(ctx context.Context, d event.EventDraft, emit func(event.EventDraft) bool) bool {
	var p event.AssistantText
	if json.Unmarshal(d.Data, &p) != nil {
		return true
	}
	const chunks = 8
	size := max(len(p.Text)/chunks, 1)
	for rest := p.Text; rest != ""; {
		n := min(size, len(rest))
		for n < len(rest) && !utf8.RuneStart(rest[n]) {
			n++
		}
		if !emit(draft(event.KindAssistantTextDelta, event.AssistantTextDelta{BlockID: p.BlockID, Text: rest[:n]})) {
			return false
		}
		rest = rest[n:]
		if r.pace.Delta > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(r.pace.Delta):
			}
		}
	}
	return true
}

func draft(kind event.EventKind, payload any) event.EventDraft {
	b, _ := json.Marshal(payload) // contract types always marshal
	return event.EventDraft{Kind: kind, Data: b}
}

// suffixIDs appends -<turn> to the ids a turn's events share (toolId, requestId, blockId), so turns never reuse one.
func suffixIDs(data json.RawMessage, turn string) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	for _, k := range []string{"toolId", "requestId", "blockId"} {
		if s, ok := m[k].(string); ok {
			m[k] = s + "-" + turn
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return b
}

// idTail is the last 8 alphanumerics of a command id: unique enough per session for the turn's ids.
func idTail(id string) string {
	var b []byte
	for i := len(id) - 1; i >= 0 && len(b) < 8; i-- {
		if c := id[i]; c != '-' {
			b = append([]byte{c}, b...)
		}
	}
	return string(b)
}
