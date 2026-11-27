// Package timeline serves the browser's event streams (SSE by seq, with catch-up and live snapshot), messages,
// commands and replies: httpapi.Timeline. It relays to flowd through flowdproto.API and turns browser requests into
// commands with ids the browser chose. Owner: timeline API area (docs/ownership.md).
package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/ship/kit/sse"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/ssewire"
	"github.com/trick77/flow/internal/store"
)

// API implements httpapi.Timeline: the session stream, the app stream, messages, interrupt and permission replies
// (the rest answer not_implemented).
type API struct {
	httpapi.NotImplemented
	Sessions *sessions.Service
	Hub      *sse.Hub[apitypes.AppEvent]
	// Retry is the wait before re-dialling flowd after a stream broke; Heartbeat the keepalive interval.
	Retry, Heartbeat time.Duration
}

func (a *API) retry() time.Duration {
	if a.Retry > 0 {
		return a.Retry
	}
	return time.Second
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
	for ctx.Err() == nil {
		api, err := a.Sessions.Sandboxes.Warm(ctx, se.ProjectID)
		if err == nil {
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
		}
		select {
		case <-ctx.Done():
		case <-time.After(a.retry()):
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

// InterruptTurn implements httpapi.Timeline.
func (a *API) InterruptTurn(w http.ResponseWriter, r *http.Request) {
	se, ok := a.session(w, r)
	if !ok {
		return
	}
	if _, err := a.Sessions.Command(r.Context(), se.ID, "interrupt-"+store.NewID(), event.CmdTurnInterrupt,
		event.TurnInterrupt{}); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReplyPermission implements httpapi.Timeline: one reply per request (the command id is the request's), so a second
// click or a retry changes nothing.
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
	_, err := a.Sessions.Command(r.Context(), se.ID, "reply-"+req, event.CmdPermissionReply, event.PermissionReply{
		RequestID: req, Decision: event.ReplyDecision(in.Decision), Message: in.Message})
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
