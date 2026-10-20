package engineproc

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/openabstractions/abstraction-inference/go/providerhost/devicelost"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
)

// LostLine binds the device-lost lines an engine prints to the process that
// printed them, which exists only once the engine has started. Lines printed
// before then wait for Bind. P is the provider's own handle on the process.
type LostLine[P any] struct {
	mu      sync.Mutex
	bound   bool
	p       P
	pending []string
	report  func(p P, marker string)
}

func (l *LostLine[P]) found(marker string) {
	l.mu.Lock()
	bound, p := l.bound, l.p
	if !bound {
		l.pending = append(l.pending, marker)
	}
	l.mu.Unlock()
	if bound {
		go l.report(p, marker)
	}
}

// Bind names the process the lines are the output of, and hands it the lines
// printed before. A nil LostLine binds nothing.
func (l *LostLine[P]) Bind(p P) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.p, l.bound = p, true
	pending := l.pending
	l.pending = nil
	l.mu.Unlock()
	for _, marker := range pending {
		go l.report(p, marker)
	}
}

// Output is where an engine's output goes: out, read on the way for the
// engine's own device-lost line in form when the provider listens for one
// (devicelost, KIT-P4). The returned LostLine is bound to the process once
// it runs; it is nil when nothing listens.
func Output[P any](out io.Writer, form devicelost.LineForm, deviceLost func(P, string)) (io.Writer, *LostLine[P]) {
	if deviceLost == nil {
		return out, nil
	}
	line := &LostLine[P]{report: deviceLost}
	return devicelost.New(out, form, line.found), line
}

// DeviceLost is the GPU fault handling's hearing of an engine's device-lost
// line (KIT-P4, resource RES-G1): the line counts only once the engine exits
// or fails its own readiness check twice within DeviceLostConfirm. An engine
// that keeps answering printed caller text, and nothing is stopped or
// quarantined. It runs on its own goroutine, never the engine's output one.
func DeviceLost(gpu *gpufault.Guard, provider, engine string, onError func(error)) func(exited <-chan struct{}, healthy func(context.Context) bool, marker string) {
	return func(exited <-chan struct{}, healthy func(context.Context) bool, marker string) {
		if !devicelost.Confirmed(context.Background(), exited, healthy, DeviceLostConfirm, DeviceLostPoll) {
			if onError != nil {
				onError(fmt.Errorf("%s: %s printed %s and still answers; not counted as a GPU reset", provider, engine, marker))
			}
			return
		}
		gpu.Report(gpufault.Event{Kind: gpufault.DeviceLost, Source: engine + ": " + marker})
	}
}
