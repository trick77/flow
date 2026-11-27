package host

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/hostproto"
)

// A request the provider withdraws (the adapter's resolved draft with expired) frees the host's one request slot:
// the session works again and the next request can be shown. Any other resolution from the adapter is dropped: the
// host alone resolves a request on the owner's reply.
func TestDraft_withdrawnRequestFreesTheSlot(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	if _, err := r.h.Command(send(t, "m1", "go")); err != nil {
		t.Fatal(err)
	}
	<-f.got
	f.out <- draft(event.KindPermissionRequested, event.PermissionRequested{RequestID: "p1", Tool: "Bash",
		Kind: event.PermissionKindCommand, Mode: event.PermissionModeAsk})
	r.waitKind(event.KindPermissionRequested, 1)
	if s := r.h.Hello(0).Status; s != event.SessionStatusApproval {
		t.Fatalf("status %s", s)
	}
	f.out <- draft(event.KindPermissionResolved, event.PermissionResolved{RequestID: "p1",
		Decision: event.PermissionDecisionExpired})
	r.waitKind(event.KindPermissionResolved, 1)
	if s := r.h.Hello(0).Status; s != event.SessionStatusWorking {
		t.Errorf("status after the withdrawal %s, want working", s)
	}
	var ce *CommandError
	_, err := r.h.Command(cmd(t, "r1", event.CmdPermissionReply, event.PermissionReply{RequestID: "p1",
		Decision: "allow_once"}))
	if !errors.As(err, &ce) || ce.Code != hostproto.CodeNotPending {
		t.Errorf("reply to the withdrawn request: %v", err)
	}

	f.out <- draft(event.KindQuestionAsked, event.QuestionAsked{RequestID: "q1", Questions: []event.Question{}})
	r.waitKind(event.KindQuestionAsked, 1)
	for _, d := range []event.EventDraft{
		// not expired
		draft(event.KindQuestionResolved, event.QuestionResolved{RequestID: "q1", Outcome: event.QuestionOutcomeAnswered}),
		// another request
		draft(event.KindQuestionResolved, event.QuestionResolved{RequestID: "q0", Outcome: event.QuestionOutcomeExpired}),
		// the open request's id, the wrong kind
		draft(event.KindPlanResolved, event.PlanResolved{RequestID: "q1", Decision: event.PlanDecisionExpired}),
		{Kind: event.KindQuestionResolved, Data: []byte(`not json`)},
	} {
		f.out <- d
	}
	f.out <- draft(event.KindAssistantText, event.AssistantText{BlockID: "b", Text: "after"})
	evs := r.waitKind(event.KindAssistantText, 1)
	if n := count(evs, event.KindQuestionResolved) + count(evs, event.KindPlanResolved); n != 0 {
		t.Errorf("%d foreign resolutions logged", n)
	}
	if s := r.h.Hello(0).Status; s != event.SessionStatusQuestion {
		t.Errorf("status %s, want question still open", s)
	}
	if _, err := r.h.Command(cmd(t, "a1", event.CmdQuestionReply, event.QuestionReply{RequestID: "q1",
		Text: "yes"})); err != nil {
		t.Errorf("the open question lost its slot: %v", err)
	}
	gapless(t, r.events())
}

// An interrupt expires the open request at once: the adapter answers it as the interrupt it is, so a reply that
// comes before turn.ended reaches no one. It is refused (not pending), the log holds one expired resolution and the
// session's mode stays.
func TestInterrupt_expiresTheOpenRequest(t *testing.T) {
	f := newFake()
	r := start(t, filepath.Join(t.TempDir(), "events.jsonl"), f)
	if _, err := r.h.Command(send(t, "m1", "go")); err != nil {
		t.Fatal(err)
	}
	<-f.got
	f.out <- draft(event.KindPlanProposed, event.PlanProposed{RequestID: "pl1", Plan: "do it"})
	r.waitKind(event.KindPlanProposed, 1)
	r.h.mu.Lock()
	mode := r.h.mode
	r.h.mu.Unlock()
	if _, err := r.h.Command(cmd(t, "i1", event.CmdTurnInterrupt, event.TurnInterrupt{})); err != nil {
		t.Fatal(err)
	}
	if c := <-f.got; c.Kind != event.CmdTurnInterrupt {
		t.Fatalf("adapter got %s", c.Kind)
	}
	r.waitKind(event.KindPlanResolved, 1)
	if s := r.h.Hello(0).Status; s != event.SessionStatusWorking {
		t.Errorf("status after the interrupt %s, want working until the turn ends", s)
	}
	var ce *CommandError
	_, err := r.h.Command(cmd(t, "r1", event.CmdPlanReply, event.PlanReply{RequestID: "pl1",
		Decision: event.PlanReplyDecisionApproved, Mode: event.PermissionModeAcceptEdits}))
	if !errors.As(err, &ce) || ce.Code != hostproto.CodeNotPending {
		t.Errorf("reply after the interrupt: %v", err)
	}
	r.h.mu.Lock()
	if r.h.mode != mode {
		t.Errorf("mode %s after a refused reply, want %s", r.h.mode, mode)
	}
	r.h.mu.Unlock()
	f.out <- draft(event.KindTurnEnded, event.TurnEnded{Outcome: event.TurnOutcomeInterrupted})
	evs := r.waitKind(event.KindTurnEnded, 1)
	if n := count(evs, event.KindPlanResolved); n != 1 {
		t.Errorf("%d plan resolutions, want the one expired", n)
	}
	for _, e := range evs {
		if e.Kind == event.KindPlanResolved {
			p, _ := e.Payload()
			if d := p.(*event.PlanResolved).Decision; d != event.PlanDecisionExpired {
				t.Errorf("plan resolved %s, want expired", d)
			}
		}
	}
	select {
	case c := <-f.got:
		t.Errorf("adapter got %s after the interrupt", c.Kind)
	default:
	}
	gapless(t, r.events())
}
