// Package flowdclient is Flow's HTTP implementation of flowdproto.API and flowdproto.Dialer (bearer token,
// Flow-Protocol header, SSE with Last-Event-ID, terminals over WebSocket). Jobs, diffs, control and ports are not
// part of the walking skeleton and answer ErrUnsupported. Owner: flowd runtime area (docs/ownership.md).
package flowdclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/ssewire"
	"github.com/trick77/flow/internal/terminals/wsterm"
)

// ErrUnsupported is a protocol call this build of the client does not make yet.
var ErrUnsupported = errors.New("flowdclient: not supported yet")

// Dialer implements flowdproto.Dialer over one pooled HTTP client.
type Dialer struct {
	HTTP *http.Client
}

// NewDialer returns a Dialer with a transport for many long-lived streams.
func NewDialer() *Dialer {
	t := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Dialer{HTTP: &http.Client{Transport: t}}
}

// Dial implements flowdproto.Dialer.
func (d *Dialer) Dial(e flowdproto.Endpoint) flowdproto.API {
	return &Client{base: "http://" + e.Address, token: e.Token, http: d.HTTP}
}

// Client is one sandbox's flowd.
type Client struct {
	base  string
	token string
	http  *http.Client
}

var _ flowdproto.API = (*Client)(nil)

// callTimeout bounds a request/response call. A var for tests.
var callTimeout = 5 * time.Minute

// cloneTimeout bounds the calls that clone or fetch (EnsureRepo, EnsureSession, AddWorktree): as long as a
// sandbox bring-up may take (sandboxes' bringTimeout), or a large repository on a slow link is cut off mid-clone
// and starts from zero on every try. A var for tests.
var cloneTimeout = 15 * time.Minute

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set(flowdproto.HeaderProtocol, strconv.Itoa(flowdproto.Protocol))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	return c.callWithin(ctx, callTimeout, method, path, body, out)
}

func (c *Client) callWithin(ctx context.Context, timeout time.Duration, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", flowdproto.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return decodeError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Error is a refusal flowd answered with; Is maps its code to the flowdproto errors.
type Error struct {
	Status int
	Body   flowdproto.ErrorBody
}

func (e *Error) Error() string {
	return fmt.Sprintf("flowd %d %s: %s", e.Status, e.Body.Code, e.Body.Detail)
}

// Is maps codes to flowdproto's errors.
func (e *Error) Is(target error) bool {
	switch e.Body.Code {
	case flowdproto.CodeNotFound:
		return target == flowdproto.ErrNotFound
	case flowdproto.CodeUnauthorized:
		return target == flowdproto.ErrUnauthorized
	case flowdproto.CodeProtocol:
		return target == flowdproto.ErrProtocol
	case flowdproto.CodeNotPending:
		return target == flowdproto.ErrNotPending
	case flowdproto.CodeBusy:
		return target == flowdproto.ErrUnavailable
	case flowdproto.CodeRepoBroken:
		return target == flowdproto.ErrRepoBroken
	case flowdproto.CodeInvalid:
		return target == flowdproto.ErrInvalid
	case flowdproto.CodeNotImplemented:
		return target == flowdproto.ErrNotImplemented
	}
	return false
}

func decodeError(resp *http.Response) error {
	e := &Error{Status: resp.StatusCode}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e.Body) != nil || e.Body.Code == "" {
		e.Body.Code = flowdproto.CodeInternal
	}
	return e
}

// Hello implements flowdproto.API; a flowd outside this build's protocol range is ErrProtocol.
func (c *Client) Hello(ctx context.Context) (flowdproto.Hello, error) {
	var h flowdproto.Hello
	if err := c.call(ctx, http.MethodGet, "/v1/hello", nil, &h); err != nil {
		return h, err
	}
	if !flowdproto.Compatible(h) {
		return h, fmt.Errorf("%w: flowd speaks %d..%d", flowdproto.ErrProtocol, h.MinProtocol, h.Protocol)
	}
	return h, nil
}

// PutEnv implements flowdproto.API.
func (c *Client) PutEnv(ctx context.Context, e flowdproto.Env) error {
	return c.call(ctx, http.MethodPut, "/v1/env", e, nil)
}

// EnsureRepo implements flowdproto.API.
func (c *Client) EnsureRepo(ctx context.Context, r flowdproto.RepoSpec) (flowdproto.RepoState, error) {
	var st flowdproto.RepoState
	return st, c.callWithin(ctx, cloneTimeout, http.MethodPost, "/v1/repos", r, &st)
}

// EnsureSession implements flowdproto.API.
func (c *Client) EnsureSession(ctx context.Context, s flowdproto.SessionSpec) (flowdproto.SessionState, error) {
	var st flowdproto.SessionState
	return st, c.callWithin(ctx, cloneTimeout, http.MethodPut, "/v1/sessions/"+url.PathEscape(s.ID), s, &st)
}

// Session implements flowdproto.API.
func (c *Client) Session(ctx context.Context, id string) (flowdproto.SessionState, error) {
	var st flowdproto.SessionState
	return st, c.call(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id), nil, &st)
}

// StopSession implements flowdproto.API.
func (c *Client) StopSession(ctx context.Context, id string, remove bool) error {
	return c.call(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(id)+"?remove="+strconv.FormatBool(remove), nil, nil)
}

// AddWorktree implements flowdproto.API.
func (c *Client) AddWorktree(ctx context.Context, id string, w flowdproto.WorktreeSpec) (flowdproto.WorktreeState, error) {
	var st flowdproto.WorktreeState
	return st, c.callWithin(ctx, cloneTimeout, http.MethodPost, "/v1/sessions/"+url.PathEscape(id)+"/worktrees", w, &st)
}

// Command implements flowdproto.API.
func (c *Client) Command(ctx context.Context, cmd event.Command) (flowdproto.CommandResult, error) {
	var r flowdproto.CommandResult
	return r, c.call(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(cmd.Session)+"/commands", cmd, &r)
}

// Events implements flowdproto.API: one stream from after; a failure is the last item (the caller re-dials from
// the last seq it got).
func (c *Client) Events(ctx context.Context, sessionID string, after int64) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		req, err := c.request(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(sessionID)+"/events", nil)
		if err != nil {
			yield(event.Event{}, err)
			return
		}
		req.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
		req.Header.Set("Accept", "text/event-stream")
		resp, err := c.http.Do(req)
		if err != nil {
			yield(event.Event{}, fmt.Errorf("%w: %w", flowdproto.ErrUnavailable, err))
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			yield(event.Event{}, decodeError(resp))
			return
		}
		stopped := false
		err = ssewire.Parse(resp.Body, func(f ssewire.Frame) bool {
			var e event.Event
			if json.Unmarshal([]byte(f.Data), &e) != nil {
				return true // not an event: skip, as for an unknown kind
			}
			if !yield(e, nil) {
				stopped = true
				return false
			}
			return true
		})
		if stopped || ctx.Err() != nil {
			return
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		yield(event.Event{}, fmt.Errorf("%w: stream ended: %w", flowdproto.ErrUnavailable, err))
	}
}

// Diff implements flowdproto.API.
func (c *Client) Diff(context.Context, string, string) (apitypes.Diff, error) {
	return apitypes.Diff{}, ErrUnsupported
}

// Control implements flowdproto.API.
func (c *Client) Control(context.Context) iter.Seq2[flowdproto.ControlRequest, error] {
	return func(yield func(flowdproto.ControlRequest, error) bool) {
		yield(flowdproto.ControlRequest{}, ErrUnsupported)
	}
}

// ReplyControl implements flowdproto.API.
func (c *Client) ReplyControl(context.Context, flowdproto.ControlReply) error { return ErrUnsupported }

// StartJob implements flowdproto.API.
func (c *Client) StartJob(context.Context, flowdproto.JobSpec) (flowdproto.JobState, error) {
	return flowdproto.JobState{}, ErrUnsupported
}

// Job implements flowdproto.API.
func (c *Client) Job(context.Context, string) (flowdproto.JobState, error) {
	return flowdproto.JobState{}, ErrUnsupported
}

// JobLog implements flowdproto.API.
func (c *Client) JobLog(context.Context, string, int) ([]flowdproto.LogLine, error) {
	return nil, ErrUnsupported
}

// EnsureTerminal implements flowdproto.API.
func (c *Client) EnsureTerminal(ctx context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	var st flowdproto.TerminalState
	return st, c.call(ctx, http.MethodPut, "/v1/terminals/"+url.PathEscape(t.ID), t, &st)
}

// attachWait bounds the WebSocket handshake with flowd (a variable for tests: the connection must outlive it).
var attachWait = 15 * time.Second

// AttachTerminal implements flowdproto.API: a WebSocket to flowd (internal/terminals/wsterm), on the pooled
// transport, not under callTimeout: it lives until Close.
func (c *Client) AttachTerminal(ctx context.Context, id string, size flowdproto.TerminalSize) (flowdproto.TerminalConn, error) {
	ctx, cancel := context.WithTimeout(ctx, attachWait)
	defer cancel()
	u := c.base + "/v1/terminals/" + url.PathEscape(id) + "/attach"
	if flowdproto.ValidTerminalSize(size.Cols, size.Rows) {
		u += "?cols=" + strconv.Itoa(size.Cols) + "&rows=" + strconv.Itoa(size.Rows)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	h.Set(flowdproto.HeaderProtocol, strconv.Itoa(flowdproto.Protocol))
	// coder/websocket owns resp.Body: nil after an upgrade, an in-memory copy after a refusal.
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: h, //nolint:bodyclose // see above
		CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, decodeError(resp)
		}
		return nil, fmt.Errorf("%w: %w", flowdproto.ErrUnavailable, err)
	}
	conn := wsterm.Wrap(ws)
	return conn, nil
}

// CloseTerminal implements flowdproto.API.
func (c *Client) CloseTerminal(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/terminals/"+url.PathEscape(id), nil, nil)
}

// Port implements flowdproto.API.
func (c *Client) Port(int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, ErrUnsupported.Error(), http.StatusNotImplemented)
	})
}
