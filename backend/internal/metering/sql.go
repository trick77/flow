package metering

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/trick77/flow/internal/store"
)

// SQL is the Store on Flow's database.
type SQL struct{ DB *sql.DB }

var _ Store = SQL{}

// insert runs one INSERT that leaves an existing row (same id, or same session and seq) alone: ON CONFLICT DO
// NOTHING covers uniqueness only, so a CHECK or NOT NULL violation still fails.
func (s SQL) insert(ctx context.Context, q string, args ...any) error {
	_, err := s.DB.ExecContext(ctx, q+` ON CONFLICT DO NOTHING`, args...)
	return err
}

// RecordTurnStart implements Store.
func (s SQL) RecordTurnStart(ctx context.Context, t TurnStart) error {
	return s.insert(ctx, `INSERT INTO metering_turn_starts (id, session_id, project_id, seq, turn, provider, model, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, t.SessionID, t.ProjectID, t.Seq, t.Turn, t.Provider, t.Model,
		store.Format(t.At))
}

const turnStartCols = `id, session_id, project_id, seq, turn, provider, model, at, recorded_at`

func (s SQL) turnStart(ctx context.Context, q string, args ...any) (TurnStart, bool, error) {
	var t TurnStart
	var at, recorded string
	err := s.DB.QueryRowContext(ctx, `SELECT `+turnStartCols+` FROM metering_turn_starts WHERE `+q+ //nolint:gosec // constant fragments
		` ORDER BY seq DESC LIMIT 1`, args...).Scan(&t.ID, &t.SessionID, &t.ProjectID, &t.Seq, &t.Turn, &t.Provider,
		&t.Model, &at, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return TurnStart{}, false, nil
	}
	if err != nil {
		return TurnStart{}, false, err
	}
	t.At, t.RecordedAt = store.Parse(at), store.Parse(recorded)
	return t, true, nil
}

// TurnStart implements Store.
func (s SQL) TurnStart(ctx context.Context, sessionID, turn string, beforeSeq int64) (TurnStart, bool, error) {
	return s.turnStart(ctx, `session_id = ? AND turn = ? AND seq < ?`, sessionID, turn, beforeSeq)
}

// LastTurnStart implements Store.
func (s SQL) LastTurnStart(ctx context.Context, sessionID string, beforeSeq int64) (TurnStart, bool, error) {
	return s.turnStart(ctx, `session_id = ? AND seq < ?`, sessionID, beforeSeq)
}

// SessionRepos implements Store from the sessions and projects tables (docs/schema.md): read-only, so metering
// needs neither area's package and both can call it.
func (s SQL) SessionRepos(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.full_name FROM session_worktrees w JOIN project_repos r
		ON r.id = w.repo_id WHERE w.session_id = ? ORDER BY w.created_at, w.rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// RecordTurn implements Store.
func (s SQL) RecordTurn(ctx context.Context, t Turn) error {
	repos, err := json.Marshal(t.Repos)
	if err != nil {
		return err
	}
	if t.Repos == nil {
		repos = []byte("[]")
	}
	return s.insert(ctx, `INSERT INTO metering_turns (id, session_id, project_id, parent_session_id, seq, turn, repos,
		provider, model, credential_mode, started_at, ended_at, duration_ms, outcome, error_code, input_tokens,
		output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, api_equivalent_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, t.ProjectID, t.ParentSessionID, t.Seq, t.Turn, string(repos), t.Provider, t.Model,
		t.CredentialMode, store.Format(t.StartedAt), store.Format(t.EndedAt), t.DurationMs, t.Outcome, t.ErrorCode,
		t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.CacheWriteTokens, t.CostUSD, t.APIEquivalentUSD)
}

// RecordStatusChange implements Store.
func (s SQL) RecordStatusChange(ctx context.Context, c StatusChange) error {
	return s.insert(ctx, `INSERT INTO metering_status_changes (id, session_id, project_id, seq, from_status, from_at,
		to_status, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SessionID, c.ProjectID, c.Seq, c.FromStatus, store.Null(c.FromAt), c.ToStatus, store.Format(c.At))
}

// LastStatusChange implements Store.
func (s SQL) LastStatusChange(ctx context.Context, sessionID string, beforeSeq int64) (StatusChange, bool, error) {
	var c StatusChange
	var from sql.NullString
	var at, recorded string
	err := s.DB.QueryRowContext(ctx, `SELECT id, session_id, project_id, seq, from_status, from_at, to_status, at,
		recorded_at FROM metering_status_changes WHERE session_id = ? AND seq < ? ORDER BY seq DESC LIMIT 1`,
		sessionID, beforeSeq).Scan(&c.ID, &c.SessionID, &c.ProjectID, &c.Seq, &c.FromStatus, &from, &c.ToStatus, &at,
		&recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return StatusChange{}, false, nil
	}
	if err != nil {
		return StatusChange{}, false, err
	}
	c.FromAt, c.At, c.RecordedAt = store.ParseNull(from), store.Parse(at), store.Parse(recorded)
	return c, true, nil
}

// RecordWait implements Store.
func (s SQL) RecordWait(ctx context.Context, w Wait) error {
	return s.insert(ctx, `INSERT INTO metering_waits (id, session_id, project_id, reason, started_at, ended_at,
		duration_ms, resource, position, holder_session_id, holder_repo, seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.SessionID, w.ProjectID, w.Reason, store.Format(w.StartedAt), store.Format(w.EndedAt), w.DurationMs,
		w.Resource, w.Position, w.HolderSessionID, w.HolderRepo, w.Seq)
}

// RecordCheckRun implements Store.
func (s SQL) RecordCheckRun(ctx context.Context, r CheckRun) error {
	return s.insert(ctx, `INSERT INTO metering_check_runs (id, session_id, project_id, repo, started_at, duration_ms,
		queue_wait_ms, result, exit_code, failing_step) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SessionID, r.ProjectID, r.Repo, store.Format(r.StartedAt), r.DurationMs, r.QueueWaitMs, r.Result,
		r.ExitCode, r.FailingStep)
}

// RecordHelperCall implements Store.
func (s SQL) RecordHelperCall(ctx context.Context, c HelperCall) error {
	return s.insert(ctx, `INSERT INTO metering_helper_calls (id, task, session_id, project_id, provider, model,
		started_at, duration_ms, input_tokens, output_tokens, cost_usd, outcome, error_class)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Task, c.SessionID, c.ProjectID, c.Provider, c.Model, store.Format(c.StartedAt), c.DurationMs,
		c.InputTokens, c.OutputTokens, c.CostUSD, c.Outcome, c.ErrorClass)
}

// RecordPermissionRequest implements Store.
func (s SQL) RecordPermissionRequest(ctx context.Context, r PermissionRequest) error {
	return s.insert(ctx, `INSERT INTO metering_permission_requests (id, session_id, project_id, seq, request_id, tool,
		kind, mode, requested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SessionID, r.ProjectID, r.Seq, r.RequestID, r.Tool, r.Kind, r.Mode, store.Format(r.RequestedAt))
}

// PermissionRequest implements Store.
func (s SQL) PermissionRequest(ctx context.Context, sessionID, requestID string, beforeSeq int64) (PermissionRequest, bool, error) {
	var r PermissionRequest
	var requested, recorded string
	err := s.DB.QueryRowContext(ctx, `SELECT id, session_id, project_id, seq, request_id, tool, kind, mode,
		requested_at, recorded_at FROM metering_permission_requests WHERE session_id = ? AND request_id = ? AND seq < ?
		ORDER BY seq DESC LIMIT 1`, sessionID, requestID, beforeSeq).Scan(&r.ID, &r.SessionID, &r.ProjectID, &r.Seq,
		&r.RequestID, &r.Tool, &r.Kind, &r.Mode, &requested, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return PermissionRequest{}, false, nil
	}
	if err != nil {
		return PermissionRequest{}, false, err
	}
	r.RequestedAt, r.RecordedAt = store.Parse(requested), store.Parse(recorded)
	return r, true, nil
}

// RecordPermissionDecision implements Store.
func (s SQL) RecordPermissionDecision(ctx context.Context, d PermissionDecision) error {
	return s.insert(ctx, `INSERT INTO metering_permission_decisions (id, session_id, project_id, seq, request_id, tool,
		kind, decision, requested_at, decided_at, wait_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.SessionID, d.ProjectID, d.Seq, d.RequestID, d.Tool, d.Kind, d.Decision, store.Null(d.RequestedAt),
		store.Format(d.DecidedAt), d.WaitMs)
}

// RecordPullRequest implements Store.
func (s SQL) RecordPullRequest(ctx context.Context, p PullRequest) error {
	return s.insert(ctx, `INSERT INTO metering_pull_requests (id, session_id, project_id, repo, number, action, seq, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, p.ID, p.SessionID, p.ProjectID, p.Repo, p.Number, p.Action, p.Seq,
		store.Format(p.At))
}

// RecordFileChange implements Store.
func (s SQL) RecordFileChange(ctx context.Context, c FileChange) error {
	return s.insert(ctx, `INSERT INTO metering_file_changes (id, session_id, project_id, seq, turn, repo, files,
		lines_added, lines_removed, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SessionID, c.ProjectID, c.Seq, c.Turn, c.Repo, c.Files, c.LinesAdded, c.LinesRemoved, store.Format(c.At))
}

// RecordSandboxSample implements Store.
func (s SQL) RecordSandboxSample(ctx context.Context, x SandboxSample) error {
	return s.insert(ctx, `INSERT INTO metering_sandbox_samples (id, sandbox_id, project_id, session_id, at, state,
		memory_bytes, swap_bytes, disk_bytes, cpu_usec, psi_cpu_some_us, psi_mem_some_us, psi_mem_full_us,
		psi_io_some_us, psi_io_full_us, oom_kills) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.SandboxID, x.ProjectID, x.SessionID, store.Format(x.At), x.State, x.MemoryBytes, x.SwapBytes,
		x.DiskBytes, x.CPUUsec, x.PSICPUSomeUs, x.PSIMemSomeUs, x.PSIMemFullUs, x.PSIIOSomeUs, x.PSIIOFullUs,
		x.OOMKills)
}

// RecordCounter implements Store.
func (s SQL) RecordCounter(ctx context.Context, c Counter) error {
	return s.insert(ctx, `INSERT INTO metering_counters (id, counter, class, session_id, project_id, seq, at, count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.Counter, c.Class, c.SessionID, c.ProjectID, c.Seq, store.Format(c.At),
		c.Count)
}
