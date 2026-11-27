//go:build !linux

package supervisor

import "net"

// peerPID is unknown off Linux (flowd runs only there; tests elsewhere get no session attribution).
func peerPID(net.Conn) int { return 0 }
