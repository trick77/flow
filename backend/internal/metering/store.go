package metering

import "context"

// Store keeps the ledger. Every Record method inserts one row and never updates one: a row whose id, or whose
// session and seq, is already there is left as it is, so recording twice is recording once. Lookups by seq take
// only rows before it, so a re-follow reads what the first pass read.
type Store interface {
	RecordTurnStart(ctx context.Context, s TurnStart) error
	// TurnStart is the start of the session's turn before seq; ok is false when metering never saw it.
	TurnStart(ctx context.Context, sessionID, turn string, beforeSeq int64) (s TurnStart, ok bool, err error)
	// LastTurnStart is the session's latest turn start before seq.
	LastTurnStart(ctx context.Context, sessionID string, beforeSeq int64) (s TurnStart, ok bool, err error)
	RecordTurn(ctx context.Context, t Turn) error
	// SessionRepos are the session's repositories, owner/name, in worktree order.
	SessionRepos(ctx context.Context, sessionID string) ([]string, error)
	RecordStatusChange(ctx context.Context, c StatusChange) error
	// LastStatusChange is the session's latest transition before seq; ok is false when there is none.
	LastStatusChange(ctx context.Context, sessionID string, beforeSeq int64) (c StatusChange, ok bool, err error)
	RecordWait(ctx context.Context, w Wait) error
	RecordCheckRun(ctx context.Context, r CheckRun) error
	RecordHelperCall(ctx context.Context, c HelperCall) error
	RecordPermissionRequest(ctx context.Context, r PermissionRequest) error
	// PermissionRequest is the session's latest request with requestID before seq (the id is the provider's and
	// can come again); ok is false when metering never saw it.
	PermissionRequest(ctx context.Context, sessionID, requestID string, beforeSeq int64) (r PermissionRequest, ok bool, err error)
	RecordPermissionDecision(ctx context.Context, d PermissionDecision) error
	RecordPullRequest(ctx context.Context, p PullRequest) error
	RecordFileChange(ctx context.Context, c FileChange) error
	RecordSandboxSample(ctx context.Context, s SandboxSample) error
	RecordCounter(ctx context.Context, c Counter) error
}
