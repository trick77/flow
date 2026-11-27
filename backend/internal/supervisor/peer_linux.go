//go:build linux

package supervisor

import (
	"net"
	"syscall"
)

// peerPID is the process at the other end of a unix socket (SO_PEERCRED), 0 when unknown.
func peerPID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}
