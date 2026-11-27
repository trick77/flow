//go:build linux

package term

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// ioctl runs one ioctl on f's descriptor without taking it out of the runtime poller (f.Fd() would make it
// blocking, and a blocked Read could then not be interrupted by Close).
func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// openPTY opens a new pseudo-terminal pair through /dev/ptmx (devpts, as in every Incus container).
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err := ioctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	var n uint32
	if err := ioctl(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

type winsize struct{ rows, cols, x, y uint16 }

func setSize(f *os.File, cols, rows int) error {
	ws := winsize{rows: uint16(rows), cols: uint16(cols)} //nolint:gosec // bounded by flowdproto.ValidTerminalSize
	return ioctl(f, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

type ptyShell struct {
	master *os.File
	cmd    *exec.Cmd
}

func startShell(s ShellSpec) (Shell, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	defer func() { _ = slave.Close() }()
	if err := setSize(master, s.Size.Cols, s.Size.Rows); err != nil {
		_ = master.Close()
		return nil, err
	}
	// The path is the agent user's login shell from /etc/passwd.
	cmd := exec.Command(s.Path) //nolint:gosec // see above
	cmd.Args = []string{s.Argv0}
	cmd.Dir, cmd.Env = s.Dir, s.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own with the PTY as its controlling terminal: job control and ^C work, and a hangup reaches
	// the whole session.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		_ = master.Close()
		return nil, err
	}
	return &ptyShell{master: master, cmd: cmd}, nil
}

func (p *ptyShell) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	// Linux answers EIO once the last process holding the PTY's other side has closed it: the end of output.
	if errors.Is(err, syscall.EIO) {
		err = os.ErrClosed
	}
	return n, err
}

func (p *ptyShell) Write(b []byte) (int, error) { return p.master.Write(b) }
func (p *ptyShell) Close() error                { return p.master.Close() }
func (p *ptyShell) Resize(cols, rows int) error { return setSize(p.master, cols, rows) }
func (p *ptyShell) Pid() int                    { return p.cmd.Process.Pid }

func (p *ptyShell) Hangup() {
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
	_ = p.cmd.Process.Signal(syscall.SIGHUP)
}

func (p *ptyShell) Wait() int {
	err := p.cmd.Wait()
	st := p.cmd.ProcessState
	if st == nil {
		if err != nil {
			return 1
		}
		return 0
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}
