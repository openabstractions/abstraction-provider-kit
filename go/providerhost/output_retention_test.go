package providerhost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// retainedContent is a designated store with output retention on, driven by
// a hand clock.
func retainedContent(t *testing.T) (*DesignatedContent, func(time.Duration)) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	content, err := NewDesignatedContent("retention-fixture", filepath.Join(root, "content"), filepath.Join(root, "uploads.json"), []string{exe}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = content.Component.Close() })
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(start.UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()).UTC() }
	if err := content.RetainOutputs(filepath.Join(root, "content-outputs.json"), time.Hour, clock, func(err error) { t.Error(err) }); err != nil {
		t.Fatal(err)
	}
	return content, func(after time.Duration) { now.Store(start.Add(after).UnixNano()) }
}

// store places bytes in the content store as the writer's commit does.
func store(t *testing.T, content *DesignatedContent, data []byte) string {
	t.Helper()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	ref, err := content.Store.Place(digest, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(content.Store.Path(ref), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := content.Store.Commit(ref); err != nil {
		t.Fatal(err)
	}
	return digest
}

// publish records one generated output and stores its bytes, as OutputWriter
// does.
func publish(t *testing.T, content *DesignatedContent, data []byte) string {
	t.Helper()
	content.recordOutput(fmt.Sprintf("sha256:%x", sha256.Sum256(data)))
	return store(t, content, data)
}

func present(content *DesignatedContent, digest string) bool {
	_, ok := content.Store.Find(digest)
	return ok
}

// KIT-V5: the provider's copy of a picture goes 24 hours after its last
// publication; a retention asked below 24 hours reads 24 hours. Bytes the
// record does not name, such as a staged first frame, stay.
func TestProviderPictureCopyGoesAfterItsRetention(t *testing.T) {
	content, advance := retainedContent(t)
	picture := publish(t, content, []byte("\x89PNG\r\n\x1a\nprovider copy"))
	staged := store(t, content, []byte("\x89PNG\r\n\x1a\nstaged first frame"))
	advance(MinimumOutputRetention - time.Minute)
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 0 || pass.Kept != 1 || !present(content, picture) {
		t.Fatalf("within retention: %+v %v", pass, err)
	}
	advance(MinimumOutputRetention + time.Minute)
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 1 || present(content, picture) {
		t.Fatalf("after retention: %+v %v present=%v", pass, err, present(content, picture))
	}
	if !present(content, staged) {
		t.Fatal("a copy the record does not name was removed")
	}
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 0 || pass.Kept != 0 {
		t.Fatalf("record not dropped: %+v %v", pass, err)
	}
}

// KIT-V5: a copy the runtime opened and reads through the store's content
// reader stays; it goes once the reader closed it and is idle.
func TestProviderPictureCopyBeingReadStays(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skip(err)
	}
	content, advance := retainedContent(t)
	data := []byte("\x89PNG\r\n\x1a\nbeing read")
	picture := publish(t, content, data)
	reader := api.NewContentReaderClient(serveContent(t, content))
	advance(MinimumOutputRetention + time.Minute)
	opened, err := reader.Open(picture)
	if err != nil || opened.Outcome != api.OpenOutcomeOpened {
		t.Fatalf("open %+v %v", opened, err)
	}
	if page, err := reader.Read(opened.Resource.Handle, 0, 4); err != nil || page.Outcome != api.ReadOutcomeData {
		t.Fatalf("read %+v %v", page, err)
	}
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Reading != 1 || pass.Removed != 0 || !present(content, picture) {
		t.Fatalf("while read: %+v %v", pass, err)
	}
	if page, err := reader.Read(opened.Resource.Handle, 4, int64(len(data))); err != nil || page.Outcome != api.ReadOutcomeData {
		t.Fatalf("read after the pass %+v %v", page, err)
	}
	if closed, err := reader.Close(opened.Resource.Handle); err != nil || closed.Outcome != api.CloseOutcomeClosed {
		t.Fatalf("close %+v %v", closed, err)
	}
	advance(MinimumOutputRetention + 2*time.Minute)
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 1 || present(content, picture) {
		t.Fatalf("after the reader: %+v %v", pass, err)
	}
}

// retentionEndpoints numbers serveContent's pipes within this test process.
var retentionEndpoints atomic.Int64

// serveContent serves the store's component on a private endpoint for this
// test executable, as a provider's endpoint does, and returns its transport.
func serveContent(t *testing.T, content *DesignatedContent) listen.FrameClient {
	t.Helper()
	var endpoint string
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf(`\\.\pipe\providerhost-retention-%d-%d`, os.Getpid(), retentionEndpoints.Add(1))
	} else {
		endpoint = filepath.Join(privatetest.Dir(t), "content.sock")
	}
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	l = listen.Sessions(l, listen.SessionOptions{MaxSessions: 4})
	ctx, stop := context.WithCancel(context.Background())
	if err := content.Component.Start(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				callCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
				defer cancel()
				call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
				if call != nil {
					defer call.Close()
				}
				if err != nil {
					return
				}
				reply, err := api.ServeEndpoint(call.Frame, "openabstractions", "", content.Component.Services(call, callCtx)...)
				if err == nil {
					_ = call.Reply(reply)
				}
			}()
		}
	}()
	t.Cleanup(func() { stop(); _ = l.Close(); <-done })
	return listen.FrameClient{Endpoint: endpoint, Timeout: 35 * time.Second, MaxFrame: 1 << 20, Sessions: true}
}

// A damaged record never fails a publication: it is reported and replaced,
// and retention goes on from the fresh record.
func TestProviderDamagedOutputRecordIsReplaced(t *testing.T) {
	content, advance := retainedContent(t)
	var reported []error
	content.outputs.report = func(err error) { reported = append(reported, err) }
	if err := os.WriteFile(content.outputs.path, []byte("{damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := content.SweepOutputs(context.Background()); err == nil {
		t.Fatal("a damaged record swept without an error")
	}
	picture := publish(t, content, []byte("\x89PNG\r\n\x1a\nafter damage"))
	if len(reported) != 1 {
		t.Fatalf("reports %v", reported)
	}
	advance(MinimumOutputRetention + time.Minute)
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 1 || present(content, picture) {
		t.Fatalf("after replacement: %+v %v", pass, err)
	}
}

// KIT-V5: a publication of the same bytes again restarts the retention.
func TestProviderPictureCopyRepublishedStays(t *testing.T) {
	content, advance := retainedContent(t)
	data := []byte("\x89PNG\r\n\x1a\nmade twice")
	picture := publish(t, content, data)
	advance(12 * time.Hour)
	publish(t, content, data)
	advance(MinimumOutputRetention + time.Minute)
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 0 || !present(content, picture) {
		t.Fatalf("republished: %+v %v", pass, err)
	}
	advance(12*time.Hour + MinimumOutputRetention + time.Minute)
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass.Removed != 1 {
		t.Fatalf("after the last publication: %+v %v", pass, err)
	}
}

// Without RetainOutputs a provider keeps every output and records nothing.
func TestProviderWithoutOutputRetentionKeepsEveryOutput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	content, err := NewDesignatedContent("keeping-fixture", filepath.Join(root, "content"), filepath.Join(root, "uploads.json"), []string{exe}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = content.Component.Close() })
	picture := publish(t, content, []byte("\x89PNG\r\n\x1a\nkept"))
	if pass, err := content.SweepOutputs(context.Background()); err != nil || pass != (OutputPass{}) || !present(content, picture) {
		t.Fatalf("pass %+v %v", pass, err)
	}
	if matches, err := filepath.Glob(filepath.Join(root, "*outputs*")); err != nil || len(matches) != 0 {
		t.Fatalf("output record written: %v %v", matches, err)
	}
}
