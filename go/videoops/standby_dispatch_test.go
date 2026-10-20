package videoops

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
)

type runCounter struct {
	boundKind
	runs *atomic.Int32
}

func (k runCounter) Run(context.Context, *Op) { k.runs.Add(1) }

// RES-G5: standby that begins after admission, while the job holds its
// wake lease, ends the video before the first dispatch.
func TestStandbyAfterAdmitSendsNoRun(t *testing.T) {
	old := GPUWaitBound
	GPUWaitBound = 300 * time.Millisecond
	t.Cleanup(func() { GPUWaitBound = old })
	var guard *gpufault.Guard
	var released, reports atomic.Int32
	guard, err := gpufault.New(gpufault.Options{
		Wake: func(string) (func(), error) {
			// Standby begins once the job holds its wake lease, before the
			// engine load and the first dispatch.
			guard.Report(gpufault.Event{Kind: gpufault.StandbyEnter, Source: "Kernel-Power 506"})
			return func() { released.Add(1) }, nil
		},
		OnError: func(error) { reports.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	table := idleTable(t, defaultBounds(), nil)
	runs := &atomic.Int32{}
	table.kind = runCounter{boundKind: boundKind{bounds: defaultBounds()}, runs: runs}
	table.opts.GPU = guard
	op := &Op{id: "v1", state: wire.VideoStateQueued, cancel: make(chan struct{})}
	table.run(op)
	if runs.Load() != 0 {
		t.Fatal("the kind ran in standby")
	}
	if op.state != wire.VideoStateFailed || op.reason != gpufault.WordStandby {
		t.Fatalf("op %v %q", op.state, op.reason)
	}
	if released.Load() != 1 {
		t.Fatalf("wake lease released %d times", released.Load())
	}
	if reports.Load() != 1 {
		t.Fatalf("standby under the lease reported %d times", reports.Load())
	}
}
