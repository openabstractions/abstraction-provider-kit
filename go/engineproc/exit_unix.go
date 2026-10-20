//go:build unix

package engineproc

import (
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

// processExit reads an ended engine's exit outside Windows: its code, the
// signal that ended it, its life and its peak resident memory, which is
// what this platform keeps (KIT-P3). No job limit exists here.
func processExit(state *os.ProcessState, runtimeOf time.Duration) (supervision.Exit, string) {
	exit := supervision.Exit{Runtime: runtimeOf}
	if state == nil {
		exit.Unknown = "the engine's wait returned no process state"
		return exit, ""
	}
	signal := ""
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		signal = status.Signal().String()
	} else if code := state.ExitCode(); code >= 0 {
		exit.Code = uint32(code)
	}
	if usage, ok := state.SysUsage().(*syscall.Rusage); ok {
		peak := uint64(usage.Maxrss)
		if runtime.GOOS != "darwin" {
			peak *= 1024 // kilobytes outside macOS
		}
		exit.PeakProcessMemory = peak
	}
	return exit, signal
}
