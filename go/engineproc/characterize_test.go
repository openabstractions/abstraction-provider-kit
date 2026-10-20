package engineproc

// Characterization tests (CQ-15a): they record how an engine process is
// started, shaped, waited for, stopped and reported today, so the provider
// kit's move (W1-01) can prove it changed nothing. The engine is this test
// binary started again as a fake child (fakeChildEnv); no real engine runs.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/devicelost"
	"github.com/openabstractions/abstraction-provider-kit/go/refusal"
)

// fakeChildEnv selects the fake engine's behaviour when this test binary is
// started as a child: "exit:N" exits with N, "echo" writes its arguments,
// working folder and one environment lookup to stdout and a line to stderr,
// "sleep" waits to be stopped.
const fakeChildEnv = "OA_KIT_FAKE_ENGINE"

// The fake child runs before TestMain, so it never touches the person
// folders or the test framework.
func init() {
	mode := os.Getenv(fakeChildEnv)
	if mode == "" {
		return
	}
	switch {
	case strings.HasPrefix(mode, "exit:"):
		code, _ := strconv.Atoi(strings.TrimPrefix(mode, "exit:"))
		os.Exit(code)
	case mode == "echo":
		wd, _ := os.Getwd()
		fmt.Fprintf(os.Stdout, "args=%s\n", strings.Join(os.Args[1:], "|"))
		fmt.Fprintf(os.Stdout, "wd=%s\n", wd)
		fmt.Fprintf(os.Stdout, "inherited=%q\n", os.Getenv("OA_KIT_NOT_PASSED"))
		fmt.Fprintf(os.Stdout, "passed=%q\n", os.Getenv("OA_KIT_PASSED"))
		fmt.Fprintln(os.Stderr, "stderr line")
		os.Exit(0)
	case mode == "sleep":
		time.Sleep(time.Minute)
		os.Exit(9)
	}
	os.Exit(70)
}

// syncBuffer is an engine output a test reads while the engine writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// childEnv is the clean environment a fake child starts from: its mode, and
// the folders a Windows program needs to run.
func childEnv(mode string, extra ...string) []string {
	env := []string{fakeChildEnv + "=" + mode}
	for _, name := range []string{"SystemRoot", "TEMP", "TMP"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return append(env, extra...)
}

func program(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func startFake(t *testing.T, spec Spec) (*Process, chan Exit) {
	t.Helper()
	exits := make(chan Exit, 1)
	spec.Program = program(t)
	spec.Exited = func(x Exit) { exits <- x }
	p, err := Start(spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { p.Stop(StopWithin) })
	return p, exits
}

// waitExit is the engine's exit record, or a failure after 30 seconds.
func waitExit(t *testing.T, exits <-chan Exit) Exit {
	t.Helper()
	select {
	case x := <-exits:
		return x
	case <-time.After(30 * time.Second):
		t.Fatal("no exit record")
	}
	return Exit{}
}

func waitGone(t *testing.T, p *Process) {
	t.Helper()
	select {
	case <-p.Exited():
	case <-time.After(30 * time.Second):
		t.Fatal("the engine never ended")
	}
}

func TestCharacterizeStartRefusesAnEmptyEnvironment(t *testing.T) {
	if _, err := Start(Spec{Program: program(t)}); !errors.Is(err, ErrEnvironment) {
		t.Fatalf("empty environment: %v", err)
	}
}

func TestCharacterizeStartFailsForAMissingProgram(t *testing.T) {
	if _, err := Start(Spec{Program: filepath.Join(t.TempDir(), "absent.exe"), Env: childEnv("echo")}); err == nil {
		t.Fatal("a missing program started")
	}
}

// An engine's exit code reaches its exit record, the record names the
// start, and the record's line is the last line of a provider-owned log.
func TestCharacterizeExitRecord(t *testing.T) {
	out := &syncBuffer{}
	p, exits := startFake(t, Spec{Env: childEnv("exit:3"), Output: out,
		Engine: "fake-engine", Model: "m1", Provider: "prov"})
	if p.PID() <= 0 {
		t.Fatalf("pid %d", p.PID())
	}
	x := waitExit(t, exits)
	waitGone(t, p)
	if !p.Gone() {
		t.Fatal("Gone is false after Exited closed")
	}
	if x.Engine != "fake-engine" || x.Model != "m1" || x.PID != p.PID() || x.Provider != "prov" ||
		x.Code != 3 || x.Stopped || x.Signal != "" || x.Unknown != "" || !x.Notable() || x.Crash() {
		t.Fatalf("exit record %+v", x)
	}
	if x.How() != "exited 3" {
		t.Fatalf("How %q", x.How())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if last != "prov: "+x.Line() {
		t.Fatalf("log's last line %q, want %q", last, "prov: "+x.Line())
	}
}

// A clean exit is not notable and its log prefix defaults to "provider".
func TestCharacterizeCleanExitWithoutProvider(t *testing.T) {
	out := &syncBuffer{}
	p, exits := startFake(t, Spec{Env: childEnv("exit:0"), Output: out, Engine: "e", Model: "m"})
	x := waitExit(t, exits)
	waitGone(t, p)
	if x.Code != 0 || x.Notable() || x.Provider != "" || x.How() != "exited 0" {
		t.Fatalf("clean exit %+v %q", x, x.How())
	}
	if !strings.Contains(out.String(), "provider: e for m (pid ") {
		t.Fatalf("log %q", out.String())
	}
}

// Arguments pass through unchanged, the environment is exactly the spec's,
// the working folder defaults to the program's folder, and stdout and
// stderr both reach Output.
func TestCharacterizeArgumentsEnvironmentAndFolder(t *testing.T) {
	t.Setenv("OA_KIT_NOT_PASSED", "leak")
	out := &syncBuffer{}
	p, exits := startFake(t, Spec{Args: []string{"-a", "b c", ""}, Env: childEnv("echo", "OA_KIT_PASSED=yes"), Output: out})
	waitExit(t, exits)
	waitGone(t, p)
	text := out.String()
	wantDir, _ := filepath.EvalSymlinks(filepath.Dir(program(t)))
	for _, want := range []string{"args=-a|b c|\n", `inherited=""`, `passed="yes"`, "stderr line"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q lacks %q", text, want)
		}
	}
	if !sameDir(t, text, wantDir) {
		t.Fatalf("output %q does not run in %s", text, wantDir)
	}

	dir := t.TempDir()
	out2 := &syncBuffer{}
	p2, exits2 := startFake(t, Spec{Env: childEnv("echo"), Dir: dir, Output: out2})
	waitExit(t, exits2)
	waitGone(t, p2)
	want, _ := filepath.EvalSymlinks(dir)
	if !sameDir(t, out2.String(), want) {
		t.Fatalf("output %q does not run in %s", out2.String(), want)
	}
}

func sameDir(t *testing.T, output, want string) bool {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if wd, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "wd="); ok {
			got, err := filepath.EvalSymlinks(wd)
			return err == nil && strings.EqualFold(got, want)
		}
	}
	return false
}

// A provider's stop ends the engine, Exited closes, and the record says
// the provider stopped it.
func TestCharacterizeStop(t *testing.T) {
	p, exits := startFake(t, Spec{Env: childEnv("sleep"), Engine: "e", Model: "m", Provider: "prov"})
	if p.Gone() {
		t.Fatal("gone at once")
	}
	o := &Owned{Name: "m", Process: p}
	stops := 0
	o.OnStop = func() { stops++ }
	if !o.Alive() || o.PID() != p.PID() {
		t.Fatal("owned engine not alive")
	}
	if !o.Stop() || stops != 1 || o.Alive() {
		t.Fatalf("stop: alive %v, OnStop %d", o.Alive(), stops)
	}
	x := waitExit(t, exits)
	if !x.Stopped || x.Notable() || x.How() != "stopped by prov" {
		t.Fatalf("stopped record %+v %q", x, x.How())
	}
}

func TestCharacterizeWaitEnds(t *testing.T) {
	gone := &Process{exited: make(chan struct{})}
	close(gone.exited)
	never := func(context.Context) (bool, error) { return false, nil }
	if err := Wait(context.Background(), gone, time.Minute, never); err == nil || err.Error() != "the engine exited before it was ready" {
		t.Fatalf("exited: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, running(), time.Minute, never); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if err := Wait(context.Background(), running(), 0, never); err == nil || err.Error() != "the engine was not ready within 0s" {
		t.Fatalf("budget: %v", err)
	}
	// An exit during the poll's sleep ends the wait.
	p := running()
	go func() { time.Sleep(readyPoll / 4); close(p.exited) }()
	if err := Wait(context.Background(), p, time.Minute, never); err == nil || !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("exit while polling: %v", err)
	}
	// A ready answer with an error is the error.
	boom := errors.New("boom")
	if err := Wait(context.Background(), running(), time.Minute, func(context.Context) (bool, error) { return true, boom }); !errors.Is(err, boom) {
		t.Fatalf("ready with error: %v", err)
	}
}

func TestCharacterizeOwnedReady(t *testing.T) {
	peer := &Owned{Process: running(), ReadyWithin: time.Minute,
		Probe: func(context.Context) (bool, error) { return false, ErrPeer }}
	if err := peer.Ready(context.Background()); refusal.WordOf(err) != refusal.EnginePeer {
		t.Fatalf("peer: %v", err)
	}
	other := errors.New("other")
	plain := &Owned{Process: running(), ReadyWithin: time.Minute,
		Probe: func(context.Context) (bool, error) { return false, other }}
	if err := plain.Ready(context.Background()); !errors.Is(err, other) || refusal.WordOf(err) != "" {
		t.Fatalf("other: %v", err)
	}
	ready := func(context.Context) (bool, error) { return true, nil }
	ok := &Owned{Process: running(), ReadyWithin: time.Minute, Probe: ready}
	if err := ok.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	sockets := &Owned{Process: running(), ReadyWithin: time.Minute, Probe: ready,
		Sockets: func() error { return errors.New("wide socket") }, SocketWord: "engine:socket"}
	if err := sockets.Ready(context.Background()); refusal.WordOf(err) != "engine:socket" || !strings.Contains(err.Error(), "wide socket") {
		t.Fatalf("socket rule: %v", err)
	}
}

// A socket that later breaks the rule is reported while the engine is
// resident and stops the engine.
func TestCharacterizeOwnedWatch(t *testing.T) {
	p := running()
	stopped := make(chan struct{})
	p.stopOwned = func(time.Duration) {
		select {
		case <-p.exited:
		default:
			close(p.exited)
			close(stopped)
		}
	}
	var mu sync.Mutex
	calls := 0
	reported := make(chan error, 1)
	o := &Owned{Name: "m", Process: p, ReadyWithin: time.Minute, Watch: 10 * time.Millisecond, SocketWord: "engine:socket",
		Probe: func(context.Context) (bool, error) { return true, nil },
		Sockets: func() error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls > 1 {
				return errors.New("late")
			}
			return nil
		},
		Report: func(err error) { reported <- err }}
	if err := o.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reported:
		if refusal.WordOf(err) != "engine:socket" || !strings.Contains(err.Error(), "m: late") {
			t.Fatalf("report %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no report")
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("not stopped")
	}
}

// An engine no longer resident is stopped without a report.
func TestCharacterizeOwnedWatchNotResident(t *testing.T) {
	p := running()
	stopped := make(chan struct{})
	p.stopOwned = func(time.Duration) { close(stopped) }
	n := 0
	o := &Owned{Name: "m", Process: p, ReadyWithin: time.Minute, Watch: 10 * time.Millisecond,
		Probe: func(context.Context) (bool, error) { return true, nil },
		Sockets: func() error {
			n++
			if n > 1 {
				return errors.New("late")
			}
			return nil
		},
		Resident: func() bool { return false },
		Report:   func(err error) { t.Errorf("reported %v", err) }}
	if err := o.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("not stopped")
	}
}

func TestCharacterizeProcessWithoutSupervision(t *testing.T) {
	var none *Process
	if none.PID() != 0 {
		t.Fatal("nil PID")
	}
	none.Stop(time.Second)
	if _, err := none.LendTo(nil, 1); err == nil {
		t.Fatal("nil process lent")
	}
	p := &Process{id: 42, exited: make(chan struct{})}
	p.Stop(time.Second) // no stop function: nothing happens
	if p.Contains() != nil {
		t.Fatal("contains without a job")
	}
	if _, err := p.Lend(nil); err == nil || !strings.Contains(err.Error(), "lends no handle") {
		t.Fatalf("lend: %v", err)
	}
	for _, c := range []struct {
		proc identity.Process
		want bool
	}{
		{identity.Process{PID: 42}, true},
		{identity.Process{PID: 43}, false},
		{identity.Process{PID: 0}, false},
		{identity.Process{PID: 42, Recycled: true}, false},
	} {
		if got, err := p.Verify(c.proc); got != c.want || err != nil {
			t.Errorf("Verify(%+v) = %v %v", c.proc, got, err)
		}
	}
	var asked int
	p.contains = func(pid int, _ time.Time) (bool, error) { asked = pid; return true, nil }
	if ok, _ := p.Verify(identity.Process{PID: 7}); !ok || asked != 7 {
		t.Fatal("Verify does not ask the tree")
	}
	p.lend = func(uintptr, uint32, int) (uintptr, error) { return 5, nil }
	if _, err := p.Lend(nil); err == nil || !strings.Contains(err.Error(), "needs a file") {
		t.Fatalf("nil file: %v", err)
	}
	close(p.exited)
	if _, err := p.Lend(os.Stdin); err == nil || !strings.Contains(err.Error(), "has ended") {
		t.Fatalf("ended: %v", err)
	}
}

func TestCharacterizeExitWords(t *testing.T) {
	cases := []struct {
		x       Exit
		how     string
		notable bool
		crash   bool
	}{
		{Exit{Exit: supervision.Exit{Code: 0}}, "exited 0", false, false},
		{Exit{Exit: supervision.Exit{Code: 2}}, "exited 2", true, false},
		{Exit{Exit: supervision.Exit{Code: 2, Stopped: true}}, "stopped by the provider", false, false},
		{Exit{Exit: supervision.Exit{Stopped: true}, Provider: "p"}, "stopped by p", false, false},
		{Exit{Signal: "killed"}, "killed by killed", true, false},
		{Exit{Signal: "aborted"}, "killed by aborted", true, true},
		{Exit{Signal: "aborted", Exit: supervision.Exit{Stopped: true}}, "stopped by the provider", false, false},
		{Exit{Exit: supervision.Exit{MemoryLimitHit: true}}, "exited 0", true, false},
		{Exit{Exit: supervision.Exit{Unknown: "u", Stopped: true}}, "ended, exit unknown (u)", true, false},
		{Exit{Exit: supervision.Exit{Code: 0xC0000005}}, "crashed 0xC0000005 STATUS_ACCESS_VIOLATION", true, true},
	}
	for _, c := range cases {
		if c.x.How() != c.how || c.x.Notable() != c.notable || c.x.Crash() != c.crash {
			t.Errorf("%+v: How %q Notable %v Crash %v; want %q %v %v", c.x, c.x.How(), c.x.Notable(), c.x.Crash(), c.how, c.notable, c.crash)
		}
	}
	x := Exit{Engine: "e", Model: "m", PID: 1, Exit: supervision.Exit{Runtime: 1500 * time.Millisecond}}
	if x.Line() != "e for m (pid 1) exited 0 after 1.5 s" {
		t.Fatalf("plain line %q", x.Line())
	}
	if hit := (Exit{Exit: supervision.Exit{MemoryLimitHit: true}}).Line(); !strings.HasSuffix(hit, "; a job memory limit HIT") {
		t.Fatalf("limitless hit %q", hit)
	}
	x.Log(nil) // a nil logger is ignored
}

func TestCharacterizeReport(t *testing.T) {
	var logBuf, errBuf bytes.Buffer
	report := Report(slog.New(slog.NewTextHandler(&logBuf, nil)), &errBuf, "prog")
	x := Exit{Engine: "e", Model: "m", PID: 3, Exit: supervision.Exit{Code: 1}}
	report(x)
	if errBuf.String() != "prog: "+x.Line()+"\n" || !strings.Contains(logBuf.String(), "level=WARN") {
		t.Fatalf("report %q / %q", errBuf.String(), logBuf.String())
	}
	Report(nil, nil, "prog")(x) // no logger and no stderr write nothing
}

func TestCharacterizeLog(t *testing.T) {
	if _, err := OpenLog("relative.log", 10); err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("relative: %v", err)
	}
	p := filepath.Join(t.TempDir(), "sub", "engine.log")
	l, err := OpenLog(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"12345", "67890", "abc"} {
		if n, err := l.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("write %d %v", n, err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if n, err := l.Write([]byte("after")); n != 5 || err != nil {
		t.Fatalf("write after close %d %v", n, err)
	}
	cur, _ := os.ReadFile(p)
	prev, _ := os.ReadFile(p + ".1")
	if string(cur) != "abc" || string(prev) != "1234567890" {
		t.Fatalf("log %q, previous %q", cur, prev)
	}
	// A reopened log appends and counts what is already there.
	l2, err := OpenLog(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	l2.Write([]byte("de"))
	l2.Write([]byte("f"))
	l2.Close()
	cur, _ = os.ReadFile(p)
	prev, _ = os.ReadFile(p + ".1")
	if string(cur) != "f" || string(prev) != "abcde" {
		t.Fatalf("reopened log %q, previous %q", cur, prev)
	}
	if LogBytes != 8<<20 {
		t.Fatalf("LogBytes %d", LogBytes)
	}
}

func TestCharacterizeOutputWithoutListener(t *testing.T) {
	var buf bytes.Buffer
	var form devicelost.LineForm
	w, line := Output[int](&buf, form, nil)
	if w != &buf || line != nil {
		t.Fatal("output without a listener is not passed through")
	}
	line.Bind(1) // a nil LostLine binds nothing
}

// With a listener every byte still reaches out, and a device-lost line in
// the form reaches the listener with its marker once the process is bound;
// a line printed before Bind waits for it, other lines reach nobody.
func TestCharacterizeOutputWithListener(t *testing.T) {
	var buf syncBuffer
	got := make(chan string, 4)
	w, line := Output[int](&buf, devicelost.SDServer, func(p int, marker string) { got <- strconv.Itoa(p) + ":" + marker })
	if line == nil || w == io.Writer(&buf) {
		t.Fatal("a listener's output is not read on the way")
	}
	text := "loading\nggml_vulkan: VK_ERROR_DEVICE_LOST in submit\nplain VK_ERROR_DEVICE_LOST\n"
	if n, err := io.WriteString(w, text); n != len(text) || err != nil {
		t.Fatalf("write %d %v", n, err)
	}
	if buf.String() != text {
		t.Fatalf("out %q, want %q", buf.String(), text)
	}
	select {
	case s := <-got:
		t.Fatalf("reported before Bind: %s", s)
	case <-time.After(20 * time.Millisecond):
	}
	line.Bind(9)
	select {
	case s := <-got:
		if s != "9:VK_ERROR_DEVICE_LOST" {
			t.Fatalf("report %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing report")
	}
	select {
	case s := <-got:
		t.Fatalf("a line outside the form was reported: %s", s)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestCharacterizeLostLineBinding(t *testing.T) {
	got := make(chan string, 4)
	l := &LostLine[int]{report: func(p int, marker string) { got <- strconv.Itoa(p) + ":" + marker }}
	l.found("early")
	select {
	case s := <-got:
		t.Fatalf("reported before Bind: %s", s)
	case <-time.After(20 * time.Millisecond):
	}
	l.Bind(7)
	l.found("late")
	seen := map[string]bool{}
	for range 2 {
		select {
		case s := <-got:
			seen[s] = true
		case <-time.After(5 * time.Second):
			t.Fatal("missing report")
		}
	}
	if !seen["7:early"] || !seen["7:late"] {
		t.Fatalf("reports %v", seen)
	}
}

func TestCharacterizeConstantsAndFreePort(t *testing.T) {
	if StopWithin != 20*time.Second || DeviceLostConfirm != 15*time.Second || DeviceLostPoll != time.Second ||
		readyPoll != 250*time.Millisecond || exitLineWait != 2*time.Second {
		t.Fatal("a timing constant changed")
	}
	port, err := FreePort()
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("FreePort %d %v", port, err)
	}
}
