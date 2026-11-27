package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/hostproto"
	"github.com/trick77/flow/internal/layout"
)

// GitHub delivery in the sandbox (docs/architecture.md, "Credentials, one place in flowd"): git asks flowd for the
// token on every use through the credential helper (layout.GitCredentialHelper, configured system-wide); gh reads
// the hosts.yml flowd rewrites on every push. flowd never dials Flow: a renewed token arrives with Flow's next push,
// and a helper meeting a token that is about to end waits a little for it.

// Notice codes flowd raises in a session whose git asked for a token it could not get (docs/events.md).
const (
	NoticeGitHubDisconnected = "github_disconnected"
	NoticeGitHubTokenExpired = "github_token_expired" //nolint:gosec // a notice code, not a credential
)

// credentialsPending: no push since flowd started (the helper waited for one in vain).
const credentialsPending = "credentials_pending"

// tokenMargin: a token with less left is not handed to git (a clone may outlast it); the helper waits for the push
// of the renewed one instead. Flow renews 15 minutes before the end.
const tokenMargin = time.Minute

// noticeEvery bounds a session's notices of one code: an agent retrying a private clone in a loop gets one row.
const noticeEvery = 10 * time.Minute

// credAnswer is flowd's answer on FlowdSocket to the helper: a token, or the code of why there is none. Login is
// the account, the username git sends.
type credAnswer struct {
	Token     string    `json:"token,omitempty"`
	Login     string    `json:"login,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Code      string    `json:"code,omitempty"`
}

type credRequest struct {
	Op string `json:"op"`
}

// opGitCredential is the one request FlowdSocket takes so far.
const opGitCredential = "git-credential" //nolint:gosec // a request name, not a credential

// writeGitConfig makes git in the whole sandbox use flowd's helper for https://github.com and publish a session's
// branch under its own name: push.default=current pushes the branch to the same name on origin (never to the
// default branch from a session branch, tracked or not), push.autoSetupRemote records it as the upstream on the
// first push. Through git config --file, so whatever else the image put there stays.
func (s *Supervisor) writeGitConfig(ctx context.Context) error {
	p := s.p(layout.SystemGitConfig)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // /etc
		return err
	}
	for _, kv := range [][]string{
		{"--replace-all", "credential.https://github.com.helper", layout.GitCredentialHelper},
		{"push.default", "current"},
		{"push.autoSetupRemote", "true"},
	} {
		if _, err := s.o.Run.Run(ctx, Cmd{Name: "git", Args: append([]string{"config", "--file", p}, kv...)}); err != nil {
			return err
		}
	}
	return os.Chmod(p, 0o644) //nolint:gosec // every user's git reads it
}

var (
	ghTokenRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
	ghLoginRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
)

// ghHosts is gh's hosts.yml for the token: the layout gh writes itself since multiple accounts (a users map beside
// the host's active user, token and protocol), plain text, no keyring. Token and login are checked to be plain
// words, so nothing in them can bend the YAML.
func ghHosts(login, token string) string {
	var b strings.Builder
	b.WriteString("github.com:\n")
	if login != "" {
		b.WriteString("    users:\n        " + login + ":\n            oauth_token: " + token + "\n")
	}
	b.WriteString("    oauth_token: " + token + "\n")
	if login != "" {
		b.WriteString("    user: " + login + "\n")
	}
	b.WriteString("    git_protocol: https\n")
	return b.String()
}

// ghConfig is gh's config.yml: the config version gh's migrations expect, https for git.
const ghConfig = "version: \"1\"\ngit_protocol: https\n"

// writeGitHub brings gh's files in line with the set flowd holds: hosts.yml with the token while one is valid,
// none otherwise (gh then says it is not logged in). Serialized, and from the set held now, so two pushes never
// leave the older token behind.
func (s *Supervisor) writeGitHub() error {
	s.ghMu.Lock()
	defer s.ghMu.Unlock()
	s.mu.Lock()
	e := s.envSet
	s.mu.Unlock()
	dir, err := s.rootDir(layout.Credentials, 0o711)
	if err == nil {
		dir, err = s.rootDir(layout.GHConfigDir, 0o755)
	}
	if err != nil {
		return err
	}
	if err := writeRootFile(filepath.Join(dir, filepath.Base(layout.GHConfig)), ghConfig, 0o644); err != nil {
		return err
	}
	hosts := filepath.Join(dir, filepath.Base(layout.GHHosts))
	login := e.GitHubLogin
	if !ghLoginRE.MatchString(login) {
		login = ""
	}
	if e.GitHubState != flowdproto.GitHubConnected || e.GitHubToken == "" {
		if err := os.Remove(hosts); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if !ghTokenRE.MatchString(e.GitHubToken) {
		_ = os.Remove(hosts)
		return errors.New("the GitHub token is not a plain word; gh's hosts.yml left out")
	}
	return writeRootFile(hosts, ghHosts(login, e.GitHubToken), 0o644)
}

// rootDir makes a directory below /run/flow root's with mode, refusing a link or a non-directory at its place.
func (s *Supervisor) rootDir(rel string, mode fs.FileMode) (string, error) {
	p := s.p(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o711); err != nil { //nolint:gosec // traverse only
		return "", err
	}
	if err := os.Mkdir(p, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", rel)
	}
	if os.Geteuid() == 0 {
		if err := os.Lchown(p, 0, 0); err != nil {
			return "", err
		}
	}
	return p, os.Chmod(p, mode)
}

// writeRootFile replaces path atomically: a new file (O_EXCL, so never through a planted link) renamed over it, so
// a reader sees the old content or the new, never half.
func writeRootFile(path, content string, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.WriteString(content)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// gitToken answers the helper: the current token when it has more than tokenMargin left; else it waits up to
// HelperWait for a push that brings one (a renewal on its way, the first push after a flowd restart), then names
// why there is none. disconnected answers at once: no push will change that without the owner.
func (s *Supervisor) gitToken(ctx context.Context) credAnswer {
	ctx, cancel := context.WithTimeout(ctx, s.o.HelperWait)
	defer cancel()
	for {
		s.mu.Lock()
		rev, e, wake := s.env, s.envSet, s.envWake
		s.mu.Unlock()
		if rev != 0 {
			switch e.GitHubState {
			case flowdproto.GitHubConnected:
				if e.GitHubToken != "" && (e.GitHubTokenExpiresAt.IsZero() ||
					time.Until(e.GitHubTokenExpiresAt) > tokenMargin) {
					return credAnswer{Token: e.GitHubToken, Login: e.GitHubLogin, ExpiresAt: e.GitHubTokenExpiresAt}
				}
			case flowdproto.GitHubExpired:
			default:
				return credAnswer{Code: NoticeGitHubDisconnected}
			}
		}
		select {
		case <-wake:
		case <-ctx.Done():
			if rev == 0 {
				return credAnswer{Code: credentialsPending}
			}
			return credAnswer{Code: NoticeGitHubTokenExpired}
		}
	}
}

// serveCredentials answers FlowdSocket until ctx ends: one JSON request line, one answer line per connection. The
// socket is the agent's to use (0666 in root's 0711 /run/flow): every process in the sandbox may get the token,
// the accepted risk of the spec. It takes only the credential request, nothing of Flow's API.
func (s *Supervisor) serveCredentials(ctx context.Context) error {
	path := s.p(layout.FlowdSocket)
	if err := os.MkdirAll(filepath.Dir(path), 0o711); err != nil { //nolint:gosec // traverse only
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // the agent's helper connects
		_ = l.Close()
		return err
	}
	slots := make(chan struct{}, 32) // an agent opening connections in a loop cannot pile up goroutines
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				_ = conn.Close()
				continue
			}
			go func() {
				defer func() { <-slots }()
				s.serveCredential(ctx, conn)
			}()
		}
	}()
	return nil
}

func (s *Supervisor) serveCredential(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(s.o.HelperWait + 5*time.Second))
	line, err := bufio.NewReader(conn).ReadSlice('\n')
	var req credRequest
	if err != nil || json.Unmarshal(line, &req) != nil || req.Op != opGitCredential {
		_ = json.NewEncoder(conn).Encode(credAnswer{Code: "invalid"})
		return
	}
	pid := peerPID(conn) // while the helper waits for the answer, its process is there to be read
	a := s.gitToken(ctx)
	if a.Code == NoticeGitHubDisconnected || a.Code == NoticeGitHubTokenExpired {
		if id := sessionOfPID(pid); id != "" {
			go s.raiseNotice(context.WithoutCancel(ctx), id, a.Code)
		}
	}
	_ = json.NewEncoder(conn).Encode(a)
}

// cgroupSessionRE finds the session host unit in a /proc/<pid>/cgroup: every process an agent runs (git, and the
// helper git runs) is in its session's unit.
var cgroupSessionRE = regexp.MustCompile(`/flow-session-([a-z0-9][a-z0-9._-]{0,62})\.service(/|$)`)

// sessionFromCgroup is the session whose host unit holds the process, "" for any other (a terminal, a setup
// script, flowd's own git).
func sessionFromCgroup(content string) string {
	for line := range strings.Lines(content) {
		if m := cgroupSessionRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil && layout.ValidName(m[1]) {
			return m[1]
		}
	}
	return ""
}

// sessionOfPID reads the process's cgroup (Linux; "" elsewhere or when it is gone).
func sessionOfPID(pid int) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	return sessionFromCgroup(string(b))
}

// raiseNotice has the session's host log a notice, at most once per noticeEvery and code. A host that is down or
// predates hostproto.OpNotice drops it.
func (s *Supervisor) raiseNotice(ctx context.Context, id, code string) {
	key := id + " " + code
	now := time.Now()
	s.mu.Lock()
	if last, ok := s.notices[key]; ok && now.Sub(last) < noticeEvery {
		s.mu.Unlock()
		return
	}
	s.notices[key] = now
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := s.dialHost(ctx, id)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	level := event.NoticeLevelError
	resp, err := roundTrip(conn, hostproto.Request{ID: "n", Op: hostproto.OpNotice,
		Notice: &event.Notice{Code: code, Level: level}})
	if err != nil || resp.Error != "" {
		s.o.Log.Info("notice not logged", "session", id, "code", code, "answer", resp.Error)
	}
}
