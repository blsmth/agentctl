package remoteapproval

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerPID reports the pid of the process on the other end of a unix socket
// (LOCAL_PEERPID). ok is false when conn is not a unix socket or the kernel
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
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil || serr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
