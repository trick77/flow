package term

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/flowdproto"
	"github.com/trick77/flow/internal/host/claude"
)

// ShellSpec is what StartShell starts.
type ShellSpec struct {
	Path string
	// Argv0 is the program name the shell sees: "-bash" makes a login shell.
	Argv0 string
	Dir   string
	Env   []string
	Size  Size
}

// flushWait bounds how long an ended terminal keeps writing out its clients' queues.
const flushWait = 3 * writeWait

// hangupWait is how long a stopped terminal waits for its hung-up shell before SIGKILL (a var for tests).
var hangupWait = 5 * time.Second

// StartShell starts a shell on a new PTY; Linux only (pty_linux.go), elsewhere it fails.
var StartShell = startShell

// ReadExit reads the exit status a terminal recorded in file (flowd: layout.TerminalExitFile); ok is false when
// there is none.
func ReadExit(file string) (Exit, bool) {
	b, err := os.ReadFile(file) //nolint:gosec // flowd's own path, from a checked terminal id
	var e Exit
	if err != nil || json.Unmarshal(b, &e) != nil {
		return Exit{}, false
	}
	return e, true
}

// Run is `flowd term`: start the login shell in --dir, serve --socket until the shell exits, record its exit status.
// A stop (SIGTERM from systemd) hangs the shell up and waits for it to end.
func Run(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("term", flag.ContinueOnError)
	id := fs.String("id", "", "terminal id")
	session := fs.String("session", "", "session id, when the terminal belongs to one")
	dir := fs.String("dir", "", "the shell's directory")
	socket := fs.String("socket", "", "unix socket to serve")
	exitFile := fs.String("exit-file", "", "where to record the shell's exit status")
	cols := fs.Int("cols", 80, "columns")
	rows := fs.Int("rows", 24, "rows")
	auth := fs.String("claude-auth", string(event.ClaudeAuthSubscriptionLogin),
		"the Claude mode whose credential the shell gets (subscription_login: none)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || *dir == "" || *socket == "" || *exitFile == "" {
		return errors.New("term: --id, --dir, --socket and --exit-file are required")
	}
	size := Size{Cols: 80, Rows: 24}
	if flowdproto.ValidTerminalSize(*cols, *rows) {
		size = Size{Cols: *cols, Rows: *rows}
	}
	_ = os.Remove(*exitFile)
	if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	if err := os.Chmod(*socket, 0o600); err != nil {
		return err
	}
	path := LoginShell("/etc/passwd", os.Getuid())
	sh, err := StartShell(ShellSpec{Path: path, Argv0: "-" + filepath.Base(path), Dir: *dir,
		Env: ShellEnv(os.Environ(), event.ClaudeAuth(*auth)), Size: size})
	if err != nil {
		return fmt.Errorf("term: starting %s: %w", path, err)
	}
	srv := NewServer(sh, Info{ID: *id, Session: *session, Dir: *dir, Size: size}, RingSize)
	go func() { _ = srv.Serve(l) }()
	log.InfoContext(ctx, "terminal started", "id", *id, "pid", sh.Pid())
	select {
	case <-srv.Done():
	case <-ctx.Done():
		sh.Hangup()
		select {
		case <-srv.Done():
		case <-time.After(hangupWait):
			// A shell that ignores SIGHUP would hold the stop until systemd's own timeout (90 s), with no exit recorded.
			log.Warn("shell ignored the hangup, killing it", "id", *id)
			sh.Kill()
			<-srv.Done()
		}
	}
	code := srv.Code()
	if err := writeExit(*exitFile, code); err != nil {
		log.Warn("recording the exit status", "id", *id, "err", err)
	}
	// The clients' queues (up to maxQueued each) go out before the process ends and takes them along: a client
	// taking frames finishes, one that takes none for writeWait is dropped; flushWait caps a trickling one.
	srv.Flush(flushWait)
	_ = l.Close()
	log.Info("terminal ended", "id", *id, "code", code)
	return nil
}

func writeExit(file string, code int) error {
	b, _ := json.Marshal(Exit{Code: code})
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { //nolint:gosec // flowd's own path, from a checked terminal id
		return err
	}
	return os.Rename(tmp, file)
}

// LoginShell is uid's shell from a passwd file, /bin/bash when it names none that exists, /bin/sh without bash.
func LoginShell(passwd string, uid int) string {
	if f, err := os.Open(passwd); err == nil { //nolint:gosec // /etc/passwd, a test file in tests
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Split(sc.Text(), ":")
			if len(fields) == 7 && fields[2] == strconv.Itoa(uid) && filepath.IsAbs(fields[6]) {
				if _, err := os.Stat(fields[6]); err == nil {
					return fields[6]
				}
			}
		}
	}
	for _, sh := range []string{"/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(sh); err == nil {
			return sh
		}
	}
	return "/bin/sh"
}

// ShellEnv is the shell's environment: what a session's agent gets from the same unit environment (claude.Env: the
// allow-list, the home-volume locations, the one credential of auth's mode; never the MCP token or another
// credential), with the terminal type xterm.js emulates.
func ShellEnv(unit []string, auth event.ClaudeAuth) []string {
	env, _ := claude.Env(unit, auth)
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k != "TERM" && k != "COLORTERM" {
			out = append(out, kv)
		}
	}
	return append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
}
