package videoops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-inference/go/service"
	"github.com/openabstractions/abstraction-resource/go/holder"
	router "github.com/openabstractions/abstraction-router/go"
	storage "github.com/openabstractions/abstraction-storage/go"
)

var legacyVideoEndpointSequence atomic.Uint64

type loadingVideoKind struct {
	finishingKind
	loading chan struct{}
}

func (k loadingVideoKind) Run(ctx context.Context, op *Op) {
	k.t.SetState(op, wire.VideoStateQueued, 1)
	// Hold the admitted worker in the load before generation can begin.
	close(k.loading)
	<-ctx.Done()
}

func TestNativeVideoLoadingObservationKeepsLegacyWire(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store, err := storage.NewContentStore("video-legacy-wire", filepath.Join(t.TempDir(), "content"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := holder.New(holder.Options{Leases: noLeases{}, Resource: "card:0"})
	if err != nil {
		t.Fatal(err)
	}
	table, err := New(Options{Model: "m", Store: store, Holder: h})
	if err != nil {
		t.Fatal(err)
	}
	defer table.Close()
	kind := loadingVideoKind{finishingKind: finishingKind{t: table}, loading: make(chan struct{})}
	table.Serve(kind)

	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	principal, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := inference.New(inference.Config{Router: router.New(),
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer leaf.Close()
	endpoint := listen.Endpoint(fmt.Sprintf("video-wire-%d-%d", os.Getpid(), legacyVideoEndpointSequence.Add(1)))
	server, err := service.ListenDesignatedLeaf(endpoint, leaf, inference.ExecutionLocal, program)
	if err != nil {
		t.Fatal(err)
	}
	server.Video = table
	active, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(active) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	transport := listen.FrameClient{Endpoint: endpoint, Server: &listen.ServerExpectation{
		Principal: principal, Program: identity.CanonicalProgramPath(program),
	}, MaxFrame: 1 << 20, Timeout: 5 * time.Second}.WithContext(ctx)
	start, err := wire.NewVideoClient(transport).Start(wire.VideoRequest{
		RequestID: "legacy-loading", Model: "m", Prompt: "fixture", DurationMs: 5000, Size: "auto",
	})
	if err != nil || start.Outcome != wire.StartOutcomeAccepted || start.Operation == "" {
		t.Fatalf("video admission: %+v, %v", start, err)
	}
	select {
	case <-kind.loading:
	case <-ctx.Done():
		t.Fatal("provider did not reach its loading phase")
	}

	request := fmt.Sprintf(`{"version":1,"service":"abstraction.inference/video@1","method":"Observe","arguments":{"operation":%q}}`, start.Operation)
	raw, err := transport.ExchangeFrame([]byte(request))
	if err != nil {
		t.Fatal(err)
	}
	// Freeze the closed VideoObservation shape from before 2fb234794b.
	// The current generated decoder cannot serve as the compatibility oracle.
	var reply struct {
		Version int    `json:"version"`
		Service string `json:"service"`
		Method  string `json:"method"`
		OK      bool   `json:"ok"`
		Payload struct {
			Value struct {
				Status        string          `json:"status"`
				Step          int64           `json:"step"`
				Steps         int64           `json:"steps"`
				QueuePosition int64           `json:"queue_position"`
				Reason        string          `json:"reason"`
				Delivery      json.RawMessage `json:"delivery"`
			} `json:"value"`
		} `json:"payload"`
	}
	legacy := json.NewDecoder(bytes.NewReader(raw))
	legacy.DisallowUnknownFields()
	if err := legacy.Decode(&reply); err != nil {
		t.Fatalf("legacy video@1 cannot decode native loading observation: %v\n%s", err, raw)
	}
	if reply.Version != 1 || reply.Service != "abstraction.inference/video@1" || reply.Method != "Observe" || !reply.OK {
		t.Fatalf("not a successful legacy Observe reply: %s", raw)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	const expected = `{"version":1,"service":"abstraction.inference/video@1","method":"Observe","ok":true,"payload":{"value":{"status":"queued","queue_position":1}}}`
	if compact.String() != expected {
		t.Fatalf("native loading observation changed the legacy wire frame:\n%s\nwant:\n%s", &compact, expected)
	}
}
