//go:build windows

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
	"syscall"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/modelbytes"
	"github.com/openabstractions/abstraction-provider-kit/go/modelfiles"
	storage "github.com/openabstractions/abstraction-storage/go"
	"golang.org/x/sys/windows"
)

// run starts r on op and arg with handles inherited and returns its lines.
func run(t *testing.T, r receiver, op, arg string, handles ...windows.Handle) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.argv[0], append(append(r.argv[1:], op), arg)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: syshandles(handles)}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s %s: %v\n%s", r.name, op, arg, err, out)
	}
	return strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), "\n")
}

func syshandles(hs []windows.Handle) []syscall.Handle {
	out := make([]syscall.Handle, 0, len(hs))
	for _, h := range hs {
		if h != 0 {
			out = append(out, syscall.Handle(h))
		}
	}
	return out
}

// inheritable duplicates h with access (0 keeps h's own) as an
// inheritable handle; the test closes it at its end.
func inheritable(t *testing.T, h windows.Handle, access uint32) windows.Handle {
	t.Helper()
	var d windows.Handle
	cur := windows.CurrentProcess()
	opts := uint32(0)
	if access == 0 {
		opts = windows.DUPLICATE_SAME_ACCESS
	}
	if err := windows.DuplicateHandle(cur, h, cur, &d, access, true, opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(d) })
	return d
}

// files is a storage store over one folder: what the fill's located
// source is in the suite.
type files struct{ dir string }

func (f files) Name() string { return "suite" }
func (f files) Find(digest string) (storage.Ref, bool) {
	h := strings.TrimPrefix(digest, "sha256:")
	if _, err := os.Stat(filepath.Join(f.dir, h)); err != nil {
		return storage.Ref{}, false
	}
	return storage.NewRef("suite", digest, h, 0), true
}
func (f files) Place(digest string, size int64) (storage.Ref, error) {
	return storage.NewRef("suite", digest, strings.TrimPrefix(digest, "sha256:"), size), nil
}
func (f files) Path(r storage.Ref) string { return filepath.Join(f.dir, storage.Locator(r)) }

type fixture struct {
	data    []byte
	digest  string
	path    string
	section *modelfiles.Section
}

// newFixture fills a section from a located storage reference: random
// bytes of an odd length, more than one fill chunk.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	data := make([]byte, modelfiles.ChunkSize+3<<20+12345)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	store := files{dir: t.TempDir()}
	path := filepath.Join(store.dir, h)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, ok := store.Find("sha256:" + h)
	if !ok {
		t.Fatal("the suite's store does not find its object")
	}
	released := make(chan struct{})
	s, err := modelfiles.FillRef(context.Background(), nil, store, ref, func() { close(released) })
	if err != nil {
		t.Fatal(err)
	}
	// The section is freed when its last loan ends (INF-MS5); this loan
	// keeps it for the whole suite.
	keep, err := s.Lend()
	if err != nil {
		t.Fatal(err)
	}
	keep.Started()
	t.Cleanup(func() {
		s.Close()
		keep.End()
		select {
		case <-released:
		case <-time.After(10 * time.Second):
			t.Error("the section's holding was not released after its last loan")
		}
	})
	return &fixture{data: data, digest: h, path: path, section: s}
}

// loan lends the section by inheritance with a lease, and returns the URI
// and the handles the child inherits.
func (f *fixture) loan(t *testing.T) (modelbytes.URI, *modelbytes.Lease, []windows.Handle) {
	t.Helper()
	l, err := f.section.Lend()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.End)
	lease, err := modelbytes.NewLease()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Close)
	w, err := lease.Inheritable()
	if err != nil {
		t.Fatal(err)
	}
	u, err := l.URI(w)
	if err != nil {
		t.Fatal(err)
	}
	return u, lease, []windows.Handle{windows.Handle(l.Handle()), windows.Handle(w)}
}

func TestConformance(t *testing.T) {
	f := newFixture(t)
	for _, r := range receivers(t) {
		t.Run(r.name, func(t *testing.T) {
			t.Run("open", func(t *testing.T) { conformOpen(t, r, f) })
			t.Run("offset", func(t *testing.T) { conformOffset(t, r, f) })
			t.Run("write refused", func(t *testing.T) { conformWrite(t, r, f) })
			t.Run("lease ends at release", func(t *testing.T) { conformLease(t, r, f, "hold") })
			t.Run("lease ends at exit", func(t *testing.T) { conformLease(t, r, f, "exit") })
			t.Run("refusals", func(t *testing.T) { conformRefusals(t, r, f) })
			t.Run("file handle and path", func(t *testing.T) { conformFile(t, r, f) })
		})
	}
}

func conformOpen(t *testing.T, r receiver, f *fixture) {
	u, lease, hs := f.loan(t)
	lines := run(t, r, "open", u.String(), hs...)
	lease.Lent()
	expectOpen(t, lines, f.data, f.digest, true)
	waitLease(t, lease)
}

func conformOffset(t *testing.T, r receiver, f *fixture) {
	u, _, hs := f.loan(t)
	// An offset past one view granule and off any page boundary.
	u.Offset, u.Length, u.SHA256 = 65537+4096+3, 1<<20+7, ""
	lines := run(t, r, "open", u.String(), hs...)
	expectOpen(t, lines, f.data[u.Offset:u.Offset+u.Length], "-", true)
}

func conformWrite(t *testing.T, r receiver, f *fixture) {
	u, _, hs := f.loan(t)
	lines := run(t, r, "write", u.String(), hs...)
	if !strings.HasPrefix(lines[0], "ok ") {
		t.Fatalf("receiver answered %q", lines)
	}
	for _, l := range lines[1:] {
		if !strings.HasSuffix(l, " refused") {
			t.Errorf("a write through the loan was not refused: %q", l)
		}
	}
	if len(lines) < 3 {
		t.Errorf("receiver tried too few writes: %q", lines)
	}
}

func conformLease(t *testing.T, r receiver, f *fixture, op string) {
	u, lease, hs := f.loan(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.argv[0], append(append(r.argv[1:], op), u.String())...)
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: syshandles(hs)}
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

func conformRefusals(t *testing.T, r receiver, f *fixture) {
	good, _, hs := f.loan(t)
	refused := func(name, arg, want string, handles ...windows.Handle) {
		t.Helper()
		lines := run(t, r, "open", arg, handles...)
		if lines[0] != "refused "+want {
			t.Errorf("%s: receiver answered %q, want refused %s", name, lines, want)
		}
	}
	// Malformed URIs, refused before any descriptor is touched.
	for name, arg := range malformed(good, f.digest) {
		refused(name, arg, "uri", hs...)
		if _, err := modelbytes.Parse(arg); err == nil {
			t.Errorf("%s: Parse accepted %q", name, arg)
		}
	}
	// A descriptor this process never received.
	refused("not inherited", "fd:8388592?offset=0&length=1&kind=memory", "handle")
	// Beyond the object: the section's size is its length rounded up to 64 KiB.
	past := good
	past.Length = uint64(f.section.Length()+(64<<10))&^(64<<10-1) + 1
	refused("length past the end", past.String(), "size", hs...)

	// An event is no section and no file.
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ev)
	evi := inheritable(t, ev, 0)
	refused("event handle", fmt.Sprintf("fd:%d?offset=0&length=1&kind=memory", evi), "handle", evi)

	// A memory section with the default descriptor is unsealed: any process
	// of the account can raise a handle to it to write.
	raw, err := windows.CreateFileMapping(windows.InvalidHandle, nil, windows.PAGE_READWRITE|0x08000000, 0, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(raw)
	ro := inheritable(t, raw, 0x5)
	refused("unsealed section", fmt.Sprintf("fd:%d?offset=0&length=1024&kind=memory", ro), "writable", ro)
	rw := inheritable(t, raw, 0x7)
	refused("writable section handle", fmt.Sprintf("fd:%d?offset=0&length=1024&kind=memory", rw), "writable", rw)

	// A file handle is never kind=memory, and a writable one is refused.
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fh := inheritable(t, windows.Handle(file.Fd()), 0)
	refused("file handle as memory", fmt.Sprintf("fd:%d?offset=0&length=1&kind=memory", fh), "handle", fh)
	wf, err := os.OpenFile(f.path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer wf.Close()
	wh := inheritable(t, windows.Handle(wf.Fd()), 0)
	refused("writable file handle", fmt.Sprintf("fd:%d?offset=0&length=1", wh), "writable", wh)
	refused("missing path", filepath.Join(t.TempDir(), "absent.safetensors"), "path")
}

// conformFile checks the two other spellings an engine receives: a
// read-only file handle with no kind, and a plain path.
func conformFile(t *testing.T, r receiver, f *fixture) {
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fh := inheritable(t, windows.Handle(file.Fd()), 0x120089) // FILE_GENERIC_READ
	u := modelbytes.URI{Handle: uint64(fh), Length: uint64(len(f.data)), SHA256: f.digest}
	expectOpen(t, run(t, r, "open", u.String(), fh), f.data, f.digest, false)
	lines := run(t, r, "open", f.path)
	expectOpen(t, lines, f.data, "-", false)
}
