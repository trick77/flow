package supervisor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdclient"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/layout"
)

// propUnits records the systemd properties each unit was started with.
type propUnits struct {
	*procUnits
	mu    sync.Mutex
	props map[string][]string
}

func (u *propUnits) Start(ctx context.Context, unit string, argv, props []string) error {
	u.mu.Lock()
	u.props[unit] = props
	u.mu.Unlock()
	return u.procUnits.Start(ctx, unit, argv, props)
}

// The credentials Flow pushes reach a session's host only through its unit's EnvironmentFile on tmpfs, and only
// the one of the session's Claude mode.
func TestSessionEnv_oneCredentialPerMode(t *testing.T) {
	root, err := os.MkdirTemp("", "se") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	units := &propUnits{procUnits: newUnits(), props: map[string][]string{}}
	t.Cleanup(units.stopAll)
	// Everything flowd logs meanwhile is scanned for the secrets at the end.
	logged := &lockedBuffer{}
	s := New(Options{Version: "test", Sandbox: "sb", Root: root, Units: units, Run: ExecRunner{}, Exe: "flowd",
		Log: slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	srv := httptest.NewServer(s.Handler("tok"))
	t.Cleanup(srv.Close)
	api := flowdclient.NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(srv.URL, "http://"), Token: "tok"})
	ctx := context.Background()
	if err := api.PutEnv(ctx, flowdproto.Env{Revision: 2, GitName: `Jan "J" $HOME`, GitEmail: "j@example.com",
		ClaudeOAuthToken: "oauth-secret", AnthropicAPIKey: "api-secret"}); err != nil {
		t.Fatal(err)
	}
	// An older revision changes nothing.
	_ = api.PutEnv(ctx, flowdproto.Env{Revision: 1, ClaudeOAuthToken: "old"})

	for _, tc := range []struct {
		id   string
		auth event.ClaudeAuth
		want string
	}{
		{"s-token", event.ClaudeAuthSubscriptionToken, `CLAUDE_CODE_OAUTH_TOKEN="oauth-secret"`},
		{"s-default", "", `CLAUDE_CODE_OAUTH_TOKEN="oauth-secret"`},
		{"s-key", event.ClaudeAuthAPIKey, `ANTHROPIC_API_KEY="api-secret"`},
		{"s-login", event.ClaudeAuthSubscriptionLogin, ""},
		{"s-replay", event.ClaudeAuthSubscriptionToken, ""},
	} {
		limits := flowdproto.Limits{MemoryMaxBytes: 5 << 30, CPUQuotaPercent: 300, TasksMax: 4096}
		if _, err := api.EnsureSession(ctx, flowdproto.SessionSpec{ID: tc.id, Name: tc.id, Provider: event.ProviderClaude,
			ClaudeAuth: tc.auth, Model: "m", Mode: event.PermissionModeAsk, Limits: limits,
			Replay: tc.id == "s-replay"}); err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
		path := root + layout.SessionEnvFile(tc.id)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v", tc.id, fi.Mode())
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		var creds []string
		for _, l := range lines {
			if strings.Contains(l, "secret") || strings.Contains(l, "old") {
				creds = append(creds, l)
			}
		}
		if (tc.want == "" && len(creds) != 0) || (tc.want != "" && !slices.Equal(creds, []string{tc.want})) {
			t.Errorf("%s: credentials %v", tc.id, creds)
		}
		if !slices.Contains(lines, `GIT_AUTHOR_NAME="Jan \"J\" \$HOME"`) || !slices.Contains(lines,
			`GIT_COMMITTER_EMAIL="j@example.com"`) {
			t.Errorf("%s: identity in %v", tc.id, lines)
		}
		if !slices.ContainsFunc(lines, regexp.MustCompile(`^BACKEND_MCP_TOKEN="[0-9a-f]{64}"$`).MatchString) {
			t.Errorf("%s: no MCP token in %v", tc.id, lines)
		}
		units.mu.Lock()
		props := units.props[layout.HostUnit(tc.id)]
		units.mu.Unlock()
		for _, want := range []string{"EnvironmentFile=" + path, "MemoryMax=5368709120", "CPUQuota=300%",
			"TasksMax=4096"} {
			if !slices.Contains(props, want) {
				t.Errorf("%s: props %v lack %s", tc.id, props, want)
			}
		}
		spec, _ := os.ReadFile(root + layout.SessionSpecFile(tc.id))
		if strings.Contains(string(spec), "secret") {
			t.Errorf("%s: a credential in spec.json", tc.id)
		}
		if err := api.StopSession(ctx, tc.id, false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: environment file left after stop: %v", tc.id, err)
		}
	}

	_, err = api.EnsureSession(ctx, flowdproto.SessionSpec{ID: "s-oc", Name: "s-oc", Provider: event.ProviderOpencode,
		Model: "m", Mode: event.PermissionModeAsk})
	var fe *flowdclient.Error
	if !errors.As(err, &fe) || fe.Body.Code != flowdproto.CodeNotImplemented {
		t.Errorf("opencode session: %v", err)
	}
	if _, err := os.Stat(root + layout.SessionSpecFile("s-oc")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused session left a spec")
	}
	if out := logged.String(); strings.Contains(out, "secret") || !strings.Contains(out, "host started") {
		t.Errorf("flowd's log holds a credential (or nothing): %s", out)
	}
}

// lockedBuffer is a log destination shared by goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestEnvQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":         `"plain"`,
		`a\b"c$d` + "`": `"a\\b\"c\$d\` + "`" + `"`,
		"two\nlines\r":  `"two lines"`,
	} {
		if got := envQuote(in); got != want {
			t.Errorf("envQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
