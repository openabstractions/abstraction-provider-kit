package engineproc

import (
	"context"
	"errors"
	"time"

	"github.com/openabstractions/abstraction-inference/go/providerhost/enginehttp"
	"github.com/openabstractions/abstraction-provider-kit/go/refusal"
)

// ErrPeer is a readiness that met a server on the engine's socket other than
// the engine; Owned refuses it with refusal.EnginePeer.
var ErrPeer = errors.New("a process other than the engine serves its socket")

// Owned is one started engine as the resource kit's holder owns it
// (holder.Owned, KIT-P5): its readiness, its socket rule at readiness and
// while it runs, whether it lives, and a stop of this process alone.
type Owned struct {
	Name    string
	Process *Process
	// ReadyWithin bounds the readiness Probe answers.
	ReadyWithin time.Duration
	Probe       func(context.Context) (bool, error)
	// Sockets is the engine's socket rule (inference INF-EL2), checked at
	// readiness and every Watch while it runs; nil checks none. SocketWord is
	// its refusal word.
	Sockets    func() error
	SocketWord string
	Watch      time.Duration
	// Resident reports whether this launch is still the holder's resident
	// for Name, so a late socket report names the engine that broke the
	// rule; nil reads true.
	Resident func() bool
	Report   func(error)
	// OnStop runs once a stop is confirmed.
	OnStop func()
	// AfterFailedCheck, set by tests only, runs between a failed running
	// socket check and the stop it leads to.
	AfterFailedCheck func()
}

func (o *Owned) PID() int { return o.Process.PID() }

// Ready waits for the engine's readiness, then confirms its socket rule. A
// refusal here is a failed readiness: the holder stops the engine and
// releases the lease before the caller is told.
func (o *Owned) Ready(ctx context.Context) error {
	if err := Wait(ctx, o.Process, o.ReadyWithin, o.Probe); err != nil {
		if errors.Is(err, ErrPeer) {
			return refusal.New(refusal.EnginePeer, "%v", err)
		}
		if errors.Is(err, enginehttp.ErrEngineUnreachable) {
			return refusal.New(refusal.EngineUnreachable, "%v", err)
		}
		return err
	}
	if o.Sockets != nil {
		if err := o.Sockets(); err != nil {
			return refusal.New(o.SocketWord, "%v", err)
		}
		go o.watch()
	}
	return nil
}

// watch checks the engine's sockets again every Watch while it runs. A
// socket the rule refuses stops this engine by its own process, never
// another engine loaded for the same name since; the holder sees it exit and
// gives its lease back. The word is reported while this engine is still the
// resident.
func (o *Owned) watch() {
	if o.Watch <= 0 {
		return
	}
	ticker := time.NewTicker(o.Watch)
	defer ticker.Stop()
	for {
		select {
		case <-o.Process.exited:
			return
		case <-ticker.C:
		}
		if err := o.Sockets(); err != nil {
			select {
			case <-o.Process.exited:
				return
			default:
			}
			if o.AfterFailedCheck != nil {
				o.AfterFailedCheck()
			}
			if (o.Resident == nil || o.Resident()) && o.Report != nil {
				o.Report(refusal.New(o.SocketWord, "%s: %v", o.Name, err))
			}
			o.Process.Stop(StopWithin)
			return
		}
	}
}

func (o *Owned) Alive() bool { return !o.Process.Gone() }

// Stop stops this process and reports true once its tree is gone.
func (o *Owned) Stop() bool {
	o.Process.Stop(StopWithin)
	if o.Alive() {
		return false
	}
	if o.OnStop != nil {
		o.OnStop()
	}
	return true
}
