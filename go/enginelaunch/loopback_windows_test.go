//go:build windows

package enginelaunch

import (
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

// CheckLoopback passes this process's own listener on 127.0.0.1 through the
// real owner tables, and refuses it once the tree also listens beyond
// loopback.
func TestCheckLoopbackReadsTheOwnerTables(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	self := func(pid int, _ time.Time) (bool, error) { return pid == os.Getpid(), nil }
	if err := CheckLoopback(nil, port, self); err != nil {
		t.Fatalf("a loopback listener was refused: %v", err)
	}
	wide := func() ([]identity.Socket, error) {
		sockets, err := identity.Sockets()
		return append(sockets, identity.Socket{Proto: "tcp", Addr: netip.MustParseAddr("0.0.0.0"), Port: port, PID: os.Getpid()}), err
	}
	if err := CheckLoopback(wide, port, self); !errors.Is(err, identity.ErrBeyondLoopback) {
		t.Fatalf("a listener beyond loopback passed: %v", err)
	}
}
