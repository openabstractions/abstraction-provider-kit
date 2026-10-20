//go:build linux

package modelbytes_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/modelbytes"
	"golang.org/x/sys/unix"
)

// On Linux OA lends a sealed memfd (KIT-MB2, MEMORY-LENDING.md section 8).
// The suite here makes one the way a lender would and passes it, and every
// other case's descriptor, to the receiver as an inherited descriptor from
// 3 upward (os/exec ExtraFiles).

type posixFixture struct {
	data   []byte
	digest string
	path   string
	// memfd is the sealed object, reopened read-only.
	memfd *os.File
}

func newPosixFixture(t *testing.T) *posixFixture {
	t.Helper()
	data := make([]byte, 16<<20+3<<20+12345)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	f := &posixFixture{data: data, digest: hex.EncodeToString(sum[:])}
	f.path = filepath.Join(t.TempDir(), f.digest)
	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.memfd = memfd(t, data, true)
	return f
}

// memfd fills a memfd with data, seals it when sealed, and returns it
// reopened read-only.
func memfd(t *testing.T, data []byte, sealed bool) *os.File {
	t.Helper()
	fd, err := unix.MemfdCreate("oa-model-bytes", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		t.Skipf("no memfd here: %v", err)
	}
	w := os.NewFile(uintptr(fd), "memfd")
	defer w.Close()
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if sealed {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
			t.Fatal(err)
		}
	}
	ro, err := os.Open(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	return ro
}

// runFiles starts r on op and arg with files inherited at 3 upward.
func runFiles(t *testing.T, r receiver, op, arg string, files ...*os.File) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.argv[0], append(append(r.argv[1:], op), arg)...)
	cmd.ExtraFiles = files
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s %s: %v\n%s", r.name, op, arg, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestConformancePOSIX(t *testing.T) {
	f := newPosixFixture(t)
	good := modelbytes.URI{Handle: 3, Length: uint64(len(f.data)), SHA256: f.digest, Memory: true}
	for _, r := range receivers(t) {
		t.Run(r.name, func(t *testing.T) {
			t.Run("open", func(t *testing.T) {
				lines := runFiles(t, r, "open", good.String(), f.memfd)
				expectOpen(t, lines, f.data, f.digest, true)
			})
			t.Run("offset", func(t *testing.T) {
				u := good
				u.Offset, u.Length, u.SHA256 = 65537+4096+3, 1<<20+7, ""
				expectOpen(t, runFiles(t, r, "open", u.String(), f.memfd), f.data[u.Offset:u.Offset+u.Length], "-", true)
			})
			t.Run("write refused", func(t *testing.T) {
				lines := runFiles(t, r, "write", good.String(), f.memfd)
				if !strings.HasPrefix(lines[0], "ok ") || len(lines) < 3 {
					t.Fatalf("receiver answered %q", lines)
				}
				for _, l := range lines[1:] {
					if !strings.HasSuffix(l, " refused") {
						t.Errorf("a write through the loan was not refused: %q", l)
					}
				}
			})
			t.Run("lease ends at release", func(t *testing.T) { posixLease(t, r, f, good, "hold") })
			t.Run("lease ends at exit", func(t *testing.T) { posixLease(t, r, f, good, "exit") })
			t.Run("refusals", func(t *testing.T) { posixRefusals(t, r, f, good) })
			t.Run("file descriptor and path", func(t *testing.T) {
				file, err := os.Open(f.path)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				u := modelbytes.URI{Handle: 3, Length: uint64(len(f.data)), SHA256: f.digest}
				expectOpen(t, runFiles(t, r, "open", u.String(), file), f.data, f.digest, false)
				expectOpen(t, runFiles(t, r, "open", f.path), f.data, "-", false)
			})
		})
	}
}

func posixLease(t *testing.T, r receiver, f *posixFixture, good modelbytes.URI, op string) {
	lease, err := modelbytes.NewLease()
	if err != nil {
		t.Fatal(err)
	}
	u := good
	u.Lease = 4
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.argv[0], append(append(r.argv[1:], op), u.String())...)
	cmd.ExtraFiles = []*os.File{f.memfd, lease.Writer()}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lease.Lent()
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "ok ") {
		t.Fatalf("receiver answered %q", sc.Text())
	}
	if op == "hold" {
		select {
		case <-lease.Done():
			t.Fatal("the lease ended while the receiver held the bytes")
		case <-time.After(200 * time.Millisecond):
		}
	}
	stdin.Close()
	waitLease(t, lease)
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func posixRefusals(t *testing.T, r receiver, f *posixFixture, good modelbytes.URI) {
	refused := func(name, arg, want string, files ...*os.File) {
		t.Helper()
		lines := runFiles(t, r, "open", arg, files...)
		if lines[0] != "refused "+want {
			t.Errorf("%s: receiver answered %q, want refused %s", name, lines, want)
		}
	}
	for name, arg := range malformed(good, f.digest) {
		refused(name, arg, "uri", f.memfd)
	}
	refused("not inherited", "fd:200?offset=0&length=1&kind=memory", "handle")
	past := good
	past.Length++
	refused("length past the end", past.String(), "size", f.memfd)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	refused("pipe", "fd:3?offset=0&length=1&kind=memory", "handle", pr)
	unsealed := memfd(t, f.data, false)
	refused("unsealed memfd", "fd:3?offset=0&length=1024&kind=memory", "writable", unsealed)
	rw, err := os.OpenFile(f.path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	refused("writable descriptor", "fd:3?offset=0&length=1", "writable", rw)
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	refused("file as memory", "fd:3?offset=0&length=1&kind=memory", "handle", file)
	refused("missing path", filepath.Join(t.TempDir(), "absent.safetensors"), "path")
}
