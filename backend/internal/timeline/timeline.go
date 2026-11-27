// Package timeline serves the browser's event streams (SSE by seq, with catch-up and live snapshot), messages,
// commands and replies: httpapi.Timeline. It relays to flowd through flowdproto.API and turns browser requests into
// commands with ids the browser chose. Owner: timeline API area (docs/ownership.md).
package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/ship/kit/sse"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	sbx "github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/ssewire"
	"github.com/trick77/flow/internal/store"
)

// API implements httpapi.Timeline: the session stream and log, the app stream, messages and the queue, interrupt
// and the replies to permission, question and plan requests.
type API struct {
	httpapi.NotImplemented
	Sessions *sessions.Service
	Hub      *sse.Hub[apitypes.AppEvent]
	// Retry is the wait before re-dialling flowd after a stream broke, and the first wait for a sandbox that does not
	// run, doubling up to MaxRetry; Heartbeat the keepalive interval.
	Retry, MaxRetry, Heartbeat time.Duration
}

func (a *API) retry() time.Duration {
	if a.Retry > 0 {
		return a.Retry
	}
	return time.Second
}

func (a *API) maxRetry() time.Duration {
	if a.MaxRetry > 0 {
		return a.MaxRetry
	}
	return 30 * time.Second
}

func (a *API) heartbeat() time.Duration {
	if a.Heartbeat > 0 {
		return a.Heartbeat
	}
	return 15 * time.Second
}

func (a *API) session(w http.ResponseWriter, r *http.Request) (sessions.Session, bool) {
	se, err := a.Sessions.Store.Get(r.Context(), r.PathValue("sessionId"))
	if errors.Is(err, sessions.ErrNotFound) {
		err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return se, false
	}
	return se, true
}

// position is where a stream starts: the larger of ?after and Last-Event-ID, 0 without either. An EventSource that
// reconnects by itself keeps the URL it opened with, so its ?after lags behind the Last-Event-ID it sends.
func position(r *http.Request) (int64, bool) {
	var pos int64
	for _, v := range []string{r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")} {
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		pos = max(pos, n)
	}
	return pos, true
}

// StreamSessionEvents implements httpapi.Timeline: the durable events after the position, then live, as SSE
// events named "flow" with the seq as id. When the stream from flowd breaks (flowd restart, sandbox restart) it is
// dialled again from the last seq sent, so the browser sees neither a gap nor a repeat.
func (a *API) StreamSessionEvents(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	last, ok := position(r)
	if !ok {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "after"})
		return
	}
	sw, err := ssewire.Open(w)
	if err != nil {
		return
	}
	defer sw.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		t := time.NewTicker(a.heartbeat())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if sw.Comment() != nil {
					cancel()
					return
				}
			}
		}
	}()
	// A sandbox that does not run (stopped, failed, none: the browser shows its state from the app stream and offers
	// the start) is asked again ever more rarely, each time a lock and a look at the instance; a change of the
	// project's sandbox (a start) asks at once.
	changes, unsubscribe := a.sandboxChanges()
	defer unsubscribe()
	wait := a.retry()
	for ctx.Err() == nil {
		drain(changes) // only changes after this look count
		api, err := a.Sessions.Sandboxes.Warm(ctx, se.ProjectID)
		if err != nil {
			waitForSandbox(ctx, changes, se.ProjectID, wait)
			if errors.Is(err, sbx.ErrNotWarm) || errors.Is(err, sbx.ErrMayRun) || errors.Is(err, sbx.ErrNotFound) {
				wait = min(wait*2, a.maxRetry())
			} // any other failure (a busy store) is asked again at the plain retry
			continue
		}
		wait = a.retry()
		for e, err := range api.Events(ctx, se.ID, last) {
			if err != nil {
				a.Sessions.Log.Debug("session stream broke", "session", se.ID, "after", last, "err", err)
				break
			}
			if e.Seq > 0 && e.Seq <= last {
				continue
			}
			if send(sw, e) != nil {
				return
			}
			if e.Seq > 0 {
				last = e.Seq
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(a.retry()):
		}
	}
}

// Log page bounds: events per page (default and most), and the encoded bytes one page carries at most (events are
// up to event.MaxLine each). A page cut by bytes is shorter; the reader pages on from its edge.
const (
	logDefaultLimit = 500
	logMaxLimit     = 1000
	logMaxBytes     = 8 << 20
	logReadWait     = 30 * time.Second
)

// ReadSessionLog implements httpapi.Timeline: a page of durable events from the log, forwards after ?after (default
// 0) or backwards before ?before, at most ?limit (default 500, at most 1000) and about 8 MiB. Seqs are gapless, so a
// page is one forward read from flowd's stream between two known seqs. The log lives in the sandbox: one that is not
// running is never started for it and answers sandbox_unavailable (the timeline offers the start).
func (a *API) ReadSessionLog(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	param := func(name string) (int64, bool, bool) {
		v := q.Get(name)
		if v == "" {
			return 0, false, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, true, err == nil && n >= 0
	}
	after, hasAfter, ok1 := param("after")
	before, hasBefore, ok2 := param("before")
	limit, hasLimit, ok3 := param("limit")
	switch {
	case !ok1 || hasAfter && hasBefore:
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "after"})
		return
	case !ok2:
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "before"})
		return
	case !ok3 || hasLimit && (limit < 1 || limit > logMaxLimit):
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "limit"})
		return
	}
	if !hasLimit {
		limit = logDefaultLimit
	}
	ctx, cancel := context.WithTimeout(r.Context(), logReadWait)
	defer cancel()
	api, err := a.Sessions.Sandboxes.Warm(ctx, se.ProjectID)
	if err != nil {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error()))
		return
	}
	st, err := api.Session(ctx, se.ID)
	if err != nil {
		if errors.Is(err, flowdproto.ErrNotFound) {
			err = apitypes.Fail(apitypes.ErrorCodeNotFound, "")
		} else {
			err = apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, err.Error())
		}
		httpapi.WriteError(w, err)
		return
	}
	// Past the end there is nothing to read; clamped, after+limit cannot overflow into a negative page.
	after = min(after, st.LastSeq)
	lo, hi := after+1, min(st.LastSeq, after+limit)
	if hasBefore {
		hi = min(before-1, st.LastSeq)
		lo = max(1, hi-limit+1)
	}
	page := apitypes.EventPage{Events: []json.RawMessage{}, LastSeq: st.LastSeq}
	// A stream that breaks (flowd restarting) is dialled again from the next seq, a few times.
	next, size, full := lo, 0, false
	var streamErr error
	for attempt := 0; next <= hi && !full && attempt < 3; attempt++ {
		for e, err := range api.Events(ctx, se.ID, next-1) {
			if err != nil {
				streamErr = err
				break
			}
			if e.Seq < next {
				continue // ephemeral (no seq), or before the page
			}
			b, err := json.Marshal(e)
			if err != nil {
				httpapi.WriteError(w, err)
				return
			}
			page.Events = append(page.Events, b)
			size += len(b)
			next = e.Seq + 1
			for hasBefore && size > logMaxBytes && len(page.Events) > 1 {
				size -= len(page.Events[0]) // backwards: the newest stay, the reader pages on before the first
				page.Events = page.Events[1:]
			}
			if full = !hasBefore && size > logMaxBytes; full || next > hi {
				break
			}
		}
	}
	if next <= hi && !full {
		detail := "log stream ended early"
		if streamErr != nil {
			detail = streamErr.Error()
		}
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, detail))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, page)
}

// sandboxChanges subscribes to the app stream for sandbox changes; without a hub (or one at its cap) the channel
// never delivers and the waits run to their end.
func (a *API) sandboxChanges() (<-chan apitypes.AppEvent, func()) {
	if a.Hub != nil {
		if ch, unsubscribe, ok := a.Hub.Subscribe(""); ok {
			return ch, unsubscribe
		}
	}
	return nil, func() {}
}

func drain(ch <-chan apitypes.AppEvent) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// waitForSandbox waits up to d, less when the project's sandbox changes or ctx ends.
func waitForSandbox(ctx context.Context, changes <-chan apitypes.AppEvent, projectID string, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case e, ok := <-changes:
			if !ok {
				changes = nil // the hub closed: the timer alone
				continue
			}
			if e.Kind == apitypes.AppEventKindSandboxUpdated && e.ProjectID == projectID {
				return
			}
		}
	}
}

func send(sw *ssewire.Writer, e event.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f := ssewire.Frame{Event: "flow", Data: string(b)}
	if e.Seq > 0 {
		f.ID = strconv.FormatInt(e.Seq, 10)
	}
	return sw.Send(f)
}

// StreamAppEvents implements httpapi.Timeline: AppEvents as SSE events named "app"; no replay.
func (a *API) StreamAppEvents(w http.ResponseWriter, r *http.Request) {
	ch, unsubscribe, ok := a.Hub.Subscribe("")
	if !ok {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeInternal, "app stream closed"))
		return
	}
	defer unsubscribe()
	sw, err := ssewire.Open(w)
	if err != nil {
		return
	}
	defer sw.Close()
	t := time.NewTicker(a.heartbeat())
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			if sw.Comment() != nil {
				return
			}
		case e, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(e)
			if sw.Send(ssewire.Frame{Event: "app", Data: string(b)}) != nil {
				return
			}
		}
	}
}

// SendMessage implements httpapi.Timeline: the browser's clientId is the message id and the command id, so a retry
// is acknowledged as a duplicate.
func (a *API) SendMessage(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	var in apitypes.MessageCreate
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	if in.ClientID == "" || len(in.ClientID) > 64 {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "clientId"})
		return
	}
	if strings.TrimSpace(in.Text) == "" || !event.TextFits(in.Text) {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "text"})
		return
	}
	whenBusy := event.WhenBusyQueue
	if in.Interrupt {
		whenBusy = event.WhenBusyInterrupt
	}
	res, err := a.Sessions.Command(r.Context(), se.ID, in.ClientID, event.CmdMessageSend, event.MessageSend{
		MessageID: in.ClientID, Text: in.Text, Origin: event.MessageOriginUser, WhenBusy: whenBusy})
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	// The host says whether it queued the message: the mirrored status read before sending can be stale.
	httpapi.WriteJSON(w, http.StatusAccepted, apitypes.MessageAccepted{MessageID: in.ClientID, Queued: res.Queued})
}

// InterruptTurn implements httpapi.Timeline. Nothing runs in a stopped sandbox: there the interrupt is a no-op,
// never a start of it.
func (a *API) InterruptTurn(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	if _, _, err := a.Sessions.CommandIfRunning(r.Context(), se.ID, "interrupt-"+store.NewID(), event.CmdTurnInterrupt,
		event.TurnInterrupt{}); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReplyPermission implements httpapi.Timeline: one reply per request (the command id is the request's), so a second
// click or a retry changes nothing. In a stopped sandbox no request is pending (request_not_pending): the sandbox
// is not started for it.
func (a *API) ReplyPermission(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	var in apitypes.PermissionAnswer
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	switch in.Decision {
	case apitypes.ReplyDecisionAllowOnce, apitypes.ReplyDecisionAllowAlways, apitypes.ReplyDecisionDeny:
	default:
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "decision"})
		return
	}
	req := r.PathValue("requestId")
	a.reply(w, r, se, req, event.CmdPermissionReply, event.PermissionReply{RequestID: req,
		Decision: event.ReplyDecision(in.Decision), Message: in.Message})
}

// reply sends one owner reply to a request: one per request (the command id is the request's), so a second click or
// a retry changes nothing; in a stopped sandbox no request is pending (request_not_pending), and the sandbox is not
// started for it.
func (a *API) reply(w http.ResponseWriter, r *http.Request, se sessions.Session, req string, kind event.CommandKind, payload any) {
	_, ran, err := a.Sessions.CommandIfRunning(r.Context(), se.ID, "reply-"+req, kind, payload)
	if err == nil && !ran {
		err = apitypes.Fail(apitypes.ErrorCodeRequestNotPending, "")
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReplyQuestion implements httpapi.Timeline: chosen option ids and/or free text per question, or one text typed in
// the composer; the sidecar turns them into AskUserQuestion's answers (keyed by question text). A reply that answers
// nothing is refused: the agent would hear "no answer" while the log says answered.
func (a *API) ReplyQuestion(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	var in apitypes.QuestionAnswer
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	answered := strings.TrimSpace(in.Text) != ""
	size := len(in.Text)
	answers := make([]event.Answer, 0, len(in.Answers))
	for _, ans := range in.Answers {
		if ans.QuestionID == "" {
			httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "answers"})
			return
		}
		answered = answered || len(ans.OptionIds) > 0 || strings.TrimSpace(ans.Text) != ""
		size += len(ans.Text)
		ids := ans.OptionIds
		if ids == nil {
			ids = []string{} // required in the contract: never null
		}
		answers = append(answers, event.Answer{QuestionID: ans.QuestionID, OptionIds: ids, Text: ans.Text})
	}
	if !answered {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "answers"})
		return
	}
	if size > maxReplyText {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "text"})
		return
	}
	req := r.PathValue("requestId")
	reply := event.QuestionReply{RequestID: req, Text: in.Text}
	if len(answers) > 0 {
		reply.Answers = answers
	}
	a.reply(w, r, se, req, event.CmdQuestionReply, reply)
}

// maxReplyText bounds the free text of a question or plan reply: the host logs the resolution before the agent hears
// it, and a resolution too large to log would leave the request resolved with nothing on record.
const maxReplyText = 64 << 10

// ReplyPlan implements httpapi.Timeline: approve (optionally with the mode to go on in: the session's mode changes,
// held to its cap by the host) or reject with feedback, which the agent reads as the reason to keep planning.
func (a *API) ReplyPlan(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	var in apitypes.PlanAnswer
	if err := httpapi.Decode(r, &in); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	switch in.Decision {
	case apitypes.PlanReplyDecisionApproved, apitypes.PlanReplyDecisionRejected:
	default:
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "decision"})
		return
	}
	mode := event.PermissionMode(in.Mode)
	if mode != "" && (!slices.Contains(event.PermissionModeValues, mode) || in.Decision != apitypes.PlanReplyDecisionApproved) {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "mode"})
		return
	}
	if mode != "" && se.ModeCap != "" &&
		slices.Index(event.PermissionModeValues, mode) > slices.Index(event.PermissionModeValues, event.PermissionMode(se.ModeCap)) {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeModeAboveCap, ""))
		return
	}
	if len(in.Feedback) > maxReplyText {
		httpapi.WriteError(w, &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "feedback"})
		return
	}
	req := r.PathValue("requestId")
	a.reply(w, r, se, req, event.CmdPlanReply, event.PlanReply{RequestID: req,
		Decision: event.PlanReplyDecision(in.Decision), Feedback: in.Feedback, Mode: mode})
}

// SendQueuedNow implements httpapi.Timeline: the queued message moves to the head and the running turn is
// interrupted (or, between turns, it starts at once). Like sending a message, it starts a stopped sandbox. A message
// no longer queued (sent or removed already) is not_found. The command id is the message's, so a retry or second
// click is acknowledged, not applied twice.
func (a *API) SendQueuedNow(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	msg := r.PathValue("messageId")
	// Held: a host that is down starts without sending its queue's head first, then runs this message.
	_, _, err := a.Sessions.CommandHeld(r.Context(), se.ID, "send-now-"+msg, event.CmdMessageSendNow,
		event.MessageSendNow{MessageID: msg}, true, false)
	queueAnswer(w, err)
}

// RemoveQueued implements httpapi.Timeline: drops a queued message. A host that is down (after a sandbox restart,
// which leaves queues as they are) is started without sending its queue's head, then stopped again. Never starts a
// stopped sandbox: the answer there is sandbox_unavailable. A message no longer queued is not_found.
func (a *API) RemoveQueued(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	msg := r.PathValue("messageId")
	_, ran, err := a.Sessions.CommandHeld(r.Context(), se.ID, "remove-"+msg, event.CmdMessageRemove,
		event.MessageRemove{MessageID: msg}, false, true)
	if err == nil && !ran {
		err = apitypes.Fail(apitypes.ErrorCodeSandboxUnavailable, "sandbox not running")
	}
	queueAnswer(w, err)
}

// queueAnswer answers a queue operation: the host's "not queued" is not_found here (the message, not a request).
func queueAnswer(w http.ResponseWriter, err error) {
	var ae *apitypes.Error
	if errors.As(err, &ae) && ae.Code == apitypes.ErrorCodeRequestNotPending {
		err = apitypes.Fail(apitypes.ErrorCodeNotFound, "message not queued")
	}
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
