// Package quarantine is a provider's gpu list and gpu clear: the requests
// its GPU fault handling paused after a driver reset, listed, and let run
// again by a person (resource CONTRACT.md RES-G3, KIT-Q1).
package quarantine

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-resource/go/gpufault"
)

// Run is program's "gpu" command with args (list or clear and their
// options). state names the provider's state folder from --state; usage is
// the provider's help text.
func Run(program string, args []string, state func(string) (string, error), usage string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "-help" || args[0] == "help" {
		if _, err := io.WriteString(stdout, usage); err != nil {
			return 1
		}
		return 0
	}
	fs := flag.NewFlagSet("gpu "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	//unchecked: usage text on a help/error path; a failed write to stderr has nothing further to report
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	stateDir := fs.String("state", "", "")
	all := fs.Bool("all", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	dir, err := state(*stateDir)
	if err != nil {
		//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
		fmt.Fprintf(stderr, "%s gpu: %v\n", program, err)
		return 2
	}
	path := filepath.Join(dir, gpufault.QuarantineFile)
	switch args[0] {
	case "list":
		for _, aside := range gpufault.DamagedQuarantines(path) {
			//unchecked: CLI output; a failed write leaves the exit code to report the run
			fmt.Fprintf(stdout, "a damaged quarantine file was moved aside: %s\n", aside)
		}
		entries, err := gpufault.ReadQuarantine(path)
		if errors.Is(err, gpufault.ErrDamagedQuarantine) {
			//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
			fmt.Fprintf(stderr, "%s gpu list: %v; '%s gpu clear --all' moves it aside and starts an empty quarantine\n", program, err, program)
			return 1
		}
		if err != nil {
			//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
			fmt.Fprintf(stderr, "%s gpu list: %v\n", program, err)
			return 1
		}
		for _, e := range entries {
			until := e.Until.Local().Format(time.DateTime)
			if e.Held {
				until = "until cleared"
			}
			//unchecked: CLI output; a failed write leaves the exit code to report the run
			fmt.Fprintf(stdout, "%s  %d strike(s)  %s  %s %s %s\n", e.Key, e.Strikes, until,
				e.Fingerprint.Model, e.Fingerprint.Engine, e.Fingerprint.Shape)
		}
		return 0
	case "clear":
		key := ""
		if !*all {
			if fs.NArg() != 1 {
				//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
				fmt.Fprintf(stderr, "%s gpu clear: name one KEY from gpu list, or --all\n", program)
				return 2
			}
			key = fs.Arg(0)
		}
		removed, moved, err := gpufault.ClearQuarantine(path, key)
		if err != nil {
			//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
			fmt.Fprintf(stderr, "%s gpu clear: %v\n", program, err)
			return 1
		}
		if moved != "" {
			//unchecked: CLI output; a failed write leaves the exit code to report the run
			fmt.Fprintf(stdout, "the damaged quarantine file was moved aside to %s\n", moved)
			if *all {
				return 0
			}
		}
		if removed == 0 {
			//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
			fmt.Fprintf(stderr, "%s gpu clear: no paused request matched\n", program)
			return 1
		}
		//unchecked: CLI output; a failed write leaves the exit code to report the run
		fmt.Fprintf(stdout, "%d paused request(s) may run again\n", removed)
		return 0
	}
	//unchecked: CLI output; the process exit code, not a returned error, already reports the outcome
	fmt.Fprintf(stderr, "%s gpu: unknown command %q\n\n%s", program, args[0], usage)
	return 2
}
