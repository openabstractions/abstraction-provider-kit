package modelobserver

import (
	"context"
	"errors"
	"net/http"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/enginehttp"
)

// NewClient selects a verified transport for each local engine request. expected
// must read a native registration or an already validated program observation;
// an endpoint or product name alone supplies no expected program. Missing proof
// returns a nonnil refusing client, never the unverified compatibility client.
// Configure its Timeout before serving, or retain the caller's own deadline.
func NewClient(expected func(context.Context, string) (string, error)) *http.Client {
	me, err := identity.Self()
	account := ""
	if err == nil {
		account, err = me.Account()
	}
	return &http.Client{Transport: &verifiedTransport{account: account, expected: expected, failure: err, inspect: inspect, client: enginehttp.NewClient},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type verifiedTransport struct {
	account  string
	expected func(context.Context, string) (string, error)
	failure  error
	inspect  func(context.Context, string) (listener, error)
	client   func(string, identity.Process) (*http.Client, error)
}

func (t *verifiedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.failure != nil || t.account == "" || t.expected == nil || request.URL == nil || request.URL.User != nil {
		return nil, enginehttp.ErrEnginePeer
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	base := request.URL.Scheme + "://" + request.URL.Host
	if _, local := presenceMode("", base); !local {
		return nil, enginehttp.ErrEnginePeer
	}
	program, err := t.expected(request.Context(), request.URL.String())
	if err != nil || program == "" {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, errors.Join(enginehttp.ErrEnginePeer, identity.ErrNotProven)
	}
	peer, err := t.inspect(request.Context(), base)
	if err != nil {
		return nil, errors.Join(enginehttp.ErrEnginePeer, engineProofCause(err))
	}
	if peer.account != t.account || !identity.SameSubjectProgram(peer.program, program) {
		return nil, errors.Join(enginehttp.ErrEnginePeer, identity.ErrNotProven)
	}
	client, err := t.client(base, peer.process)
	if err != nil {
		return nil, engineProofCause(err)
	}
	if client == nil || client.Transport == nil {
		return nil, enginehttp.ErrEnginePeer
	}
	response, err := client.Transport.RoundTrip(request)
	return response, engineProofCause(err)
}

func engineProofCause(err error) error {
	// A caller's deadline or cancellation can end the proof wait itself.
	// Keep both original causes without claiming an engine identity failure.
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, identity.ErrNotProven) || errors.Is(err, identity.ErrNoBinding) || errors.Is(err, enginehttp.ErrEnginePeer) {
		return errors.Join(err, identity.ErrNotProven)
	}
	return err
}
