package credentials

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/profile"
	"github.com/trick77/flow/internal/store"
)

func newService(t *testing.T) (*Service, *int, *bytes.Buffer) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sealer, _ := NewAESSealer(bytes.Repeat([]byte{3}, 32))
	changes := 0
	var logged bytes.Buffer
	s := &Service{Store: SQL{DB: db}, Sealer: sealer, Changed: func() { changes++ },
		Log: slog.New(slog.NewTextHandler(&logged, nil))}
	return s, &changes, &logged
}

func TestService(t *testing.T) {
	s, changes, logged := newService(t)
	ctx := t.Context()
	tok, err := s.Set(ctx, apitypes.ApiKeyCreate{Kind: apitypes.ApiKeyKindClaudeToken, Value: " fake-token-1\n", Label: "laptop"})
	if err != nil || tok.EnvName != "CLAUDE_CODE_OAUTH_TOKEN" || tok.Label != "laptop" || *changes != 1 {
		t.Fatalf("Set = %+v, %v (changes %d)", tok, err, *changes)
	}
	key, err := s.Set(ctx, apitypes.ApiKeyCreate{Kind: apitypes.ApiKeyKindAnthropic, Value: "fake-key"})
	if err != nil || key.EnvName != "ANTHROPIC_API_KEY" {
		t.Fatal(key, err)
	}
	or, err := s.Set(ctx, apitypes.ApiKeyCreate{Kind: apitypes.ApiKeyKindOpencodeProvider, Provider: "open-router",
		Value: "fake-or"})
	if err != nil || or.EnvName != "OPEN_ROUTER_API_KEY" {
		t.Fatal(or, err)
	}
	// One per kind: a second token replaces the first, with its own id and time.
	time.Sleep(2 * time.Millisecond)
	tok2, err := s.Set(ctx, apitypes.ApiKeyCreate{Kind: apitypes.ApiKeyKindClaudeToken, Value: "fake-token-2"})
	if err != nil || tok2.ID == tok.ID || !tok2.CreatedAt.After(tok.CreatedAt) {
		t.Fatal(tok2, err)
	}
	keys, _ := s.Keys(ctx)
	if len(keys) != 3 {
		t.Errorf("keys %+v", keys)
	}
	e, err := s.Current(ctx)
	if err != nil || e.ClaudeOAuthToken != "fake-token-2" || e.AnthropicAPIKey != "fake-key" ||
		e.ProviderKeys["OPEN_ROUTER_API_KEY"] != "fake-or" || e.GitHubState != "disconnected" {
		t.Errorf("Current = %+v, %v", e, err)
	}
	e1, _ := s.Env(ctx)
	e2, _ := s.Env(ctx)
	if e1.Revision <= 0 || e2.Revision <= e1.Revision || e2.ClaudeOAuthToken != "fake-token-2" {
		t.Errorf("revisions %d, %d", e1.Revision, e2.Revision)
	}
	if err := s.Delete(ctx, key.ID); err != nil || *changes != 5 {
		t.Errorf("Delete: %v (changes %d)", err, *changes)
	}
	if err := s.Delete(ctx, key.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete: %v", err)
	}
	if e, _ := s.Current(ctx); e.AnthropicAPIKey != "" {
		t.Error("a deleted key is still delivered")
	}
	// A row sealed under another key is skipped and logged by kind, never its value.
	s.Sealer, _ = NewAESSealer(bytes.Repeat([]byte{4}, 32))
	if e, err := s.Current(ctx); err != nil || e.ClaudeOAuthToken != "" {
		t.Errorf("unreadable rows: %+v %v", e, err)
	}
	if out := logged.String(); strings.Contains(out, "fake-") || !strings.Contains(out, "does not open") {
		t.Errorf("log %s", out)
	}
	// ... and not listed as stored: no sandbox gets it, so the UI and dev-flow.sh must not count it as configured.
	if keys, err := s.Keys(ctx); err != nil || len(keys) != 0 {
		t.Errorf("unreadable rows listed: %+v %v", keys, err)
	}
}

// The set carries the owner's git identity from the profile (writeSessionEnv turns it into GIT_AUTHOR_* and
// GIT_COMMITTER_*), both halves or neither: Flow makes up no identity.
func TestEnv_gitIdentity(t *testing.T) {
	s, _, _ := newService(t)
	db := s.Store.(SQL).DB
	s.Profile = profile.SQL{DB: db}
	ctx := t.Context()
	if e, err := s.Env(ctx); err != nil || e.GitName != "" || e.GitEmail != "" {
		t.Errorf("empty profile: %+v %v", e, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE owner_profile SET git_name = 'Jan', git_email = ''`); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Env(ctx); e.GitName != "" || e.GitEmail != "" {
		t.Errorf("name without email: %+v", e)
	}
	if _, err := db.ExecContext(ctx, `UPDATE owner_profile SET git_email = 'jan@example.com'`); err != nil {
		t.Fatal(err)
	}
	if e, err := s.Env(ctx); err != nil || e.GitName != "Jan" || e.GitEmail != "jan@example.com" {
		t.Errorf("identity: %+v %v", e, err)
	}
}

func TestService_refusals(t *testing.T) {
	s, changes, _ := newService(t)
	for name, in := range map[string]apitypes.ApiKeyCreate{
		"empty":             {Kind: apitypes.ApiKeyKindAnthropic, Value: "  "},
		"line break inside": {Kind: apitypes.ApiKeyKindAnthropic, Value: "fake\nsecret"},
		"too long":          {Kind: apitypes.ApiKeyKindAnthropic, Value: strings.Repeat("x", MaxSecret+1)},
		"unknown kind":      {Kind: "github", Value: "x"},
		"provider on token": {Kind: apitypes.ApiKeyKindClaudeToken, Provider: "x", Value: "x"},
		"bad provider":      {Kind: apitypes.ApiKeyKindOpencodeProvider, Provider: "Open Router", Value: "x"},
		"no provider":       {Kind: apitypes.ApiKeyKindOpencodeProvider, Value: "x"},
		"leading digit":     {Kind: apitypes.ApiKeyKindOpencodeProvider, Provider: "302ai", Value: "x"},
		"claude's variable": {Kind: apitypes.ApiKeyKindOpencodeProvider, Provider: "anthropic", Value: "x"},
	} {
		_, err := s.Set(t.Context(), in)
		var ae *apitypes.Error
		if !errors.As(err, &ae) || ae.Code != apitypes.ErrorCodeInvalidRequest || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if *changes != 0 {
		t.Error("a refused key announced a change")
	}
}

func TestAPI(t *testing.T) {
	s, _, _ := newService(t)
	a := &API{Service: s}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/connections/api-keys", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.CreateAPIKey(w, req)
		return w
	}
	if w := post(`{"kind":"anthropic","value":"fake\u0007secret"}`); w.Code != 400 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("bad value: %d %s", w.Code, w.Body)
	}
	w := post(`{"kind":"anthropic","value":"fake-secret","label":"work"}`)
	if w.Code != 201 || strings.Contains(w.Body.String(), "fake-secret") {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	a.GetConnections(w, httptest.NewRequest("GET", "/api/connections", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "fake-secret") || !strings.Contains(w.Body.String(), `"work"`) ||
		!strings.Contains(w.Body.String(), `"createdAt"`) {
		t.Errorf("connections: %d %s", w.Code, w.Body)
	}
	keys, _ := s.Keys(context.Background())
	del := func(id string) int {
		req := httptest.NewRequest("DELETE", "/", nil)
		req.SetPathValue("keyId", id)
		w := httptest.NewRecorder()
		a.DeleteAPIKey(w, req)
		return w.Code
	}
	if c := del(keys[0].ID); c != 204 {
		t.Errorf("delete: %d", c)
	}
	if c := del(keys[0].ID); c != 404 {
		t.Errorf("delete again: %d", c)
	}
	w = httptest.NewRecorder()
	a.StartGitHubDeviceFlow(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 501 {
		t.Errorf("device flow: %d", w.Code)
	}
}
