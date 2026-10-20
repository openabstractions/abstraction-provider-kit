package videoops

import (
	"context"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-resource/go/gpufault"
)

type standbyEvents struct{ events chan gpufault.Event }

func (s standbyEvents) Watch(ctx context.Context, deliver func(gpufault.Event)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-s.events:
			deliver(ev)
		}
	}
}

// standbyGuard is a guard whose machine has entered Modern Standby.
func standbyGuard(t *testing.T) (*gpufault.Guard, standbyEvents) {
	t.Helper()
	src := standbyEvents{events: make(chan gpufault.Event)}
	guard, err := gpufault.New(gpufault.Options{
		Records: src, Settle: 100 * time.Millisecond, Window: 2 * time.Second,
		Probe: func(context.Context) error { return nil },
		Wake:  func(string) (func(), error) { return func() {}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go guard.Run(ctx)
	src.events <- gpufault.Event{Kind: gpufault.StandbyEnter, Source: "Kernel-Power 506"}
	deadline := time.Now().Add(5 * time.Second)
	for guard.AdmitLoad(context.Background()) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the guard never entered standby")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return guard, src
}

// A queued video held by standby ends gpu:standby at the wait bound.
func TestVideoGPUWaitEndsAtBound(t *testing.T) {
	guard, _ := standbyGuard(t)
	old := GPUWaitBound
	GPUWaitBound = 300 * time.Millisecond
	t.Cleanup(func() { GPUWaitBound = old })
	table := idleTable(t, defaultBounds(), nil)
	start := time.Now()
	if word := table.admitGPU(context.Background(), guard, &Op{}); word != gpufault.WordStandby {
		t.Fatalf("word %q", word)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("waited %s", d)
	}
}

// A queued video whose standby clears before the bound proceeds.
func TestVideoGPUWaitProceedsAfterWake(t *testing.T) {
	guard, src := standbyGuard(t)
	table := idleTable(t, defaultBounds(), nil)
	go func() {
		time.Sleep(300 * time.Millisecond)
		src.events <- gpufault.Event{Kind: gpufault.StandbyExit, Source: "Kernel-Power 507"}
	}()
	if word := table.admitGPU(context.Background(), guard, &Op{}); word != "" {
		t.Fatalf("word %q", word)
	}
}
