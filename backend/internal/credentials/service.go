package credentials

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/profile"
	"github.com/trick77/flow/internal/store"
)

// Environment variables flowd sets for a kind (docs/architecture.md, "Credentials"); an OpenCode provider key is
// <PROVIDER>_API_KEY.
const (
	ClaudeTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN" //nolint:gosec // a variable's name, not a credential
	AnthropicEnv   = "ANTHROPIC_API_KEY"       //nolint:gosec // a variable's name, not a credential
)

// MaxSecret bounds a stored secret: a setup-token or an API key is around a hundred bytes.
const MaxSecret = 4096

// providerRE starts with a letter: the provider becomes an environment variable name, which cannot start with a
// digit.
var providerRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// Service stores the owner's API keys and the Claude subscription token sealed, lists them without their values and
// builds the set flowd gets (Env). A subscription login (Claude's third mode) stores nothing: it lives in the home
// volume. Secrets never reach a log, an error or a response.
type Service struct {
	Store  Store
	Sealer Sealer
	Log    *slog.Logger
	// Changed is called after a credential was stored or deleted (Flow pushes the new set to running sandboxes).
	Changed func()
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Profile gives the git identity Env carries (the owner's profile; commits are made as the user). nil: none.
	Profile ProfileReader
	// GitHub gives the user token Env carries (package github). nil: GitHub disconnected.
	GitHub GitHub

	mu  sync.Mutex
	rev int64
}

// ProfileReader is what Env reads of the profile (profile.Store has it).
type ProfileReader interface {
	Get(ctx context.Context) (profile.Profile, error)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) changed() {
	if s.Changed != nil {
		s.Changed()
	}
}

// EnvName is the variable flowd sets for a kind and provider.
func EnvName(kind, provider string) string {
	switch kind {
	case KindClaudeToken:
		return ClaudeTokenEnv
	case KindAnthropic:
		return AnthropicEnv
	case KindOpenCodeProvider:
		return strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
	}
	return ""
}

func invalid(field string) error {
	return &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: field}
}

// Set stores or replaces the secret of a kind (and provider, for opencode_provider). The value is trimmed; one with
// a control character (a pasted line break inside it) or past MaxSecret is refused without echoing it.
func (s *Service) Set(ctx context.Context, in apitypes.ApiKeyCreate) (apitypes.ApiKey, error) {
	kind, provider := string(in.Kind), strings.TrimSpace(in.Provider)
	switch kind {
	case KindClaudeToken, KindAnthropic:
		if provider != "" {
			return apitypes.ApiKey{}, invalid("provider")
		}
	case KindOpenCodeProvider:
		// A provider whose variable is a Claude credential's (anthropic → ANTHROPIC_API_KEY) would hand that key to
		// sessions of another Claude mode.
		if !providerRE.MatchString(provider) || slices.Contains([]string{ClaudeTokenEnv, AnthropicEnv},
			EnvName(kind, provider)) {
			return apitypes.ApiKey{}, invalid("provider")
		}
	default:
		return apitypes.ApiKey{}, invalid("kind")
	}
	value := strings.TrimSpace(in.Value)
	if value == "" || len(value) > MaxSecret || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return apitypes.ApiKey{}, invalid("value")
	}
	label := strings.TrimSpace(in.Label)
	if len(label) > 200 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return apitypes.ApiKey{}, invalid("label")
	}
	sealed, err := s.Sealer.Seal([]byte(value))
	if err != nil {
		return apitypes.ApiKey{}, errors.New("credentials: sealing failed")
	}
	now := s.now().UTC()
	c := Credential{ID: store.NewID(), Kind: kind, Provider: provider, Label: label, EnvName: EnvName(kind, provider),
		Sealed: sealed, CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Put(ctx, c); err != nil {
		return apitypes.ApiKey{}, err
	}
	s.log().Info("credential stored", "kind", kind, "provider", provider, "id", c.ID)
	s.changed()
	return key(c), nil
}

// Delete removes a stored key or token by id; ErrNotFound for none (also for the GitHub connection, which has its
// own route).
func (s *Service) Delete(ctx context.Context, id string) error {
	list, err := s.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.ID != id || c.Kind == KindGitHub {
			continue
		}
		if err := s.Store.Delete(ctx, id); err != nil {
			return err
		}
		s.log().Info("credential deleted", "kind", c.Kind, "provider", c.Provider, "id", id)
		s.changed()
		return nil
	}
	return ErrNotFound
}

// Keys lists the stored keys and tokens, never their values. A row that does not open with this seal key is left
// out, as Current leaves it out of what sandboxes get: listing it would show a credential as configured that no
// session receives. Current logs it.
func (s *Service) Keys(ctx context.Context) ([]apitypes.ApiKey, error) {
	list, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []apitypes.ApiKey{}
	for _, c := range list {
		if c.Kind == KindGitHub {
			continue
		}
		if _, err := s.Sealer.Open(c.Sealed); err != nil {
			continue
		}
		out = append(out, key(c))
	}
	return out, nil
}

func key(c Credential) apitypes.ApiKey {
	return apitypes.ApiKey{ID: c.ID, Kind: apitypes.ApiKeyKind(c.Kind), Provider: c.Provider, EnvName: c.EnvName,
		Label: c.Label, CreatedAt: c.CreatedAt}
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Current implements Source: the stored secrets, opened. One that does not open (sealed under another
// BACKEND_SEAL_KEY) is left out and logged by kind, so the rest still reaches the sandboxes.
func (s *Service) Current(ctx context.Context) (Env, error) {
	list, err := s.Store.List(ctx)
	if err != nil {
		return Env{}, err
	}
	e := Env{GitHubState: flowdproto.GitHubDisconnected}
	for _, c := range list {
		if c.Kind == KindGitHub {
			continue // the GitHub App area's: tokens, renewal and state
		}
		plain, err := s.Sealer.Open(c.Sealed)
		if err != nil {
			s.log().Warn("stored credential does not open with this seal key; store it again", "kind", c.Kind,
				"provider", c.Provider, "id", c.ID)
			continue
		}
		switch c.Kind {
		case KindClaudeToken:
			e.ClaudeOAuthToken = string(plain)
		case KindAnthropic:
			e.AnthropicAPIKey = string(plain)
		case KindOpenCodeProvider:
			if e.ProviderKeys == nil {
				e.ProviderKeys = map[string]string{}
			}
			e.ProviderKeys[c.EnvName] = string(plain)
		}
	}
	if s.GitHub != nil {
		t := s.GitHub.Token(ctx)
		e.GitHubState, e.GitHubLogin = t.State, t.Login
		if t.State == flowdproto.GitHubConnected {
			e.GitHubToken, e.GitHubTokenExpiresAt = t.Token, t.ExpiresAt
		}
	}
	return e, nil
}

// Env is the set for flowd's PUT /v1/env. Its revision is taken before the store is read and from the clock, so it
// grows across Flow restarts (flowd ignores a revision not above its own) and a push built from an older read never
// wins over one built from a newer read.
func (s *Service) Env(ctx context.Context) (flowdproto.Env, error) {
	s.mu.Lock()
	rev := max(s.now().UnixMicro(), s.rev+1)
	s.rev = rev
	s.mu.Unlock()
	c, err := s.Current(ctx)
	if err != nil {
		return flowdproto.Env{}, err
	}
	e := flowdproto.Env{Revision: rev, GitHubToken: c.GitHubToken, GitHubTokenExpiresAt: c.GitHubTokenExpiresAt,
		GitHubState: c.GitHubState, GitHubLogin: c.GitHubLogin, ClaudeOAuthToken: c.ClaudeOAuthToken, AnthropicAPIKey: c.AnthropicAPIKey,
		ProviderKeys: c.ProviderKeys}
	if s.Profile != nil {
		p, err := s.Profile.Get(ctx)
		if err != nil {
			return flowdproto.Env{}, err
		}
		// Both or neither: a name without an email (or the reverse) would make git fall back for the other half,
		// and Flow invents no identity of its own.
		if name, email := strings.TrimSpace(p.GitName), strings.TrimSpace(p.GitEmail); name != "" && email != "" {
			e.GitName, e.GitEmail = name, email
		}
	}
	return e, nil
}
