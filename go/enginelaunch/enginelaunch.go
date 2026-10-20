package enginelaunch

import (
	"fmt"
	identity "github.com/openabstractions/abstraction-identity"
	"net/netip"
)

// CheckLoopback applies abstraction-identity's loopback-only rule (ID-T7) to
// an engine's contained process tree: every socket the tree owns is on a
// loopback address, and the tree holds the TCP listener on 127.0.0.1 at
// port. Membership is proved by process id and creation time through
// contains, so a reused process id is never counted. sockets reads the
// socket owner tables; nil reads them through identity.Sockets, and a
// platform without them refuses. Every refusal wraps
// identity.ErrBeyondLoopback; the caller stops the tree.
func CheckLoopback(sockets func() ([]identity.Socket, error), port int, contains identity.TreeContains) error {
	if port < 1 || port > 65535 || contains == nil {
		return fmt.Errorf("%w: no port or process tree to check", identity.ErrBeyondLoopback)
	}
	if sockets == nil {
		sockets = identity.Sockets
	}
	owned, err := sockets()
	if err != nil {
		return fmt.Errorf("%w: owner tables: %v", identity.ErrBeyondLoopback, err)
	}
	listener := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(port))
	return identity.CheckLoopbackOnly(owned, identity.OwnedByTree(contains), listener)
}
