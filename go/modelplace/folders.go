package modelplace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	config "github.com/openabstractions/abstraction-config/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// Searches are the --from folders, then every store files reads when there
// is one: the order a models install searches in (KIT-M1).
func Searches(from []string, files StoreFiles) ([]Search, error) {
	searches := make([]Search, 0, len(from)+1)
	for _, dir := range from {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		searches = append(searches, FolderSearch(abs))
	}
	if files != nil {
		searches = append(searches, StoreSearch{Files: files})
	}
	if len(searches) == 0 {
		return nil, errors.New("no folder or store to search; pass --from DIR or --home DIR")
	}
	return searches, nil
}

// AdoptSetFolders makes each folder directly under root that holds a file
// found for files a model folder of this account, once per folder, and
// names each through adopted; no other folder's access changes (KIT-M3,
// identity ID-D3). An empty root adopts nothing.
func AdoptSetFolders(root string, linkRoots []string, files []File, found map[string]string, adopted func(dir string)) error {
	if root == "" {
		return nil
	}
	heldRoot, err := fileid.Open(context.Background(), fileid.Self, root, fileid.Options{})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer heldRoot.Close()
	done := map[string]bool{}
	for _, f := range files {
		dir := SetFolderOf(root, found[f.Name])
		if dir == "" {
			continue
		}
		key, err := modelFolderKey(dir)
		if err != nil {
			return err
		}
		if done[key] {
			continue
		}
		done[key] = true
		ok, err := adoptModelFolder(root, dir, linkRoots, heldRoot)
		if err != nil {
			return err
		}
		if ok && adopted != nil {
			adopted(dir)
		}
	}
	return nil
}

// ProtectFolders makes each recorded set folder under root a model folder of
// this account (identity ID-D3), once, for folders made before OA protected
// them, and lists every other folder under root as left alone. A root
// CheckModelsRoot refuses changes nothing; with dryRun nothing is written
// and each recorded folder is reported as it stands. It returns how many
// recorded folders were refused (KIT-M3).
func ProtectFolders(root string, recorded, linkRoots []string, dryRun bool, out io.Writer) (int, error) {
	if !filepath.IsAbs(root) {
		return 0, fmt.Errorf("%w: models root must be absolute", privatedir.ErrNotModelFolder)
	}
	heldRoot, err := fileid.Open(context.Background(), fileid.Self, root, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", privatedir.ErrNotModelFolder, err)
	}
	defer heldRoot.Close()
	if err := privatedir.CheckModelsRoot(root); err != nil {
		return 0, err
	}
	isRecorded := map[string]bool{}
	for _, dir := range recorded {
		key, err := modelFolderKey(dir)
		if err != nil {
			return 0, err
		}
		isRecorded[key] = true
	}
	entries, err := heldRoot.File().ReadDir(-1)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		key, err := modelFolderKey(dir)
		if err != nil {
			return 0, err
		}
		if !isRecorded[key] {
			//unchecked: CLI output; the caller's exit code reports the run
			fmt.Fprintf(out, "%s: not a recorded set; left as it is\n", dir)
		}
	}
	failed := 0
	for _, dir := range recorded {
		if dryRun {
			state := "would be protected"
			if privatedir.CheckModelFolder(dir) == nil {
				state = "already a model folder; protecting would rewrite the entries below it"
			}
			//unchecked: CLI output; the caller's exit code reports the run
			fmt.Fprintf(out, "%s: %s\n", dir, state)
			continue
		}
		ok, err := adoptModelFolder(root, dir, linkRoots, heldRoot)
		if err != nil || !ok {
			failed++
			if err == nil {
				err = fmt.Errorf("%w: not a set folder directly under the models root", privatedir.ErrNotModelFolder)
			}
			//unchecked: CLI output; the caller's exit code reports the run
			fmt.Fprintf(out, "%s: refused: %v\n", dir, err)
			continue
		}
		//unchecked: CLI output; the caller's exit code reports the run
		fmt.Fprintf(out, "%s: protected\n", dir)
	}
	return failed, nil
}

// Adoption deduplication is request-local and uses native identity. The
// shared candidate placeKey retains its separate missing-file semantics.
func modelFolderKey(p string) (string, error) {
	if err := fileid.OlderPath(p); err != nil {
		return "", err
	}
	id, err := fileid.Resolve(context.Background(), fileid.Self, p, fileid.Options{})
	if errors.Is(err, os.ErrNotExist) {
		pending, pendingErr := fileid.Pending(context.Background(), fileid.Self, p)
		if pendingErr != nil {
			return "", pendingErr
		}
		return pending.Key(), nil
	}
	if err != nil {
		return "", err
	}
	return id.Key(), nil
}

// RuntimeStateFor is the runtime's per-user state folder: runtime-v1 under
// config.DataDirFor. The runtime keeps its managed job store in it
// (JobStoreIn).
func RuntimeStateFor(platform string, getenv func(string) string, home func() (string, error)) (string, error) {
	base, err := config.DataDirFor(platform, getenv, home)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "runtime-v1"), nil
}

// JobStoreIn is the managed job store of the runtime whose state folder is
// state.
func JobStoreIn(state string) string { return filepath.Join(state, "jobs") }

// DefaultJobStore is the runtime's managed job store, OA's other folder a
// model file may share a name with (LinkResult, JOB-A17): JobStoreIn the
// per-user RuntimeStateFor. It is "" when this environment names no data
// folder.
func DefaultJobStore(getenv func(string) string) string {
	return defaultJobStoreFor(runtime.GOOS, getenv)
}

func defaultJobStoreFor(platform string, getenv func(string) string) string {
	// Retain the existing XDG mapping for every non-Windows/macOS provider.
	if platform != "windows" && platform != "darwin" {
		platform = "linux"
	}
	state, err := RuntimeStateFor(platform, getenv, func() (string, error) {
		return getenv("HOME"), nil
	})
	if err != nil {
		return ""
	}
	return JobStoreIn(state)
}

// OwnedRoots are OA's own folders, beside the models root a set folder lies
// under, where the job service creates another name of a model file: the
// runtime's job store, whose results LinkResult hard-links into a stage
// (JOB-A17). They are the linkRoots every AdoptSetFolders and ProtectFolders
// caller passes; the models root itself is always admitted. A folder this
// environment does not resolve is left out, so a staged file's job-store
// name is then refused (KIT-M3).
func OwnedRoots(getenv func(string) string) []string {
	return ownedRoots(DefaultJobStore(getenv))
}

func ownedRoots(jobStore string) []string {
	if jobStore == "" {
		return nil
	}
	return []string{filepath.Clean(jobStore)}
}

// JobStoreFlag is a models command's --job-store: the job store of a runtime
// that keeps its state elsewhere. Set refuses an empty or relative folder, so
// a command line can never drop the job store from OA's own roots.
type JobStoreFlag struct{ dir string }

func (f *JobStoreFlag) String() string {
	if f == nil {
		return ""
	}
	return f.dir
}

func (f *JobStoreFlag) Set(v string) error {
	if v == "" {
		return errors.New("an empty job store would refuse every file the job service staged; omit the flag for the runtime's own")
	}
	if !filepath.IsAbs(v) {
		return errors.New("the job store must be an absolute folder")
	}
	f.dir = filepath.Clean(v)
	return nil
}

// OwnedRoots are OwnedRoots(getenv) with the job store the flag names in
// place of the runtime's default one.
func (f *JobStoreFlag) OwnedRoots(getenv func(string) string) []string {
	if f == nil || f.dir == "" {
		return OwnedRoots(getenv)
	}
	return ownedRoots(f.dir)
}
