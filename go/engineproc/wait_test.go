package engineproc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-inference/go/providerhost/enginehttp"
	"github.com/openabstractions/abstraction-provider-kit/go/refusal"
)

// unreachable is a probe's connect error a starting engine never gives, as
// enginehttp wraps it.
var unreachable = fmt.Errorf("%w: dial unix e.sock: connect: %w", enginehttp.ErrEngineUnreachable, syscall.EINVAL)

func running() *Process { return &Process{exited: make(chan struct{})} }

// A probe that can never connect ends readiness within
// enginehttp.UnreachableFor and a
// poll, named engine:unreachable with the system error, long before the
// budget (the 2026-10-03 live run waited its whole ten minutes).
func TestWaitEndsOnAnEndpointThatCannotBeReached(t *testing.T) {
	owned := &Owned{Name: "m", Process: running(), ReadyWithin: time.Minute,
		Probe: func(context.Context) (bool, error) { return false, unreachable }}
	began := time.Now()
	err := owned.Ready(context.Background())
	took := time.Since(began)
	if took < enginehttp.UnreachableFor || took > enginehttp.UnreachableFor+2*time.Second {
		t.Fatalf("readiness ended after %s, want about %s", took, enginehttp.UnreachableFor)
	}
	if refusal.WordOf(err) != refusal.EngineUnreachable || !strings.Contains(err.Error(), syscall.EINVAL.Error()) {
		t.Fatalf("got %v, want %s naming %q", err, refusal.EngineUnreachable, syscall.EINVAL.Error())
	}
}

// An unreachable answer between answers of an engine still loading is a
// passing state: the streak starts again and the wait keeps its budget.
func TestWaitKeepsWaitingThroughABrokenStreak(t *testing.T) {
	calls := 0
	budget := enginehttp.UnreachableFor + time.Second
	err := Wait(context.Background(), running(), budget, func(context.Context) (bool, error) {
		calls++
		if calls%4 == 0 {
			return false, nil
		}
		return false, unreachable
	})
	if err == nil || !strings.Contains(err.Error(), "not ready within") || errors.Is(err, enginehttp.ErrEngineUnreachable) {
		t.Fatalf("got %v, want the budget's end", err)
	}
}

// A short readiness budget cannot promote one unreachable connect into the
// typed three-second refusal. The ordinary timeout is the only result.
func TestWaitShortBudgetKeepsUnreachableUntyped(t *testing.T) {
	owned := &Owned{Name: "m", Process: running(), ReadyWithin: readyPoll / 2,
		Probe: func(context.Context) (bool, error) { return false, unreachable }}
	err := owned.Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not ready within") ||
		errors.Is(err, enginehttp.ErrEngineUnreachable) || refusal.WordOf(err) == refusal.EngineUnreachable {
		t.Fatalf("short readiness budget: %v; want an ordinary timeout", err)
	}
}

// A refused connection is the engine not listening yet, and a ready engine
// is reported at the first answer that says so.
func TestWaitReturnsAtTheFirstReadyAnswer(t *testing.T) {
	calls := 0
	began := time.Now()
	err := Wait(context.Background(), running(), time.Minute, func(context.Context) (bool, error) {
		calls++
		return calls == 3, nil
	})
	if err != nil || calls != 3 || time.Since(began) > 2*readyPoll+time.Second {
		t.Fatalf("err %v after %d calls in %s", err, calls, time.Since(began))
	}
}

// Any other probe error still ends the wait at once.
func TestWaitEndsAtOnceOnAnotherError(t *testing.T) {
	err := Wait(context.Background(), running(), time.Minute, func(context.Context) (bool, error) {
		return false, ErrPeer
	})
	if !errors.Is(err, ErrPeer) {
		t.Fatalf("got %v", err)
	}
}
