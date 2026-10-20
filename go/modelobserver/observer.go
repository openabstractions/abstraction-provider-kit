// Package modelobserver observes existing local model servers from their
// endpoint declarations. It starts no program and holds no resource lease.
package modelobserver

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/enginehttp"
	router "github.com/openabstractions/abstraction-router/go"
)

// Reading records a responding server and the available program provenance.
// It grants no permission or process custody.
type Reading struct {
	Host    *router.Host
	Program string
	// Presence is verified or unverified. Unverified records have no Program
	// claim and exist only for the explicitly unsupported macOS/IPv6 proof.
	Presence string
}

var unverifiedReadClient = &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// presenceMode classifies capability before I/O. A supported proof failure
// never changes this mode. Only loopback endpoint records qualify.
func presenceMode(goos, base string) (unverified, valid bool) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false, false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return false, false
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return goos == "darwin", true
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		return false, false
	}
	return goos == "darwin" || addr.Is6(), true
}

type listener struct {
	program, account string
	process          identity.Process
}

func inspect(ctx context.Context, base string) (listener, error) {
	peer, err := enginehttp.InspectListener(ctx, base)
	if err != nil {
		return listener{}, err
	}
	user, err := peer.User.AtLeast(identity.ProofBound)
	if err != nil {
		return listener{}, err
	}
	account, err := user.Account()
	if err != nil {
		return listener{}, err
	}
	process, err := peer.Process.AtLeast(identity.ProofBound)
	if err != nil || process.PID <= 0 || process.StartTime.IsZero() || process.Recycled {
		return listener{}, identity.ErrNoBinding
	}
	program, err := identity.SubjectProgram(peer, identity.ProofBound)
	if err != nil {
		return listener{}, err
	}
	return listener{program: program, account: account, process: process}, nil
}

// Observe normally requires a same-account listener program and a listing
// bound to that exact process, including an empty listing. Each HTTP connection
// is independently bound and rechecked by enginehttp.NewClient. Only known
// unsupported macOS/IPv6 proof permits a live listing marked unverified with
// no Program claim. A supported proof failure never selects that path.
// Cached negative router readings are reused only within maxAge and keep their
// original timestamp. An Up reading has no process provenance and requires a
// fresh listing; it cannot establish that this observed program responded.
func Observe(ctx context.Context, candidates []*router.Host, snapshot func() ([]router.HostState, time.Time), maxAge time.Duration) ([]Reading, time.Time) {
	self, err := identity.Self()
	if err != nil {
		return nil, time.Now()
	}
	account, err := self.Account()
	if err != nil {
		return nil, time.Now()
	}
	return observeForPlatform(ctx, candidates, snapshot, maxAge, account, inspect, enginehttp.NewClient, runtime.GOOS, unverifiedReadClient)
}

func observeForPlatform(ctx context.Context, candidates []*router.Host, snapshot func() ([]router.HostState, time.Time), maxAge time.Duration, account string, inspect func(context.Context, string) (listener, error), client func(string, identity.Process) (*http.Client, error), goos string, fallback *http.Client) ([]Reading, time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	observed := time.Now()
	if ctx.Err() != nil || account == "" || inspect == nil || client == nil {
		return nil, observed
	}
	var snapshotAt time.Time
	known := map[string]router.HostState{}
	if snapshot != nil {
		states, at := snapshot()
		if !at.IsZero() && time.Since(at) < maxAge {
			snapshotAt = at
			for _, state := range states {
				known[state.Host] = state
			}
		}
	}
	readings := make([]Reading, len(candidates))
	var joined sync.WaitGroup
	var ageMu sync.Mutex
	for i, host := range candidates {
		if host == nil || !host.Servable() || host.Hosted {
			continue
		}
		joined.Add(1)
		go func(i int, host *router.Host) {
			defer joined.Done()
			unverified, valid := presenceMode(goos, host.Base)
			if !valid {
				return
			}
			var before listener
			if !unverified {
				var err error
				before, err = inspect(ctx, host.Base)
				if err != nil || before.account != account || before.program == "" {
					return
				}
			}
			if state, ok := known[host.Name]; ok && state.Base == host.Base && state.Wire == host.Wire && state.DeclaredBy == host.DeclaredBy && !state.Hosted && !state.Up {
				ageMu.Lock()
				if snapshotAt.Before(observed) {
					observed = snapshotAt
				}
				ageMu.Unlock()
				return
			}
			boundClient := fallback
			if !unverified {
				var err error
				boundClient, err = client(host.Base, before.process)
				if err != nil {
					return
				}
			}
			if boundClient == nil {
				return
			}
			read := router.NewWithHTTPClient(boundClient, host)
			if err := read.SurveyContext(ctx); err != nil {
				return
			}
			states, _, _, _, _ := read.Residency(false)
			if len(states) != 1 || !states[0].Up {
				return
			}
			if unverified {
				readings[i] = Reading{Host: host, Presence: "unverified"}
				return
			}
			after, err := inspect(ctx, host.Base)
			if err != nil || !sameListener(before, after) {
				return
			}
			readings[i] = Reading{Host: host, Program: after.program, Presence: "verified"}
		}(i, host)
	}
	joined.Wait()
	var out []Reading
	for _, reading := range readings {
		if reading.Host != nil {
			out = append(out, reading)
		}
	}
	return out, observed
}

func sameListener(a, b listener) bool {
	return a.program == b.program && a.account == b.account && a.process.PID == b.process.PID &&
		a.process.StartTime.Equal(b.process.StartTime) && a.process.Generation == b.process.Generation && !a.process.Recycled && !b.process.Recycled
}
