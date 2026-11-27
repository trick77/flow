package timeline

import (
	"bufio"
	"context"
	"encoding/json"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/ship/kit/sse"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/flowdproto/flowdfake"
	"github.com/trick77/flow/internal/projects"
	sbx "github.com/trick77/flow/internal/sandboxes"
	"github.com/trick77/flow/internal/sessions"
	"github.com/trick77/flow/internal/ssewire"
	"github.com/trick77/flow/internal/store"
)

// flaky wraps the fake flowd: its first event stream breaks after a few events (a flowd restart), and a reply to
// request "gone" is refused as not pending.
type flaky struct {
	*flowdfake.Fake
	mu     sync.Mutex
	broken bool
	dials  []int64
}

func (f *flaky) Events(ctx context.Context, id string, after int64) iter.Seq2[event.Event, error] {
	f.mu.Lock()
	f.dials = append(f.dials, after)
	breakIt := !f.broken
	f.broken = true
	f.mu.Unlock()
	return func(yield func(event.Event, error) bool) {
		n := 0
		for e, err := range f.Fake.Events(ctx, id, after) {
			if breakIt && n == 3 {
				yield(event.Event{}, flowdproto.ErrUnavailable)
				return
			}
			n++
			if !yield(e, err) {
				return
			}
		}
	}
}

func (f *flaky) Command(ctx context.Context, c event.Command) (flowdproto.CommandResult, error) {
	if c.Kind == event.CmdPermissionReply && strings.Contains(string(c.Data), `"gone"`) {
		return flowdproto.CommandResult{}, flowdproto.ErrNotPending
	}
	return f.Fake.Command(ctx, c)
}

var errNotWarm = sbx.ErrNotWarm

// sandboxes hands out the fake flowd; with stopped set Warm answers not warm (a stopped sandbox), and Ensure counts
// the bring-ups asked for.
type sandboxes struct {
	api     flowdproto.API
	mu      sync.Mutex
	stopped bool
	ensures int
	warms   int
}

func (s *sandboxes) Ensure(context.Context, string) (flowdproto.API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensures++
	s.stopped = false
	return s.api, nil
}

func (s *sandboxes) Warm(context.Context, string) (flowdproto.API, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warms++
	if s.stopped {
		return nil, errNotWarm
	}
	return s.api, nil
}

func (s *sandboxes) Forget(string) {}

func (s *sandboxes) set(stopped bool) (ensures, warms int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = stopped
	return s.ensures, s.warms
}

func (s *sandboxes) counts() (ensures, warms int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensures, s.warms
}

func demo(t *testing.T) []event.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "contract", "fixtures", "demo.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []event.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, event.MaxLine)
	for sc.Scan() {
		e, err := event.ParseLine(append(sc.Bytes(), '\n'))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

type rig struct {
	api  *API
	fake *flaky
	sb   *sandboxes
	srv  *httptest.Server
	id   string
	hub  *sse.Hub[apitypes.AppEvent]
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ps := projects.SQL{DB: db}
	_ = ps.Create(t.Context(), projects.Project{ID: "p1", Name: "demo"}, []projects.Repo{{ID: "r1", FullName: "a/b", DefaultBranch: "main"}})
	fake := &flaky{Fake: flowdfake.New("sb")}
	fake.Script = flowdfake.Replay(demo(t))
	hub := sse.NewHub[apitypes.AppEvent](sse.HubOptions{})
	t.Cleanup(hub.Close)
	sb := &sandboxes{api: fake}
	svc := &sessions.Service{Store: sessions.SQL{DB: db}, Projects: ps, Sandboxes: sb, Hub: hub,
		Log: slog.New(slog.DiscardHandler)}
	s, err := svc.Create(t.Context(), apitypes.SessionCreate{ProjectID: "p1", RepoIds: []string{"r1"}, Provider: "claude",
		Model: "m", Mode: "ask", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	a := &API{Sessions: svc, Hub: hub, Retry: 10 * time.Millisecond, Heartbeat: 20 * time.Millisecond}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{sessionId}/events", a.StreamSessionEvents)
	mux.HandleFunc("GET /events", a.StreamAppEvents)
	mux.HandleFunc("POST /s/{sessionId}/messages", a.SendMessage)
	mux.HandleFunc("POST /s/{sessionId}/interrupt", a.InterruptTurn)
	mux.HandleFunc("POST /s/{sessionId}/permissions/{requestId}", a.ReplyPermission)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &rig{api: a, fake: fake, sb: sb, srv: srv, id: s.Summary.ID, hub: hub}
}

func (r *rig) post(t *testing.T, path, body string) int {
	t.Helper()
	resp, err := http.Post(r.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestStream_catchUpReplyAndRedial(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/s/"+r.id+"/events", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := make(chan ssewire.Frame, 100)
	go func() { _ = ssewire.Parse(resp.Body, func(f ssewire.Frame) bool { frames <- f; return true }) }()
	next := func() event.Event {
		select {
		case f := <-frames:
			var e event.Event
			_ = json.Unmarshal([]byte(f.Data), &e)
			if f.Event != "flow" || f.ID == "" {
				t.Errorf("frame %+v", f)
			}
			return e
		case <-ctx.Done():
			t.Fatal("stream stalled")
		}
		return event.Event{}
	}
	if c := r.post(t, "/s/"+r.id+"/messages", `{"clientId":"m1","text":"go"}`); c != http.StatusAccepted {
		t.Fatalf("message: %d", c)
	}
	var seqs []int64
	var req0 string
	for req0 == "" {
		e := next()
		seqs = append(seqs, e.Seq)
		if e.Kind == event.KindPermissionRequested {
			var p event.PermissionRequested
			_ = json.Unmarshal(e.Data, &p)
			req0 = p.RequestID
		}
	}
	if c := r.post(t, "/s/"+r.id+"/permissions/"+req0, `{"decision":"allow_once"}`); c != http.StatusNoContent {
		t.Fatalf("reply: %d", c)
	}
	for {
		e := next()
		seqs = append(seqs, e.Seq)
		if e.Kind == event.KindTurnEnded {
			break
		}
	}
	for i, s := range seqs {
		if s != int64(i+2) {
			t.Fatalf("seqs %v: gap or repeat at %d", seqs, i)
		}
	}
	r.fake.mu.Lock()
	dials := r.fake.dials
	r.fake.mu.Unlock()
	if len(dials) < 2 || dials[0] != 1 || dials[1] != 4 {
		t.Errorf("dials from %v, want 1 then the last seq sent (4)", dials)
	}
}

func TestCommandsAndRefusals(t *testing.T) {
	r := newRig(t)
	for path, want := range map[string]int{
		"/s/" + r.id + "/interrupt": http.StatusNoContent,
		"/s/nope/interrupt":         http.StatusNotFound,
		"/s/nope/messages":          http.StatusNotFound,
		"/s/nope/permissions/x":     http.StatusNotFound,
	} {
		if c := r.post(t, path, `{}`); c != want {
			t.Errorf("%s: %d, want %d", path, c, want)
		}
	}
	for body, want := range map[string]int{`{"clientId":"","text":"x"}`: 400, `{"clientId":"a","text":" "}`: 400,
		`nope`: 400, `{"clientId":"a","text":"x","interrupt":true}`: 202} {
		if c := r.post(t, "/s/"+r.id+"/messages", body); c != want {
			t.Errorf("message %s: %d", body, c)
		}
	}
	for body, want := range map[string]int{`{"decision":"maybe"}`: 400, `nope`: 400} {
		if c := r.post(t, "/s/"+r.id+"/permissions/x", body); c != want {
			t.Errorf("reply %s: %d", body, c)
		}
	}
	if c := r.post(t, "/s/"+r.id+"/permissions/gone", `{"decision":"deny"}`); c != http.StatusConflict {
		t.Errorf("reply to a request not pending: %d", c)
	}
	resp, _ := http.Get(r.srv.URL + "/s/" + r.id + "/events?after=-1")
	_ = resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("bad position: %d", resp.StatusCode)
	}
	resp, _ = http.Get(r.srv.URL + "/s/nope/events")
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("unknown session stream: %d", resp.StatusCode)
	}
}

// A stream of a session whose sandbox is stopped asks for the sandbox ever more rarely (each look takes the
// project's lock and asks Incus), and at once when the project's sandbox changes (the owner started it).
func TestStream_stoppedSandboxBacksOffAndWakesOnAChange(t *testing.T) {
	r := newRig(t)
	r.api.MaxRetry = time.Minute
	_, warms0 := r.sb.set(true)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/s/"+r.id+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := make(chan ssewire.Frame, 100)
	go func() {
		_ = ssewire.Parse(resp.Body, func(f ssewire.Frame) bool {
			if f.Event != "" {
				frames <- f
			}
			return true
		})
	}()
	time.Sleep(600 * time.Millisecond)
	// Retry 10 ms doubling: 10+20+40+80+160+320 ms, six or seven looks; a fixed retry would have made sixty.
	if _, warms := r.sb.counts(); warms-warms0 > 10 {
		t.Errorf("%d looks at a stopped sandbox in 600 ms", warms-warms0)
	}
	select {
	case f := <-frames:
		t.Fatalf("frame from a stopped sandbox %+v", f)
	default:
	}
	r.sb.set(false)
	r.hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSandboxUpdated, ProjectID: "other"})
	r.hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSandboxUpdated, ProjectID: "p1"})
	select {
	case f := <-frames:
		if f.Event != "flow" {
			t.Errorf("frame %+v", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the start of the sandbox did not wake the stream")
	}
}

// Interrupting or answering a request in a stopped sandbox starts it not: the interrupt is a no-op, the reply finds
// no request pending.
func TestCommands_stoppedSandboxStartsNothing(t *testing.T) {
	r := newRig(t)
	if err := r.fake.StopSession(t.Context(), r.id, false); err != nil { // the stop ended the host too
		t.Fatal(err)
	}
	ensures, _ := r.sb.set(true)
	if c := r.post(t, "/s/"+r.id+"/interrupt", `{}`); c != http.StatusNoContent {
		t.Errorf("interrupt in a stopped sandbox: %d", c)
	}
	if c := r.post(t, "/s/"+r.id+"/permissions/x", `{"decision":"allow_once"}`); c != http.StatusConflict {
		t.Errorf("reply in a stopped sandbox: %d", c)
	}
	if n, _ := r.sb.counts(); n != ensures {
		t.Errorf("%d bring-ups for commands with nothing to act on", n-ensures)
	}
	if st, _ := r.fake.Session(t.Context(), r.id); st.Unit != flowdproto.UnitInactive {
		t.Errorf("host started for a no-op: %s", st.Unit)
	}
}

// The text limit is measured as encoded JSON: 60'000 "<" are 360'000 bytes once escaped, over the limit, while
// 200'000 plain letters pass.
func TestSendMessage_textLimitAfterEncoding(t *testing.T) {
	r := newRig(t)
	body := func(text string) string {
		b, _ := json.Marshal(apitypes.MessageCreate{ClientID: "c-" + text[:1], Text: text})
		return string(b)
	}
	if c := r.post(t, "/s/"+r.id+"/messages", body(strings.Repeat("<", 60_000))); c != http.StatusBadRequest {
		t.Errorf("escaped text over the limit: %d", c)
	}
	if c := r.post(t, "/s/"+r.id+"/messages", body(strings.Repeat("a", 200_000))); c != http.StatusAccepted {
		t.Errorf("text under the limit: %d", c)
	}
}

// The queued flag says what the host did with the message, not what the mirrored status said before it was sent.
func TestSendMessage_queuedComesFromTheHost(t *testing.T) {
	r := newRig(t)
	accepted := func(body string) apitypes.MessageAccepted {
		t.Helper()
		resp, err := http.Post(r.srv.URL+"/s/"+r.id+"/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var a apitypes.MessageAccepted
		_ = json.NewDecoder(resp.Body).Decode(&a)
		return a
	}
	if a := accepted(`{"clientId":"q1","text":"first"}`); a.Queued {
		t.Error("a message to an idle session reported queued")
	}
	// The first turn now waits for its permission reply: the next message queues behind it.
	if a := accepted(`{"clientId":"q2","text":"second"}`); !a.Queued {
		t.Error("a message behind a running turn was not reported queued")
	}
	if a := accepted(`{"clientId":"q2","text":"second"}`); !a.Queued {
		t.Error("a retry of a queued message was not reported queued")
	}
}

func TestAppEvents(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", r.srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go func() {
		for ctx.Err() == nil && r.hub.Len() == 0 {
			time.Sleep(time.Millisecond)
		}
		r.hub.Publish(apitypes.AppEvent{Kind: apitypes.AppEventKindSessionUpdated, SessionID: r.id})
	}()
	var got ssewire.Frame
	_ = ssewire.Parse(resp.Body, func(f ssewire.Frame) bool { got = f; return false })
	if got.Event != "app" || !strings.Contains(got.Data, r.id) {
		t.Errorf("frame %+v", got)
	}
	r.hub.Close()
	w := httptest.NewRecorder()
	r.api.StreamAppEvents(w, httptest.NewRequest("GET", "/events", nil))
	if w.Code != 500 {
		t.Errorf("closed hub: %d", w.Code)
	}
}

// An EventSource that reconnects by itself keeps its URL (?after of when it opened) and sends Last-Event-ID: the
// larger of the two is where the tab really is.
func TestPosition(t *testing.T) {
	for _, c := range []struct {
		q, last string
		want    int64
		ok      bool
	}{{"", "", 0, true}, {"?after=7", "", 7, true}, {"", "9", 9, true}, {"?after=3", "12", 12, true},
		{"?after=12", "3", 12, true}, {"?after=x", "", 0, false}, {"", "-1", 0, false}} {
		r := httptest.NewRequest("GET", "/"+c.q, nil)
		if c.last != "" {
			r.Header.Set("Last-Event-ID", c.last)
		}
		n, ok := position(r)
		if ok != c.ok || (ok && n != c.want) {
			t.Errorf("%q / %q = %d %v, want %d", c.q, c.last, n, ok, c.want)
		}
	}
}
