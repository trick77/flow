-- The agent's reasoning effort per session, as the log last set it (session.started, session.settings; the mirror
-- writes it, as model). default: Flow passes none and the agent uses its own; every session before this one ran so.
ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT 'default'
    CHECK (effort IN ('default', 'low', 'medium', 'high', 'xhigh', 'max'));
-- The effort a turn ran on, fixed at its start as the model is, so cost and duration can be compared by effort.
-- What Flow asked for: Claude Code lowers a level the model lacks without saying so. No CHECK (see 0002).
ALTER TABLE metering_turn_starts ADD COLUMN effort TEXT NOT NULL DEFAULT 'default';
ALTER TABLE metering_turns ADD COLUMN effort TEXT NOT NULL DEFAULT 'default';
