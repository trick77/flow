package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trick77/flow/internal/apitypes"
	"github.com/trick77/flow/internal/credentials"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/httpapi"
	"github.com/trick77/flow/internal/store"
)

// Defaults of a Service.
const (
	// DefaultLead: renew this long before the 8-hour token ends. Short on purpose: GitHub invalidates the old access
	// token with every refresh, so each renewal cuts off whatever still holds the old one until the push arrives;
	// renewing once per token's life at its end keeps that to once in 8 hours. 15 minutes leave room for retries
	// when GitHub is briefly away.
	DefaultLead = 15 * time.Minute
	// DefaultTick is how often Run checks whether the token is due.
	DefaultTick = time.Minute
	// DefaultRepush is how often Run pushes the set while connected, changed or not.
	DefaultRepush = 10 * time.Minute
	// callTimeout bounds one call to GitHub.
	callTimeout = 30 * time.Second
	// maxRepos bounds listGitHubRepos (the schema has no paging), maxInstallationPages the installations read.
	maxRepos             = 1000
	maxInstallationPages = 10
	// reuseFlow: a start while a device code has more than this left hands out the same code.
	reuseFlow = time.Minute
)

// Service is the GitHub App area: the device flow, the one user token (renewed single-flight, the rotating refresh
// token stored before a new access token is handed out), the repository listing and the default-branch lookup. It
// implements credentials.GitHub. Tokens never reach a log, an error or an API answer.
type Service struct {
	// Client is nil when no App is configured: the connection is reported unavailable.
	Client *Client
	Store  credentials.Store
	Sealer credentials.Sealer
	Log    *slog.Logger
	Now    func() time.Time
	// Changed: what sandboxes should hold changed (a renewal, a connect, a disconnect, a refused refresh token):
	// push the credential set to warm sandboxes.
	Changed func()
	// Publish: the connection's state changed (connections.updated).
	Publish func()
	// RenewalFailed counts a failed renewal (metering's credential_renewal_failure) by class: GitHub's OAuth code,
	// else "transient". nil: not counted.
	RenewalFailed func(class string)
	Lead          time.Duration
	Tick          time.Duration
	Repush        time.Duration
	// Anonymous finds a default branch without a token (projects.RemoteDefaultBranch): public repositories, and
	// every lookup while not connected.
	Anonymous func(ctx context.Context, fullName string) (string, error)

	// mu guards the token: loading, renewing, connecting and disconnecting all hold it, so two renewals never use
	// one refresh token and a disconnect never races a renewal's write.
	mu     sync.Mutex
	loaded bool
	cur    *record
	// unsaved: cur holds what GitHub handed out but the store does not have yet. Nothing is handed out until it is
	// written; the write is tried again on every use, GitHub is never asked again with the old refresh token.
	unsaved bool

	flowMu sync.Mutex
	flow   *deviceFlow
}

var _ credentials.GitHub = (*Service)(nil)

// record is the connection: the credentials row's id, account and tokens.
type record struct {
	id        string
	login     string
	tokens    credentials.GitHubTokens
	createdAt time.Time
}

// lost: GitHub refused the refresh token (or it ran out); the owner connects again.
func (r *record) lost() bool { return r.tokens.AccessToken == "" && r.tokens.RefreshToken == "" }

type deviceFlow struct {
	deviceCode, userCode, uri string
	expiresAt, next           time.Time
	interval                  time.Duration
	slowed                    bool
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) lead() time.Duration { return cmp.Or(s.Lead, DefaultLead) }

func (s *Service) notify() {
	if s.Changed != nil {
		s.Changed()
	}
	if s.Publish != nil {
		s.Publish()
	}
}

// load reads the stored connection once; callers hold mu. A row that does not open with this seal key counts as
// none (connecting again replaces it).
func (s *Service) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	c, err := s.Store.Get(ctx, credentials.KindGitHub, "")
	if errors.Is(err, credentials.ErrNotFound) {
		s.loaded, s.cur = true, nil
		return nil
	}
	if err != nil {
		return err
	}
	s.loaded, s.cur = true, nil
	t, ok := s.open(c.Sealed)
	if !ok {
		s.log().Warn("stored GitHub connection does not open with this seal key; connect GitHub again", "id", c.ID)
		return nil
	}
	s.cur = &record{id: c.ID, login: c.Login, tokens: t, createdAt: c.CreatedAt}
	return nil
}

// open unseals a stored connection's tokens.
func (s *Service) open(sealed []byte) (credentials.GitHubTokens, bool) {
	var t credentials.GitHubTokens
	plain, err := s.Sealer.Open(sealed)
	return t, err == nil && json.Unmarshal(plain, &t) == nil
}

// persist writes cur sealed; callers hold mu.
func (s *Service) persist(ctx context.Context) error {
	r := s.cur
	plain, err := json.Marshal(r.tokens) //nolint:gosec // sealed on the next line, never stored or logged plain
	if err != nil {
		return err
	}
	sealed, err := s.Sealer.Seal(plain)
	if err != nil {
		return errors.New("github: sealing failed")
	}
	c := credentials.Credential{ID: r.id, Kind: credentials.KindGitHub, Label: r.login, Sealed: sealed,
		Login: r.login, CreatedAt: r.createdAt, UpdatedAt: s.now().UTC()}
	if !r.tokens.AccessExpiresAt.IsZero() {
		c.ExpiresAt = &r.tokens.AccessExpiresAt
	}
	if !r.tokens.RefreshExpiresAt.IsZero() {
		c.RefreshExpiresAt = &r.tokens.RefreshExpiresAt
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	if err := s.Store.Put(ctx, c); err != nil {
		return err
	}
	s.unsaved = false
	return nil
}

// Token implements credentials.GitHub.
func (s *Service) Token(ctx context.Context) credentials.GitHubToken {
	s.mu.Lock()
	t, changed := s.token(ctx)
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	return t
}

// token is Token under mu; changed says what sandboxes hold must be replaced (and the state republished).
func (s *Service) token(ctx context.Context) (t credentials.GitHubToken, changed bool) {
	t.State = flowdproto.GitHubDisconnected
	if s.Client == nil {
		return t, false
	}
	if err := s.load(ctx); err != nil {
		s.log().Error("reading the GitHub connection", "err", err)
		t.State = flowdproto.GitHubExpired
		return t, false
	}
	r := s.cur
	if r == nil {
		return t, false
	}
	t.Login = r.login
	if r.lost() {
		return t, false
	}
	if s.unsaved {
		if err := s.persist(ctx); err != nil {
			s.log().Error("storing the renewed GitHub token", "err", err)
			t.State = flowdproto.GitHubExpired
			return t, false
		}
		// Sandboxes were told there is no token while the write was pending: push the one now stored.
		changed = true
	}
	now := s.now()
	exp := r.tokens.AccessExpiresAt
	if exp.IsZero() || now.Before(exp.Add(-s.lead())) {
		t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, exp
		return t, changed
	}
	if r.tokens.RefreshToken == "" || (!r.tokens.RefreshExpiresAt.IsZero() && !now.Before(r.tokens.RefreshExpiresAt)) {
		if now.Before(exp) {
			t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, exp
			return t, changed
		}
		s.markLost(ctx, "the GitHub authorization ran out")
		return t, true
	}
	// Not the caller's context: a renewal cut off after GitHub answered would lose the only valid refresh token.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	nt, err := s.Client.Refresh(rctx, r.tokens.RefreshToken)
	cancel()
	if err != nil {
		var oe *OAuthError
		if s.RenewalFailed != nil {
			class := "transient"
			if errors.As(err, &oe) {
				class = oe.Code
			}
			s.RenewalFailed(class)
		}
		if Transient(err) || (errors.As(err, &oe) && oe.Code == "incorrect_client_credentials") {
			// incorrect_client_credentials is Flow's configuration (a wrong BACKEND_GITHUB_CLIENT_ID), not the
			// authorization: it is kept for when the setting is fixed.
			s.log().Warn("renewing the GitHub token failed; trying again", "err", err)
			if now.Before(exp) {
				t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, exp
				return t, changed
			}
			t.State = flowdproto.GitHubExpired
			return t, changed
		}
		s.markLost(ctx, "GitHub refused the refresh token")
		return t, true
	}
	r.tokens = tokens(nt, now, r.tokens.AccountID)
	s.unsaved = true
	if err := s.persist(ctx); err != nil {
		// The old token is dead now: sandboxes must drop it, the new one waits for the write.
		s.log().Error("storing the renewed GitHub token", "err", err)
		t.State = flowdproto.GitHubExpired
		return t, true
	}
	s.log().Info("GitHub token renewed", "login", r.login, "expiresAt", r.tokens.AccessExpiresAt)
	t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, r.tokens.AccessExpiresAt
	return t, true
}

// markLost keeps the connection's account but drops its tokens: the owner connects again. Never retried with the
// old refresh token. Callers hold mu.
func (s *Service) markLost(ctx context.Context, why string) {
	s.cur.tokens = credentials.GitHubTokens{AccountID: s.cur.tokens.AccountID}
	s.unsaved = true
	s.log().Warn(why+"; connect GitHub again in Settings", "login", s.cur.login)
	if err := s.persist(ctx); err != nil {
		s.log().Error("storing the lost GitHub connection", "err", err)
	}
}

// tokens turns GitHub's answer into what is stored, its lifetimes counted from now.
func tokens(t Token, now time.Time, account int64) credentials.GitHubTokens {
	out := credentials.GitHubTokens{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, AccountID: account}
	if t.ExpiresIn > 0 {
		out.AccessExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
	}
	if t.RefreshTokenExpiresIn > 0 {
		out.RefreshExpiresAt = now.Add(time.Duration(t.RefreshTokenExpiresIn) * time.Second).UTC()
	}
	return out
}

// Run renews the token when it is due and pushes the new one (Token's notify), until ctx ends. Push-driven on
// purpose: flowd cannot dial Flow, so a sandbox gets a renewed token only when Flow pushes it. While connected it
// also pushes every Repush: a warm sandbox that missed a push (flowd restarting just then, a network blip) would
// otherwise hold a token the renewal killed until the next renewal, hours later.
func (s *Service) Run(ctx context.Context) {
	if s.Client == nil {
		return
	}
	t := time.NewTicker(cmp.Or(s.Tick, DefaultTick))
	defer t.Stop()
	last := s.now()
	for {
		if tok := s.Token(ctx); tok.State == flowdproto.GitHubConnected && s.now().Sub(last) >= cmp.Or(s.Repush,
			DefaultRepush) && s.Changed != nil {
			last = s.now()
			s.Changed()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Connection implements credentials.GitHub.
func (s *Service) Connection(ctx context.Context) apitypes.GitHubConnection {
	c := apitypes.GitHubConnection{State: apitypes.GitHubStateDisconnected, Available: s.Client != nil}
	s.mu.Lock()
	err := s.load(ctx)
	var r record
	has := s.cur != nil
	if has {
		r = *s.cur
	}
	s.mu.Unlock()
	if err != nil {
		s.log().Error("reading the GitHub connection", "err", err)
	}
	if !has {
		s.flowMu.Lock()
		if s.Client != nil && s.flow != nil && s.now().Before(s.flow.expiresAt) {
			c.State = apitypes.GitHubStatePending
		}
		s.flowMu.Unlock()
		return c
	}
	c.Login = r.login
	created := r.createdAt
	c.ConnectedAt = &created
	if r.lost() || s.Client == nil {
		c.State = apitypes.GitHubStateExpired
		return c
	}
	c.State = apitypes.GitHubStateConnected
	if exp := r.tokens.AccessExpiresAt; !exp.IsZero() {
		c.TokenExpiresAt = &exp
	}
	if exp := r.tokens.RefreshExpiresAt; !exp.IsZero() {
		c.RefreshExpiresAt = &exp
	}
	return c
}

func upstream(detail string) error { return apitypes.Fail(apitypes.ErrorCodeUpstream, detail) }

// detail is what an API error may say about a GitHub failure: GitHub's OAuth code or the status, never more.
func detail(err error) string {
	var oe *OAuthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Error()
	}
	return "github not reachable"
}

// StartDeviceFlow implements credentials.GitHub: one flow at a time; a start while a code is out hands out the
// same code, so the route cannot be used to make GitHub issue codes.
func (s *Service) StartDeviceFlow(w http.ResponseWriter, r *http.Request) {
	if s.Client == nil {
		httpapi.NotImplemented{}.StartGitHubDeviceFlow(w, r)
		return
	}
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	now := s.now()
	if f := s.flow; f == nil || !now.Before(f.expiresAt.Add(-reuseFlow)) {
		ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
		dc, err := s.Client.RequestDeviceCode(ctx)
		cancel()
		if err != nil {
			s.log().Warn("GitHub device flow start failed", "err", err)
			httpapi.WriteError(w, upstream(detail(err)))
			return
		}
		interval := time.Duration(max(dc.Interval, 5)) * time.Second
		expires := time.Duration(cmp.Or(dc.ExpiresIn, 900)) * time.Second
		s.flow = &deviceFlow{deviceCode: dc.DeviceCode, userCode: dc.UserCode, uri: dc.VerificationURI,
			expiresAt: now.Add(expires), next: now.Add(interval), interval: interval}
		if s.Publish != nil {
			s.Publish()
		}
	}
	f := s.flow
	httpapi.WriteJSON(w, http.StatusOK, apitypes.DeviceFlow{UserCode: f.userCode, VerificationURI: f.uri,
		ExpiresAt: f.expiresAt.UTC(), IntervalSeconds: int(f.interval / time.Second)})
}

// PollDeviceFlow implements credentials.GitHub. GitHub is asked at most once per its interval (longer after a
// slow_down), whatever the browser does; in between the answer is pending from memory. The device code never
// leaves Flow.
func (s *Service) PollDeviceFlow(w http.ResponseWriter, r *http.Request) {
	if s.Client == nil {
		httpapi.NotImplemented{}.PollGitHubDeviceFlow(w, r)
		return
	}
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	answer := func(state apitypes.GitHubState) {
		c := apitypes.GitHubConnection{State: state, Available: true}
		if s.flow != nil && s.flow.slowed {
			c.IntervalSeconds = int(s.flow.interval / time.Second)
		}
		httpapi.WriteJSON(w, http.StatusOK, c)
	}
	f, now := s.flow, s.now()
	switch {
	case f == nil:
		answer(apitypes.GitHubStateDisconnected)
		return
	case !now.Before(f.expiresAt):
		s.flow = nil
		answer(apitypes.GitHubStateExpired)
		return
	case now.Before(f.next):
		answer(apitypes.GitHubStatePending)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), callTimeout)
	defer cancel()
	tok, err := s.Client.PollDeviceToken(ctx, f.deviceCode)
	var oe *OAuthError
	switch {
	case err == nil:
	case errors.As(err, &oe) && oe.Code == "authorization_pending":
		f.next = now.Add(f.interval)
		answer(apitypes.GitHubStatePending)
		return
	case errors.As(err, &oe) && oe.Code == "slow_down":
		f.interval = max(f.interval+5*time.Second, time.Duration(oe.Interval)*time.Second)
		f.slowed, f.next = true, now.Add(f.interval)
		answer(apitypes.GitHubStatePending)
		return
	case errors.As(err, &oe) && (oe.Code == "expired_token" || oe.Code == "token_expired"):
		s.flow = nil
		answer(apitypes.GitHubStateExpired)
		return
	case errors.As(err, &oe) && oe.Code == "access_denied":
		s.flow = nil
		answer(apitypes.GitHubStateDisconnected)
		return
	case errors.As(err, &oe):
		// incorrect_device_code, device_flow_disabled, incorrect_client_credentials, ...: this code is done.
		s.flow = nil
		s.log().Warn("GitHub device flow failed", "err", err)
		httpapi.WriteError(w, upstream(detail(err)))
		return
	default:
		// GitHub away for a moment: the code stays valid, the next poll asks again.
		s.log().Warn("GitHub device flow poll failed; trying again", "err", err)
		f.next = now.Add(f.interval)
		answer(apitypes.GitHubStatePending)
		return
	}
	s.flow = nil
	u, err := s.Client.User(ctx, tok.AccessToken)
	if err != nil {
		s.log().Warn("reading the connected GitHub account failed", "err", err)
		httpapi.WriteError(w, upstream(detail(err)))
		return
	}
	if err := s.connect(ctx, u, tok); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	s.notify()
	httpapi.WriteJSON(w, http.StatusOK, s.Connection(r.Context()))
}

// connect replaces the stored connection with a new one.
func (s *Service) connect(ctx context.Context, u User, tok Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cur = &record{id: store.NewID(), login: u.Login, tokens: tokens(tok, now, u.ID), createdAt: now.UTC()}
	s.loaded, s.unsaved = true, true
	if tok.RefreshToken == "" && tok.ExpiresIn > 0 {
		s.log().Warn("GitHub handed out an expiring token without a refresh token; check the App's settings")
	}
	if err := s.persist(ctx); err != nil {
		// Kept in memory and written on the next use (token).
		s.log().Error("storing the GitHub connection", "err", err)
		return err
	}
	s.log().Info("GitHub connected", "login", u.Login)
	return nil
}

// Disconnect implements credentials.GitHub: the stored tokens go and sandboxes get the set without a token. No
// revoke call: GitHub's revoke endpoints (DELETE /applications/{client_id}/token and /grant) need the client
// secret, which Flow does not hold; the owner revokes the authorization on GitHub if wanted.
func (s *Service) Disconnect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	err := s.load(r.Context())
	if err == nil && s.cur != nil {
		err = s.Store.Delete(r.Context(), s.cur.id)
		if errors.Is(err, credentials.ErrNotFound) {
			err = nil
		}
	}
	if err == nil {
		s.cur, s.unsaved = nil, false
	}
	s.mu.Unlock()
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	s.flowMu.Lock()
	s.flow = nil
	s.flowMu.Unlock()
	s.log().Info("GitHub disconnected")
	s.notify()
	w.WriteHeader(http.StatusNoContent)
}

// connected is the token for Flow's own calls, or an API error saying why there is none.
func (s *Service) connected(ctx context.Context) (string, error) {
	t := s.Token(ctx)
	switch t.State {
	case flowdproto.GitHubConnected:
		return t.Token, nil
	case flowdproto.GitHubExpired:
		return "", upstream("github token not renewed")
	}
	return "", apitypes.Fail(apitypes.ErrorCodeGithubNotConnected, "")
}

// ListRepos implements credentials.GitHub: every repository of every installation the owner can see, at most
// maxRepos, sorted by name, filtered by ?query (a case-insensitive part of owner/name).
func (s *Service) ListRepos(w http.ResponseWriter, r *http.Request) {
	if s.Client == nil {
		httpapi.WriteError(w, apitypes.Fail(apitypes.ErrorCodeGithubNotConnected, ""))
		return
	}
	repos, err := s.repos(r.Context())
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	out := apitypes.GitHubRepoList{Items: []apitypes.GitHubRepo{}}
	for _, rp := range repos {
		if q == "" || strings.Contains(strings.ToLower(rp.FullName), q) {
			out.Items = append(out.Items, apitypes.GitHubRepo{FullName: rp.FullName, DefaultBranch: rp.DefaultBranch,
				Private: rp.Private, Description: rp.Description})
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) repos(ctx context.Context) ([]Repo, error) {
	token, err := s.connected(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*callTimeout)
	defer cancel()
	inst, err := s.Client.Installations(ctx, token, maxInstallationPages)
	if err != nil {
		s.log().Warn("listing GitHub installations failed", "err", err)
		return nil, upstream(detail(err))
	}
	seen := map[string]bool{}
	var out []Repo
	for _, in := range inst {
		if len(out) >= maxRepos {
			break
		}
		list, err := s.Client.InstallationRepos(ctx, token, in.ID, maxRepos-len(out))
		if err != nil {
			s.log().Warn("listing GitHub repositories failed", "err", err)
			return nil, upstream(detail(err))
		}
		for _, rp := range list {
			if k := strings.ToLower(rp.FullName); !seen[k] {
				seen[k] = true
				out = append(out, rp)
			}
		}
	}
	slices.SortFunc(out, func(a, b Repo) int {
		return strings.Compare(strings.ToLower(a.FullName), strings.ToLower(b.FullName))
	})
	return out, nil
}

// statusError is an anonymous lookup's failure that knows its HTTP status (projects.StatusError).
type statusError interface{ HTTPStatus() int }

// DefaultBranch finds a repository's default branch for a new project: with the connection through the REST API
// (private repositories included), else, and for a public repository the installation does not cover (404),
// anonymously. A repository neither way finds is github_not_connected while not connected (it may be private),
// else invalid_request.
func (s *Service) DefaultBranch(ctx context.Context, fullName string) (string, error) {
	connected := false
	if s.Client != nil {
		if t := s.Token(ctx); t.State == flowdproto.GitHubConnected {
			connected = true
			rctx, cancel := context.WithTimeout(ctx, callTimeout)
			rp, err := s.Client.Repo(rctx, t.Token, fullName)
			cancel()
			var se *StatusError
			switch {
			case err == nil && rp.DefaultBranch != "":
				return rp.DefaultBranch, nil
			case errors.As(err, &se) && se.Status == http.StatusNotFound:
			case err != nil:
				return "", upstream(detail(err))
			}
		}
	}
	br, err := s.Anonymous(ctx, fullName)
	var st statusError
	if err != nil && errors.As(err, &st) && (st.HTTPStatus() == http.StatusUnauthorized ||
		st.HTTPStatus() == http.StatusNotFound) {
		if connected {
			return "", &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "repos", Detail: fullName}
		}
		return "", &apitypes.Error{Code: apitypes.ErrorCodeGithubNotConnected, Field: "repos", Detail: fullName}
	}
	return br, err
}
