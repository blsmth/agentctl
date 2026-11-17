//go:build !darwin && !linux

package remoteapproval

import "net"

// PeerPID is unavailable on this platform; the guard relies on the pidfile
// and ping checks alone.
func PeerPID(net.Conn) (int, bool) { return 0, false }
