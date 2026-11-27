-- The clientId of the first message a session was created with: createSession repeated with the same clientId (a
-- retry after a lost response) answers the session it made, never a second one running the same message. NULL for a
-- session created without a message.
ALTER TABLE sessions ADD COLUMN client_id TEXT;
CREATE UNIQUE INDEX sessions_client_id ON sessions (client_id) WHERE client_id IS NOT NULL;
