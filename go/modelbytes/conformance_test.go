package modelbytes_test

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/modelbytes"
)

// The model bytes conformance suite (KIT-MB6): every receiver an engine
// vendors, the C header and the Python module here, runs each case as a
// child the lender starts with the case's descriptors, and answers in the
// lines testdata/receiver_c.c defines. conformance_windows_test.go lends
// sections by handle inheritance; conformance_linux_test.go lends sealed
// memfds as inherited descriptors.

// receiver is one implementation under the suite: argv before the
// operation and its argument.
type receiver struct {
	name string
	argv []string
}

func receivers(t *testing.T) []receiver {
	t.Helper()
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []receiver
	if py := pythonExe(); py != "" {
		out = append(out, receiver{"python", []string{py, "-I", "-B", filepath.Join(here, "testdata", "receiver_py.py"), here}})
	} else {
		t.Log("no Python 3 found: the Python receiver is not run")
	}
	if exe := buildC(t, here); exe != "" {
		out = append(out, receiver{"c", []string{exe}})
	}
	if len(out) == 0 {
		t.Skip("no receiver could be run: neither Python 3 nor a C compiler was found")
	}
	return out
}

// pythonExe is the interpreter itself, so inherited handles reach the
// receiver without the py launcher between.
func pythonExe() string {
	for _, try := range [][]string{{"py", "-3.12"}, {"py", "-3"}, {"python3"}, {"python"}} {
		args := append(try[1:], "-c", "import sys; print(sys.executable)")
		out, err := exec.Command(try[0], args...).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

// buildC compiles the C receiver with gcc, or cl when it is on PATH.
func buildC(t *testing.T, here string) string {
	exe := filepath.Join(t.TempDir(), "receiver_c")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	src := filepath.Join(here, "testdata", "receiver_c.c")
	var cmd *exec.Cmd
	if gcc, err := exec.LookPath("gcc"); err == nil {
		cmd = exec.Command(gcc, "-std=c99", "-Wall", "-Wextra", "-Werror", "-Wno-cast-function-type", "-O1", "-I", here, "-o", exe, src)
	} else if cl, err := exec.LookPath("cl"); err == nil {
		cmd = exec.Command(cl, "/nologo", "/W4", "/WX", "/I", here, "/Fe:"+exe, src)
		cmd.Dir = filepath.Dir(exe)
	} else {
		t.Log("no C compiler on PATH: the C receiver is not run")
		return ""
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile the C receiver: %v\n%s", err, out)
	}
	return exe
}

// expectOpen checks the ok line and the probe and range lines of an open
// of want's bytes.
func expectOpen(t *testing.T, lines []string, want []byte, digest string, memory bool) {
	t.Helper()
	n := int64(len(want))
	mem := 0
	if memory {
		mem = 1
	}
	if len(lines) != 8 || lines[0] != fmt.Sprintf("ok %d %s %d", n, digest, mem) {
		t.Fatalf("receiver answered %q", lines)
	}
	tail := int64(0)
	if n > 16 {
		tail = n - 16
	}
	for i, off := range []int64{0, n / 2, tail} {
		end := min(off+16, n)
		if got := fmt.Sprintf("probe %d ok %s 1", off, hex.EncodeToString(want[off:end])); lines[1+i] != got {
			t.Errorf("probe %d: %q, want %q", off, lines[1+i], got)
		}
	}
	for i, w := range []string{"range_end range", "range_far range", "range_zero ok", "released"} {
		if lines[4+i] != w {
			t.Errorf("line %d: %q, want %q", 4+i, lines[4+i], w)
		}
	}
}

func waitLease(t *testing.T, lease *modelbytes.Lease) {
	t.Helper()
	select {
	case <-lease.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the lease did not end")
	}
}

// malformed are URIs every receiver refuses before it touches a descriptor.
func malformed(good modelbytes.URI, digest string) map[string]string {
	base := fmt.Sprintf("fd:%d?offset=0&length=%d", good.Handle, good.Length)
	return map[string]string{
		"unknown parameter":   base + "&mode=rw",
		"repeated parameter":  base + "&offset=0",
		"leading zero":        fmt.Sprintf("fd:%d?offset=00&length=%d", good.Handle, good.Length),
		"no length":           fmt.Sprintf("fd:%d?offset=0", good.Handle),
		"zero length":         fmt.Sprintf("fd:%d?offset=0&length=0", good.Handle),
		"upper-case digest":   base + "&sha256=" + strings.ToUpper(digest),
		"kind other":          base + "&kind=file",
		"64-bit overflow":     fmt.Sprintf("fd:%d?offset=18446744073709551615&length=2", good.Handle),
		"no query":            fmt.Sprintf("fd:%d", good.Handle),
		"signed number":       fmt.Sprintf("fd:%d?offset=-0&length=%d", good.Handle, good.Length),
		"trailing ampersand":  base + "&",
		"descriptor not a nr": "fd:x?offset=0&length=1",
	}
}
