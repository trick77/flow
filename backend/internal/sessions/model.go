package sessions

import "time"

// Session is what Flow keeps of a session: the sidebar's fields and how far Flow has followed its log.
//
// Table: sessions
type Session struct {
	ID                string
	ProjectID         string
	ParentID          *string
	Name              string
	Title             string
	TitleSource       string
	Provider          string
	ClaudeAuth        string
	ProviderSessionID string
	Model             string
	Mode              string
	ModeCap           string
	Branch            string
	Status            string
	StatusAt          time.Time
	Unseen            bool
	SeenSeq           int64
	LastSeq           int64
	TurnStartedAt     *time.Time
	LastActivityAt    time.Time
	ArchivedAt        *time.Time
	CleanupPending    bool
	// ClientID is the clientId of the message the session was created with ("" without one): a repeated create with
	// it answers this session.
	ClientID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Worktree is one repository of a session, on the session's branch, with its pull request.
//
// Table: session_worktrees
type Worktree struct {
	ID        string
	SessionID string
	RepoID    string
	Branch    string
	Path      string
	BaseRef   string
	BaseSHA   string
	PRNumber  int // 0: no pull request yet
	PRURL     string
	PRTitle   string
	PRState   string
	PROrder   int
	PRNote    string
	CreatedAt time.Time
	UpdatedAt time.Time
}
