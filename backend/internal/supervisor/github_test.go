package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdclient"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/hostproto"
	"github.com/trick77/flow/internal/layout"
)

// helperSocketEnv makes the test binary act as "flowd git-credential" against that socket, so git itself can run
// the helper in TestGitCredential_throughGit.
const helperSocketEnv = "FLOW_TEST_HELPER_SOCKET"

func TestMain(m *testing.M) {
	if sock := os.Getenv(helperSocketEnv); sock != "" && len(os.Args) > 1 && os.Args[1] == "git-credential" {
		gitCredential(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, sock, 10*time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// credSupervisor is a flowd serving FlowdSocket under a short temporary root (unix socket paths are short).
func credSupervisor(t *testing.T) (*Supervisor, string, *lockedBuffer) {
	t.Helper()
	root, err := os.MkdirTemp("", "gh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	logged := &lockedBuffer{}
	s := New(Options{Root: root, Run: ExecRunner{}, HelperWait: 500 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(logged, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.serveCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if strings.Contains(logged.String(), "ghu_") {
			t.Errorf("token in flowd's log: %s", logged.String())
		}
	})
	return s, s.p(layout.FlowdSocket), logged
}

func helper(t *testing.T, sock string, args []string, in string) (stdout, stderr string) {
	t.Helper()
	var out, errw bytes.Buffer
	gitCredential(args, strings.NewReader(in), &out, &errw, sock, 5*time.Second)
	return out.String(), errw.String()
}

const githubGet = "protocol=https\nhost=github.com\npath=octo/private.git\n\n"

func TestGitCredential(t *testing.T) {
	s, sock, _ := credSupervisor(t)
	ctx := context.Background()
	// Before the first push the helper waits for one, then says why it has nothing.
	if out, errw := helper(t, sock, []string{"get"}, githubGet); out != "" || !strings.Contains(errw, credentialsPending) {
		t.Errorf("before a push: %q %q", out, errw)
	}
	exp := time.Now().Add(8 * time.Hour).Truncate(time.Second)
	// Through Flow's client and flowd's API, as a push arrives.
	srv := httptest.NewServer(s.Handler("tok"))
	t.Cleanup(srv.Close)
	api := flowdclient.NewDialer().Dial(flowdproto.Endpoint{Address: strings.TrimPrefix(srv.URL, "http://"), Token: "tok"})
	if err := api.PutEnv(ctx, flowdproto.Env{Revision: 1, GitHubState: flowdproto.GitHubConnected,
		GitHubToken: "ghu_one", GitHubTokenExpiresAt: exp, GitHubLogin: "octo"}); err != nil {
		t.Fatal(err)
	}
	out, errw := helper(t, sock, []string{"get"}, githubGet)
	want := "username=octo\npassword=ghu_one\npassword_expiry_utc=" + strconv.FormatInt(exp.Unix(), 10) + "\n"
	if out != want || errw != "" {
		t.Errorf("get: %q %q, want %q", out, errw, want)
	}
	// Only github.com over https; store and erase do nothing.
	for name, c := range map[string]struct {
		args []string
		in   string
	}{
		"other host": {[]string{"get"}, "protocol=https\nhost=gitlab.com\n\n"},
		"http":       {[]string{"get"}, "protocol=http\nhost=github.com\n\n"},
		"store":      {[]string{"store"}, githubGet},
		"erase":      {[]string{"erase"}, githubGet},
		"no op":      {nil, githubGet},
	} {
		if out, errw := helper(t, sock, c.args, c.in); out != "" || errw != "" {
			t.Errorf("%s: %q %q", name, out, errw)
		}
	}
	// A token about to end is not handed out: the helper waits for the push of the renewed one.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 2, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_old",
		GitHubTokenExpiresAt: time.Now().Add(20 * time.Second), GitHubLogin: "octo"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = s.PutEnv(ctx, flowdproto.Env{Revision: 3, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_new",
			GitHubTokenExpiresAt: exp, GitHubLogin: "octo"})
	}()
	if out, _ := helper(t, sock, []string{"get"}, githubGet); !strings.Contains(out, "password=ghu_new\n") {
		t.Errorf("after the renewal push: %q", out)
	}
	// No push comes: expired, by name.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 4, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_old",
		GitHubTokenExpiresAt: time.Now().Add(-time.Minute)})
	if out, errw := helper(t, sock, []string{"get"}, githubGet); out != "" ||
		!strings.Contains(errw, NoticeGitHubTokenExpired) {
		t.Errorf("expired: %q %q", out, errw)
	}
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 5, GitHubState: flowdproto.GitHubExpired})
	if out, errw := helper(t, sock, []string{"get"}, githubGet); out != "" ||
		!strings.Contains(errw, NoticeGitHubTokenExpired) {
		t.Errorf("renewal failing: %q %q", out, errw)
	}
	// Not connected: at once, no wait.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 6, GitHubState: flowdproto.GitHubDisconnected})
	t0 := time.Now()
	if out, errw := helper(t, sock, []string{"get"}, githubGet); out != "" ||
		!strings.Contains(errw, NoticeGitHubDisconnected) || time.Since(t0) > 400*time.Millisecond {
		t.Errorf("disconnected: %q %q after %s", out, errw, time.Since(t0))
	}
	// flowd away: the helper says so and gives git nothing.
	if out, errw := helper(t, sock+".gone", []string{"get"}, githubGet); out != "" ||
		!strings.Contains(errw, "flowd_unreachable") {
		t.Errorf("no flowd: %q %q", out, errw)
	}
}

// git itself runs the helper from its config and gets the token (git credential fill: what clone, fetch and push
// do when github.com asks for credentials). A non-GitHub host gets nothing from it.
func TestGitCredential_throughGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	s, sock, _ := credSupervisor(t)
	_ = s.PutEnv(context.Background(), flowdproto.Env{Revision: 1, GitHubState: flowdproto.GitHubConnected,
		GitHubToken: "ghu_viagit", GitHubTokenExpiresAt: time.Now().Add(8 * time.Hour), GitHubLogin: "octo"})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	// The empty helper first drops any helper git brings along (a desktop git's keychain helper must never be asked
	// by a test).
	if err := os.WriteFile(cfg, []byte("[credential]\n\thelper =\n[credential \"https://github.com\"]\n\thelper = "+
		exe+" git-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fill := func(in string) (string, error) {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+cfg,
			"GIT_TERMINAL_PROMPT=0", helperSocketEnv+"="+sock, "GIT_ASKPASS=", "SSH_ASKPASS=")
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := fill("protocol=https\nhost=github.com\npath=octo/private.git\n\n")
	// Answers are never printed: on a machine with a real credential helper they could hold a real token.
	if err != nil || !strings.Contains(out, "username=octo\n") || !strings.Contains(out, "password=ghu_viagit\n") {
		t.Errorf("fill: %v, our token in the answer: %v", err, strings.Contains(out, "ghu_viagit"))
	}
	if out, err := fill("protocol=https\nhost=example.com\n\n"); err == nil || strings.Contains(out, "password=") {
		t.Errorf("other host got a password: %v", err)
	}
}

func TestWriteGitHub(t *testing.T) {
	s, _, _ := credSupervisor(t)
	ctx := context.Background()
	hosts, config := s.p(layout.GHHosts), s.p(layout.GHConfig)
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 5, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_abc",
		GitHubTokenExpiresAt: time.Now().Add(time.Hour), GitHubLogin: "octo"})
	b, err := os.ReadFile(hosts)
	want := "github.com:\n    users:\n        octo:\n            oauth_token: ghu_abc\n    oauth_token: ghu_abc\n" +
		"    user: octo\n    git_protocol: https\n"
	if err != nil || string(b) != want {
		t.Fatalf("hosts.yml %q %v", b, err)
	}
	if fi, _ := os.Stat(hosts); fi.Mode().Perm() != 0o644 {
		t.Errorf("hosts.yml mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(hosts)); fi.Mode().Perm() != 0o755 {
		t.Errorf("gh dir mode %v", fi.Mode())
	}
	if b, _ := os.ReadFile(config); string(b) != ghConfig {
		t.Errorf("config.yml %q", b)
	}
	// Renewed: replaced, never a leftover temporary file.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 6, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_def",
		GitHubLogin: "octo"})
	if b, _ := os.ReadFile(hosts); !strings.Contains(string(b), "oauth_token: ghu_def\n") ||
		strings.Contains(string(b), "ghu_abc") {
		t.Errorf("renewed %q", b)
	}
	// An older push changes nothing.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 4, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_old"})
	if b, _ := os.ReadFile(hosts); !strings.Contains(string(b), "ghu_def") {
		t.Errorf("older push applied: %q", b)
	}
	// A token that is not a plain word never reaches the YAML.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 7, GitHubState: flowdproto.GitHubConnected,
		GitHubToken: "ghu_x\n  evil: 1", GitHubLogin: "octo"})
	if _, err := os.Stat(hosts); !os.IsNotExist(err) {
		t.Errorf("odd token written: %v", err)
	}
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 8, GitHubState: flowdproto.GitHubConnected, GitHubToken: "ghu_ghi"})
	if b, _ := os.ReadFile(hosts); string(b) != "github.com:\n    oauth_token: ghu_ghi\n    git_protocol: https\n" {
		t.Errorf("without a login %q", b)
	}
	// Disconnected: no hosts.yml, gh says it is not logged in.
	_ = s.PutEnv(ctx, flowdproto.Env{Revision: 9, GitHubState: flowdproto.GitHubDisconnected})
	if _, err := os.Stat(hosts); !os.IsNotExist(err) {
		t.Errorf("hosts.yml left: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(hosts))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
}

// A session whose git met no token gets one notice per code in its log (through its host), not one per retry.
func TestRaiseNotice(t *testing.T) {
	s, _, _ := credSupervisor(t)
	id := "s1"
	sock := s.p(layout.HostSocket(id))
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	got := make(chan hostproto.Request, 10)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var req hostproto.Request
			if json.NewDecoder(conn).Decode(&req) == nil {
				got <- req
				_ = json.NewEncoder(conn).Encode(hostproto.Response{ID: req.ID})
			}
			_ = conn.Close()
		}
	}()
	ctx := context.Background()
	s.raiseNotice(ctx, id, NoticeGitHubDisconnected)
	s.raiseNotice(ctx, id, NoticeGitHubDisconnected)
	s.raiseNotice(ctx, id, NoticeGitHubTokenExpired)
	var codes []string
	for len(codes) < 2 {
		select {
		case req := <-got:
			if req.Op != hostproto.OpNotice || req.Notice == nil || req.Notice.Level != event.NoticeLevelError {
				t.Fatalf("request %+v", req)
			}
			codes = append(codes, req.Notice.Code)
		case <-time.After(5 * time.Second):
			t.Fatalf("notices %v", codes)
		}
	}
	select {
	case req := <-got:
		t.Errorf("repeated notice %+v", req.Notice)
	case <-time.After(100 * time.Millisecond):
	}
	if strings.Join(codes, ",") != NoticeGitHubDisconnected+","+NoticeGitHubTokenExpired {
		t.Errorf("codes %v", codes)
	}
}

func TestSessionFromCgroup(t *testing.T) {
	for in, want := range map[string]string{
		"0::/system.slice/flow-session-0193a8f2-9000-7000-8000-0000000000d1.service\n": "0193a8f2-9000-7000-8000-0000000000d1",
		"0::/system.slice/flow-session-abc.service/sub\n":                              "abc",
		"0::/system.slice/flow-term-s-abc-1.service\n":                                 "",
		"0::/system.slice/flowd.service\n":                                             "",
		"0::/system.slice/flow-session-../x.service\n":                                 "",
		"0::/system.slice/flow-session-Abc.service\n":                                  "",
		"": "",
	} {
		if got := sessionFromCgroup(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The config flowd writes makes a bare git push publish the session branch under its own name, also when the
// branch tracks the default branch, and never move the default branch; a branch made as flowd makes worktrees
// (--no-track) gets its upstream on that first push.
func TestGitConfig_push(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	s := New(Options{Root: root, Run: ExecRunner{}, Log: slog.New(slog.DiscardHandler)})
	if err := s.writeGitConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfgPath := s.p(layout.SystemGitConfig)
	b, _ := os.ReadFile(cfgPath)
	for _, want := range []string{"helper = " + layout.GitCredentialHelper, "default = current", "autoSetupRemote = true"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("gitconfig lacks %q:\n%s", want, b)
		}
	}
	// Written again (every flowd start): still one helper line.
	_ = s.writeGitConfig(context.Background())
	if b, _ := os.ReadFile(cfgPath); strings.Count(string(b), "helper =") != 1 {
		t.Errorf("helper repeated:\n%s", b)
	}
	work := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_SYSTEM="+cfgPath, "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	bare := filepath.Join(work, "remote.git")
	git(work, "init", "--quiet", "--bare", "-b", "main", bare)
	seed := filepath.Join(work, "seed")
	git(work, "clone", "--quiet", bare, seed)
	git(seed, "commit", "--quiet", "--allow-empty", "-m", "base")
	git(seed, "push", "--quiet", "origin", "HEAD:main")
	mainBefore := git(bare, "rev-parse", "main")

	checkout := filepath.Join(work, "checkout")
	git(work, "clone", "--quiet", bare, checkout)
	// As flowd makes a session worktree.
	wt := filepath.Join(work, "wt")
	git(checkout, "worktree", "add", "--quiet", "--no-track", "-b", "flow/brave-otter", wt, "origin/main")
	git(wt, "commit", "--quiet", "--allow-empty", "-m", "work")
	git(wt, "push", "--quiet")
	if got, want := git(bare, "rev-parse", "flow/brave-otter"), git(wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("session branch on the remote %s, want %s", got, want)
	}
	if git(wt, "rev-parse", "--abbrev-ref", "@{upstream}") != "origin/flow/brave-otter" {
		t.Error("no upstream after the first push")
	}
	// A branch that tracks origin/main (made by hand): still its own name, main untouched.
	wt2 := filepath.Join(work, "wt2")
	git(checkout, "worktree", "add", "--quiet", "--track", "-b", "flow/calm-heron", wt2, "origin/main")
	git(wt2, "commit", "--quiet", "--allow-empty", "-m", "more")
	git(wt2, "push", "--quiet")
	if git(bare, "rev-parse", "flow/calm-heron") != git(wt2, "rev-parse", "HEAD") {
		t.Error("tracking branch not pushed under its name")
	}
	if git(bare, "rev-parse", "main") != mainBefore {
		t.Error("the default branch moved")
	}
}
