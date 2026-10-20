package modelobserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-provider-kit/go/enginehttp"
	router "github.com/openabstractions/abstraction-router/go"
)

func fixtureClient(string, identity.Process) (*http.Client, error) { return http.DefaultClient, nil }

func TestObserverNegativeSnapshotRequiresSameProductOwner(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("native product observation requires loopback proof")
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.Write([]byte(`{"data":[]}`)) }))
	defer server.Close()
	host := router.LMStudio(server.URL)
	host.DeclaredBy = router.DeclaredByLMStudio
	at := time.Now().Add(-10 * time.Second)
	for _, owner := range []string{router.DeclaredByLMStudio, "operator"} {
		t.Run(owner, func(t *testing.T) {
			before := requests.Load()
			snapshot := func() ([]router.HostState, time.Time) {
				return []router.HostState{{Host: host.Name, Base: host.Base, Wire: host.Wire, DeclaredBy: owner}}, at
			}
			got, observed := Observe(t.Context(), []*router.Host{host}, snapshot, 30*time.Second)
			if owner == router.DeclaredByLMStudio {
				if len(got) != 0 || !observed.Equal(at) || requests.Load() != before {
					t.Fatalf("same-owner negative cache changed: %+v %v", got, observed)
				}
			} else if len(got) != 1 || got[0].Presence != "verified" || got[0].Program == "" || !observed.After(at) || requests.Load() <= before {
				t.Fatalf("operator shadow suppressed fresh native observation: %+v %v", got, observed)
			}
		})
	}
}

func observe(ctx context.Context, candidates []*router.Host, snapshot func() ([]router.HostState, time.Time), age time.Duration, account string, inspect func(context.Context, string) (listener, error), client func(string, identity.Process) (*http.Client, error)) ([]Reading, time.Time) {
	return observeForPlatform(ctx, candidates, snapshot, age, account, inspect, client, "windows", unverifiedReadClient)
}

func TestObserverHelperProcess(t *testing.T) {
	marker := os.Getenv("OA_MODELOBSERVER_TEST_MARKER")
	if marker == "" {
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(listener.Addr())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := os.WriteFile(marker, []byte("request"), 0o600); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"data":[]}`))
	})}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

func TestObserverRefusesABAListenerSwap(t *testing.T) {
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("listener identity unsupported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
	defer serverA.Close()
	a, err := inspect(ctx, serverA.URL)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "b-request")
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestObserverHelperProcess$")
	child.Env = append(os.Environ(), "OA_MODELOBSERVER_TEST_MARKER="+marker)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + strings.TrimSpace(address)
	// Reproduce the A-B-A observation trace deterministically: both outer
	// inspections see actual process A; the HTTP endpoint is held by real
	// process B between them. A plain survey incorrectly attributes B to A.
	got, _ := observe(ctx, []*router.Host{router.LMStudio(base)}, nil, time.Minute, a.account, func(context.Context, string) (listener, error) { return a, nil }, enginehttp.NewClient)
	if len(got) != 0 {
		t.Fatal("A-B-A listener swap became program+response evidence", got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("request bytes reached swapped process B", err)
	}
}

func TestObserverNativeListenerPresence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	present, err := inspect(ctx, server.URL)
	if !identity.LoopbackCeiling().Bindable {
		if err == nil {
			t.Fatal("unsupported platform supplied listener presence", present)
		}
		if runtime.GOOS == "darwin" {
			got, _ := Observe(ctx, []*router.Host{router.LMStudio(server.URL)}, nil, time.Minute)
			if len(got) != 1 || got[0].Presence != "unverified" || got[0].Program != "" {
				t.Fatal("unsupported native presence was not explicit", got)
			}
		}
		return
	}
	if err != nil {
		t.Fatal("native fixture listener presence", err)
	}
	if present.program == "" || present.account == "" {
		t.Fatal(present)
	}
}

type observerRoundTripper func(*http.Request) (*http.Response, error)

func (f observerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestObserverUnsupportedPresenceIsExplicitAndRequiresLiveServer(t *testing.T) {
	for _, platform := range []struct{ goos, base string }{{"darwin", "http://127.0.0.1:1234"}, {"darwin", "http://LOCALHOST:1234/proxy"}, {"windows", "http://[::1]:1234"}, {"linux", "http://[::1]:1234/proxy"}} {
		for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/%s/%d", platform.goos, platform.base, status), func(t *testing.T) {
				var proofCalls atomic.Int64
				fallback := &http.Client{Transport: observerRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
				})}
				got, _ := observeForPlatform(context.Background(), []*router.Host{router.LMStudio(platform.base)}, nil, time.Minute, "fixture", func(context.Context, string) (listener, error) {
					proofCalls.Add(1)
					return listener{}, identity.ErrNoBinding
				}, enginehttp.NewClient, platform.goos, fallback)
				if proofCalls.Load() != 0 {
					t.Fatal("unsupported capability attempted or inferred proof")
				}
				if status == http.StatusOK {
					if len(got) != 1 || got[0].Presence != "unverified" || got[0].Program != "" {
						t.Fatal(got)
					}
				} else if len(got) != 0 {
					t.Fatal("failed unsupported server promoted", got)
				}
			})
		}
	}
}

func TestObserverUnsupportedPathNeverCatchesMalformedForeignOrPeerFailure(t *testing.T) {
	var requests atomic.Int64
	fallback := &http.Client{Transport: observerRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}
	for _, base := range []string{"bad endpoint", "http://127.0.0.1:0", "http://127.0.0.1:1234?query", "http://user@127.0.0.1:1234", "http://192.0.2.4:1234", "http://[2001:db8::1]:1234"} {
		got, _ := observeForPlatform(context.Background(), []*router.Host{router.LMStudio(base)}, nil, time.Minute, "fixture", func(context.Context, string) (listener, error) { return listener{}, identity.ErrNoBinding }, enginehttp.NewClient, "darwin", fallback)
		if len(got) != 0 {
			t.Fatal("invalid or foreign endpoint promoted", base, got)
		}
	}
	for _, goos := range []string{"windows", "linux"} {
		for _, refusal := range []error{identity.ErrNoBinding, identity.ErrPeerMoved, enginehttp.ErrEnginePeer, context.DeadlineExceeded} {
			got, _ := observeForPlatform(context.Background(), []*router.Host{router.LMStudio("http://127.0.0.1:1234")}, nil, time.Minute, "fixture", func(context.Context, string) (listener, error) { return listener{}, refusal }, enginehttp.NewClient, goos, fallback)
			if len(got) != 0 {
				t.Fatal("supported proof refusal fell back", goos, refusal, got)
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatal("refused observation reached fallback HTTP", requests.Load())
	}
}

func TestObserverRequiresBothProgramPresenceAndRespondingServer(t *testing.T) {
	var failed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failed.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	host := router.LMStudio(server.URL)
	present := listener{program: "fixture-program", account: "fixture-account", process: identity.Process{PID: 7, StartTime: time.Unix(7, 0)}}
	for _, mode := range []string{"present", "missing", "other-account", "changed-process", "changed-program", "down"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			inspect := func(context.Context, string) (listener, error) {
				call := calls.Add(1)
				out := present
				switch mode {
				case "missing":
					return listener{}, identity.ErrNoBinding
				case "other-account":
					out.account = "other"
				case "changed-process":
					if call == 2 {
						out.process.StartTime = out.process.StartTime.Add(time.Second)
					}
				case "changed-program":
					if call == 2 {
						out.program = "replacement"
					}
				}
				return out, nil
			}
			snapshot := func() ([]router.HostState, time.Time) {
				return []router.HostState{{Host: host.Name, Base: host.Base, Up: mode != "down"}}, time.Now()
			}
			got, _ := observe(context.Background(), []*router.Host{host}, snapshot, 30*time.Second, present.account, inspect, fixtureClient)
			if mode == "present" {
				if len(got) != 1 || got[0].Program != present.program || got[0].Presence != "verified" || calls.Load() != 2 {
					t.Fatal(got, calls.Load())
				}
			} else if len(got) != 0 {
				t.Fatal("unsupported presence or failed server was promoted", mode, got)
			}
		})
	}
	failed.Store(true)
	up := func() ([]router.HostState, time.Time) {
		return []router.HostState{{Host: host.Name, Base: host.Base, Up: true}}, time.Now()
	}
	if got, _ := observe(context.Background(), []*router.Host{host}, up, time.Minute, present.account, func(context.Context, string) (listener, error) { return present, nil }, fixtureClient); len(got) != 0 {
		t.Fatal("cached Up bypassed the current server protocol", got)
	}
	server.Close()
	if got, _ := observe(context.Background(), []*router.Host{host}, nil, time.Minute, present.account, func(context.Context, string) (listener, error) { return present, nil }, fixtureClient); len(got) != 0 {
		t.Fatal("program evidence alone promoted a stopped server", got)
	}
}

func TestObserverKeepsFastServerAndOriginalSurveyAge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
	defer server.Close()
	fast := router.LMStudio(server.URL)
	slow := router.Ollama("http://127.0.0.1:1")
	present := listener{program: "fixture-program", account: "fixture-account", process: identity.Process{PID: 7, StartTime: time.Unix(7, 0)}}
	inspect := func(ctx context.Context, base string) (listener, error) {
		if base == slow.Base {
			<-ctx.Done()
			return listener{}, ctx.Err()
		}
		return present, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	got, _ := observe(ctx, []*router.Host{slow, fast}, nil, time.Minute, present.account, inspect, fixtureClient)
	if len(got) != 1 || got[0].Host != fast {
		t.Fatal("slow presence discarded fast server", got)
	}
	at := time.Now().Add(-10 * time.Second)
	snapshot := func() ([]router.HostState, time.Time) {
		return []router.HostState{{Host: fast.Name, Base: fast.Base, Up: true}}, at
	}
	got, observed := observe(context.Background(), []*router.Host{fast}, snapshot, 30*time.Second, present.account, inspect, fixtureClient)
	if len(got) != 1 || !observed.After(at) {
		t.Fatal(got, observed, at)
	}
	negative := func() ([]router.HostState, time.Time) {
		return []router.HostState{{Host: fast.Name, Base: fast.Base}}, at
	}
	got, observed = observe(context.Background(), []*router.Host{fast}, negative, 30*time.Second, present.account, inspect, fixtureClient)
	if len(got) != 0 || !observed.Equal(at) {
		t.Fatal("negative reading's original age was extended", got, observed, at)
	}
	if got, _ := observe(context.Background(), []*router.Host{fast}, snapshot, time.Minute, present.account, func(context.Context, string) (listener, error) { return listener{}, errors.New("listener disappeared") }, fixtureClient); len(got) != 0 {
		t.Fatal("cached Up bypassed program presence", got)
	}
}
