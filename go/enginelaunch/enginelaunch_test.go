package enginelaunch

import (
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	"net/netip"
	"os"
	"testing"
	"time"
)

// CheckLoopback refuses a tree that owns a socket beyond loopback, one
// whose listener is not observed, an owner table that cannot be read, and a
// missing port or tree, each wrapping identity.ErrBeyondLoopback.
func TestCheckLoopbackRefusals(t *testing.T) {
	self := func(pid int, _ time.Time) (bool, error) { return pid == os.Getpid(), nil }
	table := func(sockets ...identity.Socket) func() ([]identity.Socket, error) {
		return func() ([]identity.Socket, error) { return sockets, nil }
	}
	listener := identity.Socket{Proto: "tcp", Addr: netip.MustParseAddr("127.0.0.1"), Port: 41234, PID: os.Getpid()}
	wide := identity.Socket{Proto: "udp", Addr: netip.MustParseAddr("0.0.0.0"), Port: 5353, PID: os.Getpid()}
	for name, c := range map[string]struct {
		sockets  func() ([]identity.Socket, error)
		port     int
		contains identity.TreeContains
	}{
		"beyond loopback":  {table(listener, wide), 41234, self},
		"no listener":      {table(), 41234, self},
		"another port":     {table(listener), 41235, self},
		"unreadable table": {func() ([]identity.Socket, error) { return nil, errors.New("no table") }, 41234, self},
		"no port":          {table(listener), 0, self},
		"no tree":          {table(listener), 41234, nil},
	} {
		if err := CheckLoopback(c.sockets, c.port, c.contains); !errors.Is(err, identity.ErrBeyondLoopback) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
