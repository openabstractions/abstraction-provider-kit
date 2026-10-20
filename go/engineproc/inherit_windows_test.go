package engineproc

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"errors"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	"github.com/openabstractions/abstraction-provider-kit/go/enginelaunch"
	"golang.org/x/sys/windows"
)

// This engine is the current test binary. The provider creates the endpoint;
// no model, engine binary, download or account runtime participates.
func TestEngineInheritsUnixEndpointAndClosesItWithItsTree(t *testing.T) {
	if value := os.Getenv("OA_KIT_ENDPOINT_HANDLE"); value != "" {
		h, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		file := os.NewFile(uintptr(h), "inherited-endpoint")
		listener, err := net.FileListener(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		listener.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		body := make([]byte, 4)
		if _, err := io.ReadFull(conn, body); err != nil || string(body) != "ping" {
			t.Fatalf("request %q: %v", body, err)
		}
		if _, err := conn.Write([]byte("pong")); err != nil {
			t.Fatal(err)
		}
		return
	}
	root, err := os.MkdirTemp("", "oa-t4-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dir, err := enginelaunch.NewSocketDir(filepath.Join(root, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Remove()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: dir.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	file, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		t.Fatal(err)
	}
	var output syncBuffer
	p, err := Start(Spec{Program: program(t), Args: []string{"-test.run=^TestEngineInheritsUnixEndpointAndClosesItWithItsTree$"},
		Env: childEnv("", fmt.Sprintf("OA_KIT_ENDPOINT_HANDLE=%d", file.Fd())), Inherit: []uintptr{file.Fd()}, Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(StopWithin) })
	// The engine's inherited copy must be the only remaining listener.
	file.Close()
	listener.Close()
	conn, err := net.DialTimeout("unix", dir.Socket, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := enginelaunch.CheckSocketOnly(nil, p.Contains()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4)
	if _, err := io.ReadFull(conn, body); err != nil || string(body) != "pong" {
		t.Fatalf("response %q: %v; %s", body, err, output.String())
	}
	conn.Close()
	select {
	case <-p.Exited():
	case <-time.After(15 * time.Second):
		t.Fatal("engine fixture did not join", output.String())
	}
	if conn, err := net.DialTimeout("unix", dir.Socket, time.Second); err == nil {
		conn.Close()
		t.Fatal("endpoint remained after child tree ended")
	}
}

func TestEngineInheritRefusesInvalidHandleBeforeLaunch(t *testing.T) {
	for _, handles := range [][]uintptr{{0}, {^uintptr(0)}} {
		p, err := Start(Spec{Program: program(t), Args: []string{"-test.run=^$"}, Env: childEnv(""), Inherit: handles})
		if p != nil {
			p.Stop(StopWithin)
		}
		if !errors.Is(err, supervision.ErrInherit) {
			t.Fatalf("%v: %v", handles, err)
		}
	}
}
