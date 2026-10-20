// Package engineproc starts and supervises one engine process a provider
// owns: a Windows job with kill-on-close and a per-process memory limit, a
// clean environment the caller built, the engine's output read for its
// device-lost line and kept in a bounded log, one exit record per end, and a
// holder.Owned for the resource kit's holder (CONTRACT.md KIT-P1..KIT-P6).
package engineproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-inference/go/providerhost/enginehttp"
)

// StopWithin bounds how long a stop waits for an engine's tree to be
// confirmed gone.
const StopWithin = 20 * time.Second

// DeviceLostConfirm bounds how long an engine that printed a device-lost
// line has to exit or fail its readiness twice before the line is set aside;
// DeviceLostPoll is how often it is checked (KIT-P4).
const (
	DeviceLostConfirm = 15 * time.Second
	DeviceLostPoll    = time.Second
)

// readyPoll is how often an engine's readiness is asked while it loads.
const readyPoll = 250 * time.Millisecond

// A Process is one running engine owned by a provider. On Windows Exited is
// closed only after its contained process tree is confirmed gone. Other
// platforms retain the direct-child lifecycle.
type Process struct {
	id        int
	exited    chan struct{}
	stopOwned func(time.Duration)
	// contains answers whether a process is in this engine's contained
	// tree; the Windows job sets it. Without it no socket counts as the
	// engine's, and a socket check refuses.
	contains identity.TreeContains
	// lend duplicates a handle into the running engine (KIT-S6); nil where
	// the platform supervises no job.
	lend func(h uintptr, access uint32, pid int) (uintptr, error)
}

// fileReadAccess is what a lent file's handle may do in the engine:
// FILE_GENERIC_READ, read its bytes and attributes and wait on it.
const fileReadAccess = 0x120089

// Lend gives the running engine a read-only handle to f and returns the
// handle's value in the engine, which reads a file its sandbox may not open
// by path through it and cannot write it (KIT-S6, facade FAC-P4). The
// engine closes its copy once read; its exit closes it at the latest. The
// caller keeps f.
func (p *Process) Lend(f *os.File) (uintptr, error) {
	return p.LendTo(f, p.id)
}

// LendTo lends to a process in this engine's owned tree, such as the base
// Python interpreter spawned by a venv launcher. The supervisor proves job
// membership from a live process handle before duplicating the file handle.
func (p *Process) LendTo(f *os.File, pid int) (uintptr, error) {
	if p == nil || p.lend == nil {
		return 0, fmt.Errorf("engineproc: this platform lends no handle to an engine")
	}
	if p.Gone() {
		return 0, fmt.Errorf("engineproc: the engine has ended")
	}
	if f == nil {
		return 0, fmt.Errorf("engineproc: lend needs a file")
	}
	return p.lend(f.Fd(), fileReadAccess, pid)
}

// PID is the process id the provider started.
func (p *Process) PID() int {
	if p == nil {
		return 0
	}
	return p.id
}

// Exited is closed once the engine's tree is confirmed gone.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// Gone reports whether Exited is closed.
func (p *Process) Gone() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// Contains answers whether a process is in this engine's contained tree;
// nil outside a supervised job.
func (p *Process) Contains() identity.TreeContains { return p.contains }

// Verify proves the server end of a connection to this engine is the
// engine: a member of its contained tree by process id and creation time
// where the platform supervises one, else the process the provider started,
// which stays its unreaped child while it runs.
func (p *Process) Verify(server identity.Process) (bool, error) {
	if server.PID <= 0 || server.Recycled {
		return false, nil
	}
	if p.contains != nil {
		return p.contains(server.PID, server.StartTime)
	}
	return server.PID == p.id, nil
}

// Stop ends only the engine this provider started. The platform
// implementation bounds its confirmation; a holder keeps the lease while
// Exited stays open.
func (p *Process) Stop(within time.Duration) {
	if p == nil || p.stopOwned == nil {
		return
	}
	p.stopOwned(within)
}

// Spec is one engine start: the program, its arguments and clean
// environment, where its output goes and its job's per-process memory
// limit. Dir is the working folder, empty for the program's own folder.
// Stdin and Stdout, when set, are the child's ends of an engine's protocol
// pipes; the caller closes its copies once the start returns. Output then
// receives the engine's stderr alone.
type Spec struct {
	Program       string
	Args, Env     []string
	Dir           string
	Stdin, Stdout *os.File
	Output        io.Writer
	MemoryLimit   uint64
	// Engine and Model name the start in its exit record; Exited, when
	// set, receives that record once the engine's tree has ended
	// (KIT-P3). Provider names the provider in the record and its log line.
	Engine, Model, Provider string
	Exited                  func(Exit)
	// Container, when set, is the SID of the AppContainer the engine starts
	// in, from a sandbox.Launch its caller prepared (KIT-S3). The start
	// fails rather than run the engine uncontained, and a platform without
	// AppContainers refuses it.
	Container string
}

// ErrEnvironment is a start whose environment is empty: an engine never
// inherits the provider's (KIT-P1).
var ErrEnvironment = fmt.Errorf("engineproc: an engine starts only from a clean environment")

// Wait asks ready until it reports the engine ready, the engine exits, ctx
// ends or budget passes. An error from ready ends the wait at once, except
// enginehttp.ErrEngineUnreachable, a connect a starting engine never fails with,
// which ends it once every answer for enginehttp.UnreachableFor was that
// error (enginehttp.UnreachableRun, KIT-P5).
func Wait(ctx context.Context, p *Process, budget time.Duration, ready func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(budget)
	var run enginehttp.UnreachableRun
	for {
		select {
		case <-p.exited:
			return fmt.Errorf("the engine exited before it was ready")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		healthy, err := ready(ctx)
		if end := run.Note(err); end != nil {
			return end
		}
		if err != nil && !errors.Is(err, enginehttp.ErrEngineUnreachable) {
			return err
		}
		if healthy {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the engine was not ready within %s", budget)
		}
		timer := time.NewTimer(readyPoll)
		select {
		case <-timer.C:
		case <-p.exited:
			timer.Stop()
			return fmt.Errorf("the engine exited before it was ready")
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// FreePort is a loopback port nothing holds right now, for an engine that
// serves TCP only.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
