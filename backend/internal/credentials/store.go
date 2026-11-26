// Package credentials stores the owner's global credentials sealed, and hands the current set to whoever delivers
// them to sandboxes (owner: GitHub App and credentials area, docs/ownership.md).
package credentials

import (
	"context"
	"time"
)

// Kinds of credential.
const (
	KindGitHub           = "github"
	KindClaudeToken      = "claude_token"
	KindAnthropic        = "anthropic"
	KindOpenCodeProvider = "opencode_provider"
)

// Sealer encrypts secrets at rest. kit/secret only compares secrets in constant time and has no encryption, so the
// implementation is Flow's own (AES-256-GCM from the standard library, key from BACKEND_* configuration); this
// interface is the contract the store and its fakes share.
type Sealer interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// GitHubTokens is what a sealed github credential holds.
type GitHubTokens struct {
	AccessToken      string    `json:"accessToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshToken     string    `json:"refreshToken"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
}

// Store keeps credential rows; it never sees plain secrets, the caller seals them first.
type Store interface {
	List(ctx context.Context) ([]Credential, error)
	Get(ctx context.Context, kind, provider string) (Credential, error)
	Put(ctx context.Context, c Credential) error
	Delete(ctx context.Context, id string) error
}

// Env is the credential set flowd needs in a sandbox, plain. Flow builds it fresh for every push to flowd.
type Env struct {
	GitHubToken          string
	GitHubTokenExpiresAt time.Time
	// GitHubState: connected, expired or disconnected (flowdproto.GitHub*).
	GitHubState      string
	ClaudeOAuthToken string
	AnthropicAPIKey  string
	// ProviderKeys maps OpenCode provider environment variable names (OPENROUTER_API_KEY, ...) to values.
	ProviderKeys map[string]string
}

// Source yields the current Env; the GitHub App area implements it with token renewal.
type Source interface {
	Current(ctx context.Context) (Env, error)
}
