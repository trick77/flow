-- Phase 1 schema. Flow has one owner: no users table, no user id columns.
-- Timeline content lives in the sandboxes' event logs; these tables hold what the sidebar, the settings and the
-- orchestration need. Times are UTC RFC 3339 text. Ids are UUIDs chosen by Flow.

-- The owner's profile and settings: exactly one row.
CREATE TABLE owner_profile (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    git_name          TEXT NOT NULL DEFAULT '',
    git_email         TEXT NOT NULL DEFAULT '',
    language          TEXT NOT NULL DEFAULT 'en',
    format_locale     TEXT NOT NULL DEFAULT 'en-CH',
    time_zone         TEXT NOT NULL DEFAULT 'Europe/Zurich',
    sidebar_grouping  TEXT NOT NULL DEFAULT 'status' CHECK (sidebar_grouping IN ('status', 'project')),
    config_repo       TEXT NOT NULL DEFAULT '',
    config_sync_state TEXT NOT NULL DEFAULT 'never' CHECK (config_sync_state IN ('never', 'syncing', 'synced', 'failed')),
    config_synced_at  TEXT,
    config_commit     TEXT NOT NULL DEFAULT '',
    config_files      INTEGER NOT NULL DEFAULT 0,
    config_error_code TEXT NOT NULL DEFAULT '',
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
INSERT INTO owner_profile (id) VALUES (1);

-- Global credentials, sealed (encrypted) by Flow: the GitHub App user tokens, the Claude subscription token, API
-- keys. One per kind and provider; storing another replaces it.
CREATE TABLE credentials (
    id                 TEXT PRIMARY KEY NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('github', 'claude_token', 'anthropic', 'opencode_provider')),
    provider           TEXT NOT NULL DEFAULT '',
    label              TEXT NOT NULL DEFAULT '',
    env_name           TEXT NOT NULL DEFAULT '',
    sealed             BLOB NOT NULL,
    login              TEXT NOT NULL DEFAULT '',
    expires_at         TEXT,
    refresh_expires_at TEXT,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (kind, provider)
);

CREATE TABLE projects (
    id         TEXT PRIMARY KEY NOT NULL,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE project_repos (
    id             TEXT PRIMARY KEY NOT NULL,
    project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    full_name      TEXT NOT NULL,
    default_branch TEXT NOT NULL,
    position       INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (project_id, full_name)
);

-- One sandbox per project. flowd_token is sealed: Flow needs it again after its own restart to reach a running flowd.
CREATE TABLE sandboxes (
    id            TEXT PRIMARY KEY NOT NULL,
    project_id    TEXT NOT NULL UNIQUE REFERENCES projects (id) ON DELETE CASCADE,
    instance      TEXT NOT NULL UNIQUE,
    state         TEXT NOT NULL DEFAULT 'none'
                  CHECK (state IN ('none', 'setup', 'warm', 'stopping', 'stopped', 'failed', 'deleting')),
    progress_code TEXT NOT NULL DEFAULT '',
    error_code    TEXT NOT NULL DEFAULT '',
    arch          TEXT NOT NULL DEFAULT '',
    flowd_version TEXT NOT NULL DEFAULT '',
    flowd_token   BLOB,
    setup_hash    TEXT NOT NULL DEFAULT '',
    last_used_at  TEXT,
    started_at    TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- What the sidebar needs of a session; the conversation is in the sandbox's log. last_seq is how far Flow has
-- followed that log, seen_seq how far the owner has looked at it.
CREATE TABLE sessions (
    id                  TEXT PRIMARY KEY NOT NULL,
    project_id          TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    parent_id           TEXT REFERENCES sessions (id) ON DELETE SET NULL,
    name                TEXT NOT NULL,
    title               TEXT NOT NULL DEFAULT '',
    title_source        TEXT NOT NULL DEFAULT 'derived' CHECK (title_source IN ('owner', 'helper', 'derived', 'parent')),
    provider            TEXT NOT NULL CHECK (provider IN ('claude', 'opencode')),
    -- The Claude credential mode, chosen per session. Claude Code prefers an API key over a subscription token over
    -- a stored login, so flowd sets only the chosen credential. Empty for OpenCode.
    claude_auth         TEXT NOT NULL DEFAULT ''
                        CHECK (claude_auth IN ('', 'subscription_token', 'subscription_login', 'api_key')),
    provider_session_id TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL,
    mode                TEXT NOT NULL CHECK (mode IN ('plan', 'ask', 'accept_edits', 'auto', 'full_access')),
    mode_cap            TEXT NOT NULL DEFAULT ''
                        CHECK (mode_cap IN ('', 'plan', 'ask', 'accept_edits', 'auto', 'full_access')),
    branch              TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'idle'
                        CHECK (status IN ('idle', 'working', 'queued', 'approval', 'plan', 'question', 'done', 'failed')),
    status_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    unseen              INTEGER NOT NULL DEFAULT 0 CHECK (unseen IN (0, 1)),
    seen_seq            INTEGER NOT NULL DEFAULT 0,
    last_seq            INTEGER NOT NULL DEFAULT 0,
    turn_started_at     TEXT,
    last_activity_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    archived_at         TEXT,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (project_id, name)
);
CREATE INDEX sessions_sidebar ON sessions (archived_at, last_activity_at);
CREATE INDEX sessions_parent ON sessions (parent_id);

-- A session has many worktrees: one per repository, all on the session's branch, each with its pull request.
CREATE TABLE session_worktrees (
    id         TEXT PRIMARY KEY NOT NULL,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    repo_id    TEXT NOT NULL REFERENCES project_repos (id) ON DELETE RESTRICT,
    branch     TEXT NOT NULL,
    path       TEXT NOT NULL,
    base_ref   TEXT NOT NULL,
    base_sha   TEXT NOT NULL DEFAULT '',
    pr_number  INTEGER NOT NULL DEFAULT 0,
    pr_url     TEXT NOT NULL DEFAULT '',
    pr_title   TEXT NOT NULL DEFAULT '',
    pr_state   TEXT NOT NULL DEFAULT 'not_opened'
               CHECK (pr_state IN ('not_opened', 'draft', 'open', 'merged', 'closed')),
    pr_order   INTEGER NOT NULL DEFAULT 0,
    pr_note    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, repo_id)
);
CREATE INDEX session_worktrees_repo ON session_worktrees (repo_id);
