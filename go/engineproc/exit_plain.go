//go:build !windows && !unix

package engineproc

import (
	"os"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

// processExit reads an ended engine's exit code and life where the
// platform keeps no signal or memory count (KIT-P3).
func processExit(state *os.ProcessState, runtimeOf time.Duration) (supervision.Exit, string) {
	exit := supervision.Exit{Runtime: runtimeOf}
	if state == nil {
		exit.Unknown = "the engine's wait returned no process state"
	} else if state.ExitCode() >= 0 {
		exit.Code = uint32(state.ExitCode())
	}
	return exit, ""
}
