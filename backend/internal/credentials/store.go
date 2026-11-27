// Package credentials stores the owner's global credentials sealed, and hands the current set to whoever delivers
// them to sandboxes (owner: GitHub App and credentials area, docs/ownership.md).
package credentials

import (
	"context"
	"net/http"
	"time"

	"github.com/trick77/flow/internal/apitypes"
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

// GitHubTokens is what a sealed github credential holds. Both tokens empty: GitHub refused the refresh token (or it
// expired), the connection needs the owner to connect again; the row stays for the login it shows. A zero
// AccessExpiresAt is a token that does not expire (an App without expiring user tokens): nothing to renew.
type GitHubTokens struct {
	AccessToken      string    `json:"accessToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshToken     string    `json:"refreshToken"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	// AccountID is the GitHub user's numeric id (GET /user), the account the tokens act as.
	AccountID int64 `json:"accountId,omitempty"`
}

// GitHub is the GitHub App area as Env and the connections API need it (package github implements it).
type GitHub interface {
	// Token is the current user token for sandboxes, renewed first when it is due: token and expiry with state
	// connected; no token with state expired (renewal failing, Flow tries again) or disconnected (none, or the
	// owner must connect again). Never the refresh token.
	Token(ctx context.Context) GitHubToken
	// Connection is the state the API reports.
	Connection(ctx context.Context) apitypes.GitHubConnection
	StartDeviceFlow(w http.ResponseWriter, r *http.Request)
	PollDeviceFlow(w http.ResponseWriter, r *http.Request)
	Disconnect(w http.ResponseWriter, r *http.Request)
	ListRepos(w http.ResponseWriter, r *http.Request)
}

// GitHubToken is what Token hands out: no refresh token, ever.
type GitHubToken struct {
	Token     string
	ExpiresAt time.Time
	// State is flowdproto.GitHubConnected, GitHubExpired or GitHubDisconnected.
	State string
	Login string
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
	GitHubLogin          string
	// GitHubState: connected, expired or disconnected (flowdproto.GitHub*).
	GitHubState      string
	ClaudeOAuthToken string
	AnthropicAPIKey  string
	// ProviderKeys maps OpenCode provider environment variable names (OPENROUTER_API_KEY, ...) to values.
	ProviderKeys map[string]string
}

// Source yields the current Env (Service implements it; the GitHub part comes from GitHub, package github).
type Source interface {
	Current(ctx context.Context) (Env, error)
}
