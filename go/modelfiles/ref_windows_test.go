//go:build windows

package modelfiles

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	storage "github.com/openabstractions/abstraction-storage/go"
)

// folderStore is a storage store over one folder whose files are named by
// their hex digest: Find answers only present files; Place reserves a name
// and writes nothing, as storage Store.Place does.
type folderStore struct {
	name, dir string
	local     bool
}

func (f folderStore) Name() string { return f.name }
func (f folderStore) Find(digest string) (storage.Ref, bool) {
	h := strings.TrimPrefix(digest, "sha256:")
	st, err := os.Stat(filepath.Join(f.dir, h))
	if err != nil {
		return storage.Ref{}, false
	}
	return storage.NewRef(f.name, digest, h, st.Size()), true
}
func (f folderStore) Place(digest string, size int64) (storage.Ref, error) {
	return storage.NewRef(f.name, digest, strings.TrimPrefix(digest, "sha256:"), size), nil
}

type localFolderStore struct{ folderStore }

func (f localFolderStore) Path(r storage.Ref) string { return filepath.Join(f.dir, storage.Locator(r)) }

func storeWith(t *testing.T, data []byte) (localFolderStore, storage.Ref) {
	t.Helper()
	st := localFolderStore{folderStore{name: "suite", dir: t.TempDir()}}
	if err := os.WriteFile(filepath.Join(st.dir, strings.TrimPrefix(digestOf(data), "sha256:")), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, ok := st.Find(digestOf(data))
	if !ok {
		t.Fatal("the store does not find its object")
	}
	return st, ref
}

// INF-MS9: a located storage reference fills a section with its bytes; the
// first fill through span records records the spans and a later fill is
// proved by them.
func TestFillRefFromALocatedReference(t *testing.T) {
	data := payload()
	st, ref := storeWith(t, data)
	r, err := OpenSpanRecords(filepath.Join(t.TempDir(), "records"))
	if err != nil {
		t.Fatal(err)
	}
	var c counter
	s, err := FillRef(context.Background(), r, st, ref, c.release)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mapped(t, s), data) || s.Digest() != ref.Digest || s.Length() != int64(len(data)) {
		t.Fatal("the section does not hold the reference's bytes")
	}
	rec, ok := r.Lookup(ref.Digest)
	if !ok || !slices.Equal(rec.Spans, s.ChunkDigests()) {
		t.Fatalf("the first fill recorded %v (%v)", rec.Spans, ok)
	}
	s.Close()
	waitFreed(t, s, &c)

	// A changed file under a recorded digest is refused by the record alone.
	changed := bytes.Clone(data)
	changed[len(changed)-1] ^= 1
	if err := os.WriteFile(filepath.Join(st.dir, strings.TrimPrefix(ref.Digest, "sha256:")), changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FillRef(context.Background(), r, st, ref, nil); !errors.Is(err, ErrDigest) {
		t.Fatalf("a changed object filled: %v", err)
	}
	// Without records the whole digest refuses it too.
	if _, err := FillRef(context.Background(), nil, st, ref, nil); !errors.Is(err, ErrDigest) {
		t.Fatalf("a changed object filled without records: %v", err)
	}
}

// INF-MS9: a reference a store placed but does not hold is requested bytes;
// a reference from another store, and a store with no binding on this
// machine, fill nothing. Each refusal releases the holding once.
func TestFillRefRefusesWhatIsNotLocatedHere(t *testing.T) {
	data := payload()
	st, ref := storeWith(t, data)
	placed, err := st.Place(digestOf([]byte("not yet here")), 12)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		store storage.Store
		ref   storage.Ref
		want  error
	}{
		"requested":     {st, placed, ErrRequested},
		"another store": {st, storage.NewRef("other", ref.Digest, "x", 0), ErrSource},
		"no binding":    {st.folderStore, ref, ErrSource},
	} {
		var c counter
		_, err := FillRef(context.Background(), nil, tc.store, tc.ref, c.release)
		if !errors.Is(err, tc.want) || c.n.Load() != 1 {
			t.Errorf("%s: %v, released %d times", name, err, c.n.Load())
		}
	}
}
