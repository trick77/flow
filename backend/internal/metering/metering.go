// Package metering is Flow's usage ledger (owner: metering area, docs/ownership.md): append-only rows in Flow's
// database, collected from the first real session so usage can be charted later without the sandbox logs, which
// can be deleted. Nothing reads them yet: no pages, charts or exports.
//
// Two ways in. Recorder.Observe takes every event the session follower reads and writes turn starts and turns,
// status transitions, owner waits, permission requests and decisions, file-change counts and the counters below.
// The Record* methods take what the stream does not carry (check runs, helper-model calls, pull requests, sandbox
// samples, rate-limit and other waits, restart and renewal counters); their owners call them. The package imports
// no area: every area can call it (sessions does, for its follower).
//
// Rules every row follows: inserted once, never updated; a stable id; UTC times, stream rows timed by the event's
// ts, never the clock; names, codes, counts and times only, never prompt, message, command, path or code content.
// Rows from the stream are unique by session and seq, so the follower handing an event on again after a Flow
// restart (it resumes from the last seq it recorded, and hands on before recording) adds nothing. A closing row
// reads its opening row from the ledger, bounded by its own seq; memory only carries a settings change to the
// next turn start.
//
// Turn counters from turn.ended: turn_error for every outcome error, class = errorCode ("unknown" when absent);
// provider_error for the provider-side ones among them, i.e. all classes except the limits the owner set
// (max_turns, budget, max_budget_usd, max_structured_output_retries) and the host's own (adapter, adapter_exited);
// turn_lost for outcome lost (the agent process died mid-turn), no class.
package metering

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/store"
)

// Credential modes of a turn: the Claude ones as the session chose (event.ClaudeAuth), provider_key for OpenCode.
const (
	CredentialSubscriptionToken = "subscription_token"
	CredentialSubscriptionLogin = "subscription_login"
	CredentialAPIKey            = "api_key"
	CredentialProviderKey       = "provider_key"
)

// Wait reasons.
const (
	WaitCheckQueue      = "check_queue"
	WaitSandboxStart    = "sandbox_start"
	WaitSandboxResume   = "sandbox_resume"
	WaitOwnerApproval   = "owner_approval"
	WaitOwnerQuestion   = "owner_question"
	WaitOwnerPlan       = "owner_plan"
	WaitRateLimit       = "rate_limit"
	WaitProviderRetry   = "provider_retry"
	WaitStartedSessions = "started_sessions"
	// WaitRenewal is a wait for a credential renewal.
	WaitRenewal = "credential_renewal"
)

// Check-run results.
const (
	CheckPassed    = "passed"
	CheckFailed    = "failed"
	CheckError     = "error"
	CheckCancelled = "cancelled"
)

// Helper-call outcomes.
const (
	HelperOK    = "ok"
	HelperError = "error"
)

// Pull-request actions: every state change, a reopen included, is its own row.
const (
	PullRequestOpened   = "opened"
	PullRequestReopened = "reopened"
	PullRequestMerged   = "merged"
	PullRequestClosed   = "closed"
)

// Reliability counters (classes: see the package doc).
const (
	CounterTurnError     = "turn_error"
	CounterTurnLost      = "turn_lost"
	CounterProviderError = "provider_error"
	CounterPassthrough   = "passthrough"
	CounterUnknownKind   = "unknown_kind"
	CounterFlowdRestart  = "flowd_restart"
	CounterFlowRestart   = "flow_restart"
	CounterReconnect     = "reconnect"
	// CounterRenewalFailure counts failed credential renewals.
	CounterRenewalFailure = "credential_renewal_failure"
)

var (
	waitReasons = []string{WaitCheckQueue, WaitSandboxStart, WaitSandboxResume, WaitOwnerApproval, WaitOwnerQuestion,
		WaitOwnerPlan, WaitRateLimit, WaitProviderRetry, WaitStartedSessions, WaitRenewal}
	checkResults   = []string{CheckPassed, CheckFailed, CheckError, CheckCancelled}
	helperOutcomes = []string{HelperOK, HelperError}
	prActions      = []string{PullRequestOpened, PullRequestReopened, PullRequestMerged, PullRequestClosed}
	counters       = []string{CounterTurnError, CounterTurnLost, CounterProviderError, CounterPassthrough,
		CounterUnknownKind, CounterFlowdRestart, CounterFlowRestart, CounterReconnect, CounterRenewalFailure}
	// ownerWaits are the statuses in which a session waits for its owner.
	ownerWaits = map[string]string{string(event.SessionStatusApproval): WaitOwnerApproval,
		string(event.SessionStatusQuestion): WaitOwnerQuestion, string(event.SessionStatusPlan): WaitOwnerPlan}
	// notProvider are the error classes of a turn that are not the provider's: limits the owner set, the host's.
	notProvider = []string{"max_turns", "budget", "max_budget_usd", "max_structured_output_retries", "adapter",
		"adapter_exited"}
)

// ErrInvalid is a row the ledger refuses: an unknown enum value, a missing id or time.
var ErrInvalid = errors.New("metering: invalid row")

func invalid(what string) error { return fmt.Errorf("%w: %s", ErrInvalid, what) }

// Session is what the recorder needs of a followed session, as the sessions area's row has it.
type Session struct {
	ID         string
	ProjectID  string
	ParentID   *string
	Provider   string
	ClaudeAuth string
	// Model is the session's model as the follower read it: the last fallback for a turn's.
	Model string
	// Effort likewise ("" reads as default).
	Effort string
}

// Recorder writes the ledger. Create it with its Store.
type Recorder struct {
	Store Store
	// Now is the clock for rows recorded without a time; time.Now by default.
	Now func() time.Time

	mu sync.Mutex
	// live holds a session's provider, model and effort from session.started or session.settings until the next
	// turn starts with them: an entry lives between a settings change and a turn start, or until Forget.
	live map[string]modelRef
}

type modelRef struct{ provider, model, effort string }

func (r *Recorder) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Forget drops what the recorder remembers of a session; the follower calls it when it stops following one.
func (r *Recorder) Forget(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, sessionID)
}

// Observe records what one event of a session's log adds to the ledger. It fails only when the ledger cannot be
// written; an event it cannot read is skipped, so one bad line never holds the follower up.
func (r *Recorder) Observe(ctx context.Context, se Session, e event.Event) error {
	if e.Seq <= 0 {
		return nil // ephemeral: not in the log, no position to be idempotent on
	}
	p, err := e.Payload()
	if errors.Is(err, event.ErrUnknownKind) {
		return r.counter(ctx, se, e, CounterUnknownKind, string(e.Kind))
	}
	if err != nil {
		return nil //nolint:nilerr // a newer major or a broken payload: skipped, see above
	}
	switch p := p.(type) {
	case *event.SessionStarted:
		r.setModel(se.ID, string(p.Provider), p.Model, string(p.Effort))
	case *event.SessionSettings:
		r.setModel(se.ID, "", p.Model, string(p.Effort))
	case *event.TurnStarted:
		return r.turnStart(ctx, se, e, p)
	case *event.SessionStatusChanged:
		return r.status(ctx, se, e, string(p.Status))
	case *event.PermissionRequested:
		return r.Store.RecordPermissionRequest(ctx, PermissionRequest{ID: store.NewID(), SessionID: se.ID,
			ProjectID: se.ProjectID, Seq: e.Seq, RequestID: p.RequestID, Tool: p.Tool, Kind: string(p.Kind),
			Mode: string(p.Mode), RequestedAt: e.TS})
	case *event.PermissionResolved:
		return r.decision(ctx, se, e, p)
	case *event.TurnEnded:
		return r.turn(ctx, se, e, p)
	case *event.FilesChanged:
		c := FileChange{ID: store.NewID(), SessionID: se.ID, ProjectID: se.ProjectID, Seq: e.Seq, Turn: e.Turn,
			Repo: p.Repo, Files: int64(len(p.Files)), At: e.TS}
		for _, f := range p.Files {
			c.LinesAdded += int64(f.Added)
			c.LinesRemoved += int64(f.Removed)
		}
		return r.Store.RecordFileChange(ctx, c)
	case *event.ProviderPassthrough:
		return r.counter(ctx, se, e, CounterPassthrough, p.Name)
	}
	return nil
}

func (r *Recorder) setModel(id, provider, model, effort string) {
	if model == "" && effort == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live == nil {
		r.live = map[string]modelRef{}
	}
	l := r.live[id]
	if model != "" {
		l.model = model
	}
	if effort != "" {
		l.effort = effort
	}
	if provider != "" {
		l.provider = provider
	}
	r.live[id] = l
}

// turnStart fixes the turn's provider, model and effort: the event's (the host stamps them, the effort default
// included), else a change since the last turn, else the last turn's, else the session row's, else default.
func (r *Recorder) turnStart(ctx context.Context, se Session, e event.Event, p *event.TurnStarted) error {
	r.mu.Lock()
	l := r.live[se.ID]
	r.mu.Unlock()
	prev, _, err := r.Store.LastTurnStart(ctx, se.ID, e.Seq)
	if err != nil {
		return err
	}
	s := TurnStart{ID: store.NewID(), SessionID: se.ID, ProjectID: se.ProjectID, Seq: e.Seq, Turn: e.Turn,
		Provider: firstOf(string(p.Provider), l.provider, prev.Provider, se.Provider),
		Model:    firstOf(p.Model, l.model, prev.Model, se.Model),
		Effort:   firstOf(string(p.Effort), l.effort, prev.Effort, se.Effort, string(event.EffortDefault)), At: e.TS}
	if err := r.Store.RecordTurnStart(ctx, s); err != nil {
		return err
	}
	r.Forget(se.ID) // carried into the ledger: the next turn reads it there
	return nil
}

// maxClass bounds a counter's class: a provider's message name is short, and one that is not is cut.
const maxClass = 64

// class is s cut to maxClass bytes on a rune boundary.
func class(s string) string {
	if len(s) <= maxClass {
		return s
	}
	n := maxClass
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (r *Recorder) counter(ctx context.Context, se Session, e event.Event, counter, cls string) error {
	seq := e.Seq
	return r.Store.RecordCounter(ctx, Counter{ID: store.NewID(), Counter: counter, Class: class(cls), SessionID: &se.ID,
		ProjectID: se.ProjectID, Seq: &seq, At: e.TS, Count: 1})
}

// status records a transition, and the owner wait it ends. The previous status comes from the ledger, not from
// memory, so a transition after a restart still knows what it ended.
func (r *Recorder) status(ctx context.Context, se Session, e event.Event, to string) error {
	prev, ok, err := r.Store.LastStatusChange(ctx, se.ID, e.Seq)
	if err != nil {
		return err
	}
	c := StatusChange{ID: store.NewID(), SessionID: se.ID, ProjectID: se.ProjectID, Seq: e.Seq, ToStatus: to, At: e.TS}
	if ok {
		at := prev.At
		c.FromStatus, c.FromAt = prev.ToStatus, &at
	}
	if err := r.Store.RecordStatusChange(ctx, c); err != nil {
		return err
	}
	reason := ownerWaits[c.FromStatus]
	if !ok || reason == "" || c.FromStatus == to {
		return nil
	}
	seq := e.Seq
	return r.Store.RecordWait(ctx, Wait{ID: store.NewID(), SessionID: &se.ID, ProjectID: se.ProjectID, Reason: reason,
		StartedAt: prev.At, EndedAt: e.TS, DurationMs: e.TS.Sub(prev.At).Milliseconds(), Seq: &seq})
}

func (r *Recorder) decision(ctx context.Context, se Session, e event.Event, p *event.PermissionResolved) error {
	d := PermissionDecision{ID: store.NewID(), SessionID: se.ID, ProjectID: se.ProjectID, Seq: e.Seq,
		RequestID: p.RequestID, Decision: string(p.Decision), DecidedAt: e.TS}
	req, ok, err := r.Store.PermissionRequest(ctx, se.ID, p.RequestID, e.Seq)
	if err != nil {
		return err
	}
	if ok {
		at, wait := req.RequestedAt, e.TS.Sub(req.RequestedAt).Milliseconds()
		d.Tool, d.Kind, d.RequestedAt, d.WaitMs = req.Tool, req.Kind, &at, &wait
	}
	return r.Store.RecordPermissionDecision(ctx, d)
}

// CredentialMode is a session's credential mode as the ledger names it.
func CredentialMode(se Session) string {
	if se.Provider == string(event.ProviderOpencode) {
		return CredentialProviderKey
	}
	return se.ClaudeAuth
}

func (r *Recorder) turn(ctx context.Context, se Session, e event.Event, p *event.TurnEnded) error {
	start, _, err := r.Store.TurnStart(ctx, se.ID, e.Turn, e.Seq)
	if err != nil {
		return err
	}
	repos, err := r.Store.SessionRepos(ctx, se.ID)
	if err != nil {
		return err
	}
	t := Turn{ID: store.NewID(), SessionID: se.ID, ProjectID: se.ProjectID, ParentSessionID: se.ParentID, Seq: e.Seq,
		Turn: e.Turn, Repos: repos, Provider: firstOf(string(p.Provider), start.Provider, se.Provider),
		Model:  firstOf(p.Model, start.Model, se.Model),
		Effort: firstOf(start.Effort, se.Effort, string(event.EffortDefault)), CredentialMode: CredentialMode(se),
		StartedAt: e.TS.Add(-time.Duration(p.DurationMs) * time.Millisecond), EndedAt: e.TS, DurationMs: p.DurationMs,
		Outcome: string(p.Outcome), ErrorCode: p.ErrorCode, CostUSD: p.CostUSD}
	if p.Usage != nil {
		t.InputTokens, t.OutputTokens = p.Usage.InputTokens, p.Usage.OutputTokens
		t.CacheReadTokens, t.CacheWriteTokens = p.Usage.CacheReadTokens, p.Usage.CacheWriteTokens
	}
	// The Claude Agent SDK computes total_cost_usd client-side from its bundled price table, whatever the
	// credential (agent-sdk/cost-tracking: an estimate, not billing data): on a subscription it is what the turn
	// would have cost at API prices, not a charge.
	if p.CostUSD != nil && t.Provider == string(event.ProviderClaude) &&
		(t.CredentialMode == CredentialSubscriptionToken || t.CredentialMode == CredentialSubscriptionLogin) {
		t.APIEquivalentUSD = p.CostUSD
	}
	if err := r.Store.RecordTurn(ctx, t); err != nil {
		return err
	}
	switch p.Outcome {
	case event.TurnOutcomeError:
		cls := firstOf(p.ErrorCode, "unknown")
		if err := r.counter(ctx, se, e, CounterTurnError, cls); err != nil {
			return err
		}
		if !slices.Contains(notProvider, cls) {
			return r.counter(ctx, se, e, CounterProviderError, cls)
		}
	case event.TurnOutcomeLost:
		return r.counter(ctx, se, e, CounterTurnLost, "")
	}
	return nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (r *Recorder) id(id string) string {
	if id == "" {
		return store.NewID()
	}
	return id
}

// RecordWait records a wait that does not show in a session's log (owner: whoever holds the resource: the gate
// queue, sandboxes, the provider adapters, credentials). DurationMs defaults to EndedAt - StartedAt. Give ID a
// stable value to make a repeat a no-op.
func (r *Recorder) RecordWait(ctx context.Context, w Wait) error {
	if !slices.Contains(waitReasons, w.Reason) {
		return invalid("wait reason " + w.Reason)
	}
	if w.StartedAt.IsZero() || w.EndedAt.Before(w.StartedAt) {
		return invalid("wait times")
	}
	if w.DurationMs == 0 {
		w.DurationMs = w.EndedAt.Sub(w.StartedAt).Milliseconds()
	}
	w.ID = r.id(w.ID)
	return r.Store.RecordWait(ctx, w)
}

// RecordCheckRun records one gate run (owner: gate).
func (r *Recorder) RecordCheckRun(ctx context.Context, c CheckRun) error {
	if !slices.Contains(checkResults, c.Result) {
		return invalid("check result " + c.Result)
	}
	if c.SessionID == "" || c.Repo == "" || c.StartedAt.IsZero() {
		return invalid("check run session, repo or start")
	}
	c.ID = r.id(c.ID)
	return r.Store.RecordCheckRun(ctx, c)
}

// RecordHelperCall records one helper-model call (owner: helper).
func (r *Recorder) RecordHelperCall(ctx context.Context, c HelperCall) error {
	if !slices.Contains(helperOutcomes, c.Outcome) {
		return invalid("helper outcome " + c.Outcome)
	}
	if c.Task == "" || c.Provider == "" || c.Model == "" || c.StartedAt.IsZero() {
		return invalid("helper task, provider, model or start")
	}
	c.ID = r.id(c.ID)
	return r.Store.RecordHelperCall(ctx, c)
}

// RecordPullRequest records one state change of a pull request (owner: sessions, from pull_requests.updated, or
// github). Idempotent by ID when given, else by Seq (one row per pull request and event); every change otherwise
// is its own row, so a reopened pull request keeps its later history. At defaults to now.
func (r *Recorder) RecordPullRequest(ctx context.Context, p PullRequest) error {
	if !slices.Contains(prActions, p.Action) {
		return invalid("pull request action " + p.Action)
	}
	if p.SessionID == "" || p.Repo == "" || p.Number <= 0 {
		return invalid("pull request session, repo or number")
	}
	if p.At.IsZero() {
		p.At = r.now()
	}
	p.ID = r.id(p.ID)
	return r.Store.RecordPullRequest(ctx, p)
}

// RecordSandboxSample records one sample of a sandbox or a session's unit (owner: sandboxes, flowd runtime). At
// defaults to now.
func (r *Recorder) RecordSandboxSample(ctx context.Context, s SandboxSample) error {
	if s.SandboxID == "" || s.State == "" {
		return invalid("sample sandbox or state")
	}
	if s.At.IsZero() {
		s.At = r.now()
	}
	s.ID = r.id(s.ID)
	return r.Store.RecordSandboxSample(ctx, s)
}

// RecordCounter records an occurrence of a reliability event outside a session's log (flowd and Flow restarts,
// reconnects, credential renewal failures). Count defaults to 1, At to now.
func (r *Recorder) RecordCounter(ctx context.Context, c Counter) error {
	if !slices.Contains(counters, c.Counter) {
		return invalid("counter " + c.Counter)
	}
	c.Class = class(c.Class)
	if c.Count == 0 {
		c.Count = 1
	}
	if c.At.IsZero() {
		c.At = r.now()
	}
	c.ID = r.id(c.ID)
	return r.Store.RecordCounter(ctx, c)
}
