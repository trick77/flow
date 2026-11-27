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
