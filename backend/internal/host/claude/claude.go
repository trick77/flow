package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/trick77/flow/internal/event"
)

// Error codes of turns the adapter ends itself (docs/ownership.md lists them as the host's, not the provider's).
const (
	// CodeExited: the sidecar exited, or its pipe broke, while the turn ran.
	CodeExited = "adapter_exited"
	// CodeStart: the sidecar could not be started for the turn.
	CodeStart = "adapter"
	// NoticeNotRun precedes a turn the adapter ends because its sidecar died (params: messageId): the message may
	// not have run, or not fully, and the owner may send it again. The adapter never does that itself.
	NoticeNotRun = "message_may_not_have_run"
)

// Stop bounds. The sidecar's immediate shutdown interrupts a running turn and waits up to its own grace
// (sidecar/src/session.ts interruptGraceMs, 30 s) for the turn to end before it closes Claude Code, and its SIGTERM
// handler awaits that same shutdown; so the host waits past that grace before SIGTERM, then KillWait before SIGKILL.
// StopLimit is the one total bound, below systemd's default stop timeout (90 s).
const (
	SidecarInterruptGrace = 30 * time.Second
	DefaultShutdownWait   = SidecarInterruptGrace + 10*time.Second
	DefaultKillWait       = 5 * time.Second
	StopLimit             = DefaultShutdownWait + DefaultKillWait
)

// Config wires an Adapter.
type Config struct {
	// Argv starts the sidecar: node and <release>/sidecar/dist/main.js (a fake in tests). --record <file> is added
	// when RecordPath is set.
	Argv       []string
	RecordPath string
	// Dir is the session directory, the sidecar's working directory.
	Dir string
	// Env is the sidecar's whole environment (see Env).
	Env     []string
	Session Session
	// State reads the host's log state at each sidecar start; it must not be called with the host's lock held, and
	// the adapter never does (it calls it from Run).
	State func() State
	Log   *slog.Logger
	// ShutdownWait bounds the sidecar's own shutdown after a host.shutdown (DefaultShutdownWait); then SIGTERM, then
	// after KillWait (DefaultKillWait) SIGKILL to its process group. KillWait also bounds how long the adapter waits
	// for the rest of the output (stdout, stderr) of a sidecar that exited.
	ShutdownWait, KillWait time.Duration
	// StderrCap bounds the stderr bytes journalled per sidecar process (default 1 MiB).
	StderrCap int64
	Now       func() time.Time
}

// Adapter is the Claude adapter (host.Adapter): one sidecar process at a time, started on demand.
type Adapter struct {
	cfg  Config
	mu   sync.Mutex
	in   []event.Command
	wake chan struct{}
	seq  int
	pipe func() (*os.File, *os.File, error) // os.Pipe; tests make it fail
}

// New returns an Adapter; nothing starts before the first message.
func New(cfg Config) *Adapter {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.ShutdownWait == 0 {
		cfg.ShutdownWait = DefaultShutdownWait
	}
	if cfg.KillWait == 0 {
		cfg.KillWait = DefaultKillWait
	}
	if cfg.StderrCap == 0 {
		cfg.StderrCap = 1 << 20
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Adapter{cfg: cfg, wake: make(chan struct{}, 1), pipe: os.Pipe}
}

// Config is the adapter's configuration, defaults filled in.
func (a *Adapter) Config() Config { return a.cfg }

// Send implements host.Adapter. It never blocks and never drops: the host calls it under its lock, also while Run
// waits for the host to take a draft.
func (a *Adapter) Send(c event.Command) error {
	a.mu.Lock()
	a.in = append(a.in, c)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

func (a *Adapter) take() []event.Command {
	a.mu.Lock()
	defer a.mu.Unlock()
	in := a.in
	a.in = nil
	return in
}

// run is the state of Run: the sidecar and the turn it was given.
type run struct {
	a   *Adapter
	ctx context.Context
	out chan<- event.EventDraft
	p   *proc
	// turn: a message.send went to the sidecar and its turn.ended has not come back; interrupted: a turn.interrupt
	// followed it.
	turn, interrupted bool
	// sent is the turn's message.send.
	sent event.Command
}

// Run implements host.Adapter: it relays commands and drafts, restarts the sidecar on the next message after it
// ended, and stops it when ctx ends. It returns only then.
func (a *Adapter) Run(ctx context.Context, out chan<- event.EventDraft) error {
	r := &run{a: a, ctx: ctx, out: out}
	for {
		for _, c := range a.take() {
			r.command(c)
		}
		var drafts <-chan event.EventDraft
		var exited <-chan struct{}
		if r.p != nil {
			drafts, exited = r.p.drafts, r.p.exited
		}
		select {
		case <-ctx.Done():
			r.stop()
			return nil
		case <-a.wake:
		case d, ok := <-drafts:
			if !ok {
				r.ended()
				continue
			}
			r.draft(d)
		case <-exited:
			r.ended()
		}
	}
}

func (r *run) command(c event.Command) {
	switch c.Kind {
	case event.CmdMessageSend:
		if r.p == nil {
			p, err := r.a.spawn()
			if err != nil {
				r.a.cfg.Log.Error("sidecar did not start", "err", err)
				r.endTurn(event.TurnOutcomeError, CodeStart)
				return
			}
			r.p = p
		}
		r.turn, r.interrupted = true, false
		r.sent = c
	case event.CmdTurnInterrupt:
		if r.turn {
			r.interrupted = true
		}
	case event.CmdHostShutdown:
		if r.p != nil {
			r.p.shutdown = true
		}
	}
	// Without a sidecar there is nothing to tell: no turn runs, and settings go into the next start line.
	if r.p != nil {
		r.p.write(c)
	}
}

func (r *run) draft(d event.EventDraft) {
	if d.Kind == event.KindNotice && d.Cmd == r.p.startID {
		// The sidecar refused its start line: it would never run a turn. Stop it; the turn fails as an exit.
		r.a.cfg.Log.Error("sidecar refused its start line")
		r.p.kill()
	}
	r.forward(d)
}

func (r *run) forward(d event.EventDraft) {
	if d.Kind == event.KindTurnEnded {
		r.turn = false
	}
	select {
	case r.out <- d:
	case <-r.ctx.Done(): // the host stops: it writes nothing more
	}
}

// endTurn ends the turn the sidecar can no longer end; the host expires the open request and adds the duration.
func (r *run) endTurn(outcome event.TurnOutcome, code string) {
	t := event.TurnEnded{Outcome: outcome}
	if outcome != event.TurnOutcomeInterrupted {
		t.ErrorCode = code
	}
	b, _ := json.Marshal(t) // contract types always marshal
	r.turn, r.interrupted = false, false
	r.forward(event.EventDraft{Kind: event.KindTurnEnded, Data: b})
}

// ended takes the rest of a sidecar's output once it exited or closed its stdout, makes sure it is gone and ends
// its turn. The next message starts a new one.
func (r *run) ended() {
	p := r.p
	p.reap(r.a.cfg.KillWait, r.forward)
	r.p = nil
	r.a.cfg.Log.Warn("sidecar ended", "exit", p.exitText(), "turnOpen", r.turn)
	if !r.turn {
		return
	}
	if r.interrupted {
		r.endTurn(event.TurnOutcomeInterrupted, "")
		return
	}
	// Never sent again by itself: the message may have started work, and running it twice can repeat commands. The
	// owner learns it may not have run (fully) and decides.
	var m event.MessageSend
	_ = json.Unmarshal(r.sent.Data, &m)
	b, _ := json.Marshal(event.Notice{Code: NoticeNotRun, Level: event.NoticeLevelError,
		Params: map[string]string{"messageId": m.MessageID}})
	r.forward(event.EventDraft{Kind: event.KindNotice, Data: b})
	r.endTurn(event.TurnOutcomeError, CodeExited)
}

// stop ends the sidecar with the host: a host.shutdown (now, unless the host already sent its own), stdin closed,
// a bounded wait, then signals.
func (r *run) stop() {
	p := r.p
	if p == nil {
		return
	}
	if !p.shutdown {
		if c, err := event.NewCommand("host-stop", r.a.cfg.Session.ID, event.CmdHostShutdown,
			event.HostShutdown{Reason: event.ShutdownReasonStop}); err == nil {
			p.write(c)
		}
	}
	p.closeIn()
	discard := func(event.EventDraft) {}
	if !p.waitFor(r.a.cfg.ShutdownWait, discard) {
		r.a.cfg.Log.Warn("sidecar did not stop in time, terminating")
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		if !p.waitFor(r.a.cfg.KillWait, discard) {
			p.kill()
		}
	}
	p.reap(r.a.cfg.KillWait, discard)
	r.p = nil
}

// spawn starts a sidecar and queues its start line.
func (a *Adapter) spawn() (*proc, error) {
	st := a.cfg.State()
	a.seq++
	id := "sidecar-start-" + strconv.FormatInt(a.cfg.Now().UnixNano(), 36) + "-" + strconv.Itoa(a.seq)
	line, err := StartLine(id, NewStartSpec(a.cfg.Session, st))
	if err != nil {
		return nil, err
	}
	argv := a.cfg.Argv
	if a.cfg.RecordPath != "" {
		argv = append(append([]string{}, argv...), "--record", a.cfg.RecordPath)
	}
	if len(argv) == 0 {
		return nil, errors.New("no sidecar command")
	}
	// The command is the host's own configuration (layout.NodeBin and the release's sidecar), never input.
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // see above
	cmd.Dir, cmd.Env = a.cfg.Dir, a.cfg.Env
	if cmd.Env == nil {
		cmd.Env = []string{} // never the host's own environment
	}
	// Its own process group: a kill reaches Claude Code and what it runs, not only node.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Files, not exec's copying pipes: Wait then returns when the sidecar exits even if a process it started still
	// holds its stdout or stderr. Every end opened is closed on every error path.
	var opened []*os.File
	closeAll := func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}
	var pairs [3][2]*os.File // stdin, stdout, stderr: read end, write end
	for i := range pairs {
		r, w, err := a.pipe()
		if err != nil {
			closeAll()
			return nil, err
		}
		pairs[i] = [2]*os.File{r, w}
		opened = append(opened, r, w)
	}
	inR, inW, outR, outW, errR, errW := pairs[0][0], pairs[0][1], pairs[1][0], pairs[1][1], pairs[2][0], pairs[2][1]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if err := cmd.Start(); err != nil {
		closeAll()
		return nil, err
	}
	// The child's ends are its own now.
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	stdin := inW
	a.cfg.Log.Info("sidecar started", "pid", cmd.Process.Pid, "resume", st.Resume, "record", a.cfg.RecordPath != "")
	p := &proc{cmd: cmd, stdin: stdin, stdout: outR, startID: id, session: a.cfg.Session.ID, drafts: make(chan event.EventDraft, 64),
		exited: make(chan struct{}), quit: make(chan struct{}), inWake: make(chan struct{}, 1), log: a.cfg.Log}
	p.queue = append(p.queue, line)
	stderrDone := make(chan struct{})
	go p.writeLoop()
	go p.readLoop()
	go func() {
		logStderr(errR, a.cfg.StderrCap, a.cfg.Log)
		close(stderrDone)
	}()
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
		// Its last words are still in the pipe: read them to the end, unless a process it started holds the pipe
		// open past the bound.
		select {
		case <-stderrDone:
		case <-time.After(a.cfg.KillWait):
		}
		_ = errR.Close()
	}()
	return p, nil
}

// proc is one running sidecar.
type proc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *os.File
	startID string
	session string
	log     *slog.Logger
	// shutdown: a host.shutdown went to it.
	shutdown bool
	drafts   chan event.EventDraft // closed at the end of stdout
	exited   chan struct{}         // closed once it exited; waitErr is set then
	waitErr  error
	quit     chan struct{} // closed when nobody takes drafts any more
	quitOnce sync.Once
	killOnce sync.Once

	mu      sync.Mutex
	queue   [][]byte
	closing bool
	inWake  chan struct{}
}

// write queues a command for stdin; it never blocks.
func (p *proc) write(c event.Command) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	p.mu.Lock()
	if !p.closing {
		p.queue = append(p.queue, append(b, '\n'))
	}
	p.mu.Unlock()
	p.signal()
}

func (p *proc) closeIn() {
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
	p.signal()
}

func (p *proc) signal() {
	select {
	case p.inWake <- struct{}{}:
	default:
	}
}

// writeLoop writes queued lines to stdin in order; a broken pipe ends the sidecar (it is gone or deaf).
func (p *proc) writeLoop() {
	defer func() { _ = p.stdin.Close() }()
	for {
		p.mu.Lock()
		q, closing := p.queue, p.closing
		p.queue = nil
		p.mu.Unlock()
		for _, line := range q {
			if _, err := p.stdin.Write(line); err != nil {
				p.log.Warn("sidecar stdin broken", "err", err)
				p.kill()
				return
			}
		}
		if closing && len(q) == 0 {
			_ = p.stdin.Close()
			return
		}
		if len(q) > 0 {
			continue
		}
		select {
		case <-p.inWake:
		case <-p.exited:
			return
		}
	}
}

// readLoop turns stdout lines into drafts. A line that is no draft of a contract kind, or too long, is refused and
// journalled without its content: it never reaches the log.
func (p *proc) readLoop() {
	defer close(p.drafts)
	err := readLines(p.stdout, event.MaxLine, func(line []byte) {
		if len(line) == 0 {
			return
		}
		d, why := parseDraft(line)
		if why != "" {
			p.log.Warn("sidecar draft refused", "reason", why, "kind", short(string(d.Kind)), "bytes", len(line))
			if d = p.refused(d); d.Kind == "" {
				return
			}
		}
		select {
		case p.drafts <- d:
		case <-p.quit:
		}
	}, func(n int) {
		p.log.Warn("sidecar draft refused", "reason", "too_long", "bytes", n)
	})
	if err != nil && !errors.Is(err, os.ErrClosed) {
		p.log.Warn("sidecar stdout", "err", err)
	}
	_ = p.stdout.Close()
}

// refused handles a refused draft whose loss would leave the session waiting: an unreadable turn.ended still ends
// the turn (error, errorCode adapter, as the host fails one it cannot read), and a request the owner can never see
// is answered no, as the host answers requests it cannot show. Anything else is dropped (zero draft).
func (p *proc) refused(d event.EventDraft) event.EventDraft {
	var req struct {
		RequestID string `json:"requestId"`
	}
	_ = json.Unmarshal(d.Data, &req)
	const why = "This request could not be shown to the owner."
	var c event.Command
	var err error
	id := "refuse-" + req.RequestID
	switch d.Kind {
	case event.KindTurnEnded:
		b, _ := json.Marshal(event.TurnEnded{Outcome: event.TurnOutcomeError, ErrorCode: CodeStart})
		return event.EventDraft{Kind: event.KindTurnEnded, Data: b}
	case event.KindPermissionRequested:
		c, err = event.NewCommand(id, p.session, event.CmdPermissionReply,
			event.PermissionReply{RequestID: req.RequestID, Decision: event.ReplyDecisionDeny, Message: why})
	case event.KindQuestionAsked:
		c, err = event.NewCommand(id, p.session, event.CmdQuestionReply,
			event.QuestionReply{RequestID: req.RequestID, Text: why})
	case event.KindPlanProposed:
		c, err = event.NewCommand(id, p.session, event.CmdPlanReply,
			event.PlanReply{RequestID: req.RequestID, Decision: event.PlanReplyDecisionRejected, Feedback: why})
	default:
		return event.EventDraft{}
	}
	if err == nil && req.RequestID != "" {
		p.write(c)
	}
	return event.EventDraft{}
}

func short(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// kill ends the sidecar's whole process group and stops reading it.
func (p *proc) kill() {
	p.killOnce.Do(func() {
		select {
		case <-p.exited:
			return // reaped: its group id may belong to someone else by now
		default:
		}
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			_ = p.cmd.Process.Kill()
		}
	})
}

// waitFor passes drafts on until the sidecar exited, at most d; false when it did not.
func (p *proc) waitFor(d time.Duration, forward func(event.EventDraft)) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	drafts := (<-chan event.EventDraft)(p.drafts)
	for {
		select {
		case <-p.exited:
			return true
		case dr, ok := <-drafts:
			if !ok {
				drafts = nil
				continue
			}
			forward(dr)
		case <-t.C:
			return false
		}
	}
}

// reap passes on the rest of the output and returns once the sidecar exited and its stdout ended; after wait it
// kills the group and closes stdout (a process it started may hold the pipe open).
func (p *proc) reap(wait time.Duration, forward func(event.EventDraft)) {
	defer p.quitOnce.Do(func() { close(p.quit) })
	t := time.NewTimer(wait)
	defer t.Stop()
	drafts := (<-chan event.EventDraft)(p.drafts)
	exited := (<-chan struct{})(p.exited)
	for drafts != nil || exited != nil {
		select {
		case d, ok := <-drafts:
			if !ok {
				drafts = nil
				continue
			}
			forward(d)
		case <-exited:
			exited = nil
		case <-t.C:
			p.kill()
			if exited == nil {
				// It exited, yet its stdout is still open: a process of its group holds it, so the group exists
				// and its id is still its own.
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
			_ = p.stdout.Close()
		}
	}
}

func (p *proc) exitText() string {
	var ee *exec.ExitError
	switch {
	case p.waitErr == nil:
		return "0"
	case errors.As(p.waitErr, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "signal " + ws.Signal().String()
		}
		return strconv.Itoa(ee.ExitCode())
	}
	return fmt.Sprint(p.waitErr)
}
