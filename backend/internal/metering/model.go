package metering

import "time"

// TurnStart is a turn as it started: the provider and model it runs on, fixed then so a model change during the
// turn does not move it.
//
// Table: metering_turn_starts
type TurnStart struct {
	ID         string
	SessionID  string
	ProjectID  string
	Seq        int64
	Turn       string
	Provider   string
	Model      string
	At         time.Time
	RecordedAt time.Time
}

// Turn is one ended turn of a session: who ran it on what, how long, and what it used.
//
// Table: metering_turns
type Turn struct {
	ID              string
	SessionID       string
	ProjectID       string
	ParentSessionID *string
	// Seq is the turn.ended event's position in the session's log.
	Seq  int64
	Turn string
	// Repos are the session's repositories (owner/name) when the turn ended.
	Repos    []string
	Provider string
	// Model is the model used for this turn, not the session's current one.
	Model string
	// CredentialMode is subscription_token, subscription_login, api_key or provider_key.
	CredentialMode   string
	StartedAt        time.Time
	EndedAt          time.Time
	DurationMs       int64
	Outcome          string
	ErrorCode        string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// CostUSD is the cost as the provider reported it, nil when it reported none.
	CostUSD *float64
	// APIEquivalentUSD is the turn at API prices, for subscription turns whose provider reports one.
	APIEquivalentUSD *float64
	RecordedAt       time.Time
}

// StatusChange is one session status transition: FromStatus held from FromAt until At.
//
// Table: metering_status_changes
type StatusChange struct {
	ID         string
	SessionID  string
	ProjectID  string
	Seq        int64
	FromStatus string
	FromAt     *time.Time
	ToStatus   string
	At         time.Time
	RecordedAt time.Time
}

// Wait is a bottleneck: something waited for a resource from StartedAt to EndedAt.
//
// Table: metering_waits
type Wait struct {
	ID         string
	SessionID  *string
	ProjectID  string
	Reason     string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMs int64
	// Resource is what was waited on: a repo for the check queue, a sandbox, a provider, a credential kind.
	Resource string
	// Position, HolderSessionID and HolderRepo describe a check-queue wait: the place when queued and what held
	// the slot.
	Position        *int64
	HolderSessionID *string
	HolderRepo      string
	// Seq is the event that ended the wait, for waits read from a session's log.
	Seq        *int64
	RecordedAt time.Time
}

// CheckRun is one run of the gate for a session's repository.
//
// Table: metering_check_runs
type CheckRun struct {
	ID          string
	SessionID   string
	ProjectID   string
	Repo        string
	StartedAt   time.Time
	DurationMs  int64
	QueueWaitMs int64
	// Result is passed, failed, error or cancelled.
	Result   string
	ExitCode *int64
	// FailingStep is the name of the step that failed, never its output.
	FailingStep string
	RecordedAt  time.Time
}

// HelperCall is one of the backend's own model calls (titles, branch names, summaries, commit and PR text).
//
// Table: metering_helper_calls
type HelperCall struct {
	ID           string
	Task         string
	SessionID    *string
	ProjectID    string
	Provider     string
	Model        string
	StartedAt    time.Time
	DurationMs   int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      *float64
	// Outcome is ok or error; ErrorClass a code for the error.
	Outcome    string
	ErrorClass string
	RecordedAt time.Time
}

// PermissionRequest is a permission request as asked: the tool and its permission class, never what it would run.
//
// Table: metering_permission_requests
type PermissionRequest struct {
	ID          string
	SessionID   string
	ProjectID   string
	Seq         int64
	RequestID   string
	Tool        string
	Kind        string
	Mode        string
	RequestedAt time.Time
	RecordedAt  time.Time
}

// PermissionDecision is the decision on a permission request, with the request's facts copied in.
//
// Table: metering_permission_decisions
type PermissionDecision struct {
	ID        string
	SessionID string
	ProjectID string
	Seq       int64
	RequestID string
	Tool      string
	Kind      string
	Decision  string
	// RequestedAt and WaitMs are nil when the request was asked before metering saw it.
	RequestedAt *time.Time
	DecidedAt   time.Time
	WaitMs      *int64
	RecordedAt  time.Time
}

// PullRequest is one state change of a session's pull request: every open, close, reopen and merge is a row.
//
// Table: metering_pull_requests
type PullRequest struct {
	ID        string
	SessionID string
	ProjectID string
	Repo      string
	Number    int64
	// Action is opened, reopened, merged or closed.
	Action string
	// Seq is the pull_requests.updated event that carried the change, when it came from a session's log.
	Seq        *int64
	At         time.Time
	RecordedAt time.Time
}

// FileChange is what a turn changed in one repository, as counts.
//
// Table: metering_file_changes
type FileChange struct {
	ID           string
	SessionID    string
	ProjectID    string
	Seq          int64
	Turn         string
	Repo         string
	Files        int64
	LinesAdded   int64
	LinesRemoved int64
	At           time.Time
	RecordedAt   time.Time
}

// SandboxSample is a coarse sample of a sandbox, or of one session's unit in it. The counters (CPU, pressure-stall
// totals, OOM kills) are cumulative since the unit started, as the kernel reports them.
//
// Table: metering_sandbox_samples
type SandboxSample struct {
	ID           string
	SandboxID    string
	ProjectID    string
	SessionID    *string
	At           time.Time
	State        string
	MemoryBytes  int64
	SwapBytes    int64
	DiskBytes    int64
	CPUUsec      int64
	PSICPUSomeUs int64
	PSIMemSomeUs int64
	PSIMemFullUs int64
	PSIIOSomeUs  int64
	PSIIOFullUs  int64
	OOMKills     int64
	RecordedAt   time.Time
}

// Counter is one occurrence of a reliability event (Count > 1 only for a batch).
//
// Table: metering_counters
type Counter struct {
	ID      string
	Counter string
	// Class is the error class, the passed-through message's name or the unknown kind: a code, never a sentence.
	Class      string
	SessionID  *string
	ProjectID  string
	Seq        *int64
	At         time.Time
	Count      int64
	RecordedAt time.Time
}
