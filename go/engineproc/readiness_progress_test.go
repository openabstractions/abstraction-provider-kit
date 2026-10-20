package engineproc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReadinessProgressOutlivesWholeLoadBudget(t *testing.T) {
	clock := time.Unix(100, 0)
	started := clock
	var progress uint64
	process := &Process{exited: make(chan struct{})}
	probe := func(context.Context) (bool, error) {
		clock = clock.Add(60 * time.Second)
		progress++ // a real phase/byte advance, never the timer's own tick
		return clock.Sub(started) >= 240*time.Second, nil
	}
	if err := waitReadinessAt(t.Context(), process, 180*time.Second, probe, func() uint64 { return progress }, func() time.Time { return clock }); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessHeartbeatWithoutProgressStalls(t *testing.T) {
	clock := time.Unix(100, 0)
	process := &Process{exited: make(chan struct{})}
	probe := func(context.Context) (bool, error) {
		clock = clock.Add(60 * time.Second) // healthy transport, unchanged load
		return false, nil
	}
	err := waitReadinessAt(t.Context(), process, 180*time.Second, probe, func() uint64 { return 1 }, func() time.Time { return clock })
	if err == nil || !strings.Contains(err.Error(), "readiness stalled") {
		t.Fatal(err)
	}
}

func TestReadinessSixthResetAttemptThenHeartbeatStalls(t *testing.T) {
	clock := time.Unix(100, 0)
	started := clock
	process := &Process{exited: make(chan struct{})}
	// ModelHost projects phase lines 4,1,2,3,4,unknown to this counter trace.
	// A subsequent heartbeat is still 4. Only the first sample may reset.
	counts := []uint64{4, 4, 4, 4, 4, 4, 4}
	attempts := 0
	var progress uint64
	probe := func(context.Context) (bool, error) {
		attempts++
		if attempts > len(counts) {
			return false, context.DeadlineExceeded // Bound an incorrectly extended fixture.
		}
		if attempts == 1 {
			clock = clock.Add(30 * time.Second)
		} else {
			clock = clock.Add(10 * time.Second)
		}
		progress = counts[attempts-1]
		return false, nil
	}
	err := waitReadinessAt(t.Context(), process, time.Minute, probe, func() uint64 { return progress }, func() time.Time { return clock })
	if err == nil || !strings.Contains(err.Error(), "readiness stalled") || attempts != 7 || progress != 4 || clock.Sub(started) != 90*time.Second {
		t.Fatalf("attempts=%d progress=%d error=%v", attempts, progress, err)
	}
}
