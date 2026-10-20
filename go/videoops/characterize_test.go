package videoops

// Characterization tests (CQ-15a): they record how the video operation
// table admits, refuses, queues, cancels, reports and retires operations
// today, so the provider kit's move (W1-01) can prove it changed nothing.
// The engine kind is a fake; no engine runs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-provider-kit/go/sdcpp"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
	"github.com/openabstractions/abstraction-resource/go/holder"
	storage "github.com/openabstractions/abstraction-storage/go"
)

// boundKind holds a caller to its bounds and refuses the prompt "refuse".
type boundKind struct{ bounds Bounds }

func (k boundKind) Bounds() Bounds { return k.bounds }
func (k boundKind) Admit(p sdcpp.VideoParams) (wire.StartOutcome, string) {
	if p.Prompt == "refuse" {
		return wire.StartOutcomeUnsupportedFeature, "kind:refused"
	}
	return wire.StartOutcomeAccepted, ""
}
func (k boundKind) Fingerprint(sdcpp.VideoParams) gpufault.Fingerprint {
	return gpufault.Fingerprint{Model: "m"}
}
func (k boundKind) Run(context.Context, *Op) {}

func defaultBounds() Bounds {
	return Bounds{Width: 512, Height: 512, FPS: 16, Sizes: [][2]int{{512, 512}, {832, 480}}, MaxFrames: 81, MaxFPS: 16, FirstFrame: FirstFrameOptional}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// idleTable is a table whose worker never runs, so admitted operations
// stay queued; its kind is set directly.
func idleTable(t *testing.T, bounds Bounds, now *clock) *Table {
	t.Helper()
	store, err := storage.NewContentStore("videoops-char", filepath.Join(t.TempDir(), "content"))
	if err != nil {
		t.Skipf("content store: %v", err)
	}
	h, err := holder.New(holder.Options{Leases: noLeases{}, Resource: "card:0"})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Model: "m", Store: store, Holder: h}
	if now != nil {
		opts.Now = now.Now
	}
	table, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	table.kind = boundKind{bounds: bounds}
	return table
}

var caller = inference.Subject{Account: "a", Program: "app"}

func request(id string) wire.VideoRequest {
	return wire.VideoRequest{RequestID: id, Model: "m", Prompt: "surf", DurationMs: 5000, Size: "auto"}
}

func TestCharacterizeNew(t *testing.T) {
	store, err := storage.NewContentStore("videoops-char", filepath.Join(t.TempDir(), "content"))
	if err != nil {
		t.Skipf("content store: %v", err)
	}
	h, err := holder.New(holder.Options{Leases: noLeases{}, Resource: "card:0"})
	if err != nil {
		t.Fatal(err)
	}
	const needsStore = "videoops: the table needs a model and a content store"
	const needsHolder = "videoops: the table needs the provider's holder"
	for _, c := range []struct {
		name string
		opts Options
		want string
	}{
		{"no model", Options{Store: store, Holder: h}, needsStore},
		{"no store", Options{Model: "m", Holder: h}, needsStore},
		{"no holder", Options{Model: "m", Store: store}, needsHolder},
	} {
		if _, err := New(c.opts); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	table, err := New(Options{Model: "m", Store: store, Holder: h, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if table.Model() != "m" || table.TimeLimit() != DefaultTimeLimit || table.opts.Retention != 24*time.Hour || table.Active() {
		t.Fatalf("defaults: %q %s %s %v", table.Model(), table.TimeLimit(), table.opts.Retention, table.Active())
	}
	longer, _ := New(Options{Model: "m", Store: store, Holder: h, TimeLimit: time.Minute, Retention: 48 * time.Hour})
	if longer.TimeLimit() != time.Minute || longer.opts.Retention != 48*time.Hour {
		t.Fatal("explicit limits not kept")
	}
	if DefaultTimeLimit != 30*time.Minute || DefaultReady != 15*time.Minute || MaxQueue != 32 || MaxOperations != 4096 || GPUWait != time.Second {
		t.Fatal("a bound changed")
	}
}

func TestCharacterizeStartRefusals(t *testing.T) {
	table := idleTable(t, defaultBounds(), nil)
	required := idleTable(t, Bounds{Width: 512, Height: 512, FPS: 16, Sizes: [][2]int{{512, 512}}, MaxFrames: 81, MaxFPS: 16, FirstFrame: FirstFrameRequired}, nil)
	none := idleTable(t, Bounds{Width: 512, Height: 512, FPS: 16, Sizes: [][2]int{{512, 512}}, MaxFrames: 81, MaxFPS: 16, FirstFrame: FirstFrameNone}, nil)
	edit := func(e func(*wire.VideoRequest)) wire.VideoRequest { r := request("x"); e(&r); return r }
	cases := []struct {
		name    string
		table   *Table
		caller  inference.Subject
		req     wire.VideoRequest
		outcome wire.StartOutcome
		reason  string
	}{
		{"no program", table, inference.Subject{Account: "a"}, request("x"), wire.StartOutcomeForbidden, ""},
		{"no request id", table, caller, edit(func(r *wire.VideoRequest) { r.RequestID = "" }), wire.StartOutcomeInvalid, "request_id"},
		{"long request id", table, caller, edit(func(r *wire.VideoRequest) { r.RequestID = strings.Repeat("r", 129) }), wire.StartOutcomeInvalid, "request_id"},
		{"bad utf8 request id", table, caller, edit(func(r *wire.VideoRequest) { r.RequestID = "\xff" }), wire.StartOutcomeInvalid, "request_id"},
		{"other model", table, caller, edit(func(r *wire.VideoRequest) { r.Model = "n" }), wire.StartOutcomeNoHost, "model:unknown"},
		{"empty prompt", table, caller, edit(func(r *wire.VideoRequest) { r.Prompt = "" }), wire.StartOutcomeInvalid, "prompt"},
		{"long prompt", table, caller, edit(func(r *wire.VideoRequest) { r.Prompt = strings.Repeat("p", 32769) }), wire.StartOutcomeInvalid, "prompt"},
		{"bad utf8 prompt", table, caller, edit(func(r *wire.VideoRequest) { r.Prompt = "surf\xff" }), wire.StartOutcomeInvalid, "prompt"},
		{"zero duration", table, caller, edit(func(r *wire.VideoRequest) { r.DurationMs = 0 }), wire.StartOutcomeInvalid, "duration_ms"},
		{"long duration", table, caller, edit(func(r *wire.VideoRequest) { r.DurationMs = 600001 }), wire.StartOutcomeInvalid, "duration_ms"},
		{"negative fps", table, caller, edit(func(r *wire.VideoRequest) { r.Fps = -1 }), wire.StartOutcomeInvalid, "fps"},
		{"high fps", table, caller, edit(func(r *wire.VideoRequest) { r.Fps = 121 }), wire.StartOutcomeInvalid, "fps"},
		{"negative seed", table, caller, edit(func(r *wire.VideoRequest) { r.Seed = -1 }), wire.StartOutcomeInvalid, "seed"},
		{"seed beyond", table, caller, edit(func(r *wire.VideoRequest) { r.Seed = sdcpp.MaxSeed + 1 }), wire.StartOutcomeInvalid, "seed"},
		{"digest without type", table, caller, edit(func(r *wire.VideoRequest) { r.ImageDigest = "sha256:00" }), wire.StartOutcomeInvalid, "image_media_type"},
		{"type without digest", table, caller, edit(func(r *wire.VideoRequest) { r.ImageMediaType = "image/png" }), wire.StartOutcomeInvalid, "image_media_type"},
		{"unknown extension", table, caller, edit(func(r *wire.VideoRequest) { r.RequiredExtensions = []string{"x.y"} }), wire.StartOutcomeUnsupportedFeature, "extension:x.y"},
		{"frame required", required, caller, request("x"), wire.StartOutcomeUnsupportedFeature, ReasonFrameRequired},
		{"frame refused", none, caller, edit(func(r *wire.VideoRequest) { r.ImageDigest, r.ImageMediaType = "sha256:00", "image/png" }), wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonFirstFrame},
		{"unparsed size", table, caller, edit(func(r *wire.VideoRequest) { r.Size = "big" }), wire.StartOutcomeInvalid, "size"},
		{"half size", table, caller, edit(func(r *wire.VideoRequest) { r.Size = "512x" }), wire.StartOutcomeInvalid, "size"},
		{"unmeasured size", table, caller, edit(func(r *wire.VideoRequest) { r.Size = "1024x1024" }), wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonImageSize},
		{"fps above set", table, caller, edit(func(r *wire.VideoRequest) { r.Fps = 24 }), wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonVideoBound},
		{"frames above set", table, caller, edit(func(r *wire.VideoRequest) { r.DurationMs = 6000 }), wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonVideoBound},
		{"unknown frame digest", table, caller, edit(func(r *wire.VideoRequest) {
			r.ImageDigest, r.ImageMediaType = "sha256:"+strings.Repeat("0", 64), "image/png"
		}), wire.StartOutcomeInvalid, "content:unknown"},
		{"kind refuses", table, caller, edit(func(r *wire.VideoRequest) { r.Prompt = "refuse" }), wire.StartOutcomeUnsupportedFeature, "kind:refused"},
	}
	if sdcpp.MaxPromptBytes < maxPrompt {
		cases = append(cases, struct {
			name    string
			table   *Table
			caller  inference.Subject
			req     wire.VideoRequest
			outcome wire.StartOutcome
			reason  string
		}{"prompt above engine", table, caller, edit(func(r *wire.VideoRequest) { r.Prompt = strings.Repeat("p", sdcpp.MaxPromptBytes+1) }), wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonVideoBound})
	}
	for _, c := range cases {
		got := c.table.Start(context.Background(), c.caller, c.req)
		if got.Outcome != c.outcome || got.Reason != c.reason || got.Operation != "" {
			t.Errorf("%s: %+v, want %v %q", c.name, got, c.outcome, c.reason)
		}
	}
	if table.Active() {
		t.Fatal("a refusal queued work")
	}
}

func TestCharacterizeResolvedParams(t *testing.T) {
	table := idleTable(t, defaultBounds(), nil)
	cases := []struct {
		req                   wire.VideoRequest
		width, height, fps, n int
		seed                  int64
	}{
		{request("a"), 512, 512, 16, 81, 0},
		// The claim extension is the one required extension accepted.
		{wire.VideoRequest{RequestID: "claim", Model: "m", Prompt: "p", DurationMs: 5000, Size: "auto", Seed: 3,
			RequiredExtensions: []string{inference.ClaimExtension}}, 512, 512, 16, 81, 3},
		{wire.VideoRequest{RequestID: "b", Model: "m", Prompt: "p", DurationMs: 1000, Size: "832x480", Fps: 8, Seed: 7}, 832, 480, 8, 9, 7},
		{wire.VideoRequest{RequestID: "c", Model: "m", Prompt: "p", DurationMs: 1, Size: "auto", Seed: 1}, 512, 512, 16, 5, 1},
	}
	for _, c := range cases {
		a := table.Start(context.Background(), caller, c.req)
		if a.Outcome != wire.StartOutcomeAccepted || !strings.HasPrefix(a.Operation, "video-") || a.RetentionMs != (24*time.Hour).Milliseconds() {
			t.Fatalf("%s: %+v", c.req.RequestID, a)
		}
		p := table.Params(table.ops[a.Operation])
		if p.Width != c.width || p.Height != c.height || p.FPS != c.fps || p.Frames != c.n || p.Prompt != c.req.Prompt {
			t.Errorf("%s: params %+v", c.req.RequestID, p)
		}
		if c.seed != 0 && p.Seed != c.seed || c.seed == 0 && (p.Seed < 1 || p.Seed > sdcpp.MaxSeed) {
			t.Errorf("%s: seed %d", c.req.RequestID, p.Seed)
		}
	}
	if !table.Active() {
		t.Fatal("queued work not active")
	}
}

func TestCharacterizeOperationID(t *testing.T) {
	id, err := OperationID()
	if err != nil || len(id) != len("video-")+32 || !strings.HasPrefix(id, "video-") {
		t.Fatalf("id %q %v", id, err)
	}
	if other, _ := OperationID(); other == id {
		t.Fatal("ids repeat")
	}
}

func TestCharacterizeQueueObserveAndCancel(t *testing.T) {
	now := &clock{now: time.Unix(1_000_000, 0)}
	table := idleTable(t, defaultBounds(), now)
	ctx := context.Background()
	first := table.Start(ctx, caller, request("1"))
	second := table.Start(ctx, caller, request("2"))
	// The same request id names the same operation; another program's does not.
	if again := table.Start(ctx, caller, request("1")); again.Operation != first.Operation {
		t.Fatalf("held again: %+v", again)
	}
	otherCaller := inference.Subject{Account: "a", Program: "other"}
	if theirs := table.Start(ctx, otherCaller, request("1")); theirs.Operation == first.Operation || theirs.Outcome != wire.StartOutcomeAccepted {
		t.Fatalf("another program's request id: %+v", theirs)
	}
	if o := table.Observe(ctx, caller, second.Operation); o.Status != wire.VideoStateQueued || o.QueuePosition != 2 {
		t.Fatalf("second queued: %+v", o)
	}
	if o := table.Observe(ctx, inference.Subject{}, first.Operation); o.Status != wire.VideoStateForbidden {
		t.Fatalf("no program observe: %+v", o)
	}
	if o := table.Observe(ctx, caller, "video-absent"); o.Status != wire.VideoStateUnknown {
		t.Fatalf("absent: %+v", o)
	}
	if c := table.Cancel(ctx, inference.Subject{}, first.Operation); c.Outcome != wire.CancelOutcomeForbidden {
		t.Fatalf("no program cancel: %+v", c)
	}
	if c := table.Cancel(ctx, otherCaller, first.Operation); c.Outcome != wire.CancelOutcomeUnknown {
		t.Fatalf("another's cancel: %+v", c)
	}
	op := table.ops[first.Operation]
	c := table.Cancel(ctx, caller, first.Operation)
	if c.Outcome != wire.CancelOutcomeCancelled || c.ReplyOutcome == nil || *c.ReplyOutcome != wire.ReplyOutcomeCancelled || !op.Cancelled() || op.ID() != first.Operation {
		t.Fatalf("cancel: %+v", c)
	}
	if o := table.Observe(ctx, caller, second.Operation); o.QueuePosition != 1 {
		t.Fatalf("second after cancel: %+v", o)
	}
	again := table.Cancel(ctx, caller, first.Operation)
	if again.Outcome != wire.CancelOutcomeEnded || *again.ReplyOutcome != wire.ReplyOutcomeCancelled {
		t.Fatalf("cancel again: %+v", again)
	}
	// Finish after a cancel changes nothing.
	table.Finish(op, wire.VideoStateCompleted, "", &wire.Delivery{Digest: "d"})
	if o := table.Observe(ctx, caller, first.Operation); o.Status != wire.VideoStateCancelled || o.Delivery != nil {
		t.Fatalf("finish after cancel: %+v", o)
	}

	// A running operation reports its percent, clamped to 1..99, and its
	// position at least 1.
	run := table.ops[second.Operation]
	table.mu.Lock()
	table.queue, table.running = nil, run
	table.mu.Unlock()
	table.SetState(run, wire.VideoStateGenerating, 0)
	if o := table.Observe(ctx, caller, second.Operation); o.Status != wire.VideoStateGenerating || o.Steps != 0 {
		t.Fatalf("no percent yet: %+v", o)
	}
	for _, c := range [][2]int{{0, 1}, {50, 50}, {100, 99}} {
		table.SetPercent(run, c[0])
		if o := table.Observe(ctx, caller, second.Operation); o.Step != int64(c[1]) || o.Steps != 100 {
			t.Fatalf("percent %d: %+v", c[0], o)
		}
	}
	table.SetState(run, wire.VideoStateQueued, 0)
	if o := table.Observe(ctx, caller, second.Operation); o.QueuePosition != 1 {
		t.Fatalf("running op's position: %+v", o)
	}
	table.Finish(run, wire.VideoStateFailed, ReasonEngineFailed, nil)
	if o := table.Observe(ctx, caller, second.Operation); o.Status != wire.VideoStateFailed || o.Reason != ReasonEngineFailed {
		t.Fatalf("failed: %+v", o)
	}
	if c := table.Cancel(ctx, caller, second.Operation); c.Outcome != wire.CancelOutcomeEnded || *c.ReplyOutcome != wire.ReplyOutcomeUnavailable {
		t.Fatalf("cancel failed: %+v", c)
	}
	// SetState and SetPercent leave an ended operation alone.
	table.SetState(run, wire.VideoStateGenerating, 3)
	table.SetPercent(run, 40)
	if run.state != wire.VideoStateFailed || run.step != 99 {
		t.Fatalf("ended op changed: %v %d", run.state, run.step)
	}

	// Retention: an ended operation and its request id are held for 24
	// hours, then retired; the request id then admits a new operation.
	now.Add(24*time.Hour - time.Second)
	if o := table.Observe(ctx, caller, first.Operation); o.Status != wire.VideoStateCancelled {
		t.Fatalf("before retention: %+v", o)
	}
	now.Add(time.Second)
	if o := table.Observe(ctx, caller, first.Operation); o.Status != wire.VideoStateUnknown {
		t.Fatalf("after retention: %+v", o)
	}
	if fresh := table.Start(ctx, caller, request("1")); fresh.Operation == first.Operation || fresh.Outcome != wire.StartOutcomeAccepted {
		t.Fatalf("request id after retention: %+v", fresh)
	}
}

func TestCharacterizeCompletedObservation(t *testing.T) {
	table := idleTable(t, defaultBounds(), nil)
	ctx := context.Background()
	a := table.Start(ctx, caller, request("1"))
	op := table.ops[a.Operation]
	d := &wire.Delivery{Digest: "sha256:ab", MediaType: "video/mp4", Size: 5, Location: "local"}
	table.Finish(op, wire.VideoStateCompleted, "encoder:x", d)
	o := table.Observe(ctx, caller, a.Operation)
	if o.Status != wire.VideoStateCompleted || o.Reason != "encoder:x" || o.Delivery == nil || *o.Delivery != *d || o.Delivery == d {
		t.Fatalf("completed: %+v", o)
	}
	if c := table.Cancel(ctx, caller, a.Operation); c.Outcome != wire.CancelOutcomeEnded || *c.ReplyOutcome != wire.ReplyOutcomeCompleted {
		t.Fatalf("cancel completed: %+v", c)
	}
	if table.Params(op).InitImage != nil {
		t.Fatal("an ended op keeps its first frame")
	}
}

func TestCharacterizeQueueFull(t *testing.T) {
	table := idleTable(t, defaultBounds(), nil)
	ctx := context.Background()
	for i := range MaxQueue {
		if a := table.Start(ctx, caller, request("q"+string(rune('A'+i)))); a.Outcome != wire.StartOutcomeAccepted {
			t.Fatalf("queued %d: %+v", i, a)
		}
	}
	if a := table.Start(ctx, caller, request("over")); a.Outcome != wire.StartOutcomeExhausted || a.Reason != ReasonQueueFull {
		t.Fatalf("over the queue: %+v", a)
	}
}

func TestCharacterizeKeepAndLost(t *testing.T) {
	table := idleTable(t, defaultBounds(), nil)
	data := []byte("video")
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := table.Keep(digest, data); err != nil {
		t.Fatalf("keep: %v", err)
	}
	if err := table.Keep(digest, data); err != nil {
		t.Fatalf("keep again: %v", err)
	}
	if _, ok := table.opts.Store.Find(digest); !ok {
		t.Fatal("kept data not found")
	}
	if word := table.Lost(context.Background(), &Op{}); word != ReasonEngineLost {
		t.Fatalf("lost without GPU guard: %q", word)
	}
	if ReasonOf(context.Canceled) != sdcpp.ReasonVideoBound {
		t.Fatal("ReasonOf of a foreign error")
	}
}

func TestCharacterizeCloseEndsTheWorker(t *testing.T) {
	table := idleTable(t, defaultBounds(), nil)
	table.Serve(boundKind{bounds: defaultBounds()})
	table.Close()
	select {
	case <-table.Done():
	default:
		t.Fatal("Done open after Close")
	}
	table.Close() // a second close returns
}
