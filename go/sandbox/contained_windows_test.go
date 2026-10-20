package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-provider-kit/go/engineproc"
	"github.com/openabstractions/abstraction-provider-kit/go/internal/personfolders"
	"github.com/openabstractions/abstraction-provider-kit/go/testsupport"
	"golang.org/x/sys/windows"
)

// TestMain runs the test binary as a contained child when the parent asks
// for one by OA_SANDBOX_CHILD.
func TestMain(m *testing.M) {
	switch os.Getenv("OA_SANDBOX_CHILD") {
	case "boundaries":
		boundaries()
		os.Exit(0)
	case "tcp":
		fmt.Print(loopback())
		os.Exit(0)
	case "quick":
		os.Exit(0)
	case "hang":
		time.Sleep(time.Hour)
		os.Exit(0)
	case "tree-crash":
		crashTreeEditChild()
		os.Exit(2)
	}
	os.Exit(personfolders.Main(m))
}

// outsideProbes names the opt-in variable for the probes that leave the
// machine: it holds a TCP address off the machine, host:port, that answers
// the test itself (for example 1.1.1.1:443), and enables the contained
// child's TCP connect to it, a DNS lookup and a DNS query over UDP to the
// host's port 53. Without it the boundaries test stays on the machine.
const outsideProbes = testsupport.OutsideAddressEnv

func tcp(address string) string {
	c, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return "blocked"
	}
	c.Close()
	return "open"
}

func loopback() string {
	c, err := net.DialTimeout("tcp", os.Getenv("OA_LOOPBACK"), time.Second)
	if err != nil {
		return "blocked"
	}
	c.Close()
	return "open"
}

// boundaries is the contained child: what it can and cannot reach, as one
// JSON line.
func boundaries() {
	out := map[string]string{"loopback": loopback(), "loopback_udp": "blocked"}
	// A datagram to the test's loopback receiver; the receiver, not this
	// reply, says whether it arrived.
	if c, err := net.Dial("udp", os.Getenv("OA_LOOPBACK_UDP")); err == nil {
		if _, err := c.Write([]byte("contained")); err == nil {
			out["loopback_udp"] = "sent"
		}
		c.Close()
	}
	if outside := os.Getenv(outsideProbes); outside != "" {
		out["tcp"] = tcp(outside)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := net.DefaultResolver.LookupHost(ctx, "example.com"); err != nil {
			out["dns"] = "blocked"
		} else {
			out["dns"] = "resolved"
		}
		cancel()
		out["udp"] = "blocked"
		host, _, _ := net.SplitHostPort(outside)
		if c, err := net.Dial("udp", net.JoinHostPort(host, "53")); err == nil {
			// One DNS query for example.com; any answer means UDP left.
			query := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
			if _, err := c.Write(query); err == nil {
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				if n, err := c.Read(make([]byte, 512)); err == nil && n > 0 {
					out["udp"] = "answered"
				}
			}
			c.Close()
		}
	}
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), "OA_SANDBOX_CHILD=tcp")
	if b, err := child.Output(); err != nil {
		out["child"] = "failed: " + err.Error()
	} else {
		out["child"] = string(b)
	}
	state := filepath.Join(os.Getenv("OA_STATE"), "written.txt")
	if err := os.WriteFile(state, []byte("state"), 0o600); err != nil {
		out["state"] = "denied"
	} else if err := os.Remove(state); err != nil {
		out["state"] = "undeletable"
	} else {
		out["state"] = "written"
	}
	if _, err := os.ReadDir(filepath.Join(os.Getenv("USERPROFILE"), "Documents")); err != nil {
		out["documents"] = "denied"
	} else {
		out["documents"] = "listed"
	}
	if _, err := os.ReadFile(os.Getenv("OA_OUTSIDE_FILE")); err != nil {
		out["outside"] = "denied"
	} else {
		out["outside"] = "read"
	}
	out["pipe"] = "failed"
	if c, err := listen.Dial(os.Getenv("OA_PIPE")); err != nil {
		out["pipe"] = "refused: " + err.Error()
	} else {
		if _, err := c.Write([]byte("ping\n")); err == nil {
			if line, err := bufio.NewReader(c).ReadString('\n'); err == nil {
				out["pipe"] = strings.TrimSpace(line)
			}
		}
		c.Close()
	}
	// An admitted client must never be able to open another server instance.
	out["pipe_server"] = "denied"
	if h, err := windows.CreateNamedPipe(windows.StringToUTF16Ptr(os.Getenv("OA_PIPE")), windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, windows.PIPE_UNLIMITED_INSTANCES, 4096, 4096, 0, nil); err == nil {
		out["pipe_server"] = "created"
		windows.CloseHandle(h)
	}
	out["lent"] = "absent"
	if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err == nil {
		if v, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64); err == nil {
			f := os.NewFile(uintptr(v), "lent")
			if b, err := io.ReadAll(f); err == nil {
				out["lent"] = string(b)
			}
			if _, err := f.Write([]byte("x")); err == nil {
				out["lent"] += "+writable"
			}
			f.Close()
		}
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

// childBinary is a copy of this test binary in the rig's engine folder,
// which the sandbox grants read and run.
func childBinary(t *testing.T, r *rig) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.engine, "bin", "child.exe")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestContainedEngineBoundaries: an engine started in its prepared sandbox
// reaches no network, and nor does its child: nothing it sends arrives at
// the test's loopback TCP listener or UDP receiver, and with
// OA_PROVIDER_KIT_OUTSIDE_ADDRESS set, neither TCP to that address, nor
// DNS, nor UDP leaves the machine. It writes its state folder; it reads
// nothing outside its grants, the person's Documents included; it talks to
// an OA pipe that admits its container; and it reads a file it may not
// open by path through a handle the provider lends, without being able to
// write it (KIT-S3, KIT-S6; identity ID-K1).
func TestContainedEngineBoundaries(t *testing.T) {
	loop, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Close()
	if c, err := net.DialTimeout("tcp", loop.Addr().String(), time.Second); err != nil {
		t.Fatalf("uncontained loopback control: %v", err)
	} else {
		c.Close()
	}
	// The control's own connection is taken off the listener's queue, so
	// any connection left there afterwards is the contained tree's.
	if c, err := loop.Accept(); err != nil {
		t.Fatalf("uncontained loopback control: %v", err)
	} else {
		c.Close()
	}
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	if c, err := net.Dial("udp", udp.LocalAddr().String()); err != nil {
		t.Fatalf("uncontained loopback UDP control: %v", err)
	} else {
		_, err = c.Write([]byte("control"))
		c.Close()
		_ = udp.SetReadDeadline(time.Now().Add(2 * time.Second))
		if n, _, rerr := udp.ReadFrom(make([]byte, 64)); err != nil || rerr != nil || n == 0 {
			t.Fatalf("uncontained loopback UDP control: %v %v", err, rerr)
		}
	}
	outside := testsupport.OutsideNetwork(t)
	r := newRig(t)
	exe := childBinary(t, r)
	r.install(t)
	l, err := Prepare(r.manifest, []Grant{{Path: exe, Access: ReadExecute}, {Path: r.state, Access: Modify}})
	if err != nil {
		t.Fatal(err)
	}
	sid := l.SID
	pipe := fmt.Sprintf(`\\.\pipe\oa-sandbox-test-%d`, os.Getpid())
	ln, err := listen.ListenAdmitting(pipe, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if h, err := windows.CreateNamedPipe(windows.StringToUTF16Ptr(pipe), windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, windows.PIPE_UNLIMITED_INSTANCES, 4096, 4096, 0, nil); err != nil {
		t.Fatalf("uncontained pipe-instance control: %v", err)
	} else {
		windows.CloseHandle(h)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if line, err := bufio.NewReader(c).ReadString('\n'); err == nil && line == "ping\n" {
			_, _ = c.Write([]byte("pong\n"))
		}
	}()
	outsideFile := filepath.Join(r.outside, "d.txt")
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	var stderr bytes.Buffer
	env := append(os.Environ(), "OA_SANDBOX_CHILD=boundaries", "OA_STATE="+r.state, "OA_OUTSIDE_FILE="+outsideFile, "OA_PIPE="+pipe,
		"OA_LOOPBACK="+loop.Addr().String(), "OA_LOOPBACK_UDP="+udp.LocalAddr().String())
	p, err := engineproc.Start(engineproc.Spec{Program: exe, Env: env, Stdin: inR, Stdout: outW, Output: &stderr,
		Engine: "kit-test", Model: "boundaries", Provider: "kit-test", Container: sid, MemoryLimit: 1 << 30})
	inR.Close()
	outW.Close()
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	f, err := os.Open(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	v, err := p.Lend(f)
	f.Close()
	if err != nil {
		p.Stop(engineproc.StopWithin)
		t.Fatal(err)
	}
	fmt.Fprintf(inW, "%d\n", v)
	inW.Close()
	report, _ := io.ReadAll(outR)
	outR.Close()
	<-p.Exited()
	l.Close()
	var got map[string]string
	if err := json.Unmarshal(report, &got); err != nil {
		t.Fatalf("the child said %q (%s)", report, stderr.String())
	}
	want := map[string]string{"loopback": "blocked", "child": "blocked", "state": "written",
		"documents": "denied", "outside": "denied", "pipe": "pong", "pipe_server": "denied", "lent": "x"}
	if outside != "" {
		want["tcp"], want["dns"], want["udp"] = "blocked", "blocked", "blocked"
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	// The listener and the receiver are the authority on what arrived: the
	// tree's connects and its datagram ended before it exited.
	_ = loop.(*net.TCPListener).SetDeadline(time.Now().Add(300 * time.Millisecond))
	if c, err := loop.Accept(); err == nil {
		t.Errorf("the contained tree reached the loopback listener from %s", c.RemoteAddr())
		c.Close()
	}
	_ = udp.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, from, err := udp.ReadFrom(make([]byte, 64)); err == nil {
		t.Errorf("the contained tree's %d-byte datagram reached the loopback receiver from %s", n, from)
	}
	if t.Failed() {
		t.Logf("the child said %v", got)
	}
	r.removeAll(t, sid)
}

// TestContainedLaunchCost measures what the sandbox adds to a launch once
// installed: the launch check, and a contained start against an uncontained
// one of the same program, each to its exit. The target is under a second
// (KIT-S3).
func TestContainedLaunchCost(t *testing.T) {
	r := newRig(t)
	exe := childBinary(t, r)
	r.install(t)
	env := append(os.Environ(), "OA_SANDBOX_CHILD=quick")
	needs := []Grant{{Path: exe, Access: ReadExecute}, {Path: r.state, Access: Modify}}
	run := func(container bool) (time.Duration, time.Duration) {
		begin := time.Now()
		l, err := Prepare(r.manifest, needs)
		if err != nil {
			t.Fatal(err)
		}
		check := time.Since(begin)
		spec := engineproc.Spec{Program: exe, Env: env, Engine: "kit-test", Model: "quick", Provider: "kit-test"}
		if container {
			spec.Container = l.SID
		}
		begin = time.Now()
		p, err := engineproc.Start(spec)
		if err != nil {
			t.Fatal(err)
		}
		<-p.Exited()
		took := time.Since(begin)
		l.Close()
		return check, took
	}
	const rounds = 7
	var checks, contained, plain []time.Duration
	for i := 0; i < rounds; i++ {
		c, d := run(true)
		_, u := run(false)
		checks, contained, plain = append(checks, c), append(contained, d), append(plain, u)
	}
	median := func(d []time.Duration) time.Duration { slices.Sort(d); return d[len(d)/2] }
	check, in, out := median(checks), median(contained), median(plain)
	t.Logf("launch check %v; contained start to exit %v, uncontained %v, added %v (medians of %d)", check, in, out, in-out, rounds)
	if check+in-out > time.Second {
		t.Fatalf("the sandbox adds %v to a launch", check+in-out)
	}
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, m.SID)
}

// TestAStopMidLaunchLeavesTheSandboxWhole: an engine stopped while it starts,
// as a yield or an unload stops one, ends its whole contained tree; the
// grants and profile stay as Setup left them, the next launch is let in, and
// once the stopped launch lets go Setup may update (KIT-S3, KIT-P6).
func TestAStopMidLaunchLeavesTheSandboxWhole(t *testing.T) {
	r := newRig(t)
	exe := childBinary(t, r)
	r.install(t)
	needs := []Grant{{Path: exe, Access: ReadExecute}}
	l, err := Prepare(r.manifest, needs)
	if err != nil {
		t.Fatal(err)
	}
	p, err := engineproc.Start(engineproc.Spec{Program: exe, Env: append(os.Environ(), "OA_SANDBOX_CHILD=hang"),
		Engine: "kit-test", Model: "hang", Provider: "kit-test", Container: l.SID})
	if err != nil {
		t.Fatal(err)
	}
	p.Stop(engineproc.StopWithin)
	select {
	case <-p.Exited():
	case <-time.After(30 * time.Second):
		t.Fatal("the stopped engine's tree did not end")
	}
	next, err := Prepare(r.manifest, needs)
	if err != nil {
		t.Fatalf("the next launch: %v", err)
	}
	next.Close()
	l.Close()
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, m.SID)
}
