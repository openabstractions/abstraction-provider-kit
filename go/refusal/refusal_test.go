package refusal

import (
	"errors"
	"fmt"
	"testing"
	"time"

	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
	"github.com/openabstractions/abstraction-resource/go/holder"
)

// KIT-R1, KIT-R2: a load's refusal reads the lease's own word, a refusal the
// load made itself as it is, and Unavailable for the rest; a GPU refusal
// keeps the Guard's word.
func TestALoadRefusalReadsOneWord(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&holder.Failure{Stage: holder.StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeNotPermitted}}, holder.WordNotPermitted},
		{&holder.Failure{Stage: holder.StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeInsufficient}}, holder.WordInsufficient},
		{&holder.Failure{Stage: holder.StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeHoldersRefused}}, holder.WordHoldersRefused},
		{fmt.Errorf("ensure: %w", &holder.Failure{Stage: holder.StageAcquire, Err: holder.ErrLeaseEnded}), holder.WordEnded},
		{&holder.Failure{Stage: holder.StageAcquire, Err: errors.New("no lease service")}, Unavailable},
		{&holder.Failure{Stage: holder.StageStart, Err: errors.New("exec")}, Unavailable},
		{New(NoEngine, "not pinned"), NoEngine},
		{errors.New("other"), Unavailable},
	} {
		if got := WordOf(Load("card:0", "m", 1, tc.err)); got != tc.want {
			t.Fatalf("%v reads %q, want %q", tc.err, got, tc.want)
		}
	}
	if LoadWord(New(Unavailable, "x")) != EngineUnavailable || LoadWord(New(NoEngine, "x")) != NoEngine {
		t.Fatal("LoadWord")
	}
	gpu := GPU(&gpufault.Refusal{Word: gpufault.WordResetting, Detail: "settling", Until: time.Unix(0, 0)})
	if WordOf(gpu) != gpufault.WordResetting {
		t.Fatalf("GPU %v", gpu)
	}
}
