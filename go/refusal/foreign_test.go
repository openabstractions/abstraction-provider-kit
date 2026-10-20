package refusal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	res "github.com/openabstractions/abstraction-resource/go/client"
	"github.com/openabstractions/abstraction-resource/go/holder"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	"github.com/openabstractions/abstraction-resource/go/service"
)

const foreignGiB = int64(1) << 30

var foreignEndpointSequence atomic.Uint64

// A load that memory no lease covers blocks is refused naming the program
// that holds it in the provider's log (resource RES-L3: an app without OA's
// integration is measured and never driven). The app that asked reads a
// sentence that names no other program and no size (RES-T4). The provider's holder reaches a served lease book
// through the resource client, as an installed provider does. The card holds
// a stock Ollama's 20 GiB, which no lease covers, and an OA engine's 22 GiB,
// which its lease covers; the load needs more than every lease could give
// back, so the service reads insufficient, and the refusal names Ollama and
// not the larger leased engine.
func TestALoadForeignMemoryBlocksNamesTheForeignProgram(t *testing.T) {
	in := instrument.NewFixed()
	engine := service.Subject{Program: `C:\oa\modelhostd.exe`, Account: "S-1-5-21-7-1001"}
	ollama := instrument.Sample{PID: 7, Program: `C:\Users\p\AppData\Local\Programs\Ollama\ollama.exe`,
		Account: "S-1-5-21-7-1001", Amount: 20 * foreignGiB}
	in.Set(instrument.Card0, instrument.FixedResource{Capacity: 48 * foreignGiB, Samples: []instrument.Sample{ollama}})
	table := service.NewTable(in, nil, time.Nanosecond)
	permit := func(context.Context, *identity.Peer, string, string) error { return nil }
	book, err := service.OpenBook(service.BookOptions{Table: table, Hold: permit, Settle: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if out := book.Acquire(context.Background(), engine, &identity.Peer{}, instrument.Card0, 22*foreignGiB, 0); out.Lease == nil {
		t.Fatalf("the OA engine's own lease read %s", out.Outcome)
	}
	in.Set(instrument.Card0, instrument.FixedResource{Capacity: 48 * foreignGiB, Samples: []instrument.Sample{ollama,
		{PID: 8, Program: engine.Program, Account: engine.Account, Amount: 22 * foreignGiB}}})

	leases := servedLeases(t, table, book, permit)
	h, err := holder.New(holder.Options{Leases: leases, Resource: instrument.Card0, Idle: time.Hour, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	const amount = 30 * foreignGiB
	loaded := h.Ensure(context.Background(), "m", func() (holder.Component, error) {
		return holder.Component{Amount: amount, Start: func() (holder.Owned, error) {
			return nil, errors.New("a load the service refused was started")
		}}, nil
	})
	refused := Load(instrument.Card0, "m", amount, loaded)
	var r *Refusal
	if !errors.As(refused, &r) || r.Word != holder.WordInsufficient {
		t.Fatalf("the load read %v, want %s", refused, holder.WordInsufficient)
	}
	const want = "Ollama holds 20 GB that OpenAbstractions doesn't manage. Unload it in Ollama, or close Ollama."
	if r.Detail != want || refused.Error() != holder.WordInsufficient+": "+want {
		t.Fatalf("the provider's log reads %q, want %q", refused.Error(), want)
	}
	app := AppOf(refused)
	if app != ForeignMemory || strings.Contains(app, "Ollama") || strings.Contains(app, "GB") {
		t.Fatalf("the app that asked reads %q, want %q", app, ForeignMemory)
	}
}

// A table that shows no foreign memory keeps the refusal it had: the sentence
// fails open to the admission-estimate detail.
func TestALoadWithNoForeignHoldKeepsItsDetail(t *testing.T) {
	failure := &holder.Failure{Stage: holder.StageAcquire, Result: res.AcquireResult{Outcome: res.AcquireOutcomeInsufficient}}
	var r *Refusal
	if !errors.As(Load(instrument.Card0, "m", 1, failure), &r) || r.Detail == "" || r.App != "" || r.Word != holder.WordInsufficient {
		t.Fatalf("a load with no foreign hold read %+v", r)
	}
	for program, want := range map[string]string{
		`C:\Programs\Ollama\ollama.exe`:                "Ollama",
		`C:\Programs\LM Studio\LM Studio.exe`:          "LM Studio",
		"/usr/local/bin/ollama":                        "Ollama",
		"msix:Microsoft.WindowsTerminal_8wekyb3d8bbwe": "Microsoft.WindowsTerminal",
	} {
		if got := programTitle(program); got != want {
			t.Errorf("programTitle(%q) = %q, want %q", program, got, want)
		}
	}
}

func servedLeases(t *testing.T, table *service.Table, book *service.Book, permit service.Policy) *res.Client {
	t.Helper()
	endpoint := listen.Endpoint(fmt.Sprintf("kit-foreign-test-%d-%d", os.Getpid(), foreignEndpointSequence.Add(1)))
	if err := listen.CanEver(endpoint, service.Bound); err != nil {
		t.Skip("this platform cannot bind callers here: ", err)
	}
	host, err := service.Listen(endpoint, table)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.EnablePolicy(permit); err != nil {
		t.Fatal(err)
	}
	if err := host.EnableLeases(book); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		<-done
	})
	return res.New(endpoint)
}
