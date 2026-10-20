//go:build !windows

package engineproc

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Start starts spec's program as a direct child outside Windows, which has
// no contained descendant supervision here (KIT-P1).
func Start(spec Spec) (*Process, error) {
	if len(spec.Env) == 0 {
		return nil, ErrEnvironment
	}
	if spec.MemoryLimit > 0 {
		return nil, errors.New("a process memory limit needs the Windows job; this platform sets none")
	}
	if spec.Container != "" {
		return nil, errors.New("an AppContainer exists only on Windows; the engine does not start uncontained instead")
	}
	cmd := exec.Command(spec.Program, spec.Args...)
	// A non-nil Env replaces the inherited environment entirely.
	cmd.Env = spec.Env
	// The engine runs in its own proven folder unless the kind names a
	// working folder: llama.cpp's backend loader also loads backend libraries
	// from the working directory.
	cmd.Dir = spec.Dir
	if cmd.Dir == "" {
		cmd.Dir = filepath.Dir(spec.Program)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if spec.Output != nil {
		cmd.Stdout, cmd.Stderr = spec.Output, spec.Output
	}
	if spec.Stdout != nil {
		cmd.Stdout = spec.Stdout
	}
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	started := time.Now()
	p := &Process{id: cmd.Process.Pid, exited: make(chan struct{})}
	var stopped atomic.Bool
	go func() {
		//unchecked: the exit's state is read below; Wait's error repeats it
		cmd.Wait()
		// exec.Cmd has copied the engine's output by the time Wait
		// returns, so the record follows its last line (KIT-P3).
		exit, signal := processExit(cmd.ProcessState, time.Since(started))
		// Only the stop's own kill is a stop; an engine that ended first
		// keeps its code or signal.
		exit.Stopped = stopped.Load() && signal == "killed"
		// A file output is a stream the caller owns, such as the
		// provider's stderr, which the exit hook already writes to.
		log := spec.Output
		if _, file := log.(*os.File); file {
			log = nil
		}
		reportExit(spec, Exit{Engine: spec.Engine, Model: spec.Model, PID: p.id, Exit: exit, Signal: signal}, log)
		close(p.exited)
	}()
	p.stopOwned = func(within time.Duration) {
		stopped.Store(true)
		_ = cmd.Process.Kill()
		timer := time.NewTimer(within)
		defer timer.Stop()
		select {
		case <-p.exited:
		case <-timer.C:
		}
	}
	return p, nil
}
