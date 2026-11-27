package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/credentials"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/projects"
	"github.com/trick77/flow/internal/store"
)

// fakeGitHub is GitHub's OAuth and REST endpoints as Flow uses them, with rotating refresh tokens: a refresh
// invalidates the refresh token it used and the access token it replaces.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// polls answers device-token polls in order: an OAuth error code, or "ok".
	polls       []string
	pollCalls   int
	deviceCalls int
	access      string
	refresh     string
	gen         int
	// refreshError answers every refresh with this OAuth error; refreshStatus with a bare status.
	refreshError  string
	refreshStatus int
	refreshCalls  atomic.Int32
	refreshDelay  time.Duration
	// private repositories the installations hold, by installation; public repositories only the anonymous
	// lookup knows.
	installations map[int64][]Repo
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, installations: map[int64][]Repo{
		1: {{FullName: "octo/zeta", DefaultBranch: "main", Private: true},
			{FullName: "octo/Alpha", DefaultBranch: "trunk", Private: true, Description: "first"}},
		2: {{FullName: "org/mid", DefaultBranch: "develop"}, {FullName: "octo/zeta", DefaultBranch: "main", Private: true}},
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client() *Client {
	return &Client{ClientID: "Iv23test", Web: f.srv.URL, API: f.srv.URL, HTTP: f.srv.Client()}
}

func (f *fakeGitHub) oauth(w http.ResponseWriter, code string, extra map[string]any) {
	body := map[string]any{"error": code}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body) // status 200, as GitHub answers OAuth errors
}

// issue makes a new pair; callers hold mu.
func (f *fakeGitHub) issue(w http.ResponseWriter) {
	f.gen++
	f.access, f.refresh = fmt.Sprintf("ghu_access%d", f.gen), fmt.Sprintf("ghr_refresh%d", f.gen)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.access, "expires_in": 28800,
		"refresh_token": f.refresh, "refresh_token_expires_in": 15897600, "scope": "", "token_type": "bearer"})
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" && strings.Contains(r.URL.RawQuery, "gh") {
		f.t.Errorf("a token in a URL: %s", r.URL)
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("Accept") != "application/json" {
			f.t.Errorf("%s without Accept: application/json", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != "Iv23test" || r.PostForm.Has("client_secret") {
			f.t.Errorf("%s: form %v", r.URL.Path, r.PostForm.Get("client_id"))
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/login/device/code":
		f.deviceCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": fmt.Sprintf("dc-%d", f.deviceCalls),
			"user_code": "WDJB-MJHT", "verification_uri": "https://github.com/login/device", "expires_in": 900,
			"interval": 5})
	case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
		switch r.PostForm.Get("grant_type") {
		case deviceGrant:
			f.pollCalls++
			if len(f.polls) == 0 {
				f.oauth(w, "authorization_pending", nil)
				return
			}
			a := f.polls[0]
			f.polls = f.polls[1:]
			switch a {
			case "ok":
				f.issue(w)
			case "slow_down":
				f.oauth(w, a, map[string]any{"interval": 10})
			default:
				f.oauth(w, a, nil)
			}
		case "refresh_token":
			f.refreshCalls.Add(1)
			if f.refreshDelay > 0 {
				time.Sleep(f.refreshDelay)
			}
			switch {
			case f.refreshStatus != 0:
				w.WriteHeader(f.refreshStatus)
			case f.refreshError != "":
				f.oauth(w, f.refreshError, nil)
			case r.PostForm.Get("refresh_token") != f.refresh:
				f.oauth(w, "bad_refresh_token", nil)
			default:
				f.issue(w)
			}
		default:
			f.oauth(w, "unsupported_grant_type", nil)
		}
	default:
		if r.Header.Get("Authorization") != "Bearer "+f.access || f.access == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.rest(w, r)
	}
}

func (f *fakeGitHub) rest(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo", "id": 42})
	case r.URL.Path == "/user/installations":
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2,
			"installations": []map[string]any{{"id": 1}, {"id": 2}}})
	case strings.HasPrefix(r.URL.Path, "/user/installations/"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/user/installations/"),
			"/repositories"), 10, 64)
		list := f.installations[id]
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(list), "repositories": list})
	case strings.HasPrefix(r.URL.Path, "/repos/"):
		name := strings.TrimPrefix(r.URL.Path, "/repos/")
		for _, list := range f.installations {
			for _, rp := range list {
				if rp.FullName == name {
					_ = json.NewEncoder(w).Encode(rp)
					return
				}
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// clock is a settable time.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	f       *fakeGitHub
	s       *Service
	clock   *clock
	store   credentials.Store
	sealer  credentials.Sealer
	changed *atomic.Int32
	logs    *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sealer, _ := credentials.NewAESSealer(bytes.Repeat([]byte{5}, 32))
	h := &harness{f: newFake(t), clock: &clock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)},
		store: credentials.SQL{DB: db}, sealer: sealer, changed: &atomic.Int32{}, logs: &syncBuffer{}}
	h.s = h.service()
	t.Cleanup(func() {
		if out := h.logs.String(); strings.Contains(out, "ghu_") || strings.Contains(out, "ghr_") ||
			strings.Contains(out, "dc-") {
			t.Errorf("a token or device code in the log:\n%s", out)
		}
	})
	return h
}

// service is a Service on the harness's store, fake and clock: a second one is Flow after a restart.
func (h *harness) service() *Service {
	return &Service{Client: h.f.client(), Store: h.store, Sealer: h.sealer, Now: h.clock.now,
		Log: slog.New(slog.NewTextHandler(h.logs, nil)), Changed: func() { h.changed.Add(1) },
		Anonymous: func(_ context.Context, name string) (string, error) {
			if name == "public/repo" {
				return "master", nil
			}
			return "", &projects.StatusError{Repo: name, Status: http.StatusUnauthorized}
		}}
}

func call(t *testing.T, fn http.HandlerFunc, method string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	fn(w, httptest.NewRequest(method, "/", nil))
	return w
}

func pollState(t *testing.T, s *Service) apitypes.GitHubConnection {
	t.Helper()
	w := call(t, s.PollDeviceFlow, http.MethodPost)
	if w.Code != http.StatusOK {
		t.Fatalf("poll: %d %s", w.Code, w.Body)
	}
	var c apitypes.GitHubConnection
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "dc-") || strings.Contains(w.Body.String(), "gh") {
		t.Errorf("poll answer carries a code or token: %s", w.Body)
	}
	return c
}

// connect runs the device flow to its end.
func (h *harness) connect(t *testing.T) {
	t.Helper()
	h.f.mu.Lock()
	h.f.polls = []string{"ok"}
	h.f.mu.Unlock()
	if w := call(t, h.s.StartDeviceFlow, http.MethodPost); w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	h.clock.add(5 * time.Second)
	if c := pollState(t, h.s); c.State != apitypes.GitHubStateConnected {
		t.Fatalf("connect: %+v", c)
	}
}

func TestDeviceFlow(t *testing.T) {
	h := newHarness(t)
	s, f := h.s, h.f
	published := 0
	s.Publish = func() { published++ }
	f.polls = []string{"authorization_pending", "slow_down", "ok"}
	w := call(t, s.StartDeviceFlow, http.MethodPost)
	var df apitypes.DeviceFlow
	if err := json.Unmarshal(w.Body.Bytes(), &df); err != nil || w.Code != 200 || df.UserCode != "WDJB-MJHT" ||
		df.IntervalSeconds != 5 || !df.ExpiresAt.Equal(h.clock.now().Add(15*time.Minute)) ||
		df.VerificationURI != "https://github.com/login/device" || strings.Contains(w.Body.String(), "dc-") {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if c := s.Connection(t.Context()); c.State != apitypes.GitHubStatePending || !c.Available {
		t.Errorf("connection while a code is out: %+v", c)
	}
	// One flow at a time: a second start hands out the same code.
	call(t, s.StartDeviceFlow, http.MethodPost)
	if f.deviceCalls != 1 {
		t.Errorf("device codes requested: %d", f.deviceCalls)
	}
	// Polls faster than GitHub's interval are answered from memory.
	for range 5 {
		if c := pollState(t, s); c.State != apitypes.GitHubStatePending {
			t.Fatalf("early poll: %+v", c)
		}
	}
	if f.pollCalls != 0 {
		t.Errorf("GitHub polled %d times before the interval", f.pollCalls)
	}
	h.clock.add(5 * time.Second)
	if c := pollState(t, s); c.State != apitypes.GitHubStatePending || c.IntervalSeconds != 0 {
		t.Errorf("pending: %+v", c)
	}
	h.clock.add(5 * time.Second)
	if c := pollState(t, s); c.State != apitypes.GitHubStatePending || c.IntervalSeconds != 10 {
		t.Errorf("slow_down: %+v", c)
	}
	h.clock.add(5 * time.Second) // within the lengthened interval
	pollState(t, s)
	if f.pollCalls != 2 {
		t.Errorf("GitHub polled %d times, want 2 (slow_down respected)", f.pollCalls)
	}
	h.clock.add(5 * time.Second)
	c := pollState(t, s)
	if c.State != apitypes.GitHubStateConnected || c.Login != "octo" || c.TokenExpiresAt == nil ||
		!c.TokenExpiresAt.Equal(h.clock.now().Add(8*time.Hour)) || c.RefreshExpiresAt == nil || c.ConnectedAt == nil {
		t.Fatalf("connected: %+v", c)
	}
	if h.changed.Load() != 1 || published < 2 {
		t.Errorf("changed %d, published %d", h.changed.Load(), published)
	}
	// Stored sealed: the row holds no token in the clear, opens to both tokens and the account.
	row, err := h.store.Get(t.Context(), credentials.KindGitHub, "")
	if err != nil || row.Login != "octo" || bytes.Contains(row.Sealed, []byte("ghr_")) ||
		bytes.Contains(row.Sealed, []byte("ghu_")) {
		t.Fatalf("row %+v %v", row, err)
	}
	plain, _ := h.sealer.Open(row.Sealed)
	var tok credentials.GitHubTokens
	_ = json.Unmarshal(plain, &tok)
	if tok.RefreshToken != "ghr_refresh1" || tok.AccessToken != "ghu_access1" || tok.AccountID != 42 {
		t.Errorf("sealed %+v", tok)
	}
	if got := s.Token(t.Context()); got.State != flowdproto.GitHubConnected || got.Token != "ghu_access1" ||
		got.Login != "octo" {
		t.Errorf("token %+v", got)
	}
	// The flow is over: a poll finds none.
	if c := pollState(t, s); c.State != apitypes.GitHubStateDisconnected {
		t.Errorf("poll after connect: %+v", c)
	}
	// After a restart the stored connection is read back as it was, nothing renewed.
	if got := h.service().Token(t.Context()); got.Token != "ghu_access1" || f.refreshCalls.Load() != 0 {
		t.Errorf("after restart: %+v, refreshes %d", got, f.refreshCalls.Load())
	}
}

func TestDeviceFlow_errors(t *testing.T) {
	for answer, want := range map[string]apitypes.GitHubState{
		"access_denied": apitypes.GitHubStateDisconnected,
		"expired_token": apitypes.GitHubStateExpired,
		"token_expired": apitypes.GitHubStateExpired,
	} {
		t.Run(answer, func(t *testing.T) {
			h := newHarness(t)
			h.f.polls = []string{answer}
			call(t, h.s.StartDeviceFlow, http.MethodPost)
			h.clock.add(5 * time.Second)
			if c := pollState(t, h.s); c.State != want {
				t.Errorf("poll: %+v", c)
			}
			if c := pollState(t, h.s); c.State != apitypes.GitHubStateDisconnected {
				t.Errorf("poll after the end: %+v", c)
			}
			if h.changed.Load() != 0 || h.s.Token(t.Context()).State != flowdproto.GitHubDisconnected {
				t.Error("an unconfirmed flow connected")
			}
		})
	}
	t.Run("device_flow_disabled", func(t *testing.T) {
		h := newHarness(t)
		h.f.polls = []string{"device_flow_disabled"}
		call(t, h.s.StartDeviceFlow, http.MethodPost)
		h.clock.add(5 * time.Second)
		w := call(t, h.s.PollDeviceFlow, http.MethodPost)
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "device_flow_disabled") {
			t.Errorf("poll: %d %s", w.Code, w.Body)
		}
	})
	t.Run("code runs out", func(t *testing.T) {
		h := newHarness(t)
		call(t, h.s.StartDeviceFlow, http.MethodPost)
		h.clock.add(15 * time.Minute)
		if c := pollState(t, h.s); c.State != apitypes.GitHubStateExpired || h.f.pollCalls != 0 {
			t.Errorf("poll: %+v (GitHub asked %d)", c, h.f.pollCalls)
		}
		// A start near the end of a code makes a new one.
		call(t, h.s.StartDeviceFlow, http.MethodPost)
		h.clock.add(14*time.Minute + 30*time.Second)
		call(t, h.s.StartDeviceFlow, http.MethodPost)
		if h.f.deviceCalls != 3 {
			t.Errorf("device codes: %d", h.f.deviceCalls)
		}
	})
	t.Run("GitHub away", func(t *testing.T) {
		h := newHarness(t)
		call(t, h.s.StartDeviceFlow, http.MethodPost)
		h.f.srv.Close()
		h.clock.add(5 * time.Second)
		if c := pollState(t, h.s); c.State != apitypes.GitHubStatePending {
			t.Errorf("poll: %+v", c)
		}
	})
	t.Run("no flow", func(t *testing.T) {
		h := newHarness(t)
		if c := pollState(t, h.s); c.State != apitypes.GitHubStateDisconnected {
			t.Errorf("poll: %+v", c)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		h := newHarness(t)
		h.s.Client = nil
		for name, fn := range map[string]http.HandlerFunc{"start": h.s.StartDeviceFlow, "poll": h.s.PollDeviceFlow} {
			if w := call(t, fn, http.MethodPost); w.Code != http.StatusNotImplemented {
				t.Errorf("%s: %d", name, w.Code)
			}
		}
		if c := h.s.Connection(t.Context()); c.Available || c.State != apitypes.GitHubStateDisconnected {
			t.Errorf("connection %+v", c)
		}
		if b, _ := json.Marshal(h.s.Connection(t.Context())); !strings.Contains(string(b), `"available":false`) {
			t.Errorf("available not stated: %s", b)
		}
	})
}

func TestToken_renewal(t *testing.T) {
	h := newHarness(t)
	h.connect(t)
	h.changed.Store(0)
	// Not due yet: the token as it is, GitHub not asked.
	h.clock.add(7*time.Hour + 40*time.Minute)
	if got := h.s.Token(t.Context()); got.Token != "ghu_access1" || h.f.refreshCalls.Load() != 0 {
		t.Fatalf("before due: %+v", got)
	}
	// Due (15 minutes before the end): renewed once, the rotated refresh token stored, the change pushed.
	h.clock.add(10 * time.Minute)
	got := h.s.Token(t.Context())
	if got.State != flowdproto.GitHubConnected || got.Token != "ghu_access2" ||
		!got.ExpiresAt.Equal(h.clock.now().Add(8*time.Hour)) || h.f.refreshCalls.Load() != 1 || h.changed.Load() != 1 {
		t.Fatalf("renewed: %+v (refreshes %d, changed %d)", got, h.f.refreshCalls.Load(), h.changed.Load())
	}
	row, _ := h.store.Get(t.Context(), credentials.KindGitHub, "")
	plain, _ := h.sealer.Open(row.Sealed)
	var tok credentials.GitHubTokens
	_ = json.Unmarshal(plain, &tok)
	if tok.RefreshToken != "ghr_refresh2" || row.ExpiresAt == nil || !row.ExpiresAt.Equal(got.ExpiresAt) {
		t.Errorf("stored %+v %+v", tok, row)
	}
	if got := h.s.Token(t.Context()); got.Token != "ghu_access2" || h.f.refreshCalls.Load() != 1 {
		t.Errorf("again: %+v", got)
	}
	// The Run loop renews by itself.
	h.clock.add(7*time.Hour + 50*time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	h.s.Tick = time.Millisecond
	done := make(chan struct{})
	go func() { h.s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for h.f.refreshCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if h.f.refreshCalls.Load() != 2 || h.changed.Load() != 2 {
		t.Errorf("Run: refreshes %d, changed %d", h.f.refreshCalls.Load(), h.changed.Load())
	}
}

// Two renewals at once would burn each other's refresh token: one goes to GitHub, the rest wait for its result.
func TestToken_concurrent(t *testing.T) {
	h := newHarness(t)
	h.connect(t)
	h.clock.add(7*time.Hour + 50*time.Minute)
	h.f.mu.Lock()
	h.f.refreshDelay = 50 * time.Millisecond
	h.f.mu.Unlock()
	var wg sync.WaitGroup
	tokens := make(chan string, 20)
	for range 20 {
		wg.Go(func() { tokens <- h.s.Token(t.Context()).Token })
	}
	wg.Wait()
	close(tokens)
	for tok := range tokens {
		if tok != "ghu_access2" {
			t.Errorf("token %q", tok)
		}
	}
	if n := h.f.refreshCalls.Load(); n != 1 {
		t.Errorf("refreshes %d, want 1", n)
	}
}

// Flow died after GitHub rotated the refresh token and before the new one was stored: the stored one is burned.
// The connection is reported lost (reconnect needed) once, and never tried again.
func TestToken_crashWindow(t *testing.T) {
	h := newHarness(t)
	h.connect(t)
	h.f.mu.Lock()
	h.f.gen++
	h.f.refresh, h.f.access = "ghr_lost", "ghu_lost" // what the dead process got and never stored
	h.f.mu.Unlock()
	h.clock.add(7*time.Hour + 50*time.Minute)
	restarted := h.service()
	h.changed.Store(0)
	if got := restarted.Token(t.Context()); got.State != flowdproto.GitHubDisconnected || got.Token != "" {
		t.Fatalf("token %+v", got)
	}
	if h.changed.Load() != 1 {
		t.Errorf("changed %d", h.changed.Load())
	}
	for range 3 {
		restarted.Token(t.Context())
	}
	if got := h.service().Token(t.Context()); got.State != flowdproto.GitHubDisconnected {
		t.Errorf("after another restart: %+v", got)
	}
	if n := h.f.refreshCalls.Load(); n != 1 {
		t.Errorf("refreshes %d: a refused refresh token was tried again", n)
	}
	if c := restarted.Connection(t.Context()); c.State != apitypes.GitHubStateExpired || c.Login != "octo" {
		t.Errorf("connection %+v", c)
	}
	if !strings.Contains(h.logs.String(), "connect GitHub again") {
		t.Errorf("log %s", h.logs)
	}
}

// GitHub refuses the refresh token (revoked by the owner on GitHub, say): reconnect needed, published and pushed,
// counted, never tried again. A wrong client id is Flow's configuration and keeps the authorization.
func TestToken_rejected(t *testing.T) {
	h := newHarness(t)
	h.connect(t)
	published := 0
	h.s.Publish = func() { published++ }
	var failures []string
	h.s.RenewalFailed = func(class string) { failures = append(failures, class) }
	h.changed.Store(0)
	h.f.mu.Lock()
	h.f.refreshError = "incorrect_client_credentials"
	h.f.mu.Unlock()
	h.clock.add(7*time.Hour + 50*time.Minute)
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubConnected || got.Token != "ghu_access1" {
		t.Errorf("wrong client id: %+v", got)
	}
	if c := h.s.Connection(t.Context()); c.State != apitypes.GitHubStateConnected {
		t.Errorf("wrong client id lost the connection: %+v", c)
	}
	h.f.mu.Lock()
	h.f.refreshError = "bad_refresh_token"
	h.f.mu.Unlock()
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubDisconnected || got.Token != "" {
		t.Errorf("refused: %+v", got)
	}
	if c := h.s.Connection(t.Context()); c.State != apitypes.GitHubStateExpired {
		t.Errorf("connection %+v", c)
	}
	h.s.Token(t.Context())
	if n := h.f.refreshCalls.Load(); n != 2 || h.changed.Load() != 1 || published != 1 {
		t.Errorf("refreshes %d, changed %d, published %d", n, h.changed.Load(), published)
	}
	if strings.Join(failures, ",") != "incorrect_client_credentials,bad_refresh_token" {
		t.Errorf("failures %v", failures)
	}
	// Connecting again replaces the lost connection.
	h.f.mu.Lock()
	h.f.refreshError = ""
	h.f.mu.Unlock()
	h.connect(t)
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubConnected {
		t.Errorf("reconnected: %+v", got)
	}
}

func TestToken_transient(t *testing.T) {
	h := newHarness(t)
	h.connect(t)
	h.f.mu.Lock()
	h.f.refreshStatus = http.StatusBadGateway
	h.f.mu.Unlock()
	h.changed.Store(0)
	h.clock.add(7*time.Hour + 50*time.Minute)
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubConnected || got.Token != "ghu_access1" {
		t.Errorf("still valid: %+v", got)
	}
	h.clock.add(10 * time.Minute)
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubExpired || got.Token != "" {
		t.Errorf("past its end: %+v", got)
	}
	if c := h.s.Connection(t.Context()); c.State != apitypes.GitHubStateConnected {
		t.Errorf("a passing failure lost the connection: %+v", c)
	}
	h.f.mu.Lock()
	h.f.refreshStatus = 0
	h.f.mu.Unlock()
	if got := h.s.Token(t.Context()); got.Token != "ghu_access2" || h.changed.Load() != 1 {
		t.Errorf("recovered: %+v", got)
	}
}

// failingStore refuses Put while fail is set.
type failingStore struct {
	credentials.Store
	fail atomic.Bool
}

func (f *failingStore) Put(ctx context.Context, c credentials.Credential) error {
	if f.fail.Load() {
		return errors.New("disk full")
	}
	return f.Store.Put(ctx, c)
}

// The new refresh token is written before the new access token is handed out; a failed write keeps the pair in
// memory and is tried again, GitHub is not asked again with the burned token.
func TestToken_writeFails(t *testing.T) {
	h := newHarness(t)
	fs := &failingStore{Store: h.store}
	h.s.Store = fs
	h.connect(t)
	h.clock.add(7*time.Hour + 50*time.Minute)
	fs.fail.Store(true)
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubExpired || got.Token != "" {
		t.Fatalf("unstored: %+v", got)
	}
	if got := h.s.Token(t.Context()); got.Token != "" {
		t.Fatalf("handed out unstored: %+v", got)
	}
	h.changed.Store(0)
	fs.fail.Store(false)
	if got := h.s.Token(t.Context()); got.Token != "ghu_access2" || h.f.refreshCalls.Load() != 1 {
		t.Errorf("after the write: %+v, refreshes %d", got, h.f.refreshCalls.Load())
	}
	// Sandboxes were told "no token" meanwhile: the stored one is pushed.
	if h.changed.Load() != 1 {
		t.Errorf("changed %d after the write landed", h.changed.Load())
	}
	if got := h.service().Token(t.Context()); got.Token != "ghu_access2" {
		t.Errorf("stored: %+v", got)
	}
}

func TestDisconnect(t *testing.T) {
	h := newHarness(t)
	h.connect(t)
	h.changed.Store(0)
	if w := call(t, h.s.Disconnect, http.MethodDelete); w.Code != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body)
	}
	if _, err := h.store.Get(t.Context(), credentials.KindGitHub, ""); !errors.Is(err, credentials.ErrNotFound) {
		t.Errorf("row left: %v", err)
	}
	if got := h.s.Token(t.Context()); got.State != flowdproto.GitHubDisconnected || got.Token != "" {
		t.Errorf("token %+v", got)
	}
	if c := h.s.Connection(t.Context()); c.State != apitypes.GitHubStateDisconnected || c.Login != "" {
		t.Errorf("connection %+v", c)
	}
	if h.changed.Load() != 1 {
		t.Errorf("changed %d", h.changed.Load())
	}
	if w := call(t, h.s.Disconnect, http.MethodDelete); w.Code != http.StatusNoContent {
		t.Errorf("again: %d", w.Code)
	}
}

func TestListRepos(t *testing.T) {
	h := newHarness(t)
	list := func(q string) (int, []apitypes.GitHubRepo, string) {
		w := httptest.NewRecorder()
		h.s.ListRepos(w, httptest.NewRequest(http.MethodGet, "/api/github/repos?query="+q, nil))
		var out apitypes.GitHubRepoList
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Items, w.Body.String()
	}
	if code, _, body := list(""); code != http.StatusConflict || !strings.Contains(body, "github_not_connected") {
		t.Errorf("not connected: %d %s", code, body)
	}
	h.connect(t)
	code, items, body := list("")
	if code != 200 || len(items) != 3 || items[0].FullName != "octo/Alpha" || items[0].DefaultBranch != "trunk" ||
		!items[0].Private || items[0].Description != "first" || items[1].FullName != "octo/zeta" ||
		items[2].FullName != "org/mid" {
		t.Errorf("list: %d %s", code, body)
	}
	if _, items, _ := list("ALP"); len(items) != 1 || items[0].FullName != "octo/Alpha" {
		t.Errorf("query: %+v", items)
	}
	if strings.Contains(body, "ghu_") {
		t.Error("token in the answer")
	}
}

func TestDefaultBranch(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	var ae *apitypes.Error
	// Not connected: anonymous; a repository it cannot see may be private.
	if br, err := h.s.DefaultBranch(ctx, "public/repo"); br != "master" || err != nil {
		t.Errorf("public: %q %v", br, err)
	}
	if _, err := h.s.DefaultBranch(ctx, "octo/zeta"); !errors.As(err, &ae) ||
		ae.Code != apitypes.ErrorCodeGithubNotConnected {
		t.Errorf("private, not connected: %v", err)
	}
	h.connect(t)
	if br, err := h.s.DefaultBranch(ctx, "octo/Alpha"); br != "trunk" || err != nil {
		t.Errorf("private: %q %v", br, err)
	}
	// Outside the installation (404 to the token) but public: anonymous.
	if br, err := h.s.DefaultBranch(ctx, "public/repo"); br != "master" || err != nil {
		t.Errorf("public, connected: %q %v", br, err)
	}
	if _, err := h.s.DefaultBranch(ctx, "nobody/nothing"); !errors.As(err, &ae) ||
		ae.Code != apitypes.ErrorCodeInvalidRequest {
		t.Errorf("unknown, connected: %v", err)
	}
}

type noSandboxes struct{ setups []string }

func (n *noSandboxes) Setup(id string) { n.setups = append(n.setups, id) }
func (n *noSandboxes) Describe(context.Context, string) apitypes.Sandbox {
	return apitypes.Sandbox{State: apitypes.SandboxStateNone}
}
func (n *noSandboxes) Delete(context.Context, string, func(context.Context) error) error { return nil }

// A project with a private repository: refused while GitHub is not connected (the UI offers to connect), created
// with the repository's default branch once connected. flowd clones it through the credential helper.
func TestCreateProject_private(t *testing.T) {
	h := newHarness(t)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sb := &noSandboxes{}
	a := &projects.API{Store: projects.SQL{DB: db}, Sandboxes: sb, DefaultBranch: h.s.DefaultBranch}
	create := func(name string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/projects",
			strings.NewReader(`{"name":"`+name+`","repos":["https://github.com/octo/Alpha.git"]}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.CreateProject(w, r)
		return w
	}
	if w := create("before"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "github_not_connected") {
		t.Errorf("not connected: %d %s", w.Code, w.Body)
	}
	h.connect(t)
	w := create("demo")
	var p apitypes.Project
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != http.StatusCreated || len(p.Repos) != 1 || p.Repos[0].FullName != "octo/Alpha" ||
		p.Repos[0].DefaultBranch != "trunk" || len(sb.setups) != 1 {
		t.Errorf("connected: %d %s", w.Code, w.Body)
	}
}

// The token in the set flowd gets: Env carries the user token and its account, never the refresh token.
func TestEnv(t *testing.T) {
	h := newHarness(t)
	creds := &credentials.Service{Store: h.store, Sealer: h.sealer, GitHub: h.s,
		Log: slog.New(slog.NewTextHandler(h.logs, nil))}
	if e, err := creds.Env(t.Context()); err != nil || e.GitHubState != flowdproto.GitHubDisconnected ||
		e.GitHubToken != "" {
		t.Errorf("not connected: %+v %v", e, err)
	}
	h.connect(t)
	e, err := creds.Env(t.Context())
	if err != nil || e.GitHubState != flowdproto.GitHubConnected || e.GitHubToken != "ghu_access1" ||
		e.GitHubLogin != "octo" || !e.GitHubTokenExpiresAt.Equal(h.clock.now().Add(8*time.Hour)) {
		t.Errorf("connected: %+v %v", e, err)
	}
	if b, _ := json.Marshal(e); strings.Contains(string(b), "ghr_") {
		t.Errorf("refresh token in the set: %s", b)
	}
	// The GitHub row is not an API key.
	if keys, _ := creds.Keys(t.Context()); len(keys) != 0 {
		t.Errorf("keys %+v", keys)
	}
}
