-- Metering ledger (internal/metering): usage recorded from the first real session so it can be charted later
-- without the sandbox logs, which can be deleted. Append-only: rows are inserted, never updated or deleted.
-- No foreign keys on purpose: a deleted session or project keeps its ledger, and the ids are copied in at write
-- time. Nothing here holds prompt, message, command or code content: names, codes, counts and times only.
-- Rows derived from a session's event log carry its seq; UNIQUE (session_id, seq) makes a re-follow after a Flow
-- restart a no-op. Rows recorded through the API take a caller-chosen id when they must be idempotent.
-- No CHECKs on values that come from the event contract: a new enum value there is a minor change, and a CHECK
-- would stall the follower on it.

-- A turn as it started: the provider and model it runs on, fixed at the start so a model change during the turn
-- does not move it. The turn's row reads it at turn end.
CREATE TABLE metering_turn_starts (
    id          TEXT PRIMARY KEY NOT NULL,
    session_id  TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    turn        TEXT NOT NULL,
    provider    TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL DEFAULT '',
    at          TEXT NOT NULL,
    recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq)
);
CREATE INDEX metering_turn_starts_turn ON metering_turn_starts (session_id, turn);

-- One row per ended turn.
CREATE TABLE metering_turns (
    id                 TEXT PRIMARY KEY NOT NULL,
    session_id         TEXT NOT NULL,
    project_id         TEXT NOT NULL,
    parent_session_id  TEXT,
    seq                INTEGER NOT NULL,
    turn               TEXT NOT NULL DEFAULT '',
    -- JSON array of owner/name, the session's repositories when the turn ended.
    repos              TEXT NOT NULL DEFAULT '[]',
    provider           TEXT NOT NULL,
    model              TEXT NOT NULL DEFAULT '',
    -- subscription_token, subscription_login, api_key (Claude) or provider_key (OpenCode).
    credential_mode    TEXT NOT NULL DEFAULT '',
    started_at         TEXT NOT NULL,
    ended_at           TEXT NOT NULL,
    duration_ms        INTEGER NOT NULL,
    outcome            TEXT NOT NULL,
    error_code         TEXT NOT NULL DEFAULT '',
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    -- As the provider reports it; NULL when it reported none.
    cost_usd           REAL,
    -- Subscription turns only: the turn at API prices, when the provider reports one.
    api_equivalent_usd REAL,
    recorded_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq)
);
CREATE INDEX metering_turns_time ON metering_turns (started_at);

-- Every session status transition: the interval from_status [from_at, at) ended here. Working time, time waiting
-- for the owner and time in the check queue are sums over these.
CREATE TABLE metering_status_changes (
    id          TEXT PRIMARY KEY NOT NULL,
    session_id  TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    from_status TEXT NOT NULL DEFAULT '',
    from_at     TEXT,
    to_status   TEXT NOT NULL,
    at          TEXT NOT NULL,
    recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq)
);

-- Bottlenecks: something waited on a resource from started_at to ended_at.
CREATE TABLE metering_waits (
    id                TEXT PRIMARY KEY NOT NULL,
    session_id        TEXT,
    project_id        TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL CHECK (reason IN ('check_queue', 'sandbox_start', 'sandbox_resume', 'owner_approval',
                      'owner_question', 'owner_plan', 'rate_limit', 'provider_retry', 'started_sessions',
                      'credential_renewal')),
    started_at        TEXT NOT NULL,
    ended_at          TEXT NOT NULL,
    duration_ms       INTEGER NOT NULL,
    -- What was waited on (a repo for the check queue, a sandbox, a provider, a credential kind).
    resource          TEXT NOT NULL DEFAULT '',
    -- check_queue: the position when queued and what held the slot.
    position          INTEGER,
    holder_session_id TEXT,
    holder_repo       TEXT NOT NULL DEFAULT '',
    -- The event that ended the wait, for waits read from a session's log.
    seq               INTEGER,
    recorded_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq, reason)
);
CREATE INDEX metering_waits_time ON metering_waits (reason, started_at);

-- One row per check run (the gate) of a session's repository.
CREATE TABLE metering_check_runs (
    id            TEXT PRIMARY KEY NOT NULL,
    session_id    TEXT NOT NULL,
    project_id    TEXT NOT NULL,
    repo          TEXT NOT NULL,
    started_at    TEXT NOT NULL,
    duration_ms   INTEGER NOT NULL,
    queue_wait_ms INTEGER NOT NULL DEFAULT 0,
    result        TEXT NOT NULL CHECK (result IN ('passed', 'failed', 'error', 'cancelled')),
    exit_code     INTEGER,
    -- The gate step that failed (its name, never its output).
    failing_step  TEXT NOT NULL DEFAULT '',
    recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- The backend's own model calls (titles, branch names, summaries, commit and pull request text).
CREATE TABLE metering_helper_calls (
    id            TEXT PRIMARY KEY NOT NULL,
    task          TEXT NOT NULL,
    session_id    TEXT,
    project_id    TEXT NOT NULL DEFAULT '',
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    started_at    TEXT NOT NULL,
    duration_ms   INTEGER NOT NULL,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL,
    outcome       TEXT NOT NULL CHECK (outcome IN ('ok', 'error')),
    error_class   TEXT NOT NULL DEFAULT '',
    recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- A permission request as it was asked: the tool and its permission class, never the command, path or input.
CREATE TABLE metering_permission_requests (
    id           TEXT PRIMARY KEY NOT NULL,
    session_id   TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    request_id   TEXT NOT NULL,
    tool         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    mode         TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    recorded_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq)
);
CREATE INDEX metering_permission_requests_request ON metering_permission_requests (session_id, request_id);

-- Its decision, with the request's facts copied in; requested_at and wait_ms are NULL when the request row is
-- missing (asked before metering existed).
CREATE TABLE metering_permission_decisions (
    id           TEXT PRIMARY KEY NOT NULL,
    session_id   TEXT NOT NULL,
    project_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    request_id   TEXT NOT NULL,
    tool         TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL DEFAULT '',
    decision     TEXT NOT NULL,
    requested_at TEXT,
    decided_at   TEXT NOT NULL,
    wait_ms      INTEGER,
    recorded_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq)
);

-- A state change of a session's pull request; a pull request can be closed and reopened any number of times, so
-- every change is a row. Idempotent by the caller's id, or by the event that carried it (seq: one
-- pull_requests.updated can change several).
CREATE TABLE metering_pull_requests (
    id          TEXT PRIMARY KEY NOT NULL,
    session_id  TEXT NOT NULL,
    project_id  TEXT NOT NULL,
    repo        TEXT NOT NULL,
    number      INTEGER NOT NULL,
    action      TEXT NOT NULL CHECK (action IN ('opened', 'reopened', 'merged', 'closed')),
    seq         INTEGER,
    at          TEXT NOT NULL,
    recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq, repo, number)
);

-- What a turn changed in one repository: counts only, no paths.
CREATE TABLE metering_file_changes (
    id            TEXT PRIMARY KEY NOT NULL,
    session_id    TEXT NOT NULL,
    project_id    TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    turn          TEXT NOT NULL DEFAULT '',
    repo          TEXT NOT NULL,
    files         INTEGER NOT NULL,
    lines_added   INTEGER NOT NULL,
    lines_removed INTEGER NOT NULL,
    at            TEXT NOT NULL,
    recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq)
);

-- A coarse sample of a sandbox, or of one session's unit in it (session_id set). The *_us, cpu and oom columns are
-- the kernel's cumulative counters since the unit started (pressure-stall totals, cpu usage, memory.events
-- oom_kill): readers take differences and treat a drop as a restart.
CREATE TABLE metering_sandbox_samples (
    id              TEXT PRIMARY KEY NOT NULL,
    sandbox_id      TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    session_id      TEXT,
    at              TEXT NOT NULL,
    state           TEXT NOT NULL,
    memory_bytes    INTEGER NOT NULL DEFAULT 0,
    swap_bytes      INTEGER NOT NULL DEFAULT 0,
    disk_bytes      INTEGER NOT NULL DEFAULT 0,
    cpu_usec        INTEGER NOT NULL DEFAULT 0,
    psi_cpu_some_us INTEGER NOT NULL DEFAULT 0,
    psi_mem_some_us INTEGER NOT NULL DEFAULT 0,
    psi_mem_full_us INTEGER NOT NULL DEFAULT 0,
    psi_io_some_us  INTEGER NOT NULL DEFAULT 0,
    psi_io_full_us  INTEGER NOT NULL DEFAULT 0,
    oom_kills       INTEGER NOT NULL DEFAULT 0,
    recorded_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX metering_sandbox_samples_time ON metering_sandbox_samples (sandbox_id, at);

-- Reliability counters: one row per occurrence (count > 1 only for a batch), summed by readers.
CREATE TABLE metering_counters (
    id          TEXT PRIMARY KEY NOT NULL,
    counter     TEXT NOT NULL CHECK (counter IN ('turn_error', 'turn_lost', 'provider_error', 'passthrough',
                'unknown_kind', 'flowd_restart', 'flow_restart', 'reconnect', 'credential_renewal_failure')),
    -- The error class, the passed-through message's name, the unknown kind, ...: a code, never a sentence.
    class       TEXT NOT NULL DEFAULT '',
    session_id  TEXT,
    project_id  TEXT NOT NULL DEFAULT '',
    seq         INTEGER,
    at          TEXT NOT NULL,
    count       INTEGER NOT NULL DEFAULT 1,
    recorded_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, seq, counter)
);
CREATE INDEX metering_counters_time ON metering_counters (counter, at);
