package credentials

import "time"

// Credential is one stored secret: the GitHub App user tokens or an API key. Sealed holds the secret material
// encrypted by a Sealer; nothing else in the row is secret.
//
// Table: credentials
type Credential struct {
	ID               string
	Kind             string
	Provider         string
	Label            string
	EnvName          string
	Sealed           []byte
	Login            string
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
