-- An archived session whose host and worktrees are still in its sandbox: archived while the sandbox was stopped
-- (archiving never starts one), cleaned up at the sandbox's next bring-up (sessions.Service.Sweep), then 0.
ALTER TABLE sessions ADD COLUMN cleanup_pending INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_pending IN (0, 1));
