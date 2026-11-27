package sessions

import (
	"context"
	"net/http"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/httpapi"
)

// claudeModels are the Claude models the composer offers, maintained by hand: Claude Code accepts any model id, and
// Flow asks no API for a list. A new model goes here (and claudeDefaultModel, when it should be the default).
var claudeModels = []apitypes.ModelInfo{
	{ID: "claude-fable-5-2", Label: "Fable 5.2"},
	{ID: "claude-opus-5-5", Label: "Opus 5.5"},
	{ID: "claude-sonnet-5-5", Label: "Sonnet 5.5"},
	{ID: "claude-haiku-5-5", Label: "Haiku 5.5"},
}

// claudeDefaultModel is the model a new Claude session gets unless the owner picks another.
const claudeDefaultModel = "claude-opus-5-5"

// claudeEfforts are the efforts Claude Code takes (the SDK's EffortLevel), least to most, offered for every model:
// the levels a model supports are only known from a running query (supportedModels), and Claude Code lowers a level
// the model lacks by itself (max runs as high). A new session starts on Claude Code's own effort (default).
var claudeEfforts = []apitypes.Effort{apitypes.EffortLow, apitypes.EffortMedium, apitypes.EffortHigh,
	apitypes.EffortXhigh, apitypes.EffortMax}

// catalogModes are the permission modes, least to most permissive (the contract's order).
var catalogModes = []apitypes.PermissionMode{apitypes.PermissionModePlan, apitypes.PermissionModeAsk,
	apitypes.PermissionModeAcceptEdits, apitypes.PermissionModeAuto, apitypes.PermissionModeFullAccess}

// Keys lists the stored credentials without their values (credentials.Service).
type Keys interface {
	Keys(ctx context.Context) ([]apitypes.ApiKey, error)
}

// Catalog is the agent catalog: Claude with the credential modes the stored keys allow, and subscription_login when
// signedIn says the home volume holds a login (flowd's Hello; nil: not known). OpenCode is left out until its
// adapter exists (flowd refuses its sessions with not_implemented); the contract's available=false means "no key",
// not "not built".
func Catalog(ctx context.Context, keys Keys, signedIn func(context.Context) bool) (apitypes.AgentCatalog, error) {
	claude := apitypes.ProviderInfo{Provider: apitypes.ProviderClaude, Models: claudeModels,
		DefaultModel: claudeDefaultModel, Efforts: claudeEfforts, DefaultEffort: apitypes.EffortDefault}
	if keys != nil {
		list, err := keys.Keys(ctx)
		if err != nil {
			return apitypes.AgentCatalog{}, err
		}
		var token, apiKey bool
		for _, k := range list {
			token = token || k.Kind == apitypes.ApiKeyKindClaudeToken
			apiKey = apiKey || k.Kind == apitypes.ApiKeyKindAnthropic
		}
		if token {
			claude.ClaudeAuths = append(claude.ClaudeAuths, apitypes.ClaudeAuthSubscriptionToken)
		}
		if apiKey {
			claude.ClaudeAuths = append(claude.ClaudeAuths, apitypes.ClaudeAuthAPIKey)
		}
	}
	if signedIn != nil && signedIn(ctx) {
		claude.ClaudeAuths = append(claude.ClaudeAuths, apitypes.ClaudeAuthSubscriptionLogin)
	}
	claude.Available = len(claude.ClaudeAuths) > 0
	return apitypes.AgentCatalog{Providers: []apitypes.ProviderInfo{claude}, Modes: catalogModes}, nil
}

// ListAgents implements httpapi.Sessions.
func (a *API) ListAgents(w http.ResponseWriter, r *http.Request) {
	out, err := Catalog(r.Context(), a.Keys, a.ClaudeSignedIn)
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
