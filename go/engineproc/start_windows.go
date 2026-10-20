//go:build windows

package engineproc

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

// Start starts spec's program in a Windows job with kill-on-close, assigned
// atomically before its code runs (KIT-P1). The engine's stdin and protocol
// stdout, when spec names them, are the child's ends of two pipes; the
// provider keeps the other ends. Once the tree has ended, its exit record
// goes to the engine's log and to spec.Exited before Exited closes
// (KIT-P3). Exited closes only after Wait confirms the parent and every
// contained descendant ended; a failed proof leaves the lease held.
func Start(spec Spec) (*Process, error) {
	if len(spec.Env) == 0 {
		return nil, ErrEnvironment
	}
	var stream *os.File
	var reader *os.File
	copied := make(chan struct{})
	if file, ok := spec.Output.(*os.File); ok {
		stream = file
		close(copied)
	} else if spec.Output != nil {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		reader, stream = r, w
		go func() { _, _ = io.Copy(spec.Output, r); _ = r.Close(); close(copied) }()
	} else {
		close(copied)
	}
	stdout := stream
	if spec.Stdout != nil {
		stdout = spec.Stdout
	}
	// The engine runs in its own proven folder unless the kind names a
	// working folder: llama.cpp's backend loader also loads ggml-*.dll from
	// the working directory.
	dir := spec.Dir
	if dir == "" {
		dir = filepath.Dir(spec.Program)
	}
	child, err := supervision.Start(context.Background(), supervision.Options{
		Executable: spec.Program, Args: spec.Args, Env: spec.Env, Stdin: spec.Stdin, Stdout: stdout, Stderr: stream, Dir: dir,
		ForceTimeout: 20 * time.Second, ProcessMemoryLimit: spec.MemoryLimit, Container: spec.Container,
	})
	if reader != nil {
		_ = stream.Close()
	}
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, err
	}
	p := &Process{id: child.PID(), exited: make(chan struct{}), contains: child.Contains, lend: child.LendTo}
	go func() {
		_, waitErr := child.Wait()
		if exit, ok := child.Exit(); ok {
			// The tree is gone, so its output ends; the record follows the
			// engine's last line in a log this provider writes. A copy
			// still running after the wait leaves the log alone.
			var log io.Writer
			select {
			case <-copied:
				if reader != nil {
					log = spec.Output
				}
			case <-time.After(exitLineWait):
			}
			reportExit(spec, Exit{Engine: spec.Engine, Model: spec.Model, PID: p.id, Exit: exit}, log)
		}
		if waitErr == nil {
			close(p.exited)
		}
		if reader != nil {
			_ = reader.Close()
		}
	}()
	p.stopOwned = func(within time.Duration) {
		_ = child.Stop()
		timer := time.NewTimer(within)
		defer timer.Stop()
		select {
		case <-p.exited:
		case <-timer.C:
		}
	}
	return p, nil
}
