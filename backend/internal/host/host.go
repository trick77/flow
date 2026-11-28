// Package host is the session host (flowd host, one transient systemd unit per session): the log's only writer,
// the command socket (hostproto), the message queue, status derivation, turn bookkeeping (expiring open requests),
// and the agent adapter it drives. The adapter boundary has the sidecar's shape: commands of the kinds marked
// sidecar go in, event drafts come out; the Node sidecar (Claude) and the OpenCode bridge implement it, and so does
// the replay adapter that plays a recorded stream. Owner: flowd Claude adapter host area (docs/ownership.md).
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/eventlog"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/hostproto"
)

// Spec is what flowd starts a host with, written to layout.SessionSpecFile.
type Spec struct {
	Session flowdproto.SessionSpec `json:"session"`
	Dir     string                 `json:"dir"`
	// Cwd is the agent's working directory, chosen once by flowd before the session's first start and never
	// changed after (Claude Code keys its transcripts by it, so a resume must find the same one): the worktree of a
	// session made with one repository, else Dir. Empty in specs from before the field: Dir.
	Cwd       string                     `json:"cwd,omitempty"`
	Worktrees []flowdproto.WorktreeState `json:"worktrees"`
}

// AgentDir is the agent's working directory: Cwd, or Dir for a spec without one.
func (s Spec) AgentDir() string {
	if s.Cwd != "" {
		return s.Cwd
	}
	return s.Dir
}

// Adapter drives one agent. Run emits drafts until ctx ends; Send hands it a command of a kind marked sidecar and
// must not block. It writes turn.ended for every message.send it was given, also after turn.interrupt.
//
// Ids an adapter puts into drafts (toolId, requestId, blockId) must be unique within the session's whole log, across
// host restarts and adapter process lifetimes: provider ids (toolu_…, per_…) are; anything an adapter numbers itself
// must not restart from zero with its process. Flow derives reply command ids from request ids, so a reused request
// id turns the new request's reply into a duplicate that is acknowledged and never applied.
type Adapter interface {
	Run(ctx context.Context, out chan<- event.EventDraft) error
	Send(c event.Command) error
}

// Config wires a host.
type Config struct {
	Spec    Spec
	LogPath string
	Adapter Adapter
	Version string
	// ResumeReason goes into session.resumed when the log already has a session.started; sandbox_start and command
	// leave the queue as it is (Pump).
	ResumeReason event.ResumeReason
	Now          func() time.Time
	Log          *slog.Logger
}

// Host is one session's host.
type Host struct {
	cfg  Config
	w    *eventlog.Writer
	mu   sync.Mutex
	seen map[string]bool

	status event.SessionStatus
	// model is what the session is set to: the spec's, then the log's last settings change; each turn.started
	// carries it, so metering attributes a turn to the model it ran on.
	model string
	// effort likewise, default (the agent's own) included: each turn.started carries it explicitly, so a turn on the
	// agent's own effort is never read as the last turn's level.
	effort event.Effort
	// mode and modeCap are the session's now (spec, then session.settings and approved plans): what a restarted
	// agent is started with.
	mode, modeCap event.PermissionMode
	// costUSD sums the turns' reported costs; provider is set once the log shows the provider ran a turn (see
	// providerKinds). Both go into a restarted agent's start: resume and the cost its totals continue from.
	costUSD      float64
	provider     bool
	worktrees    []event.WorktreeAdded
	started      bool
	turn         string
	turns        int
	turnStart    time.Time
	interruptCmd string
	pendingID    string
	pendingKind  event.EventKind
	// pendingQuestions are the open question request's questions: a reply naming others is refused.
	pendingQuestions []event.Question
	// pendingPerm is the open permission request (nil while none is open).
	pendingPerm *event.PermissionRequested
	// allowed: the requests (rememberKey) the owner allowed for this session although the provider offered no rule
	// to remember; the host answers them itself. Rebuilt from the log on start, gone with the session.
	allowed      map[string]bool
	queue        []event.MessageSend
	blocks       map[string]string
	blockOrder   []string
	subs         map[chan hostproto.Frame]struct{}
	shutdownIdle bool
	// gateFailed: the open turn's last check run (gate.finished) had a non-zero exit, so the turn ends failed.
	gateFailed bool
	stop       chan struct{}
	stopped    bool
	// replying counts command replies not yet written: a stop waits for them, so a host.shutdown's own reply is
	// not cut off by the connection closing.
	replying atomic.Int64
}

// beginReply marks a command reply as in flight; call the result once it is written.
func (h *Host) beginReply() func() {
	h.replying.Add(1)
	return func() { h.replying.Add(-1) }
}

// waitReplies waits (at most a few seconds: a client that stopped reading must not hold the stop) until every
// command reply in flight is written.
func (h *Host) waitReplies() {
	for deadline := time.Now().Add(3 * time.Second); h.replying.Load() > 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
}

// New opens the log, rebuilds the state from it, closes a turn that died with the previous host and announces the
// session (session.started on first start, session.resumed after).
func New(cfg Config) (*Host, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.ResumeReason == "" {
		cfg.ResumeReason = event.ResumeReasonMessage
	}
	w, events, err := eventlog.Open(cfg.LogPath)
	if err != nil {
		return nil, err
	}
	h := &Host{cfg: cfg, w: w, seen: map[string]bool{}, allowed: map[string]bool{}, status: event.SessionStatusIdle,
		blocks: map[string]string{}, subs: map[chan hostproto.Frame]struct{}{}, stop: make(chan struct{}),
		model:  cfg.Spec.Session.Model,
		effort: cfg.Spec.Session.Effort, mode: cfg.Spec.Session.Mode, modeCap: cfg.Spec.Session.ModeCap}
	if h.effort == "" {
		h.effort = event.EffortDefault // a spec from a Flow before efforts
	}
	h.recover(events)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turn != "" {
		h.endTurn(event.TurnEnded{Outcome: event.TurnOutcomeLost}, "")
	}
	s := cfg.Spec
	if !h.started {
		h.started = true
		h.append(event.KindSessionStarted, "", event.SessionStarted{Provider: s.Session.Provider,
			ClaudeAuth: s.Session.ClaudeAuth, Model: s.Session.Model, Effort: h.effort, Mode: s.Session.Mode,
			ModeCap:           s.Session.ModeCap,
			ProviderSessionID: s.Session.ID, Cwd: s.AgentDir()})
		for _, wt := range s.Worktrees {
			h.append(event.KindWorktreeAdded, "", event.WorktreeAdded{Repo: wt.Repo, Branch: wt.Branch, Path: wt.Path,
				BaseRef: wt.BaseRef, BaseSHA: wt.BaseSHA})
		}
	} else {
		h.append(event.KindSessionResumed, "", event.SessionResumed{Reason: cfg.ResumeReason,
			ProviderSessionID: s.Session.ID})
	}
	return h, nil
}

// recover replays the log into the host's state: the seen command ids, status, the open turn and request, the queue.
func (h *Host) recover(events []event.Event) {
	for _, e := range events {
		if e.Cmd != "" {
			h.seen[e.Cmd] = true
		}
		p, err := e.Payload()
		if err != nil {
			continue
		}
		h.observe(e.Kind, e.Turn, p)
		switch p := p.(type) {
		case *event.SessionStarted:
			h.started = true
		case *event.SessionStatusChanged:
			h.status = p.Status
		case *event.TurnStarted:
			h.turns++
			h.turn, h.turnStart, h.gateFailed = e.Turn, e.TS, false
		case *event.TurnEnded:
			h.turn, h.gateFailed = "", false
			h.pendingID, h.pendingKind, h.pendingPerm = "", "", nil
		case *event.GateFinished:
			h.gateFailed = p.ExitCode != 0
		case *event.PermissionRequested:
			h.pendingID, h.pendingKind, h.pendingPerm = p.RequestID, e.Kind, p
		case *event.QuestionAsked:
			h.pendingID, h.pendingKind, h.pendingQuestions, h.pendingPerm = p.RequestID, e.Kind, p.Questions, nil
		case *event.PlanProposed:
			h.pendingID, h.pendingKind, h.pendingPerm = p.RequestID, e.Kind, nil
		case *event.PermissionResolved:
			// The owner's own allow for this session on a request without a provider rule (not the host's answers
			// from it): remembered again.
			if r := h.pendingPerm; r != nil && r.RequestID == p.RequestID && !r.CanAlwaysAllow && !p.Remembered &&
				p.Decision == event.PermissionDecisionAllowAlways {
				h.remember(r)
			}
			h.pendingID, h.pendingKind, h.pendingPerm = "", "", nil
		case *event.QuestionResolved, *event.PlanResolved:
			h.pendingID, h.pendingKind, h.pendingPerm = "", "", nil
		case *event.MessageQueued:
			// A message queued again (sent now) moves: its id is in the queue once, where the last entry put it.
			h.queue = slices.DeleteFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == p.MessageID })
			m := event.MessageSend{MessageID: p.MessageID, Text: p.Text, Origin: p.Origin, FromSession: p.FromSession}
			if p.Front {
				h.queue = slices.Insert(h.queue, 0, m)
			} else {
				h.queue = append(h.queue, m)
			}
		case *event.MessageDequeued:
			h.queue = slices.DeleteFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == p.MessageID })
		}
	}
}

// Close syncs and closes the log and ends every subscription.
func (h *Host) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		close(c)
		delete(h.subs, c)
	}
	return h.w.Close()
}

// Stopped is closed when a host.shutdown asks the host to end.
func (h *Host) Stopped() <-chan struct{} { return h.stop }

func (h *Host) requestStop() {
	if !h.stopped {
		h.stopped = true
		close(h.stop)
	}
}

// Hello describes the host now.
func (h *Host) Hello(pid int) hostproto.Hello {
	h.mu.Lock()
	defer h.mu.Unlock()
	return hostproto.Hello{Protocol: hostproto.Protocol, Version: h.cfg.Version, Session: h.cfg.Spec.Session.ID,
		PID: pid, LastSeq: h.w.LastSeq(), Status: h.status, Busy: h.turn != ""}
}

// append numbers and logs one durable event and announces it; callers hold mu. A failure is logged: callers whose
// later steps depend on the event use tryAppend.
func (h *Host) append(kind event.EventKind, cmd string, payload any) {
	if err := h.tryAppend(kind, cmd, h.turn, payload); err != nil {
		h.cfg.Log.Error("append", "kind", kind, "err", err)
	}
}

// tryAppend is append with the turn given and the failure returned (an event over event.MaxLine, a full disk).
func (h *Host) tryAppend(kind event.EventKind, cmd, turn string, payload any) error {
	e, err := event.New(h.cfg.Spec.Session.ID, kind, h.cfg.Now(), payload)
	if err != nil {
		return err
	}
	e.Turn, e.Cmd = turn, cmd
	return h.tryWrite(e)
}

func (h *Host) write(e event.Event) {
	if err := h.tryWrite(e); err != nil {
		h.cfg.Log.Error("append", "kind", e.Kind, "err", err)
	}
}

func (h *Host) tryWrite(e event.Event) error {
	e, err := h.w.Append(e)
	if err != nil {
		return err
	}
	if p, err := e.Payload(); err == nil {
		h.observe(e.Kind, e.Turn, p)
	}
	h.broadcast(hostproto.Frame{Appended: e.Seq})
	return nil
}

// NoticeProviderSession is the host's marker that the provider has the session: logged once, inside the turn in
// which the adapter first reported the provider's init (its session.started draft). It is the only evidence for a
// resume, and only turns ended after it count toward the prior cost: the host's own session.started proves nothing
// (written before any agent ran), and the replay adapter, also in logs from before the marker existed, never
// reports an init.
const NoticeProviderSession = "provider_session"

// observe keeps what a restarted agent is started with up to date with every logged event (on recovery and as
// written): model, mode, cap, the summed cost, whether the provider has a session, and the worktrees.
func (h *Host) observe(_ event.EventKind, turn string, p any) {
	switch p := p.(type) {
	case *event.Notice:
		if p.Code == NoticeProviderSession && turn != "" {
			h.provider = true
		}
	case *event.SessionSettings:
		if p.Model != "" {
			h.model = p.Model
		}
		if p.Effort != "" {
			h.effort = p.Effort
		}
		if p.Mode != "" {
			h.mode = p.Mode
		}
		if p.ModeCap != "" {
			h.modeCap = p.ModeCap
		}
	case *event.PlanResolved:
		if p.Decision == event.PlanDecisionApproved && p.Mode != "" {
			h.mode = p.Mode
		}
	case *event.TurnEnded:
		if p.CostUSD != nil && h.provider {
			h.costUSD += *p.CostUSD
		}
	case *event.WorktreeAdded:
		// The log, not the spec: a worktree added while the host runs is only in the log until the next start.
		if i := slices.IndexFunc(h.worktrees, func(w event.WorktreeAdded) bool { return w.Repo == p.Repo }); i >= 0 {
			h.worktrees[i] = *p
		} else {
			h.worktrees = append(h.worktrees, *p)
		}
	}
}

// modeRank orders the modes from least to most permissive (the contract's order).
func modeRank(m event.PermissionMode) int { return slices.Index(event.PermissionModeValues, m) }

// capped is mode held to cap: an agent never starts or runs above its session's cap (the SDK refuses
// bypassPermissions without the start flag the cap decides).
func capped(mode, modeCap event.PermissionMode) event.PermissionMode {
	if modeCap != "" && modeRank(modeCap) >= 0 && modeRank(mode) > modeRank(modeCap) {
		return modeCap
	}
	return mode
}

// AgentState is what the host knows about the session's agent, for (re)starting it.
type AgentState struct {
	// Resume: the provider has the session already (NoticeProviderSession in the log).
	Resume bool
	// PriorCostUSD sums the costs the provider reported for its turns; turns without one add nothing, so their
	// spend lands in the next reported turn, as within one agent process.
	PriorCostUSD float64
	Model        string
	Effort       event.Effort
	// Mode is held to ModeCap.
	Mode    event.PermissionMode
	ModeCap event.PermissionMode
	// Worktrees are the session's worktrees as logged (worktree.added), in the order they were added.
	Worktrees []event.WorktreeAdded
}

// AgentState reports the session's agent state now.
func (h *Host) AgentState() AgentState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return AgentState{Resume: h.provider, PriorCostUSD: math.Round(h.costUSD*1e9) / 1e9, Model: h.model,
		Effort: h.effort, Mode: capped(h.mode, h.modeCap), ModeCap: h.modeCap, Worktrees: slices.Clone(h.worktrees)}
}

func (h *Host) broadcast(f hostproto.Frame) {
	for c := range h.subs {
		select {
		case c <- f:
		default: // a subscriber that far behind reconnects and reads the log
			close(c)
			delete(h.subs, c)
		}
	}
}

func (h *Host) setStatus(s event.SessionStatus) {
	if h.status != s {
		h.status = s
		h.append(event.KindSessionStatus, "", event.SessionStatusChanged{Status: s})
	}
}

// Subscribe opens a live subscription: the first frame is a live.snapshot of the open blocks; cancel ends it.
func (h *Host) Subscribe() (<-chan hostproto.Frame, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := make(chan hostproto.Frame, 512)
	snap := event.LiveSnapshot{Blocks: []event.OpenBlock{}}
	for _, id := range h.blockOrder {
		snap.Blocks = append(snap.Blocks, event.OpenBlock{BlockID: id, Text: h.blocks[id]})
	}
	if e, err := event.New(h.cfg.Spec.Session.ID, event.KindLiveSnapshot, h.cfg.Now(), snap); err == nil {
		e.Turn = h.turn
		c <- hostproto.Frame{Live: &e}
	}
	h.subs[c] = struct{}{}
	return c, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[c]; ok {
			close(c)
			delete(h.subs, c)
		}
	}
}

// CommandError is a refused command; Code is a hostproto Code* value.
type CommandError struct {
	Code   string
	Detail string
}

func (e *CommandError) Error() string { return e.Code + ": " + e.Detail }

// Command applies one command; duplicate is true when its id was seen before (nothing is applied again).
func (h *Host) Command(c event.Command) (duplicate bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.ID == "" || c.Session != h.cfg.Spec.Session.ID {
		return false, &CommandError{Code: hostproto.CodeInvalid, Detail: "command id or session"}
	}
	if h.seen[c.ID] {
		return true, nil
	}
	p, err := c.Payload()
	if err != nil {
		return false, &CommandError{Code: hostproto.CodeUnknown, Detail: err.Error()}
	}
	if h.stopped {
		return false, &CommandError{Code: hostproto.CodeShutdown, Detail: "host is stopping"}
	}
	if err := h.apply(c, p); err != nil {
		return false, err
	}
	h.seen[c.ID] = true
	return false, nil
}

// Notice logs a notice flowd raised for the session, inside the running turn if one runs (the turn whose command
// met it); false while the host stops.
func (h *Host) Notice(n event.Notice) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return false
	}
	h.append(event.KindNotice, "", n)
	return true
}

// Queued reports whether the message with this id waits in the queue: what a message.send's reply tells the sender
// (also for a duplicate, as long as it waits).
func (h *Host) Queued(messageID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == messageID })
}

func (h *Host) apply(c event.Command, p any) error {
	cmd := c.ID
	switch p := p.(type) {
	case *event.MessageSend:
		switch {
		case h.turn == "" && len(h.queue) > 0:
			// Idle with messages still queued (a host started held): the new one goes behind them, or ahead with
			// interrupt as on a busy host, and the queue drains in that order.
			if err := h.enqueue(cmd, *p, p.WhenBusy == event.WhenBusyInterrupt); err != nil {
				return err
			}
			h.drain()
		case h.turn == "":
			return h.startTurn(cmd, *p)
		case p.WhenBusy == event.WhenBusyInterrupt:
			if err := h.enqueue(cmd, *p, true); err != nil {
				return err
			}
			h.interrupt(cmd)
		default:
			return h.enqueue(cmd, *p, false)
		}
	case *event.MessageSendNow:
		i := slices.IndexFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == p.MessageID })
		if i < 0 {
			return &CommandError{Code: hostproto.CodeNotPending, Detail: "message not queued"}
		}
		m := h.queue[i]
		// Logged again at the head, so a restarted host drains in the order the owner chose.
		if err := h.tryAppend(event.KindMessageQueued, cmd, h.turn, queued(m, true)); err != nil {
			return &CommandError{Code: hostproto.CodeInvalid, Detail: err.Error()}
		}
		h.queue = slices.Insert(slices.Delete(h.queue, i, i+1), 0, m)
		if h.turn != "" {
			h.interrupt(cmd)
		} else {
			h.drain()
		}
	case *event.MessageRemove:
		i := slices.IndexFunc(h.queue, func(m event.MessageSend) bool { return m.MessageID == p.MessageID })
		if i < 0 {
			return &CommandError{Code: hostproto.CodeNotPending, Detail: "message not queued"}
		}
		h.queue = slices.Delete(h.queue, i, i+1)
		h.append(event.KindMessageDequeued, cmd, event.MessageDequeued{MessageID: p.MessageID,
			Reason: event.DequeueReasonRemoved})
	case *event.TurnInterrupt:
		if h.turn != "" {
			h.interrupt(cmd)
		}
	case *event.PermissionReply:
		req := h.pendingPerm
		if err := h.resolve(event.KindPermissionRequested, p.RequestID); err != nil {
			return err
		}
		decision := event.PermissionDecision(p.Decision)
		if p.Decision == event.ReplyDecisionAllowAlways && req != nil && !req.CanAlwaysAllow {
			// The provider offered no rule to remember: Flow remembers this exact request for the session (logged as
			// the owner chose it), the agent hears allow once. A request with nothing to match is allowed once.
			if !h.remember(req) {
				decision = event.PermissionDecisionAllowOnce
			}
			p.Decision = event.ReplyDecisionAllowOnce
			if b, err := json.Marshal(p); err == nil {
				c.Data = b
			}
		}
		h.append(event.KindPermissionResolved, cmd, event.PermissionResolved{RequestID: p.RequestID,
			Decision: decision, Message: p.Message})
		h.setStatus(event.SessionStatusWorking)
		h.send(c)
	case *event.QuestionReply:
		if h.pendingKind == event.KindQuestionAsked && h.pendingID == p.RequestID {
			if bad := unknownAnswer(h.pendingQuestions, p.Answers); bad != "" {
				// The agent would hear "no answer" for it while the log said answered.
				return &CommandError{Code: hostproto.CodeInvalid, Detail: "answer names " + bad}
			}
		}
		if err := h.resolve(event.KindQuestionAsked, p.RequestID); err != nil {
			return err
		}
		h.append(event.KindQuestionResolved, cmd, event.QuestionResolved{RequestID: p.RequestID,
			Outcome: event.QuestionOutcomeAnswered, Answers: p.Answers, Text: p.Text})
		h.setStatus(event.SessionStatusWorking)
		h.send(c)
	case *event.PlanReply:
		if err := h.resolve(event.KindPlanProposed, p.RequestID); err != nil {
			return err
		}
		if p.Mode != "" {
			p.Mode = capped(p.Mode, h.modeCap) // the plan goes on in the mode the cap allows
			if b, err := json.Marshal(p); err == nil {
				c.Data = b
			}
		}
		h.append(event.KindPlanResolved, cmd, event.PlanResolved{RequestID: p.RequestID,
			Decision: event.PlanDecision(p.Decision), Feedback: p.Feedback, Mode: p.Mode})
		h.setStatus(event.SessionStatusWorking)
		h.send(c)
	case *event.SettingsSet:
		// A cap below the mode in force pulls the mode down with it; the log and the agent get the mode in force.
		modeCap := h.modeCap
		if p.ModeCap != "" {
			modeCap = p.ModeCap
		}
		mode := h.mode
		if p.Mode != "" {
			mode = p.Mode
		}
		if m := capped(mode, modeCap); m != mode {
			p.Mode = m
		}
		// Written first: a change the log does not hold must not reach the agent either.
		if err := h.tryAppend(event.KindSessionSettings, cmd, h.turn, event.SessionSettings{Model: p.Model,
			Effort: p.Effort, Mode: p.Mode, ModeCap: p.ModeCap}); err != nil {
			return &CommandError{Code: hostproto.CodeInvalid, Detail: err.Error()}
		}
		if b, err := json.Marshal(p); err == nil {
			c.Data = b
		}
		h.send(c)
	case *event.WorktreeAdd:
		h.append(event.KindWorktreeAdded, cmd, event.WorktreeAdded(*p))
	case *event.PullRequestsSet:
		h.append(event.KindPullRequestsUpdated, cmd, event.PullRequestsUpdated(*p))
	case *event.ChildStart:
		h.append(event.KindChildStarted, cmd, event.ChildStarted(*p))
	case *event.ChildReport:
		h.append(event.KindChildReported, cmd, event.ChildReported(*p))
	case *event.ReportSent:
		h.append(event.KindSessionReported, cmd, event.SessionReported(*p))
	case *event.GateUpdate:
		h.gate(cmd, p)
	case *event.HostShutdown:
		// The adapter hears the shutdown in the host's own mode: when idle it lets the turn finish, else it stops now.
		if h.turn != "" && p.WhenIdle {
			h.shutdownIdle = true
			h.send(c)
			return nil
		}
		if h.turn != "" {
			// The agent is told to stop, and the host records the end now: the adapter's own turn.ended would
			// arrive after the host is gone, and the next start would call the turn lost (failed, with the
			// downtime in its duration).
			h.interrupt(cmd)
			h.endTurn(event.TurnEnded{Outcome: event.TurnOutcomeInterrupted}, cmd)
		}
		h.send(c)
		h.requestStop()
	default:
		return &CommandError{Code: hostproto.CodeUnknown, Detail: string(c.Kind)}
	}
	return nil
}

func (h *Host) gate(cmd string, p *event.GateUpdate) {
	switch p.State {
	case event.GateStateQueued:
		h.append(event.KindGateQueued, cmd, event.GateQueued{Position: p.Position, Repo: p.Repo, AheadRepo: p.AheadRepo})
		h.setStatus(event.SessionStatusQueued)
	case event.GateStateStarted:
		h.append(event.KindGateStarted, cmd, event.GateStarted{Repo: p.Repo})
		h.setStatus(event.SessionStatusWorking)
	default:
		h.append(event.KindGateFinished, cmd, event.GateFinished{Repo: p.Repo, ExitCode: p.ExitCode,
			DurationMs: p.DurationMs})
		// docs/events.md, Status: a turn's last check run decides done or failed with its outcome. A run that finishes
		// while the turn waits in the queue (withdrawn, never started) puts it back to working.
		h.gateFailed = p.ExitCode != 0
		switch {
		case h.turn != "" && h.status == event.SessionStatusQueued:
			h.setStatus(event.SessionStatusWorking)
		case h.turn == "" && h.gateFailed:
			h.setStatus(event.SessionStatusFailed)
		case h.turn == "" && (h.status == event.SessionStatusQueued || h.status == event.SessionStatusWorking):
			h.setStatus(event.SessionStatusDone) // a run outside a turn (a terminal's make) leaves nothing working
		}
	}
}

// unknownAnswer names the first question or option id of answers the questions do not have ("" when all are known):
// the adapter can only pass on the answers it finds there.
func unknownAnswer(questions []event.Question, answers []event.Answer) string {
	for _, a := range answers {
		i := slices.IndexFunc(questions, func(q event.Question) bool { return q.ID == a.QuestionID })
		if i < 0 {
			return "question " + a.QuestionID
		}
		for _, o := range a.OptionIds {
			if !slices.ContainsFunc(questions[i].Options, func(x event.QuestionOption) bool { return x.ID == o }) {
				return "option " + o + " of " + a.QuestionID
			}
		}
	}
	return ""
}

func (h *Host) resolve(kind event.EventKind, id string) error {
	if h.pendingKind != kind || h.pendingID != id {
		return &CommandError{Code: hostproto.CodeNotPending, Detail: id}
	}
	h.pendingID, h.pendingKind, h.pendingPerm = "", "", nil
	return nil
}

// remember adds a request to the session's allow list; false when it has nothing exact to match.
func (h *Host) remember(r *event.PermissionRequested) bool {
	key, ok := rememberKey(r)
	if ok {
		h.allowed[key] = true
	}
	return ok
}

// rememberKey is what the owner's "allow for this session" covers when the provider offered no rule: exactly this
// request, never a prefix or a pattern. A command: the tool and its input with the command string as given, only the
// agent's label (description) and its timeout left out, so another flag (background, sandbox) is another request;
// an edit or write: the tool and the exact path it names; anything else: the tool and its whole input. ok false: no
// input to match (never remembered).
func rememberKey(r *event.PermissionRequested) (string, bool) {
	if r.Tool == "" || len(r.Input) == 0 {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(r.Input))
	dec.UseNumber()
	var in map[string]any
	if err := dec.Decode(&in); err != nil || in == nil || cut(in) {
		return "", false
	}
	switch r.Kind {
	case event.PermissionKindCommand:
		if c, _ := in["command"].(string); c == "" {
			return "", false
		}
		delete(in, "description")
		delete(in, "timeout")
	case event.PermissionKindEdit, event.PermissionKindWrite:
		for _, f := range []string{"file_path", "notebook_path", "path"} {
			if p, _ := in[f].(string); p != "" {
				k, err := json.Marshal([]string{r.Tool, string(r.Kind), f, p})
				return string(k), err == nil
			}
		}
	}
	b, err := json.Marshal(in) // map keys sorted: one form per input
	if err != nil {
		return "", false
	}
	k, err := json.Marshal([]string{r.Tool, string(r.Kind), "input", string(b)})
	return string(k), err == nil
}

// cutMark is what the sidecar puts between head and tail of a string it shortened to fit a draft into a log line
// (sidecar/src/limits.ts, cutHeadTail); a request it shortened carries no other mark.
const cutMark = "\n…\n"

// cut says whether a string in v may have been shortened: two different requests would then read the same, so such
// a request is never remembered or matched (a string that only looks cut is merely asked again).
func cut(v any) bool {
	switch v := v.(type) {
	case string:
		return strings.Contains(v, cutMark)
	case []any:
		return slices.ContainsFunc(v, cut)
	case map[string]any:
		for _, x := range v {
			if cut(x) {
				return true
			}
		}
	}
	return false
}

// interrupt tells the adapter to stop the turn. The open request expires at once: the adapter answers it as the
// interrupt it is, so a reply that comes before turn.ended would reach no one, and must be refused (not pending)
// instead of being logged as accepted.
func (h *Host) interrupt(cmd string) {
	if h.interruptCmd == "" {
		h.interruptCmd = cmd
	}
	if h.pendingKind != "" {
		h.expire(cmd)
		h.setStatus(event.SessionStatusWorking)
	}
	c, err := event.NewCommand(cmd, h.cfg.Spec.Session.ID, event.CmdTurnInterrupt, event.TurnInterrupt{})
	if err == nil {
		h.send(c)
	}
}

func (h *Host) send(c event.Command) {
	if err := h.cfg.Adapter.Send(c); err != nil {
		h.cfg.Log.Error("adapter send", "kind", c.Kind, "err", err)
	}
}

// startTurn logs the user message and starts its turn. The message is written first: when it cannot be (too large,
// disk full) nothing changes and the error goes back to the sender, so no turn runs without its message.
func (h *Host) startTurn(cmd string, m event.MessageSend) error {
	turn := "t-" + strconv.Itoa(h.turns+1)
	if err := h.tryAppend(event.KindMessageUser, cmd, turn, event.UserMessage{MessageID: m.MessageID, Text: m.Text,
		Origin: m.Origin, FromSession: m.FromSession}); err != nil {
		return &CommandError{Code: hostproto.CodeInvalid, Detail: err.Error()}
	}
	h.turns++
	h.turn, h.gateFailed = turn, false
	h.turnStart = h.cfg.Now()
	h.append(event.KindTurnStarted, cmd, event.TurnStarted{MessageID: m.MessageID, Provider: h.cfg.Spec.Session.Provider,
		Model: h.model, Effort: h.effort})
	h.setStatus(event.SessionStatusWorking)
	id := cmd
	if id == "" {
		id = m.MessageID
	}
	m.WhenBusy = ""
	if c, err := event.NewCommand(id, h.cfg.Spec.Session.ID, event.CmdMessageSend, m); err == nil {
		h.send(c)
	}
	return nil
}

// enqueue logs a message as queued, then queues it (first with front); a message that cannot be logged is refused.
func (h *Host) enqueue(cmd string, m event.MessageSend, front bool) error {
	if err := h.tryAppend(event.KindMessageQueued, cmd, h.turn, queued(m, front)); err != nil {
		return &CommandError{Code: hostproto.CodeInvalid, Detail: err.Error()}
	}
	if front {
		h.queue = slices.Insert(h.queue, 0, m)
	} else {
		h.queue = append(h.queue, m)
	}
	return nil
}

func queued(m event.MessageSend, front bool) event.MessageQueued {
	return event.MessageQueued{MessageID: m.MessageID, Text: m.Text, Origin: m.Origin, FromSession: m.FromSession,
		Front: front}
}

// drain starts the next queued message when no turn runs.
func (h *Host) drain() {
	if h.turn != "" || len(h.queue) == 0 || h.stopped {
		return
	}
	m := h.queue[0]
	h.queue = h.queue[1:]
	h.append(event.KindMessageDequeued, "", event.MessageDequeued{MessageID: m.MessageID, Reason: event.DequeueReasonSent})
	if err := h.startTurn("", m); err != nil {
		// It was logged once as queued, so it fits: this is the disk. The owner sees it dequeued without a turn.
		h.cfg.Log.Error("starting a queued message", "message", m.MessageID, "err", err)
	}
}

// endTurn closes the open request and the turn; callers hold mu.
func (h *Host) endTurn(t event.TurnEnded, cmd string) {
	h.expire(cmd)
	if t.DurationMs == 0 && !h.turnStart.IsZero() {
		t.DurationMs = h.cfg.Now().Sub(h.turnStart).Milliseconds()
	}
	h.append(event.KindTurnEnded, cmd, t)
	h.turn, h.interruptCmd = "", ""
	h.blocks, h.blockOrder = map[string]string{}, nil
	status := event.SessionStatusDone
	if t.Outcome == event.TurnOutcomeError || t.Outcome == event.TurnOutcomeLost || h.gateFailed {
		status = event.SessionStatusFailed
	}
	h.gateFailed = false
	h.setStatus(status)
}

// expire closes the open request as expired; callers hold mu.
func (h *Host) expire(cmd string) {
	switch h.pendingKind {
	case event.KindPermissionRequested:
		h.append(event.KindPermissionResolved, cmd, event.PermissionResolved{RequestID: h.pendingID,
			Decision: event.PermissionDecisionExpired})
	case event.KindQuestionAsked:
		h.append(event.KindQuestionResolved, cmd, event.QuestionResolved{RequestID: h.pendingID,
			Outcome: event.QuestionOutcomeExpired})
	case event.KindPlanProposed:
		h.append(event.KindPlanResolved, cmd, event.PlanResolved{RequestID: h.pendingID,
			Decision: event.PlanDecisionExpired})
	}
	h.pendingID, h.pendingKind, h.pendingPerm = "", "", nil
}

// Draft takes one draft from the adapter.
func (h *Host) Draft(d event.EventDraft) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch d.Kind {
	case event.KindAssistantTextDelta:
		var p event.AssistantTextDelta
		if json.Unmarshal(d.Data, &p) == nil {
			if _, ok := h.blocks[p.BlockID]; !ok {
				h.blockOrder = append(h.blockOrder, p.BlockID)
			}
			h.blocks[p.BlockID] += p.Text
		}
		h.live(d)
		return
	case event.KindToolProgress:
		h.live(d)
		return
	case event.KindLiveSnapshot:
		return
	case event.KindSessionStarted:
		if h.started {
			// The host announced the session itself; the adapter's is the provider's init: the first one inside a
			// turn is the durable proof that the provider has the session (resume from now on).
			if h.turn != "" && !h.provider {
				var s event.SessionStarted
				_ = json.Unmarshal(d.Data, &s)
				n := event.Notice{Code: NoticeProviderSession, Level: event.NoticeLevelInfo}
				if s.ProviderSessionID != "" {
					n.Params = map[string]string{"providerSessionId": s.ProviderSessionID}
				}
				h.append(event.KindNotice, "", n)
			}
			return
		}
		h.started = true
	case event.KindTurnEnded:
		if h.turn == "" {
			return
		}
		var t event.TurnEnded
		if err := json.Unmarshal(d.Data, &t); err != nil {
			t = event.TurnEnded{Outcome: event.TurnOutcomeError, ErrorCode: "adapter"}
		}
		h.endTurn(t, h.interruptCmd)
		if h.shutdownIdle {
			h.requestStop()
			return
		}
		h.drain()
		return
	case event.KindAssistantText:
		var p event.AssistantText
		if json.Unmarshal(d.Data, &p) == nil {
			delete(h.blocks, p.BlockID)
			h.blockOrder = slices.DeleteFunc(h.blockOrder, func(id string) bool { return id == p.BlockID })
		}
	case event.KindPermissionResolved, event.KindQuestionResolved, event.KindPlanResolved:
		if !h.withdraw(d) {
			return
		}
	case event.KindPermissionRequested, event.KindQuestionAsked, event.KindPlanProposed:
		h.request(d)
		return
	}
	h.write(event.Event{V: event.Version, TS: h.cfg.Now().UTC(), Session: h.cfg.Spec.Session.ID, Kind: d.Kind,
		Turn: h.turn, ParentTool: d.ParentTool, Cmd: d.Cmd, Data: d.Data})
	if _, ok := resolvedOf[d.Kind]; ok && h.turn != "" {
		h.setStatus(event.SessionStatusWorking) // a withdrawn request: the turn goes on
	}
}

// request takes a permission, question or plan request from the adapter; callers hold mu. It waits for the owner
// only when it is in the log inside a turn: a request nobody can see (its event not written) or that nothing would
// ever expire (no turn open) would leave the session waiting for good, so the adapter is told no at once instead.
func (h *Host) request(d event.EventDraft) {
	var req struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(d.Data, &req)
	if h.turn == "" || req.RequestID == "" {
		h.cfg.Log.Warn("adapter request outside a turn denied", "kind", d.Kind, "request", req.RequestID)
		h.refuse(d.Kind, req.RequestID)
		return
	}
	err := h.tryWrite(event.Event{V: event.Version, TS: h.cfg.Now().UTC(), Session: h.cfg.Spec.Session.ID, Kind: d.Kind,
		Turn: h.turn, ParentTool: d.ParentTool, Cmd: d.Cmd, Data: d.Data})
	if err != nil {
		h.cfg.Log.Error("adapter request not logged, denied", "kind", d.Kind, "request", req.RequestID, "err", err)
		h.refuse(d.Kind, req.RequestID)
		return
	}
	h.pendingPerm = nil
	if d.Kind == event.KindPermissionRequested {
		var p event.PermissionRequested
		if json.Unmarshal(d.Data, &p) == nil {
			if key, ok := rememberKey(&p); ok && h.allowed[key] && h.allowRemembered(p.RequestID) {
				return
			}
			h.pendingPerm = &p
		}
	}
	h.pendingID, h.pendingKind = req.RequestID, d.Kind
	if d.Kind == event.KindQuestionAsked {
		var q event.QuestionAsked
		_ = json.Unmarshal(d.Data, &q)
		h.pendingQuestions = q.Questions
	}
	h.setStatus(map[event.EventKind]event.SessionStatus{event.KindPermissionRequested: event.SessionStatusApproval,
		event.KindQuestionAsked: event.SessionStatusQuestion, event.KindPlanProposed: event.SessionStatusPlan}[d.Kind])
}

// allowRemembered answers a logged permission request the owner allowed for this session before (exactly this one):
// allow, logged as remembered, so the session never waits for the owner. false: the answer could not be logged; the
// request then waits for the owner as any other.
func (h *Host) allowRemembered(requestID string) bool {
	if err := h.tryAppend(event.KindPermissionResolved, "", h.turn, event.PermissionResolved{RequestID: requestID,
		Decision: event.PermissionDecisionAllowAlways, Remembered: true}); err != nil {
		h.cfg.Log.Error("remembered allow not logged; asking the owner", "request", requestID, "err", err)
		return false
	}
	c, err := event.NewCommand("remembered-"+requestID, h.cfg.Spec.Session.ID, event.CmdPermissionReply,
		event.PermissionReply{RequestID: requestID, Decision: event.ReplyDecisionAllowOnce})
	if err == nil {
		h.send(c)
	}
	return true
}

// refuse answers a request for the owner, who never saw it: deny, reject, or an empty answer that says so.
func (h *Host) refuse(kind event.EventKind, requestID string) {
	const why = "This request could not be shown to the owner."
	var c event.Command
	var err error
	id := "refuse-" + requestID
	switch kind {
	case event.KindPermissionRequested:
		c, err = event.NewCommand(id, h.cfg.Spec.Session.ID, event.CmdPermissionReply,
			event.PermissionReply{RequestID: requestID, Decision: event.ReplyDecisionDeny, Message: why})
	case event.KindQuestionAsked:
		c, err = event.NewCommand(id, h.cfg.Spec.Session.ID, event.CmdQuestionReply,
			event.QuestionReply{RequestID: requestID, Text: why})
	default:
		c, err = event.NewCommand(id, h.cfg.Spec.Session.ID, event.CmdPlanReply,
			event.PlanReply{RequestID: requestID, Decision: event.PlanReplyDecisionRejected, Feedback: why})
	}
	if err == nil {
		h.send(c)
	}
}

// resolvedOf is the request kind each resolution closes.
var resolvedOf = map[event.EventKind]event.EventKind{event.KindPermissionResolved: event.KindPermissionRequested,
	event.KindQuestionResolved: event.KindQuestionAsked, event.KindPlanResolved: event.KindPlanProposed}

// withdraw takes an adapter's resolution of a request: only "expired" for the request the host holds open (the
// provider withdrew it, e.g. the SDK aborted its canUseTool). It frees the slot, so the next request can be shown
// and the session works again; callers hold mu. Anything else is dropped: the host alone resolves requests on the
// owner's reply, and a second resolution of one request would contradict the first.
func (h *Host) withdraw(d event.EventDraft) bool {
	var r struct {
		RequestID string `json:"requestId"`
		Decision  string `json:"decision"`
		Outcome   string `json:"outcome"`
	}
	expired := json.Unmarshal(d.Data, &r) == nil &&
		(r.Decision == string(event.PermissionDecisionExpired) || r.Outcome == string(event.QuestionOutcomeExpired))
	if !expired || r.RequestID == "" || h.pendingID != r.RequestID || h.pendingKind != resolvedOf[d.Kind] {
		h.cfg.Log.Warn("adapter resolution dropped", "kind", d.Kind, "request", r.RequestID)
		return false
	}
	h.pendingID, h.pendingKind, h.pendingPerm = "", "", nil
	return true
}

func (h *Host) live(d event.EventDraft) {
	e := event.Event{V: event.Version, TS: h.cfg.Now().UTC(), Session: h.cfg.Spec.Session.ID, Kind: d.Kind,
		Turn: h.turn, ParentTool: d.ParentTool, Data: d.Data}
	h.broadcast(hostproto.Frame{Live: &e})
}

// Pump feeds the adapter's drafts into the host until ctx ends or the adapter stops; an adapter that stops on its
// own fails the turn it had.
func (h *Host) Pump(ctx context.Context) error {
	out := make(chan event.EventDraft, 64)
	errc := make(chan error, 1)
	go func() { errc <- h.cfg.Adapter.Run(ctx, out) }()
	// Messages queued when the previous host died run first, in their order; a newer one queues behind them. Not
	// for a host started only to log a lost turn after a sandbox start, or to take a held command (stop, reply,
	// queue removal, settings): nobody asked for a turn, so they wait for the owner (Flow stops that host again at
	// once; the next start drains).
	h.mu.Lock()
	if h.cfg.ResumeReason != event.ResumeReasonSandboxStart && h.cfg.ResumeReason != event.ResumeReasonCommand {
		h.drain()
	}
	h.mu.Unlock()
	for {
		select {
		case d := <-out:
			h.Draft(d)
		case err := <-errc:
			for {
				select {
				case d := <-out:
					h.Draft(d)
					continue
				default:
				}
				break
			}
			if ctx.Err() != nil {
				// The adapter ended because the host stops: not a failure.
				return nil //nolint:nilerr // see above
			}
			h.mu.Lock()
			if h.turn != "" {
				h.endTurn(event.TurnEnded{Outcome: event.TurnOutcomeError, ErrorCode: "adapter_exited"}, "")
			}
			h.mu.Unlock()
			if err == nil {
				return errors.New("adapter stopped")
			}
			return fmt.Errorf("adapter stopped: %w", err)
		}
	}
}

// Sync flushes the log; the host calls it every eventlog.SyncInterval.
func (h *Host) Sync() { h.w.Sync() }
