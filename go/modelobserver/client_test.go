package modelobserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/enginehttp"
)

type verifiedFixtureTransport func(*http.Request) (*http.Response, error)

func (f verifiedFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifiedClientPreservesProofAndTransportCauses(t *testing.T) {
	program := filepath.Join(t.TempDir(), "engine.exe")
	network := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("fixture offline")}
	for _, c := range []struct {
		name              string
		expected          string
		proofErr, sendErr error
		unproven          bool
	}{
		{"missing expected program", "", nil, nil, true},
		{"native proof", program, identity.ErrNotProven, nil, true},
		{"missing native binding", program, identity.ErrNoBinding, nil, true},
		{"inspection deadline", program, context.DeadlineExceeded, nil, false},
		{"listener outage", program, network, nil, false},
		{"upstream outage", program, nil, network, false},
		{"changed generation", program, nil, enginehttp.ErrEnginePeer, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var sent int
			transport := &verifiedTransport{account: "fixture", expected: func(context.Context, string) (string, error) { return c.expected, nil },
				inspect: func(context.Context, string) (listener, error) {
					return listener{account: "fixture", program: program}, c.proofErr
				},
				client: func(string, identity.Process) (*http.Client, error) {
					return &http.Client{Transport: verifiedFixtureTransport(func(*http.Request) (*http.Response, error) { sent++; return nil, c.sendErr })}, nil
				}}
			request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:1", nil)
			_, err := transport.RoundTrip(request)
			if errors.Is(err, identity.ErrNotProven) != c.unproven {
				t.Fatalf("native proof cause: %v", err)
			}
			if c.unproven && !errors.Is(err, enginehttp.ErrEnginePeer) {
				t.Fatalf("lost engine peer refusal: %v", err)
			}
			if c.proofErr != nil && !c.unproven && !errors.Is(err, c.proofErr) || c.sendErr == network && !errors.Is(err, network) {
				t.Fatalf("lost transport outage: %v", err)
			}
			if (c.expected == "" || c.proofErr != nil) && sent != 0 {
				t.Fatal("failed proof sent an HTTP request")
			}
		})
	}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://remote.invalid:443", nil)
	transport := &verifiedTransport{account: "fixture", expected: func(context.Context, string) (string, error) {
		t.Fatal("remote endpoint reached native proof")
		return "", nil
	}}
	if _, err := transport.RoundTrip(request); !errors.Is(err, enginehttp.ErrEnginePeer) || errors.Is(err, identity.ErrNotProven) {
		t.Fatalf("remote failure became a local engine proof failure: %v", err)
	}
}

func TestVerifiedClientKeepsWrappedCancellationDistinctFromProofFailure(t *testing.T) {
	program := filepath.Join(t.TempDir(), "engine.exe")
	for _, boundary := range []string{"expected", "inspect", "client", "roundtrip"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(boundary+"/"+cause.Error(), func(t *testing.T) {
				wrapped := errors.Join(enginehttp.ErrEnginePeer, cause)
				var sent int
				transport := &verifiedTransport{account: "fixture",
					expected: func(context.Context, string) (string, error) {
						if boundary == "expected" {
							return program, wrapped
						}
						return program, nil
					},
					inspect: func(context.Context, string) (listener, error) {
						if boundary == "inspect" {
							return listener{}, wrapped
						}
						return listener{account: "fixture", program: program}, nil
					},
					client: func(string, identity.Process) (*http.Client, error) {
						if boundary == "client" {
							return nil, wrapped
						}
						return &http.Client{Transport: verifiedFixtureTransport(func(*http.Request) (*http.Response, error) { sent++; return nil, wrapped })}, nil
					},
				}
				request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:1", nil)
				_, err := transport.RoundTrip(request)
				if !errors.Is(err, enginehttp.ErrEnginePeer) || !errors.Is(err, cause) || errors.Is(err, identity.ErrNotProven) {
					t.Fatalf("wrapped cancellation became engine proof failure: %v", err)
				}
				if boundary != "roundtrip" && sent != 0 {
					t.Fatal("cancelled preflight reached HTTP transport")
				}
			})
		}
	}
}

func TestVerifiedClientRequiresExpectedNativeProgram(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native loopback proof unsupported")
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.Write([]byte(`{}`)) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer, err := inspect(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range []string{"", filepath.Join(t.TempDir(), "foreign.exe"), peer.program} {
		client := NewClient(func(context.Context, string) (string, error) { return program, nil })
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		response, err := client.Do(request)
		if program == peer.program {
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		} else if !errors.Is(err, enginehttp.ErrEnginePeer) {
			t.Fatal(program, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("unverified request reached listener", requests.Load())
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	if _, err := NewClient(nil).Do(request); !errors.Is(err, enginehttp.ErrEnginePeer) || requests.Load() != 1 {
		t.Fatal("missing producer binding fell back", err, requests.Load())
	}
}

func TestVerifiedClientRefusesForeignProcessGenerationBeforeHTTP(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native loopback proof unsupported")
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.Write([]byte(`{}`)) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	selected, err := inspect(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	selected.process.PID++ // The selected generation is not this real listener.
	transport := &verifiedTransport{account: selected.account, expected: func(context.Context, string) (string, error) { return selected.program, nil },
		inspect: func(context.Context, string) (listener, error) { return selected, nil }, client: enginehttp.NewClient}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	if _, err := transport.RoundTrip(request); !errors.Is(err, enginehttp.ErrEnginePeer) || requests.Load() != 0 {
		t.Fatal(err, requests.Load())
	}
}
