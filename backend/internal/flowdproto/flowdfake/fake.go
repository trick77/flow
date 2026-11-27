// Package flowdfake is an in-memory flowd: flowdproto.API with session hosts that behave like the real ones at the
// event level (queueing, turns, permission, question and plan requests, idempotent commands), driven by scripts
// instead of agents. Flow's tests use it; so does the UI demo mode, replaying a recorded stream as the script.
package flowdfake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
)

// Script yields the drafts of one turn for a delivered message. A draft of kind permission.requested,
// question.asked or plan.proposed pauses the turn until the matching reply command; the remaining drafts follow.
type Script func(text string) []event.EventDraft

// Replay is a script that plays a recorded session's turn content for every message: each durable event of the
// recording except what a host writes itself (session, message, turn, status, queue and worktree events) and the
// resolutions of requests, which the fake writes when the reply command arrives.
func Replay(recording []event.Event) Script {
	var drafts []event.EventDraft
	for _, e := range recording {
		switch e.Kind {
		case event.KindSessionStarted, event.KindSessionResumed, event.KindSessionStatus, event.KindMessageUser,
			event.KindMessageQueued, event.KindMessageDequeued, event.KindTurnStarted, event.KindTurnEnded,
			event.KindPermissionResolved, event.KindQuestionResolved, event.KindPlanResolved, event.KindWorktreeAdded:
			continue
		}
		drafts = append(drafts, event.EventDraft{Kind: e.Kind, ParentTool: e.ParentTool, Data: e.Data})
	}
	return func(string) []event.EventDraft { return slices.Clone(drafts) }
}

// Echo is the default script: one assistant text repeating the message.
func Echo(text string) []event.EventDraft {
	return []event.EventDraft{mustDraft(event.KindAssistantText, event.AssistantText{BlockID: "b1", Text: "You said: " + text})}
}

func mustDraft(kind event.EventKind, payload any) event.EventDraft {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err) // payloads are contract types; a programmer error
	}
	return event.EventDraft{Kind: kind, Data: b}
}

// Fake is the in-memory flowd. Create with New.
type Fake struct {
	mu        sync.Mutex
	changed   chan struct{} // closed and replaced on every change
	hello     flowdproto.Hello
	env       flowdproto.Env
	repos     map[string]flowdproto.RepoState
	sessions  map[string]*host
	control   []flowdproto.ControlRequest
	replies   map[string]flowdproto.ControlReply
	terminals map[string]flowdproto.TerminalState
	diffs     map[string]apitypes.Diff
	jobs      map[string]*job
	// Script answers every delivered message; Echo by default.
	Script Script
	// JobScript yields a job's log lines and exit code; one line and 0 by default.
	JobScript func(flowdproto.JobSpec) ([]string, int)
	// Now is the clock; time.Now by default.
	Now func() time.Time
}

var _ flowdproto.API = (*Fake)(nil)

type host struct {
	spec     flowdproto.SessionSpec
	state    flowdproto.SessionState
	log      []event.Event
	cmds     map[string]bool
	queue    []event.MessageSend
	turn     string
	turns    int
	pending  []event.EventDraft // the rest of a paused turn
	waitFor  string             // request id the turn waits on
	waitKind event.EventKind    // and the kind that asked
	started  time.Time
}

// New returns a fake flowd for sandbox.
func New(sandbox string) *Fake {
	return &Fake{
		changed: make(chan struct{}),
		hello: flowdproto.Hello{Protocol: flowdproto.Protocol, MinProtocol: flowdproto.MinProtocol,
			Version: "fake", Sandbox: sandbox, Arch: "amd64", BootID: "boot-1"},
		repos: map[string]flowdproto.RepoState{}, sessions: map[string]*host{},
		replies: map[string]flowdproto.ControlReply{}, terminals: map[string]flowdproto.TerminalState{},
		diffs: map[string]apitypes.Diff{}, jobs: map[string]*job{}, Script: Echo, Now: time.Now,
		JobScript: func(j flowdproto.JobSpec) ([]string, int) { return []string{string(j.Kind) + " " + j.Repo}, 0 },
	}
}

type job struct {
	state flowdproto.JobState
	lines []string
}

// notify wakes every follower; callers hold mu.
func (f *Fake) notify() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// Hello implements flowdproto.API.
func (f *Fake) Hello(context.Context) (flowdproto.Hello, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hello
	h.EnvRevision = f.env.Revision
	h.Sessions = nil
	for _, id := range f.sessionIDs() {
		h.Sessions = append(h.Sessions, f.sessions[id].state)
	}
	h.Terminals = nil
	for _, t := range f.terminals {
		h.Terminals = append(h.Terminals, t)
	}
	slices.SortFunc(h.Terminals, func(a, b flowdproto.TerminalState) int { return cmpString(a.ID, b.ID) })
	return h, nil
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (f *Fake) sessionIDs() []string {
	ids := make([]string, 0, len(f.sessions))
	for id := range f.sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// SetClaudeSignedIn sets what Hello reports.
func (f *Fake) SetClaudeSignedIn(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hello.ClaudeSignedIn = v
}

// PutEnv implements flowdproto.API; an older revision is ignored.
func (f *Fake) PutEnv(_ context.Context, e flowdproto.Env) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.Revision > f.env.Revision {
		f.env = e
	}
	return nil
}

// Env returns the last applied Env.
func (f *Fake) Env() flowdproto.Env {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.env
}

// EnsureRepo implements flowdproto.API.
func (f *Fake) EnsureRepo(_ context.Context, r flowdproto.RepoSpec) (flowdproto.RepoState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := flowdproto.RepoState{Repo: r.Repo, Path: "/home/flow/work/" + repoName(r.Repo), HeadSHA: "0000000"}
	f.repos[r.Repo] = st
	return st, nil
}

func repoName(full string) string {
	if i := bytes.LastIndexByte([]byte(full), '/'); i >= 0 {
		return full[i+1:]
	}
	return full
}

// EnsureSession implements flowdproto.API: a new host logs session.started and its worktrees; a known one that is
// stopped logs session.resumed.
func (f *Fake) EnsureSession(_ context.Context, s flowdproto.SessionSpec) (flowdproto.SessionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.sessions[s.ID]
	if !ok {
		dir := "/home/flow/work/.sessions/" + s.Name
		h = &host{spec: s, cmds: map[string]bool{}, state: flowdproto.SessionState{ID: s.ID, Dir: dir,
			Status: event.SessionStatusIdle}}
		f.sessions[s.ID] = h
		f.append(h, event.KindSessionStarted, "", event.SessionStarted{Provider: s.Provider, ClaudeAuth: s.ClaudeAuth,
			Model: s.Model, Mode: s.Mode, ModeCap: s.ModeCap, ProviderSessionID: s.ID, Cwd: dir})
		for _, w := range s.Worktrees {
			f.addWorktree(h, w)
		}
	} else if h.state.Unit != flowdproto.UnitActive {
		f.append(h, event.KindSessionResumed, "", event.SessionResumed{Reason: event.ResumeReasonMessage,
			ProviderSessionID: s.ID})
	}
	h.state.Unit, h.state.HostVersion, h.state.HostProtocol = flowdproto.UnitActive, "fake", flowdproto.Protocol
	f.notify()
	return h.state, nil
}

func (f *Fake) addWorktree(h *host, w flowdproto.WorktreeSpec) flowdproto.WorktreeState {
	ws := flowdproto.WorktreeState{Repo: w.Repo, Branch: w.Branch, Path: h.state.Dir + "/" + repoName(w.Repo),
		BaseRef: w.BaseRef, BaseSHA: "0000000"}
	h.state.Worktrees = append(h.state.Worktrees, ws)
	f.append(h, event.KindWorktreeAdded, "", event.WorktreeAdded{Repo: ws.Repo, Branch: ws.Branch, Path: ws.Path,
		BaseRef: ws.BaseRef, BaseSHA: ws.BaseSHA})
	return ws
}

// Session implements flowdproto.API.
func (f *Fake) Session(_ context.Context, id string) (flowdproto.SessionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.sessions[id]
	if !ok {
		return flowdproto.SessionState{}, fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, id)
	}
	return h.state, nil
}

// StopSession implements flowdproto.API: a running turn ends lost, as when the unit dies.
func (f *Fake) StopSession(_ context.Context, id string, remove bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.sessions[id]
	if !ok {
		return fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, id)
	}
	if h.turn != "" {
		f.endTurn(h, event.TurnOutcomeLost, "")
	}
	h.state.Unit = flowdproto.UnitInactive
	if remove {
		h.state.Worktrees = nil
	}
	f.notify()
	return nil
}

// AddWorktree implements flowdproto.API.
func (f *Fake) AddWorktree(_ context.Context, id string, w flowdproto.WorktreeSpec) (flowdproto.WorktreeState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.sessions[id]
	if !ok {
		return flowdproto.WorktreeState{}, fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, id)
	}
	ws := f.addWorktree(h, w)
	f.notify()
	return ws, nil
}

// SetDiff sets what Diff answers for a session's repository.
func (f *Fake) SetDiff(sessionID, repo string, d apitypes.Diff) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffs[sessionID+"\x00"+repo] = d
}

// Diff implements flowdproto.API.
func (f *Fake) Diff(_ context.Context, sessionID, repo string) (apitypes.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.diffs[sessionID+"\x00"+repo]
	if !ok {
		return apitypes.Diff{}, fmt.Errorf("%w: diff of %s in %s", flowdproto.ErrNotFound, repo, sessionID)
	}
	return d, nil
}

// append numbers and logs one durable event; callers hold mu.
func (f *Fake) append(h *host, kind event.EventKind, cmd string, payload any) {
	e, err := event.New(h.spec.ID, kind, f.Now(), payload)
	if err != nil {
		panic(err) // payloads are contract types
	}
	e.Turn, e.Cmd = h.turn, cmd
	f.appendEvent(h, e)
}

func (f *Fake) appendEvent(h *host, e event.Event) {
	e.Seq = int64(len(h.log) + 1)
	h.log = append(h.log, e)
	h.state.LastSeq = e.Seq
}

func (f *Fake) setStatus(h *host, s event.SessionStatus) {
	if h.state.Status != s {
		h.state.Status = s
		f.append(h, event.KindSessionStatus, "", event.SessionStatusChanged{Status: s})
	}
}

// Command implements flowdproto.API with the host's rules.
func (f *Fake) Command(_ context.Context, c event.Command) (flowdproto.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.sessions[c.Session]
	if !ok {
		return flowdproto.CommandResult{}, fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, c.Session)
	}
	if h.cmds[c.ID] {
		return flowdproto.CommandResult{ID: c.ID, Duplicate: true, Queued: queued(h, c)}, nil
	}
	p, err := c.Payload()
	if err != nil {
		return flowdproto.CommandResult{}, err
	}
	h.cmds[c.ID] = true
	f.apply(h, c.ID, p)
	f.notify()
	return flowdproto.CommandResult{ID: c.ID, Queued: queued(h, c)}, nil
}

// queued reports, as the real host does, whether a message.send waits in the queue.
func queued(h *host, c event.Command) bool {
	var m event.MessageSend
	if c.Kind != event.CmdMessageSend || json.Unmarshal(c.Data, &m) != nil {
		return false
	}
	return slices.ContainsFunc(h.queue, func(q event.MessageSend) bool { return q.MessageID == m.MessageID })
}

func (f *Fake) apply(h *host, cmd string, p any) {
	switch p := p.(type) {
	case *event.MessageSend:
		if h.turn != "" && p.WhenBusy != event.WhenBusyInterrupt {
			h.queue = append(h.queue, *p)
			f.append(h, event.KindMessageQueued, cmd, event.MessageQueued{MessageID: p.MessageID, Text: p.Text,
				Origin: p.Origin, FromSession: p.FromSession})
			return
		}
		if h.turn != "" {
			f.endTurn(h, event.TurnOutcomeInterrupted, "")
		}
		f.startTurn(h, cmd, *p)
	case *event.MessageSendNow:
		i := slices.IndexFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == p.MessageID })
		if i < 0 {
			return
		}
		m := h.queue[i]
		h.queue = slices.Delete(h.queue, i, i+1)
		if h.turn != "" {
			f.endTurn(h, event.TurnOutcomeInterrupted, "")
		}
		f.append(h, event.KindMessageDequeued, cmd, event.MessageDequeued{MessageID: m.MessageID,
			Reason: event.DequeueReasonSent})
		f.startTurn(h, cmd, m)
	case *event.MessageRemove:
		i := slices.IndexFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == p.MessageID })
		if i >= 0 {
			h.queue = slices.Delete(h.queue, i, i+1)
			f.append(h, event.KindMessageDequeued, cmd, event.MessageDequeued{MessageID: p.MessageID,
				Reason: event.DequeueReasonRemoved})
		}
	case *event.TurnInterrupt:
		if h.turn != "" {
			f.endTurn(h, event.TurnOutcomeInterrupted, cmd)
		}
	case *event.PermissionReply:
		if h.waitFor == p.RequestID {
			f.append(h, event.KindPermissionResolved, cmd, event.PermissionResolved{RequestID: p.RequestID,
				Decision: event.PermissionDecision(p.Decision), Message: p.Message})
			f.resume(h)
		}
	case *event.QuestionReply:
		if h.waitFor == p.RequestID {
			f.append(h, event.KindQuestionResolved, cmd, event.QuestionResolved{RequestID: p.RequestID,
				Outcome: event.QuestionOutcomeAnswered, Answers: p.Answers, Text: p.Text})
			f.resume(h)
		}
	case *event.PlanReply:
		if h.waitFor == p.RequestID {
			f.append(h, event.KindPlanResolved, cmd, event.PlanResolved{RequestID: p.RequestID,
				Decision: event.PlanDecision(p.Decision), Feedback: p.Feedback, Mode: p.Mode})
			f.resume(h)
		}
	case *event.SettingsSet:
		f.append(h, event.KindSessionSettings, cmd, event.SessionSettings{Model: p.Model, Mode: p.Mode,
			ModeCap: p.ModeCap})
		if p.Model != "" {
			h.spec.Model = p.Model // the next turn's, as the host stamps it
		}
	case *event.GateUpdate:
		switch p.State {
		case event.GateStateQueued:
			f.append(h, event.KindGateQueued, cmd, event.GateQueued{Position: p.Position, Repo: p.Repo,
				AheadRepo: p.AheadRepo})
			f.setStatus(h, event.SessionStatusQueued)
		case event.GateStateStarted:
			f.append(h, event.KindGateStarted, cmd, event.GateStarted{Repo: p.Repo})
			f.setStatus(h, event.SessionStatusWorking)
		default:
			f.append(h, event.KindGateFinished, cmd, event.GateFinished{Repo: p.Repo, ExitCode: p.ExitCode,
				DurationMs: p.DurationMs})
		}
	case *event.ChildStart:
		f.append(h, event.KindChildStarted, cmd, event.ChildStarted(*p))
	case *event.ChildReport:
		f.append(h, event.KindChildReported, cmd, event.ChildReported(*p))
	case *event.ReportSent:
		f.append(h, event.KindSessionReported, cmd, event.SessionReported(*p))
	case *event.WorktreeAdd:
		f.append(h, event.KindWorktreeAdded, cmd, event.WorktreeAdded(*p))
	case *event.PullRequestsSet:
		f.append(h, event.KindPullRequestsUpdated, cmd, event.PullRequestsUpdated(*p))
	case *event.HostShutdown:
		if h.turn != "" && !p.WhenIdle {
			f.endTurn(h, event.TurnOutcomeInterrupted, cmd)
		}
	}
}

func (f *Fake) startTurn(h *host, cmd string, m event.MessageSend) {
	h.turns++
	h.turn = "t-" + strconv.Itoa(h.turns)
	h.started = f.Now()
	f.append(h, event.KindMessageUser, cmd, event.UserMessage{MessageID: m.MessageID, Text: m.Text,
		Origin: m.Origin, FromSession: m.FromSession})
	f.append(h, event.KindTurnStarted, cmd, event.TurnStarted{MessageID: m.MessageID, Provider: h.spec.Provider,
		Model: h.spec.Model})
	f.setStatus(h, event.SessionStatusWorking)
	h.pending = f.Script(m.Text)
	f.resume(h)
}

// resume plays the turn's drafts until one waits for the owner or the script ends.
func (f *Fake) resume(h *host) {
	h.waitFor, h.waitKind = "", ""
	if h.state.Status != event.SessionStatusWorking {
		f.setStatus(h, event.SessionStatusWorking)
	}
	for len(h.pending) > 0 {
		d := h.pending[0]
		h.pending = h.pending[1:]
		e := event.Event{V: event.Version, TS: f.Now().UTC(), Session: h.spec.ID, Kind: d.Kind, Turn: h.turn,
			ParentTool: d.ParentTool, Cmd: d.Cmd, Data: d.Data}
		if !d.Kind.Durable() {
			continue // the fake has no live channel
		}
		f.appendEvent(h, e)
		var req struct {
			RequestID string `json:"requestId"`
		}
		switch d.Kind {
		case event.KindPermissionRequested, event.KindQuestionAsked, event.KindPlanProposed:
			_ = json.Unmarshal(d.Data, &req)
			h.waitFor, h.waitKind = req.RequestID, d.Kind
			f.setStatus(h, map[event.EventKind]event.SessionStatus{
				event.KindPermissionRequested: event.SessionStatusApproval,
				event.KindQuestionAsked:       event.SessionStatusQuestion,
				event.KindPlanProposed:        event.SessionStatusPlan,
			}[d.Kind])
			return
		}
	}
	f.endTurn(h, event.TurnOutcomeCompleted, "")
}

func (f *Fake) endTurn(h *host, outcome event.TurnOutcome, cmd string) {
	// A request that dies with the turn is resolved as expired, so no card stays open forever.
	switch h.waitKind {
	case event.KindPermissionRequested:
		f.append(h, event.KindPermissionResolved, cmd, event.PermissionResolved{RequestID: h.waitFor,
			Decision: event.PermissionDecisionExpired})
	case event.KindQuestionAsked:
		f.append(h, event.KindQuestionResolved, cmd, event.QuestionResolved{RequestID: h.waitFor,
			Outcome: event.QuestionOutcomeExpired})
	case event.KindPlanProposed:
		f.append(h, event.KindPlanResolved, cmd, event.PlanResolved{RequestID: h.waitFor,
			Decision: event.PlanDecisionExpired})
	}
	h.waitFor, h.waitKind = "", ""
	h.pending = nil
	f.append(h, event.KindTurnEnded, cmd, event.TurnEnded{Outcome: outcome,
		DurationMs: f.Now().Sub(h.started).Milliseconds()})
	h.turn = ""
	status := event.SessionStatusDone
	if outcome == event.TurnOutcomeError || outcome == event.TurnOutcomeLost {
		status = event.SessionStatusFailed
	}
	f.setStatus(h, status)
	if len(h.queue) > 0 && outcome != event.TurnOutcomeLost {
		m := h.queue[0]
		h.queue = h.queue[1:]
		f.append(h, event.KindMessageDequeued, "", event.MessageDequeued{MessageID: m.MessageID,
			Reason: event.DequeueReasonSent})
		f.startTurn(h, "", m)
	}
}

// Events implements flowdproto.API: the log after after, then every new event until ctx ends.
func (f *Fake) Events(ctx context.Context, sessionID string, after int64) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for {
			f.mu.Lock()
			h, ok := f.sessions[sessionID]
			if !ok {
				f.mu.Unlock()
				yield(event.Event{}, fmt.Errorf("%w: session %s", flowdproto.ErrNotFound, sessionID))
				return
			}
			batch := slices.Clone(h.log[min(int(after), len(h.log)):])
			wait := f.changed
			f.mu.Unlock()
			for _, e := range batch {
				if !yield(e, nil) {
					return
				}
				after = e.Seq
			}
			select {
			case <-ctx.Done():
				return
			case <-wait:
			}
		}
	}
}

// Request queues a control request as flowd would on an MCP tool call or a gate shim; it returns the reply once
// Flow sends one, or ctx's error.
func (f *Fake) Request(ctx context.Context, r flowdproto.ControlRequest) (flowdproto.ControlReply, error) {
	f.mu.Lock()
	if r.Created.IsZero() {
		r.Created = f.Now()
	}
	f.control = append(f.control, r)
	f.notify()
	f.mu.Unlock()
	for {
		f.mu.Lock()
		rep, ok := f.replies[r.ID]
		wait := f.changed
		f.mu.Unlock()
		if ok {
			return rep, nil
		}
		select {
		case <-ctx.Done():
			return flowdproto.ControlReply{}, ctx.Err()
		case <-wait:
		}
	}
}

// Control implements flowdproto.API: unanswered requests first, then new ones.
func (f *Fake) Control(ctx context.Context) iter.Seq2[flowdproto.ControlRequest, error] {
	return func(yield func(flowdproto.ControlRequest, error) bool) {
		sent := map[string]bool{}
		for ctx.Err() == nil {
			f.mu.Lock()
			var batch []flowdproto.ControlRequest
			for _, r := range f.control {
				if _, done := f.replies[r.ID]; !done && !sent[r.ID] {
					batch = append(batch, r)
				}
			}
			wait := f.changed
			f.mu.Unlock()
			for _, r := range batch {
				sent[r.ID] = true
				if !yield(r, nil) {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-wait:
			}
		}
	}
}

// ReplyControl implements flowdproto.API; a second reply to the same id is ignored.
func (f *Fake) ReplyControl(_ context.Context, r flowdproto.ControlReply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.replies[r.ID]; !ok {
		f.replies[r.ID] = r
		f.notify()
	}
	return nil
}

// StartJob implements flowdproto.API: the job finishes at once with JobScript's output and exit code.
func (f *Fake) StartJob(_ context.Context, j flowdproto.JobSpec) (flowdproto.JobState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.jobs[j.ID]; ok {
		return st.state, nil
	}
	lines, code := f.JobScript(j)
	st := flowdproto.JobState{ID: j.ID, Kind: j.Kind, Unit: flowdproto.UnitInactive, Done: true, ExitCode: code,
		Lines: len(lines)}
	if j.Kind == flowdproto.JobConfigSync && code == 0 {
		st.Commit, st.Files = "a3f9c21", 1036
	}
	f.jobs[j.ID] = &job{state: st, lines: lines}
	return st, nil
}

// Job implements flowdproto.API.
func (f *Fake) Job(_ context.Context, id string) (flowdproto.JobState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return flowdproto.JobState{}, fmt.Errorf("%w: job %s", flowdproto.ErrNotFound, id)
	}
	return j.state, nil
}

// JobLog implements flowdproto.API.
func (f *Fake) JobLog(_ context.Context, id string, after int) ([]flowdproto.LogLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, fmt.Errorf("%w: job %s", flowdproto.ErrNotFound, id)
	}
	var out []flowdproto.LogLine
	for i := max(after, 0); i < len(j.lines); i++ {
		out = append(out, flowdproto.LogLine{N: i + 1, Text: j.lines[i]})
	}
	return out, nil
}

// EnsureTerminal implements flowdproto.API.
func (f *Fake) EnsureTerminal(_ context.Context, t flowdproto.TerminalSpec) (flowdproto.TerminalState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := flowdproto.TerminalState{ID: t.ID, Session: t.Session, Unit: flowdproto.UnitActive, Dir: t.Dir}
	f.terminals[t.ID] = st
	return st, nil
}

// AttachTerminal implements flowdproto.API with an echoing terminal.
func (f *Fake) AttachTerminal(_ context.Context, id string) (flowdproto.TerminalConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.terminals[id]; !ok {
		return nil, fmt.Errorf("%w: terminal %s", flowdproto.ErrNotFound, id)
	}
	r, w := io.Pipe()
	return &echo{r: r, w: w}, nil
}

// CloseTerminal implements flowdproto.API.
func (f *Fake) CloseTerminal(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.terminals, id)
	return nil
}

// Port implements flowdproto.API with a page that names the port.
func (f *Fake) Port(port int) http.Handler {
	page := fmt.Sprintf("preview of port %d", port)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
}

// echo is a terminal that writes back what it reads.
type echo struct {
	r          *io.PipeReader
	w          *io.PipeWriter
	cols, rows int
}

func (e *echo) Read(p []byte) (int, error)  { return e.r.Read(p) }
func (e *echo) Write(p []byte) (int, error) { return e.w.Write(p) }
func (e *echo) Close() error                { return e.w.Close() }
func (e *echo) Resize(cols, rows int) error { e.cols, e.rows = cols, rows; return nil }
