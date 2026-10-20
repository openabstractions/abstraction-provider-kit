//go:build !windows

package engineproc

import (
	"strings"
	"testing"
)

// Outside Windows a start that asks for a memory limit or an AppContainer
// is refused before anything runs (CQ-15a).
func TestCharacterizeStartRefusesWindowsOnlyContainment(t *testing.T) {
	if _, err := Start(Spec{Program: program(t), Env: childEnv("exit:0"), MemoryLimit: 1}); err == nil ||
		!strings.Contains(err.Error(), "memory limit") {
		t.Fatalf("memory limit: %v", err)
	}
	if _, err := Start(Spec{Program: program(t), Env: childEnv("exit:0"), Container: "S-1-15-2-1"}); err == nil ||
		!strings.Contains(err.Error(), "AppContainer") {
		t.Fatalf("container: %v", err)
	}
}

// A stop's kill reads as the provider's stop with the "killed" signal.
func TestCharacterizeStopSignal(t *testing.T) {
	p, exits := startFake(t, Spec{Env: childEnv("sleep")})
	p.Stop(StopWithin)
	x := waitExit(t, exits)
	if !x.Stopped || x.Signal != "killed" {
		t.Fatalf("stop record %+v", x)
	}
}
