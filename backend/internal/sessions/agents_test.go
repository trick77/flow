package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/trick77/flow/internal/apitypes"
)

type keyList []apitypes.ApiKeyKind

func (k keyList) Keys(context.Context) ([]apitypes.ApiKey, error) {
	var out []apitypes.ApiKey
	for _, kind := range k {
		out = append(out, apitypes.ApiKey{ID: string(kind), Kind: kind})
	}
	return out, nil
}

type failingKeys struct{}

func (failingKeys) Keys(context.Context) ([]apitypes.ApiKey, error) {
	return nil, errors.New("store down")
}

// GET /api/agents: Claude only (OpenCode has no adapter yet), its hand-kept models with opus as the default, the
// credential modes the stored keys allow, and the modes from least to most permissive.
func TestListAgents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keys      Keys
		auths     []apitypes.ClaudeAuth
		available bool
		login     bool
	}{
		{"nothing stored", keyList{}, nil, false, false},
		{"no key store", nil, nil, false, false},
		{"an OpenCode key only", keyList{apitypes.ApiKeyKindOpencodeProvider}, nil, false, false},
		{"token", keyList{apitypes.ApiKeyKindClaudeToken}, []apitypes.ClaudeAuth{apitypes.ClaudeAuthSubscriptionToken}, true, false},
		{"token and API key", keyList{apitypes.ApiKeyKindAnthropic, apitypes.ApiKeyKindClaudeToken},
			[]apitypes.ClaudeAuth{apitypes.ClaudeAuthSubscriptionToken, apitypes.ClaudeAuthAPIKey}, true, false},
		{"only a login on the home volume", keyList{}, []apitypes.ClaudeAuth{apitypes.ClaudeAuthSubscriptionLogin}, true, true},
	} {
		w := httptest.NewRecorder()
		login := tc.login
		(&API{Keys: tc.keys, ClaudeSignedIn: func(context.Context) bool { return login }}).ListAgents(w, httptest.NewRequest("GET", "/api/agents", nil))
		if w.Code != 200 {
			t.Fatalf("%s: status %d %s", tc.name, w.Code, w.Body)
		}
		var got apitypes.AgentCatalog
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Modes, []apitypes.PermissionMode{"plan", "ask", "accept_edits", "auto", "full_access"}) {
			t.Errorf("%s: modes %v", tc.name, got.Modes)
		}
		if len(got.Providers) != 1 {
			t.Fatalf("%s: providers %+v, want claude only", tc.name, got.Providers)
		}
		p := got.Providers[0]
		if p.Provider != apitypes.ProviderClaude || p.Available != tc.available || !slices.Equal(p.ClaudeAuths, tc.auths) {
			t.Errorf("%s: %+v", tc.name, p)
		}
		var ids []string
		for _, m := range p.Models {
			if m.Label == "" {
				t.Errorf("%s: model %s without a label", tc.name, m.ID)
			}
			ids = append(ids, m.ID)
		}
		if !slices.Equal(ids, []string{"claude-fable-5-2", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}) ||
			p.DefaultModel != "claude-opus-5-5" {
			t.Errorf("%s: models %v, default %q", tc.name, ids, p.DefaultModel)
		}
		if !slices.Equal(p.Efforts, []apitypes.Effort{"low", "medium", "high", "xhigh", "max"}) ||
			p.DefaultEffort != apitypes.EffortDefault {
			t.Errorf("%s: efforts %v, default %q", tc.name, p.Efforts, p.DefaultEffort)
		}
	}
	w := httptest.NewRecorder()
	(&API{Keys: failingKeys{}}).ListAgents(w, httptest.NewRequest("GET", "/api/agents", nil))
	if w.Code < 500 {
		t.Errorf("a key store failure answered %d", w.Code)
	}
}
