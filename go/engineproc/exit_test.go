package engineproc

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
)

// KIT-P3: a crash is one warning record carrying its exception
// status, peaks and limit; a provider's stop is information.
func TestExitIsOneLogRecord(t *testing.T) {
	crash := Exit{Engine: "oa-torch-engine", Model: "hummingbird-xt-i2v", PID: 11932, Exit: supervision.Exit{
		Code: 0xC0000005, Runtime: 75 * time.Second, PeakProcessMemory: 30 << 30, PeakJobMemory: 31 << 30,
		ProcessMemoryLimit: 43 << 30, LimitEvidence: "job message", AbnormalExits: []int{11932}}}
	var buf bytes.Buffer
	crash.Log(slog.New(slog.NewJSONHandler(&buf, nil)))
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("not one record: %q %v", buf.String(), err)
	}
	want := map[string]any{"level": "WARN", "msg": "engine exit", "engine": "oa-torch-engine", "model": "hummingbird-xt-i2v",
		"status": "0xC0000005 STATUS_ACCESS_VIOLATION", "crashed": true, "stopped": false, "memory_limit_hit": false,
		"limit_evidence": "job message", "abnormal_exits": "11932", "runtime_ms": float64(75000),
		"peak_process_memory": float64(30 << 30), "process_memory_limit": float64(43 << 30)}
	for k, v := range want {
		if record[k] != v {
			t.Fatalf("%s = %v, want %v (record %v)", k, record[k], v, record)
		}
	}
	line := crash.Line()
	for _, part := range []string{"oa-torch-engine for hummingbird-xt-i2v (pid 11932) crashed 0xC0000005 STATUS_ACCESS_VIOLATION after 75.0 s",
		"30.00 GiB per process", "limit 43.00 GiB not hit (job message)", "abnormal exit of pid 11932"} {
		if !strings.Contains(line, part) {
			t.Fatalf("line %q lacks %q", line, part)
		}
	}

	buf.Reset()
	stop := Exit{Engine: "llama-server", Model: "tiny:1b", PID: 5, Exit: supervision.Exit{Code: 1, Stopped: true}, Provider: "modelhost"}
	stop.Log(slog.New(slog.NewJSONHandler(&buf, nil)))
	if !strings.Contains(buf.String(), `"level":"INFO"`) || strings.Contains(buf.String(), `"status"`) ||
		!strings.Contains(stop.Line(), "stopped by modelhost") {
		t.Fatalf("a stop read as %q / %q", buf.String(), stop.Line())
	}

	limit := Exit{Engine: "sd-server", Exit: supervision.Exit{Code: 3, ProcessMemoryLimit: 1 << 30, MemoryLimitHit: true, LimitEvidence: "job message"}}
	if !limit.Notable() || !strings.Contains(limit.Line(), "limit 1.00 GiB HIT (job message)") || !strings.Contains(limit.Line(), "exited 3") {
		t.Fatalf("a refused allocation read as %q", limit.Line())
	}
	buf.Reset()
	unknown := Exit{Engine: "sd-server", Model: "wan", PID: 9, Exit: supervision.Exit{Unknown: "wait: The handle is invalid."}}
	unknown.Log(slog.New(slog.NewJSONHandler(&buf, nil)))
	if !unknown.Notable() || unknown.Crash() || !strings.Contains(unknown.Line(), "exit unknown (wait: The handle is invalid.)") ||
		!strings.Contains(buf.String(), `"level":"WARN"`) || !strings.Contains(buf.String(), `"unknown":"wait: The handle is invalid."`) {
		t.Fatalf("an unreadable exit read as %q / %q", unknown.Line(), buf.String())
	}
	signal := Exit{Engine: "llama-server", Signal: "segmentation fault"}
	if !signal.Crash() || !strings.Contains(signal.Line(), "killed by segmentation fault") {
		t.Fatalf("a fault signal read as %q", signal.Line())
	}
}
