// Package videoops is a provider's video@1 operation table: it admits a
// video, queues it, runs one at a time on the provider's engine kind, keeps
// each operation for its retention and its result in the provider's own
// content store, and answers Observe and Cancel (CONTRACT.md KIT-V1..KIT-V6,
// inference INF-G5). The engine kind is the provider's: sd-server, the OA
// PyTorch engine or another, behind Kind.
package videoops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-inference/go/providerhost/sdcpp"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
	"github.com/openabstractions/abstraction-resource/go/holder"
	storage "github.com/openabstractions/abstraction-storage/go"
)

const (
	// DefaultTimeLimit bounds one generation once the engine accepted it.
	DefaultTimeLimit = 30 * time.Minute
	// DefaultReady is how long a video engine has to load its model set: a
	// cold read of 11.5 GB of weights.
	DefaultReady     = 15 * time.Minute
	defaultRetention = 24 * time.Hour
	// maxRequestID bounds request_id and operation ids (video@1).
	maxRequestID = 128
	// maxPrompt is video@1's prompt bound; sdcpp.MaxPromptBytes is the
	// engine's.
	maxPrompt = 32768
	// MaxQueue bounds the videos waiting behind the running one, and
	// MaxOperations the operations held at once, running, waiting or ended
	// and retained; a Start beyond either is exhausted.
	MaxQueue      = 32
	MaxOperations = 4096
)

// Exhausted reasons a Start carries.
const (
	ReasonQueueFull = "queue:full"
	ReasonTableFull = "operations:full"
)

// Failure reasons an Observe carries. None carries engine text.
const (
	ReasonEngineFailed  = "engine:failed"
	ReasonEngineResult  = "engine:result"
	ReasonEngineTimeout = "engine:timeout"
	ReasonEngineLost    = "engine:lost"
	ReasonEngineRefused = "engine:refused"
	// ReasonFrameRequired is the unsupported_feature reason of a text-only
	// request to a model set that generates from a first frame only.
	ReasonFrameRequired = "frame:required"
)

// A FirstFrameUse says how a video model set takes a caller's first frame.
type FirstFrameUse string

const (
	// FirstFrameOptional generates from text alone or from a first frame.
	FirstFrameOptional FirstFrameUse = "optional"
	// FirstFrameRequired generates from a first frame only (image to video).
	FirstFrameRequired FirstFrameUse = "required"
	// FirstFrameNone generates from text only.
	FirstFrameNone FirstFrameUse = "none"
)

// Bounds is what a model set holds a caller to (KIT-V2): its preset size and
// rate for size auto and rate zero, its sizes, frames, rate and first-frame
// use.
type Bounds struct {
	Width, Height, FPS int
	Sizes              [][2]int
	MaxFrames, MaxFPS  int
	FirstFrame         FirstFrameUse
}

// Kind is the engine a video model set runs on.
type Kind interface {
	Bounds() Bounds
	// Admit refuses a resolved request the engine would refuse, before it
	// is queued.
	Admit(params sdcpp.VideoParams) (wire.StartOutcome, string)
	// Fingerprint is what the request dispatches on the GPU (RES-G3).
	Fingerprint(params sdcpp.VideoParams) gpufault.Fingerprint
	// Run loads the engine under the holder and follows op to its end with
	// the table's Finish; the table holds the holder's use of the model for
	// it.
	Run(ctx context.Context, op *Op)
}

// Options are the table's model, store, bounds and owners.
type Options struct {
	// Model is the name the router lists and callers ask for.
	Model string
	// Store is the provider's content store: the runtime stages first
	// frames in it and reads results from it.
	Store *storage.ContentStore
	// TimeLimit bounds one generation once the engine accepted it; zero
	// reads DefaultTimeLimit.
	TimeLimit time.Duration
	// Retention is how long an ended operation and its request_id stay
	// held; zero reads 24 hours, and less is raised to it.
	Retention time.Duration
	// Holder holds the model's use while a video runs.
	Holder *holder.Holder
	// GPU, when set, admits each video and classifies a lost engine.
	GPU *gpufault.Guard
	Now func() time.Time
}

// An Op is one video operation.
type Op struct {
	id, program, requestID string
	params                 sdcpp.VideoParams
	state                  wire.VideoState
	position               int
	// step is the percent of the generation done, read from the engine's
	// output; zero reports none.
	step     int
	reason   string
	delivery *wire.Delivery
	ended    time.Time
	cancel   chan struct{}
	// fp is what the request dispatches on the GPU, for the reset
	// quarantine (RES-G3).
	fp gpufault.Fingerprint
}

// ID is the operation's id.
func (op *Op) ID() string { return op.id }

// Cancel is closed once the caller cancelled the operation.
func (op *Op) Cancel() <-chan struct{} { return op.cancel }

// Cancelled reports whether the caller cancelled the operation.
func (op *Op) Cancelled() bool {
	select {
	case <-op.cancel:
		return true
	default:
		return false
	}
}

// Table queues video operations and runs them one at a time on its Kind.
type Table struct {
	opts Options
	kind Kind

	mu        sync.Mutex
	ops       map[string]*Op
	byRequest map[string]string
	queue     []*Op
	running   *Op
	wake      chan struct{}
	done      chan struct{}
	stopped   sync.WaitGroup
}

// New is a table for opts; Serve starts it on its kind.
func New(opts Options) (*Table, error) {
	if opts.Model == "" || opts.Store == nil {
		return nil, errors.New("videoops: the table needs a model and a content store")
	}
	if opts.Holder == nil {
		return nil, errors.New("videoops: the table needs the provider's holder")
	}
	if opts.TimeLimit <= 0 {
		opts.TimeLimit = DefaultTimeLimit
	}
	if opts.Retention < defaultRetention {
		opts.Retention = defaultRetention
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Table{opts: opts, ops: map[string]*Op{}, byRequest: map[string]string{},
		wake: make(chan struct{}, 1), done: make(chan struct{})}, nil
}

// Serve starts the worker that runs queued operations on kind.
func (t *Table) Serve(kind Kind) {
	t.kind = kind
	t.stopped.Add(1)
	go t.work()
}

// Close stops the worker; a running operation ends engine:lost.
func (t *Table) Close() {
	select {
	case <-t.done:
	default:
		close(t.done)
	}
	t.stopped.Wait()
}

// Done is closed once the table closes.
func (t *Table) Done() <-chan struct{} { return t.done }

// Model is the model the table serves.
func (t *Table) Model() string { return t.opts.Model }

// TimeLimit bounds one generation once the engine accepted it.
func (t *Table) TimeLimit() time.Duration { return t.opts.TimeLimit }

// Active reports queued or running video work; a holder keeps the engine
// while it is.
func (t *Table) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running != nil || len(t.queue) > 0
}

func refusal(outcome wire.StartOutcome, reason string) wire.VideoAdmission {
	return wire.VideoAdmission{Outcome: outcome, Reason: reason}
}

// Start admits one video (INF-G5). A request_id this caller already holds
// names its operation again and nothing else is read.
func (t *Table) Start(ctx context.Context, caller inference.Subject, request wire.VideoRequest) wire.VideoAdmission {
	if caller.Program == "" {
		return refusal(wire.StartOutcomeForbidden, "")
	}
	if request.RequestID == "" || len(request.RequestID) > maxRequestID || !utf8.ValidString(request.RequestID) {
		return refusal(wire.StartOutcomeInvalid, "request_id")
	}
	key := caller.Program + "\x00" + request.RequestID
	t.mu.Lock()
	t.sweep()
	if id, held := t.byRequest[key]; held {
		t.mu.Unlock()
		return t.admitted(id)
	}
	t.mu.Unlock()
	if request.Model != t.opts.Model {
		return refusal(wire.StartOutcomeNoHost, "model:unknown")
	}
	params, outcome, reason := t.resolve(request)
	if outcome != wire.StartOutcomeAccepted {
		return refusal(outcome, reason)
	}
	if request.ImageDigest != "" {
		frame, outcome, reason := t.firstFrame(request, params.Width, params.Height)
		if outcome != wire.StartOutcomeAccepted {
			return refusal(outcome, reason)
		}
		params.InitImage = frame
	}
	// A request the engine would refuse is refused before it is queued.
	if outcome, reason := t.kind.Admit(params); outcome != wire.StartOutcomeAccepted {
		return refusal(outcome, reason)
	}
	fp := t.kind.Fingerprint(params)
	if gpu := t.opts.GPU; gpu != nil {
		// A request that reset the driver is refused at admission while it
		// is quarantined, so its caller hears so at once (RES-G3).
		if err := gpu.Quarantine(fp); err != nil {
			return refusal(wire.StartOutcomeUnavailable, gpufault.WordQuarantined)
		}
	}
	id, err := OperationID()
	if err != nil {
		return refusal(wire.StartOutcomeUnavailable, "")
	}
	op := &Op{id: id, program: caller.Program, requestID: request.RequestID, params: params,
		state: wire.VideoStateQueued, cancel: make(chan struct{}), fp: fp}
	t.mu.Lock()
	if held, ok := t.byRequest[key]; ok {
		t.mu.Unlock()
		return t.admitted(held)
	}
	if len(t.queue) >= MaxQueue {
		t.mu.Unlock()
		return refusal(wire.StartOutcomeExhausted, ReasonQueueFull)
	}
	if len(t.ops) >= MaxOperations {
		t.mu.Unlock()
		return refusal(wire.StartOutcomeExhausted, ReasonTableFull)
	}
	t.ops[id], t.byRequest[key] = op, id
	t.queue = append(t.queue, op)
	t.mu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
	return t.admitted(id)
}

func (t *Table) admitted(id string) wire.VideoAdmission {
	return wire.VideoAdmission{Outcome: wire.StartOutcomeAccepted, Operation: id, RetentionMs: t.opts.Retention.Milliseconds()}
}

// ReasonOf is the sd policy word of err, or the video bound.
func ReasonOf(err error) string {
	if word := sdcpp.Reason(err); word != "" {
		return word
	}
	return sdcpp.ReasonVideoBound
}

// resolve turns a request into engine values: the preset's size for auto,
// the preset's rate for zero, frames from the duration aligned to 4n+1, and
// a seed. A value outside video@1 is invalid; one outside the model set's
// sizes, frames, rate or first-frame use is unsupported_feature (KIT-V2).
func (t *Table) resolve(r wire.VideoRequest) (sdcpp.VideoParams, wire.StartOutcome, string) {
	set := t.kind.Bounds()
	p := sdcpp.VideoParams{Prompt: r.Prompt}
	switch {
	case r.Prompt == "" || len(r.Prompt) > maxPrompt || !utf8.ValidString(r.Prompt):
		return p, wire.StartOutcomeInvalid, "prompt"
	case r.DurationMs < 1 || r.DurationMs > 600000:
		return p, wire.StartOutcomeInvalid, "duration_ms"
	case r.Fps < 0 || r.Fps > 120:
		return p, wire.StartOutcomeInvalid, "fps"
	case r.Seed < 0 || r.Seed > sdcpp.MaxSeed:
		return p, wire.StartOutcomeInvalid, "seed"
	case (r.ImageDigest == "") != (r.ImageMediaType == ""):
		return p, wire.StartOutcomeInvalid, "image_media_type"
	}
	for _, key := range r.RequiredExtensions {
		if key != inference.ClaimExtension {
			return p, wire.StartOutcomeUnsupportedFeature, "extension:" + key
		}
	}
	if len(r.Prompt) > sdcpp.MaxPromptBytes {
		return p, wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonVideoBound
	}
	switch {
	case r.ImageDigest == "" && set.FirstFrame == FirstFrameRequired:
		return p, wire.StartOutcomeUnsupportedFeature, ReasonFrameRequired
	case r.ImageDigest != "" && set.FirstFrame == FirstFrameNone:
		return p, wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonFirstFrame
	}
	p.Width, p.Height = set.Width, set.Height
	if r.Size != "auto" {
		w, h, ok := strings.Cut(r.Size, "x")
		width, errW := strconv.Atoi(w)
		height, errH := strconv.Atoi(h)
		if !ok || errW != nil || errH != nil {
			return p, wire.StartOutcomeInvalid, "size"
		}
		if !slices.Contains(set.Sizes, [2]int{width, height}) || sdcpp.CheckVideoSize(width, height) != nil {
			return p, wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonImageSize
		}
		p.Width, p.Height = width, height
	}
	p.FPS = int(r.Fps)
	if p.FPS == 0 {
		p.FPS = set.FPS
	}
	if p.FPS > set.MaxFPS || p.FPS > sdcpp.MaxVideoFPS {
		return p, wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonVideoBound
	}
	frames := int(math.Ceil(float64(r.DurationMs) * float64(p.FPS) / 1000))
	p.Frames = sdcpp.AlignFrames(frames)
	if p.Frames > set.MaxFrames || p.Frames > sdcpp.MaxVideoFrames {
		return p, wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonVideoBound
	}
	p.Seed = r.Seed
	if p.Seed == 0 {
		var raw [4]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return p, wire.StartOutcomeUnavailable, ""
		}
		p.Seed = int64(binary.BigEndian.Uint32(raw[:])>>1) + 1
	}
	return p, wire.StartOutcomeAccepted, ""
}

// firstFrame reads the staged frame from the provider's store and makes the
// provider's own PNG of the exact size (KIT-V3). A digest the store does not
// hold is content:unknown, and nothing is generated from the prompt alone
// (INF-G5).
func (t *Table) firstFrame(r wire.VideoRequest, width, height int) ([]byte, wire.StartOutcome, string) {
	ref, ok := t.opts.Store.Find(r.ImageDigest)
	if !ok {
		return nil, wire.StartOutcomeInvalid, "content:unknown"
	}
	file, err := os.Open(t.opts.Store.Path(ref))
	if err != nil {
		return nil, wire.StartOutcomeInvalid, "content:unknown"
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, sdcpp.MaxFirstFrameBytes+1))
	if err != nil {
		return nil, wire.StartOutcomeUnavailable, ""
	}
	if len(data) > sdcpp.MaxFirstFrameBytes {
		return nil, wire.StartOutcomeUnsupportedFeature, sdcpp.ReasonFirstFrame
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != r.ImageDigest {
		return nil, wire.StartOutcomeInvalid, "content:unknown"
	}
	frame, err := sdcpp.FirstFrame(r.ImageMediaType, data, width, height)
	if err != nil {
		return nil, wire.StartOutcomeUnsupportedFeature, ReasonOf(err)
	}
	return frame, wire.StartOutcomeAccepted, ""
}

// OperationID is a fresh operation id.
func OperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "video-" + hex.EncodeToString(raw[:]), nil
}

// Observe reads one operation without waiting. Another caller's operation
// is unknown to this one.
func (t *Table) Observe(_ context.Context, caller inference.Subject, operation string) wire.VideoObservation {
	if caller.Program == "" {
		return wire.VideoObservation{Status: wire.VideoStateForbidden}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep()
	op, ok := t.ops[operation]
	if !ok || op.program != caller.Program {
		return wire.VideoObservation{Status: wire.VideoStateUnknown}
	}
	out := wire.VideoObservation{Status: op.state}
	switch op.state {
	case wire.VideoStateQueued:
		out.QueuePosition = int64(t.position(op))
	case wire.VideoStateGenerating:
		if op.step > 0 {
			out.Step, out.Steps = int64(op.step), 100
		}
	case wire.VideoStateCompleted:
		delivery := *op.delivery
		out.Delivery = &delivery
		// A completed video may name the encoder that wrote it; one whose
		// engine wrote its own container carries none.
		out.Reason = op.reason
	case wire.VideoStateFailed:
		out.Reason = op.reason
	}
	return out
}

// position is op's place among the work ahead of it, 1 for next.
func (t *Table) position(op *Op) int {
	if t.running == op {
		return max(op.position, 1)
	}
	for i, queued := range t.queue {
		if queued == op {
			if t.running != nil {
				return i + 2
			}
			return i + 1
		}
	}
	return 0
}

// Cancel stops an operation. A queued one leaves the queue; a running one is
// stopped by its kind, which reads Op.Cancel (KIT-V4).
func (t *Table) Cancel(_ context.Context, caller inference.Subject, operation string) wire.Cancellation {
	if caller.Program == "" {
		return wire.Cancellation{Outcome: wire.CancelOutcomeForbidden}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	op, ok := t.ops[operation]
	if !ok || op.program != caller.Program {
		return wire.Cancellation{Outcome: wire.CancelOutcomeUnknown}
	}
	if !op.ended.IsZero() {
		reply := endedReply(op.state)
		return wire.Cancellation{Outcome: wire.CancelOutcomeEnded, ReplyOutcome: &reply}
	}
	for i, queued := range t.queue {
		if queued == op {
			t.queue = append(t.queue[:i], t.queue[i+1:]...)
			break
		}
	}
	t.end(op, wire.VideoStateCancelled, "")
	close(op.cancel)
	cancelled := wire.ReplyOutcomeCancelled
	return wire.Cancellation{Outcome: wire.CancelOutcomeCancelled, ReplyOutcome: &cancelled}
}

func endedReply(state wire.VideoState) wire.ReplyOutcome {
	switch state {
	case wire.VideoStateCompleted:
		return wire.ReplyOutcomeCompleted
	case wire.VideoStateCancelled:
		return wire.ReplyOutcomeCancelled
	}
	return wire.ReplyOutcomeUnavailable
}

// end records an operation's last state; t.mu is held.
func (t *Table) end(op *Op, state wire.VideoState, reason string) {
	if !op.ended.IsZero() {
		return
	}
	op.state, op.reason, op.ended = state, reason, t.opts.Now()
	op.params.InitImage = nil
}

// sweep retires operations ended longer than the retention ago, with their
// request ids and their results in the store; t.mu is held.
func (t *Table) sweep() {
	now := t.opts.Now()
	live := map[string]bool{}
	var retired []*Op
	for id, op := range t.ops {
		if !op.ended.IsZero() && now.Sub(op.ended) >= t.opts.Retention {
			retired = append(retired, op)
			delete(t.ops, id)
			delete(t.byRequest, op.program+"\x00"+op.requestID)
			continue
		}
		if op.delivery != nil {
			live[op.delivery.Digest] = true
		}
	}
	for _, op := range retired {
		if op.delivery == nil || live[op.delivery.Digest] {
			continue
		}
		if ref, ok := t.opts.Store.Find(op.delivery.Digest); ok {
			//unchecked: a result still open by a reader is removed by a later sweep's retry or stays until the provider's store is cleared
			os.Remove(t.opts.Store.Path(ref))
		}
	}
}

// work runs queued operations in order until the table closes.
func (t *Table) work() {
	defer t.stopped.Done()
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	for {
		t.mu.Lock()
		var op *Op
		if len(t.queue) > 0 {
			op, t.queue = t.queue[0], t.queue[1:]
			t.running = op
		}
		t.mu.Unlock()
		if op != nil {
			t.run(op)
			t.mu.Lock()
			t.running = nil
			t.mu.Unlock()
			continue
		}
		select {
		case <-t.done:
			return
		case <-t.wake:
		case <-sweep.C:
			t.mu.Lock()
			t.sweep()
			t.mu.Unlock()
		}
	}
}

// Finish ends op unless a cancel already ended it.
func (t *Table) Finish(op *Op, state wire.VideoState, reason string, delivery *wire.Delivery) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !op.ended.IsZero() {
		return
	}
	op.delivery = delivery
	t.end(op, state, reason)
}

// SetState records op's state and queue position while it has not ended.
func (t *Table) SetState(op *Op, state wire.VideoState, position int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if op.ended.IsZero() {
		op.state, op.position = state, position
	}
}

// SetPercent records the generation's percent, at most 99 until the result
// is kept.
func (t *Table) SetPercent(op *Op, percent int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if op.ended.IsZero() {
		op.step = max(1, min(percent, 99))
	}
}

// Params is op's resolved request.
func (t *Table) Params(op *Op) sdcpp.VideoParams {
	t.mu.Lock()
	defer t.mu.Unlock()
	return op.params
}

// run holds the model's use, admits op on the GPU, and hands it to the
// kind. The holder counts op as a use, so neither the idle clock nor a yield
// takes the engine while it runs.
func (t *Table) run(op *Op) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		select {
		case <-op.cancel:
			stop()
		case <-t.done:
			stop()
		case <-ctx.Done():
		}
	}()
	if gpu := t.opts.GPU; gpu != nil {
		if word := t.admitGPU(ctx, gpu, op); word != "" {
			t.Finish(op, wire.VideoStateFailed, word, nil)
			return
		}
		// The wake lease is held from here until the job ends (RES-G5).
		defer gpu.Busy("video " + op.id)()
	}
	end, err := t.opts.Holder.Begin(ctx, t.opts.Model)
	if err != nil {
		t.Finish(op, wire.VideoStateFailed, ReasonEngineLost, nil)
		return
	}
	defer end()
	t.kind.Run(ctx, op)
}

// GPUWait is how often a queued video or image asks again while the machine
// is in standby or the driver is coming back.
const GPUWait = time.Second

// admitGPU waits while the machine is in Modern Standby or the driver is
// coming back from a reset, and returns the word that ends op otherwise: a
// quarantined or known-bad request, or a cancel (RES-G3..RES-G5). It returns
// "" once op may run.
func (t *Table) admitGPU(ctx context.Context, gpu *gpufault.Guard, op *Op) string {
	for {
		err := gpu.AdmitRequest(ctx, op.fp)
		var refused *gpufault.Refusal
		if !errors.As(err, &refused) {
			return ""
		}
		if refused.Word != gpufault.WordStandby && refused.Word != gpufault.WordResetting {
			return refused.Word
		}
		t.SetState(op, wire.VideoStateQueued, 1)
		timer := time.NewTimer(GPUWait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return refused.Word
		}
	}
}

// Lost is the word a video whose engine went away fails with: gpu:reset when
// a driver reset is recorded within the Guard's window of the loss, which
// also quarantines the request, else engine:lost (RES-G2, RES-G3).
func (t *Table) Lost(ctx context.Context, op *Op) string {
	gpu := t.opts.GPU
	if gpu == nil {
		return ReasonEngineLost
	}
	if _, reset := gpu.Classify(ctx, time.Now()); !reset {
		return ReasonEngineLost
	}
	gpu.Failed(op.fp)
	return gpufault.WordReset
}

// Keep places data in the store under digest unless it is there.
func (t *Table) Keep(digest string, data []byte) error {
	if _, ok := t.opts.Store.Find(digest); ok {
		return nil
	}
	ref, err := t.opts.Store.Place(digest, int64(len(data)))
	if err != nil {
		return err
	}
	if err := os.WriteFile(t.opts.Store.Path(ref), data, 0o600); err != nil {
		return err
	}
	return t.opts.Store.Commit(ref)
}
