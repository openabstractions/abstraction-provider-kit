package engineproc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

// Exit is one engine's end as its provider observed it, for every engine
// kind (KIT-P3): which engine served which model, its process id, and what
// its supervisor saw: the exit code (a crash's exception status on
// Windows), the run time, the memory peaks and whether the job's memory
// limit refused an allocation.
type Exit struct {
	// Engine is the engine kind's program name: llama-server, sd-server or
	// oa-torch-engine.
	Engine string
	// Model is the model the engine was started for.
	Model string
	PID   int
	supervision.Exit
	// Signal names the signal that ended the engine outside Windows; empty
	// for an exit by code.
	Signal string
	// Provider names the provider that started the engine, the one a stop
	// is credited to; empty reads "the provider".
	Provider string
}

// How is the end in words: stopped by the provider, crashed with its
// status, killed by a signal, or exited with its code.
func (x Exit) How() string {
	switch {
	case x.Unknown != "":
		return "ended, exit unknown (" + x.Unknown + ")"
	case x.Stopped:
		by := x.Provider
		if by == "" {
			by = "the provider"
		}
		return "stopped by " + by
	case x.Signal != "":
		return "killed by " + x.Signal
	case x.Crashed():
		return "crashed " + x.Status()
	}
	return "exited " + x.Status()
}

// Crash is true for an exception status on Windows and a fault signal
// elsewhere.
func (x Exit) Crash() bool {
	switch x.Signal {
	case "":
		return x.Crashed()
	case "segmentation fault", "bus error", "illegal instruction", "aborted", "floating point exception":
		return !x.Stopped
	}
	return false
}

// Notable is true for an end that was not the provider's own stop or a
// clean exit: a crash, a signal, a refused allocation or a nonzero code.
func (x Exit) Notable() bool {
	return x.Unknown != "" || !x.Stopped && (x.Code != 0 || x.Signal != "" || x.MemoryLimitHit)
}

// Line is the exit as one line for stderr and the engine's log.
func (x Exit) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s for %s (pid %d) %s after %.1f s", x.Engine, x.Model, x.PID, x.How(), x.Runtime.Seconds())
	if x.PeakProcessMemory > 0 || x.PeakJobMemory > 0 {
		fmt.Fprintf(&b, "; peak memory %s per process, %s for the job", gib(x.PeakProcessMemory), gib(x.PeakJobMemory))
	}
	if x.ProcessMemoryLimit > 0 {
		hit := "not hit"
		if x.MemoryLimitHit {
			hit = "HIT"
		}
		fmt.Fprintf(&b, "; limit %s %s", gib(x.ProcessMemoryLimit), hit)
		if x.LimitEvidence != "" {
			fmt.Fprintf(&b, " (%s)", x.LimitEvidence)
		}
	} else if x.MemoryLimitHit {
		b.WriteString("; a job memory limit HIT")
	}
	if len(x.AbnormalExits) > 0 {
		ids := make([]string, len(x.AbnormalExits))
		for i, id := range x.AbnormalExits {
			ids[i] = strconv.Itoa(id)
		}
		fmt.Fprintf(&b, "; abnormal exit of pid %s", strings.Join(ids, ", "))
	}
	return b.String()
}

func gib(n uint64) string { return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30)) }

// Log writes the exit as one OA logging record: a warning when Notable,
// information otherwise.
func (x Exit) Log(logger *slog.Logger) {
	if logger == nil {
		return
	}
	level := slog.LevelInfo
	if x.Notable() {
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.String("engine", x.Engine), slog.String("model", x.Model), slog.Int("pid", x.PID),
		slog.String("end", x.How()), slog.Uint64("code", uint64(x.Code)), slog.Bool("crashed", x.Crash()),
		slog.Bool("stopped", x.Stopped), slog.Int64("runtime_ms", x.Runtime.Milliseconds()),
		slog.Uint64("peak_process_memory", x.PeakProcessMemory), slog.Uint64("peak_job_memory", x.PeakJobMemory),
		slog.Uint64("process_memory_limit", x.ProcessMemoryLimit), slog.Bool("memory_limit_hit", x.MemoryLimitHit),
	}
	if x.Signal == "" && x.Crashed() {
		attrs = append(attrs, slog.String("status", x.Status()))
	}
	if x.LimitEvidence != "" {
		attrs = append(attrs, slog.String("limit_evidence", x.LimitEvidence))
	}
	if x.Unknown != "" {
		attrs = append(attrs, slog.String("unknown", x.Unknown))
	}
	if x.Signal != "" {
		attrs = append(attrs, slog.String("signal", x.Signal))
	}
	if len(x.AbnormalExits) > 0 {
		ids := make([]string, len(x.AbnormalExits))
		for i, id := range x.AbnormalExits {
			ids[i] = strconv.Itoa(id)
		}
		attrs = append(attrs, slog.String("abnormal_exits", strings.Join(ids, ",")))
	}
	logger.LogAttrs(context.Background(), level, "engine exit", attrs...)
}

// exitLineWait bounds the wait for an ended engine's output to drain before
// its exit line is written to the engine's log.
const exitLineWait = 2 * time.Second

// reportExit hands an observed exit to the engine's log, when the provider
// owns that stream, and to the provider's exit hook. The line is written
// after the engine's own output has ended, so it is the log's last line.
func reportExit(spec Spec, x Exit, log io.Writer) {
	x.Provider = spec.Provider
	if log != nil {
		prefix := spec.Provider
		if prefix == "" {
			prefix = "provider"
		}
		//unchecked: a diagnostic line; the exit hook below still reports it
		io.WriteString(log, prefix+": "+x.Line()+"\n")
	}
	if spec.Exited != nil {
		spec.Exited(x)
	}
}

// Report writes each exit, of every kind, as one OA logging record on logger
// and one line on stderr under program's name (KIT-P3).
func Report(logger *slog.Logger, stderr io.Writer, program string) func(Exit) {
	return func(x Exit) {
		x.Log(logger)
		if stderr != nil {
			//unchecked: a diagnostic line; the record already went to the log
			fmt.Fprintf(stderr, "%s: %s\n", program, x.Line())
		}
	}
}
