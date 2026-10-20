package modelplace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

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
	done := map[string]bool{}
	for _, f := range files {
		dir := SetFolderOf(root, found[f.Name])
		if dir == "" || done[placeKey(dir)] {
			continue
		}
		done[placeKey(dir)] = true
		if _, err := AdoptModelFolder(root, dir, linkRoots); err != nil {
			return err
		}
		if adopted != nil {
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
	root = filepath.Clean(root)
	if err := privatedir.CheckModelsRoot(root); err != nil {
		return 0, err
	}
	isRecorded := map[string]bool{}
	for _, dir := range recorded {
		isRecorded[placeKey(dir)] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if !isRecorded[placeKey(dir)] {
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
		if _, err := AdoptModelFolder(root, dir, linkRoots); err != nil {
			failed++
			//unchecked: CLI output; the caller's exit code reports the run
			fmt.Fprintf(out, "%s: refused: %v\n", dir, err)
			continue
		}
		//unchecked: CLI output; the caller's exit code reports the run
		fmt.Fprintf(out, "%s: protected\n", dir)
	}
	return failed, nil
}

// DefaultJobStore is the runtime's managed job store, OA's other folder a
// model file may share a name with (LinkResult, JOB-A17): runtime-v1\jobs
// under the per-user data folder, as the runtime's managedJobRoot names it.
func DefaultJobStore(getenv func(string) string) string {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = getenv("LOCALAPPDATA")
	case "darwin":
		if h := getenv("HOME"); h != "" {
			base = filepath.Join(h, "Library", "Application Support")
		}
	default:
		base = getenv("XDG_DATA_HOME")
		if base == "" {
			if h := getenv("HOME"); h != "" {
				base = filepath.Join(h, ".local", "share")
			}
		}
	}
	if base == "" || !filepath.IsAbs(base) {
		return ""
	}
	return filepath.Join(base, "openabstractions", "runtime-v1", "jobs")
}
