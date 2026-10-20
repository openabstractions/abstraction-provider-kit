package providerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	contentdigest "github.com/openabstractions/abstraction-storage/go/digest"
	storageService "github.com/openabstractions/abstraction-storage/go/service"
)

// Output retention (KIT-V5). A generated output the provider published
// through OutputWriter is a delivery copy: the runtime reads it once and keeps
// its own. With RetainOutputs the provider records each publication and
// removes the copy when the retention, at least 24 hours, has passed since the
// last one. A copy an authorized call named within a reader's idle lifetime
// is still being read and stays. Inputs the runtime staged and results other
// owners wrote into the same store are never in the record and are never
// removed here.

// MinimumOutputRetention is the least time a generated output stays after its
// last publication: an ended operation's retention (KIT-V5).
const MinimumOutputRetention = 24 * time.Hour

// outputRecordLimit bounds the record file.
const outputRecordLimit = 4 << 20

// outputSweepLimit bounds the copies one pass removes; the rest wait for the
// next pass.
const outputSweepLimit = 1024

// contentAccess is the last authorized content call naming each digest. It
// records nothing until output retention is on.
type contentAccess struct {
	mu      sync.Mutex
	on      bool
	now     func() time.Time
	touched map[string]time.Time
}

func (a *contentAccess) touch(digest string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.on {
		a.touched[digest] = a.now()
	}
}

// reading reports whether a call named digest within a reader's idle
// lifetime, and forgets calls older than that.
func (a *contentAccess) reading(digest string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for d, at := range a.touched {
		if now.Sub(at) >= storageService.IdleLifetime {
			delete(a.touched, d)
		}
	}
	_, ok := a.touched[digest]
	return ok
}

type outputRecord struct {
	mu        sync.Mutex
	path      string
	retention time.Duration
	now       func() time.Time
	report    func(error)
}

// outputIndex is the record file: each output's digest and its last
// publication, RFC 3339 in UTC.
type outputIndex struct {
	Version int               `json:"version"`
	Outputs map[string]string `json:"outputs"`
}

func decodeOutputIndex(raw []byte) (outputIndex, error) {
	if len(raw) == 0 {
		return outputIndex{Version: 1, Outputs: map[string]string{}}, nil
	}
	var index outputIndex
	if err := json.Unmarshal(raw, &index); err != nil || index.Version != 1 {
		return outputIndex{}, errors.New("providerhost: invalid output record")
	}
	if index.Outputs == nil {
		index.Outputs = map[string]string{}
	}
	for digest, at := range index.Outputs {
		if _, err := time.Parse(time.RFC3339Nano, at); err != nil || !contentdigest.Valid(digest) {
			return outputIndex{}, errors.New("providerhost: invalid output record")
		}
	}
	return index, nil
}

// RetainOutputs turns on output retention for this store: every later
// publication through OutputWriter is recorded at recordPath, and
// SweepOutputs removes a copy retention after its last publication. A
// retention under MinimumOutputRetention reads the minimum. now is the clock;
// nil reads time.Now. report receives a damaged or unwritable record. Call it
// before the first publication.
func (c *DesignatedContent) RetainOutputs(recordPath string, retention time.Duration, now func() time.Time, report func(error)) error {
	if c == nil || c.access == nil || !filepath.IsAbs(recordPath) {
		return errors.New("providerhost: designated content and an absolute output record path are required")
	}
	if now == nil {
		now = time.Now
	}
	c.access.mu.Lock()
	c.access.on, c.access.now, c.access.touched = true, now, map[string]time.Time{}
	c.access.mu.Unlock()
	c.outputs = &outputRecord{path: recordPath, retention: max(retention, MinimumOutputRetention), now: now, report: report}
	return nil
}

// recordOutput records one publication of digest; without retention it
// records nothing. A publication never fails for its record: a damaged record
// is reported and replaced by a fresh one, and a record that cannot be written
// is reported and leaves this copy kept.
func (c *DesignatedContent) recordOutput(digest string) {
	r := c.outputs
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	err := cas.ChangeLimit(r.path, outputRecordLimit, func(raw []byte) ([]byte, error) {
		index, err := decodeOutputIndex(raw)
		if err != nil {
			r.reportErr(fmt.Errorf("providerhost: output record replaced: %w", err))
			index = outputIndex{Version: 1, Outputs: map[string]string{}}
		}
		index.Outputs[digest] = r.now().UTC().Format(time.RFC3339Nano)
		return json.Marshal(index)
	})
	if err != nil {
		r.reportErr(fmt.Errorf("providerhost: output %s kept unrecorded: %w", digest, err))
	}
}

func (r *outputRecord) reportErr(err error) {
	if r.report != nil {
		r.report(err)
	}
}

// OutputPass counts one retention pass.
type OutputPass struct {
	Removed, Kept, Reading int
}

// SweepOutputs is one retention pass: it removes each recorded copy whose
// retention has passed since its last publication, unless a call named it
// within a reader's idle lifetime, and drops its record. A copy that is no
// regular file, or that the platform refuses to remove, stays recorded. It
// removes at most outputSweepLimit copies.
func (c *DesignatedContent) SweepOutputs(ctx context.Context) (OutputPass, error) {
	var pass OutputPass
	r := c.outputs
	if r == nil {
		return pass, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var failures []error
	err := cas.ChangeLimitContext(ctx, r.path, outputRecordLimit, func(raw []byte) ([]byte, error) {
		pass, failures = OutputPass{}, nil
		index, err := decodeOutputIndex(raw)
		if err != nil {
			return nil, err
		}
		now := r.now()
		digests := make([]string, 0, len(index.Outputs))
		for digest := range index.Outputs {
			digests = append(digests, digest)
		}
		sort.Strings(digests)
		for _, digest := range digests {
			at, _ := time.Parse(time.RFC3339Nano, index.Outputs[digest])
			switch {
			case now.Sub(at) < r.retention || pass.Removed == outputSweepLimit:
				pass.Kept++
				continue
			case c.access.reading(digest, now):
				pass.Reading++
				continue
			}
			if err := c.removeCopy(digest); err != nil {
				failures = append(failures, fmt.Errorf("providerhost: output %s: %w", digest, err))
				pass.Kept++
				continue
			}
			delete(index.Outputs, digest)
			pass.Removed++
		}
		return json.Marshal(index)
	})
	return pass, errors.Join(append(failures, err)...)
}

// removeCopy removes one stored copy that is a regular file; an absent copy
// is already removed.
func (c *DesignatedContent) removeCopy(digest string) error {
	ref, ok := c.Store.Find(digest)
	if !ok {
		return nil
	}
	info, err := os.Lstat(c.Store.Path(ref))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("stored copy is no regular file")
	}
	return c.Store.Remove(digest)
}

// ServeOutputRetention runs SweepOutputs every interval until ctx ends and
// hands each pass to observe, which writes its one line. The returned
// function waits for the loop to stop.
func (c *DesignatedContent) ServeOutputRetention(ctx context.Context, every time.Duration, observe func(OutputPass, error)) func() {
	done := make(chan struct{})
	if c == nil || c.outputs == nil || every <= 0 {
		close(done)
		return func() {}
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			pass, err := c.SweepOutputs(ctx)
			if observe != nil {
				observe(pass, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { <-done }
}
