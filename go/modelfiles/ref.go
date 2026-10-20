package modelfiles

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"

	storage "github.com/openabstractions/abstraction-storage/go"
)

// ErrRequested: the reference names bytes its store does not hold yet. A
// placed reference (storage Store.Place) reserves where requested bytes will
// land, and a download request or an inventory want (content.thrift
// Dangling) is a promise of them. A fill reads located bytes; a fill from a
// promise, reading the request's verified stream as it arrives, is not built
// (INF-MS9).
var ErrRequested = errors.New("modelfiles: the bytes are requested, not located")

// Located projects ref, a storage reference (storage.thrift Ref), onto the
// extent a fill reads (INF-MS9). The store must be the one that issued ref
// and must find ref's digest now: a reference it does not find is requested
// bytes, ErrRequested. The store's local binding (storage Local) names the
// file on this machine; a store without one is placed elsewhere, and its
// bytes are filled by the runtime that holds them (bindings never cross
// machines). The extent is the whole file, a STORED extent from offset zero;
// a provider whose own resolve seam projects a reference to a region or a
// compressed member passes that extent to Fill itself.
func Located(store storage.Store, ref storage.Ref) (Extent, error) {
	if store == nil || store.Name() != ref.Store {
		return Extent{}, fmt.Errorf("%w: the reference was issued by store %q", ErrSource, ref.Store)
	}
	if err := CheckDigest(ref.Digest); err != nil {
		return Extent{}, err
	}
	found, ok := store.Find(ref.Digest)
	if !ok {
		return Extent{}, fmt.Errorf("%w: store %s holds no %s", ErrRequested, ref.Store, ref.Digest)
	}
	local, ok := store.(storage.Local)
	if !ok {
		return Extent{}, fmt.Errorf("%w: store %s has no binding on this machine", ErrSource, ref.Store)
	}
	path := local.Path(found)
	if path == "" {
		return Extent{}, fmt.Errorf("%w: store %s projects no path for %s", ErrSource, ref.Store, ref.Digest)
	}
	size := found.Size
	if size <= 0 {
		size = ref.Size
	}
	if size <= 0 {
		st, err := os.Stat(path)
		if err != nil {
			return Extent{}, fmt.Errorf("%w: %v", ErrSource, err)
		}
		size = st.Size()
	}
	return Extent{Path: path, Packed: size, Length: size, Method: zip.Store}, nil
}

// FillRef places the bytes ref names in a new section, proved against
// ref's digest and sealed, as Fill does (INF-MS9). records, when not nil,
// proves a later fill of the same object by its span record.
func FillRef(ctx context.Context, records *SpanRecords, store storage.Store, ref storage.Ref, release func()) (*Section, error) {
	src, err := Located(store, ref)
	if err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	return records.Fill(ctx, src, ref.Digest, release)
}
