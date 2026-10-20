package videoops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-inference/go/providerhost/sdcpp"
	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
	"github.com/openabstractions/abstraction-resource/go/holder"
	storage "github.com/openabstractions/abstraction-storage/go"
)

type noLeases struct{}

func (noLeases) Acquire(string, int64, time.Duration) (res.AcquireResult, error) {
	return res.AcquireResult{}, nil
}
func (noLeases) BindProcess(string, int) (res.ProcessBindResult, error) {
	return res.ProcessBindResult{}, nil
}
func (noLeases) Renew(string) (res.LeaseChange, error)   { return res.LeaseChange{}, nil }
func (noLeases) Release(string) (res.LeaseChange, error) { return res.LeaseChange{}, nil }
func (noLeases) Observe(ctx context.Context, _ string, _ time.Duration) (res.YieldPage, error) {
	<-ctx.Done()
	return res.YieldPage{}, ctx.Err()
}
func (noLeases) Answer(context.Context, string, res.YieldAnswer, string) (res.AnswerResult, error) {
	return res.AnswerResult{}, nil
}

// finishingKind keeps each video it runs and completes it.
type finishingKind struct {
	t   *Table
	ran chan sdcpp.VideoParams
}

func (k finishingKind) Bounds() Bounds {
	return Bounds{Width: 512, Height: 512, FPS: 16, Sizes: [][2]int{{512, 512}}, MaxFrames: 81, MaxFPS: 16, FirstFrame: FirstFrameNone}
}
func (k finishingKind) Admit(sdcpp.VideoParams) (wire.StartOutcome, string) {
	return wire.StartOutcomeAccepted, ""
}
func (k finishingKind) Fingerprint(sdcpp.VideoParams) gpufault.Fingerprint {
	return gpufault.Fingerprint{Model: "m"}
}
func (k finishingKind) Run(_ context.Context, op *Op) {
	params := k.t.Params(op)
	k.ran <- params
	data := []byte("video")
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := k.t.Keep(digest, data); err != nil {
		k.t.Finish(op, wire.VideoStateFailed, "content:unavailable", nil)
		return
	}
	k.t.Finish(op, wire.VideoStateCompleted, "", &wire.Delivery{Digest: digest, MediaType: "video/mp4", Size: int64(len(data)), Location: "local"})
}

// KIT-V1, KIT-V2: a video is admitted with the set's bounds, run once on the
// kind, kept in the provider's store and observed completed by its own
// caller; another caller's operation is unknown, and a request_id held again
// names the same operation.
func TestTheTableRunsAVideoOnItsKind(t *testing.T) {
	store, err := storage.NewContentStore("videoops-test", filepath.Join(t.TempDir(), "content"))
	if err != nil {
		t.Skipf("content store: %v", err)
	}
	h, err := holder.New(holder.Options{Leases: noLeases{}, Resource: "card:0"})
	if err != nil {
		t.Fatal(err)
	}
	table, err := New(Options{Model: "m", Store: store, Holder: h})
	if err != nil {
		t.Fatal(err)
	}
	kind := finishingKind{t: table, ran: make(chan sdcpp.VideoParams, 1)}
	table.Serve(kind)
	defer table.Close()
	caller := inference.Subject{Account: "a", Program: "app"}
	request := wire.VideoRequest{RequestID: "r1", Model: "m", Prompt: "surf", DurationMs: 5000, Size: "auto"}
	a := table.Start(context.Background(), caller, request)
	if a.Outcome != wire.StartOutcomeAccepted {
		t.Fatalf("admission %+v", a)
	}
	params := <-kind.ran
	if params.Width != 512 || params.FPS != 16 || params.Frames != 81 {
		t.Fatalf("params %+v", params)
	}
	deadline := time.Now().Add(5 * time.Second)
	for table.Observe(context.Background(), caller, a.Operation).Status != wire.VideoStateCompleted {
		if time.Now().After(deadline) {
			t.Fatal("not completed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if again := table.Start(context.Background(), caller, request); again.Operation != a.Operation {
		t.Fatalf("request_id held again: %+v", again)
	}
	if other := table.Observe(context.Background(), inference.Subject{Account: "a", Program: "other"}, a.Operation); other.Status != wire.VideoStateUnknown {
		t.Fatalf("another caller observed %+v", other)
	}
	if text := table.Start(context.Background(), caller, wire.VideoRequest{RequestID: "r2", Model: "m", Prompt: "surf", DurationMs: 5000, Size: "1024x1024"}); text.Outcome != wire.StartOutcomeUnsupportedFeature {
		t.Fatalf("an unmeasured size: %+v", text)
	}
}
