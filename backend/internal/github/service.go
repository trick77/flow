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
	// refusalPause: after a refresh refusal with a code Flow does not know, GitHub is not asked again before this.
	refusalPause = 5 * time.Minute
	// accountWait: a confirmed code whose account read keeps failing (GitHub's API away) is given up after this,
	// counted from the confirmation; a 4xx from /user ends it at once.
	accountWait = 10 * time.Minute
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

	// mu guards the token: loading, connecting and disconnecting hold it; a renewal holds it except during its call
	// to GitHub, which renewing marks, so two renewals never use one refresh token. A renewal's answer is applied
	// only while cur is still the record it renewed: a disconnect or connect during the call wins.
	mu     sync.Mutex
	loaded bool
	cur    *record
	// unsaved: cur holds what GitHub handed out but the store does not have yet. Nothing is handed out until it is
	// written; the write is tried again on every use, GitHub is never asked again with the old refresh token.
	unsaved bool
	// retryAt: no renewal before then (refusalPause after an unknown refusal code).
	retryAt time.Time
	// renewing is closed when the renewal under way ends (nil: none). Callers wait for it only while the last
	// renewal did not fail: a successful one kills the old token. failing: the last renewal failed and was kept;
	// then only Run tries again (every tick), others get the kept token at once, and try themselves only after
	// backoff (one tick), for a Service whose Run is not running.
	renewing chan struct{}
	failing  bool
	backoff  time.Time

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
	// tok: GitHub confirmed the code (at confirmed), the account is not read yet.
	tok       *Token
	confirmed time.Time
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
func (s *Service) Token(ctx context.Context) credentials.GitHubToken { return s.get(ctx, false) }

// get is Token; run says the caller is Run, which tries a failed renewal again on every tick.
func (s *Service) get(ctx context.Context, run bool) credentials.GitHubToken {
	s.mu.Lock()
	t, changed := s.token(ctx, run)
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	return t
}

// token is get under mu (released only during a renewal's call to GitHub); changed says what sandboxes hold must
// be replaced (and the state republished).
func (s *Service) token(ctx context.Context, run bool) (credentials.GitHubToken, bool) {
	changed := false
	for {
		t, done, ch := s.tokenOnce(ctx, run)
		changed = changed || ch
		if done {
			return t, changed
		}
	}
}

// tokenOnce is one look at the token; done false: a renewal ended or changed hands meanwhile, look again.
func (s *Service) tokenOnce(ctx context.Context, run bool) (t credentials.GitHubToken, done, changed bool) {
	t.State = flowdproto.GitHubDisconnected
	if s.Client == nil {
		return t, true, false
	}
	if err := s.load(ctx); err != nil {
		s.log().Error("reading the GitHub connection", "err", err)
		t.State = flowdproto.GitHubExpired
		return t, true, false
	}
	r := s.cur
	if r == nil {
		return t, true, false
	}
	t.Login = r.login
	if s.unsaved {
		// Before the lost check: a lost connection whose write failed is stored now, so a restart never sends the
		// refused refresh token again.
		if err := s.persist(ctx); err != nil {
			s.log().Error("storing the renewed GitHub token", "err", err)
			if !r.lost() {
				t.State = flowdproto.GitHubExpired
			}
			return t, true, false
		}
		// Sandboxes were told there is no token while the write was pending: push the one now stored (a lost one
		// was pushed when it was lost).
		changed = !r.lost()
	}
	if r.lost() {
		return t, true, changed
	}
	now := s.now()
	exp := r.tokens.AccessExpiresAt
	if exp.IsZero() || now.Before(exp.Add(-s.lead())) {
		t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, exp
		return t, true, changed
	}
	if r.tokens.RefreshToken == "" || (!r.tokens.RefreshExpiresAt.IsZero() && !now.Before(r.tokens.RefreshExpiresAt)) {
		if now.Before(exp) {
			t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, exp
			return t, true, changed
		}
		s.markLost(ctx, "the GitHub authorization ran out")
		return t, true, true
	}
	// keep hands out the current token while it is valid: the authorization stays, the renewal is tried again.
	keep := func() (credentials.GitHubToken, bool, bool) {
		if now.Before(exp) {
			t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, exp
			return t, true, changed
		}
		t.State = flowdproto.GitHubExpired
		return t, true, changed
	}
	if wait := s.renewing; wait != nil {
		if s.failing {
			return keep() // GitHub was away at the last try: nobody waits on this one
		}
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
		}
		s.mu.Lock()
		if ctx.Err() != nil {
			// Gone before the renewal ended: what was looked at before may be dead or disconnected by now.
			t.State = flowdproto.GitHubExpired
			return t, true, changed
		}
		return t, false, changed
	}
	if now.Before(s.retryAt) || (s.failing && !run && now.Before(s.backoff)) {
		return keep()
	}
	// Not the caller's context: a renewal cut off after GitHub answered would lose the only valid refresh token.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	wait := make(chan struct{})
	s.renewing = wait
	s.mu.Unlock()
	nt, err := s.Client.Refresh(rctx, r.tokens.RefreshToken)
	cancel()
	s.mu.Lock()
	s.renewing = nil
	close(wait)
	if s.cur != r {
		// Disconnected or connected again during the call: the answer is not for the connection held now.
		return t, false, changed
	}
	if err != nil {
		var oe *OAuthError
		if s.RenewalFailed != nil {
			class := "transient"
			if errors.As(err, &oe) {
				class = oe.Code
			}
			s.RenewalFailed(class)
		}
		if Refused(err) {
			s.failing = false
			s.markLost(ctx, "GitHub refused the refresh token")
			return t, true, true
		}
		s.failing, s.backoff = true, now.Add(cmp.Or(s.Tick, DefaultTick))
		if !Transient(err) && errors.As(err, &oe) && oe.Code != "incorrect_client_credentials" {
			// A code Flow does not know is never taken as a refusal (that would drop a refresh token that may still
			// be good): kept, and GitHub is asked again only after a pause. incorrect_client_credentials is Flow's
			// configuration (a wrong BACKEND_GITHUB_CLIENT_ID), not the authorization: kept for when it is fixed.
			s.retryAt = now.Add(refusalPause)
			s.log().Warn("GitHub answered the renewal with an unknown error; keeping the authorization, trying again later",
				"code", oe.Code, "retryAt", s.retryAt)
			return keep()
		}
		s.log().Warn("renewing the GitHub token failed; trying again", "err", err)
		return keep()
	}
	s.failing = false
	r.tokens = tokens(nt, now, r.tokens.AccountID)
	s.unsaved = true
	if err := s.persist(ctx); err != nil {
		// The old token is dead now: sandboxes must drop it, the new one waits for the write.
		s.log().Error("storing the renewed GitHub token", "err", err)
		t.State = flowdproto.GitHubExpired
		return t, true, true
	}
	s.log().Info("GitHub token renewed", "login", r.login, "expiresAt", r.tokens.AccessExpiresAt)
	t.State, t.Token, t.ExpiresAt = flowdproto.GitHubConnected, r.tokens.AccessToken, r.tokens.AccessExpiresAt
	return t, true, true
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
		if tok := s.get(ctx, true); tok.State == flowdproto.GitHubConnected && s.now().Sub(last) >= cmp.Or(s.Repush,
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
	c, has := s.connection(ctx)
	if !has {
		s.flowMu.Lock()
		if f, now := s.flow, s.now(); s.Client != nil && f != nil &&
			(f.tok != nil && now.Before(f.confirmed.Add(accountWait)) || now.Before(f.expiresAt)) {
			c.State = apitypes.GitHubStatePending
		}
		s.flowMu.Unlock()
	}
	return c
}

// connection is the stored connection's state, the device flow left out; has says whether there is one. It never
// takes flowMu: PollDeviceFlow answers with it while holding that lock.
func (s *Service) connection(ctx context.Context) (c apitypes.GitHubConnection, has bool) {
	c = apitypes.GitHubConnection{State: apitypes.GitHubStateDisconnected, Available: s.Client != nil}
	s.mu.Lock()
	err := s.load(ctx)
	var r record
	has = s.cur != nil
	if has {
		r = *s.cur
	}
	s.mu.Unlock()
	if err != nil {
		s.log().Error("reading the GitHub connection", "err", err)
	}
	if !has {
		return c, false
	}
	c.Login = r.login
	created := r.createdAt
	c.ConnectedAt = &created
	if r.lost() || s.Client == nil {
		c.State = apitypes.GitHubStateExpired
		return c, true
	}
	c.State = apitypes.GitHubStateConnected
	if exp := r.tokens.AccessExpiresAt; !exp.IsZero() {
		c.TokenExpiresAt = &exp
	}
	if exp := r.tokens.RefreshExpiresAt; !exp.IsZero() {
		c.RefreshExpiresAt = &exp
	}
	return c, true
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
		ExpiresAt: f.expiresAt.UTC(), ExpiresInSeconds: max(0, int(f.expiresAt.Sub(now)/time.Second)),
		IntervalSeconds: int(f.interval / time.Second)})
}

// PollDeviceFlow implements credentials.GitHub. GitHub is asked at most once per its interval (longer after a
// slow_down), whatever the browser does; in between the answer is pending from memory. The device code never
// leaves Flow.
func (s *Service) PollDeviceFlow(w http.ResponseWriter, r *http.Request) {
	if s.Client == nil {
		httpapi.NotImplemented{}.PollGitHubDeviceFlow(w, r)
		return
	}
	if !s.poll(w, r) {
		return
	}
	// Connected: pushed and answered outside the flow's lock, so a Disconnect waiting for it is never held up by the
	// push. A disconnect landing in between is what the answer then says: no code out, never a denial.
	s.notify()
	c, _ := s.connection(r.Context())
	c.NoCode = c.State != apitypes.GitHubStateConnected
	httpapi.WriteJSON(w, http.StatusOK, c)
}

// poll is PollDeviceFlow under flowMu; connected true: the code connected GitHub, nothing is answered yet.
func (s *Service) poll(w http.ResponseWriter, r *http.Request) (connected bool) {
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
		// No code out (over: a restart, another tab's poll ended it, a disconnect): the connection as it stands, so
		// a confirmed one never reads as denied, marked noCode, so a disconnected one does not either.
		c, _ := s.connection(r.Context())
		c.NoCode = true
		httpapi.WriteJSON(w, http.StatusOK, c)
		return false
	case !now.Before(f.expiresAt) && f.tok == nil:
		// A confirmed code outlives its expiry: the tokens it brought are good for hours.
		s.flow = nil
		answer(apitypes.GitHubStateExpired)
		return
	case now.Before(f.next):
		answer(apitypes.GitHubStatePending)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), callTimeout)
	defer cancel()
	var tok Token
	var err error
	if f.tok != nil {
		tok = *f.tok // confirmed already; only the account is still to be read
	} else {
		tok, err = s.Client.PollDeviceToken(ctx, f.deviceCode)
	}
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
	if f.tok == nil {
		// GitHub spent the device code: from here the tokens are what is kept, their lifetimes counted from now.
		f.tok, f.confirmed = &tok, now
	}
	u, err := s.Client.User(ctx, tok.AccessToken)
	if err != nil {
		var se *StatusError
		refused := errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && se.Status != http.StatusTooManyRequests
		if refused || !now.Before(f.confirmed.Add(accountWait)) {
			// GitHub refused the account read, or its API stayed away too long: this code is done.
			s.flow = nil
			s.log().Warn("reading the connected GitHub account failed; giving up the code", "err", err)
			httpapi.WriteError(w, upstream(detail(err)))
			return
		}
		// The authorization stays with the flow; the next poll reads the account again.
		s.log().Warn("reading the connected GitHub account failed; trying again", "err", err)
		f.next = now.Add(f.interval)
		answer(apitypes.GitHubStatePending)
		return
	}
	s.flow = nil
	if err := s.connect(ctx, u, tok, f.confirmed); err != nil {
		httpapi.WriteError(w, err)
		return
	}
	return true
}

// connect replaces the stored connection with a new one, its token issued at now.
func (s *Service) connect(ctx context.Context, u User, tok Token, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur = &record{id: store.NewID(), login: u.Login, tokens: tokens(tok, now, u.ID), createdAt: now.UTC()}
	s.loaded, s.unsaved, s.retryAt, s.failing = true, true, time.Time{}, false
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
	// flowMu first, in PollDeviceFlow's order (flowMu, then mu): a poll reading the account finishes its connect,
	// then this removes it; a poll can never connect again after the disconnect.
	s.flowMu.Lock()
	s.mu.Lock()
	// The stored row by kind, not by the id in memory: after a reconnect whose write failed, memory holds a new id
	// while the old row is still stored, and it would come back after a restart.
	row, err := s.Store.Get(r.Context(), credentials.KindGitHub, "")
	if err == nil {
		err = s.Store.Delete(r.Context(), row.ID)
	}
	if errors.Is(err, credentials.ErrNotFound) {
		err = nil
	}
	if err == nil {
		s.cur, s.unsaved, s.loaded = nil, false, true
	}
	s.mu.Unlock()
	if err == nil {
		s.flow = nil
	}
	s.flowMu.Unlock()
	if err != nil {
		httpapi.WriteError(w, err)
		return
	}
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
// (private repositories included), else, and for a public repository the installation does not cover (404) or the
// token is refused for (401/403: revoked on GitHub, an org's SSO), anonymously. A repository neither way finds is
// github_not_connected while not connected (it may be private), upstream when the token was refused, else
// invalid_request.
func (s *Service) DefaultBranch(ctx context.Context, fullName string) (string, error) {
	connected := false
	var refused error
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
			case errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden):
				refused = err
			case err != nil:
				return "", upstream(detail(err))
			}
		}
	}
	br, err := s.Anonymous(ctx, fullName)
	var st statusError
	if err != nil && errors.As(err, &st) && (st.HTTPStatus() == http.StatusUnauthorized ||
		st.HTTPStatus() == http.StatusNotFound) {
		if refused != nil {
			return "", upstream(detail(refused))
		}
		if connected {
			return "", &apitypes.Error{Code: apitypes.ErrorCodeInvalidRequest, Field: "repos", Detail: fullName}
		}
		return "", &apitypes.Error{Code: apitypes.ErrorCodeGithubNotConnected, Field: "repos", Detail: fullName}
	}
	return br, err
}
