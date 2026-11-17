package remoteapproval

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerPID reports the pid of the process on the other end of a unix socket
// (SO_PEERCRED). ok is false when conn is not a unix socket or the kernel
// does not say.
func PeerPID(conn net.Conn) (int, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || serr != nil || cred == nil || cred.Pid <= 0 {
		return 0, false
	}
	return int(cred.Pid), true
}
