package sandboxes

import "time"

// Sandbox is the one sandbox of a project as Flow last saw it; Incus holds the truth about the instance.
//
// Table: sandboxes
type Sandbox struct {
	ID           string
	ProjectID    string
	Instance     string
	State        string
	ProgressCode string
	ErrorCode    string
	Arch         string
	FlowdVersion string
	// FlowdToken is the sealed bearer token Flow presents to this sandbox's flowd.
	FlowdToken []byte
	SetupHash  string
	LastUsedAt *time.Time
	StartedAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
