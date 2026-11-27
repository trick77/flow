package supervisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/flow/internal/layout"
)

// helperTimeout bounds the helper's whole exchange with flowd (flowd waits up to HelperWait for a push).
const helperTimeout = 30 * time.Second

// RunGitCredential is "flowd git-credential <operation>", git's credential helper for https://github.com (git
// credential protocol: attributes on stdin up to a blank line; for get, username and password on stdout). It asks
// flowd for the token on every get and never stores one; store and erase do nothing. stdout carries only the
// protocol; a reason for no token goes to stderr, and git then fails with its own error. Always exit status 0: git
// treats a failing helper like one with no answer anyway.
func RunGitCredential(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	gitCredential(args, stdin, stdout, stderr, layout.FlowdSocket, helperTimeout)
	return 0
}

func gitCredential(args []string, stdin io.Reader, stdout, stderr io.Writer, socket string, timeout time.Duration) {
	attrs := readCredentialAttrs(stdin)
	if len(args) != 1 || args[0] != "get" || attrs["protocol"] != "https" || attrs["host"] != "github.com" {
		return
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second) //nolint:gosec // flowd's own socket (layout), not input
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "flow: flowd does not answer, no GitHub token (flowd_unreachable)")
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	var a credAnswer
	if json.NewEncoder(conn).Encode(credRequest{Op: opGitCredential}) == nil {
		_ = json.NewDecoder(bufio.NewReader(io.LimitReader(conn, 64<<10))).Decode(&a)
	}
	if a.Token != "" && !strings.ContainsAny(a.Token, "\n\x00") {
		user := a.Login
		if !ghLoginRE.MatchString(user) {
			user = "x-access-token"
		}
		out := "username=" + user + "\npassword=" + a.Token + "\n"
		if !a.ExpiresAt.IsZero() {
			// git (2.41+) keeps no copy past it.
			out += "password_expiry_utc=" + strconv.FormatInt(a.ExpiresAt.Unix(), 10) + "\n"
		}
		_, _ = io.WriteString(stdout, out)
		return
	}
	_, _ = fmt.Fprintln(stderr, helperMessage(a.Code))
}

// helperMessage is the line git shows for no token; the code in parentheses matches the session's notice.
func helperMessage(code string) string {
	switch code {
	case NoticeGitHubDisconnected:
		return "flow: GitHub is not connected (github_disconnected): public repositories work; connect GitHub in " +
			"Flow's settings for private ones and pushes"
	case NoticeGitHubTokenExpired:
		return "flow: GitHub token expired (github_token_expired); Flow will renew it"
	case credentialsPending:
		return "flow: Flow has not delivered credentials since flowd started (credentials_pending)"
	}
	return "flow: no GitHub token (" + code + ")"
}

// readCredentialAttrs reads git's key=value lines up to a blank line or the end, at most 64 KiB.
func readCredentialAttrs(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(r, 64<<10))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}
