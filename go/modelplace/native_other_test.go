//go:build !windows

package modelplace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestNativeSetFolderWalksLinkBeforeDotDot(t *testing.T) {
	root := t.TempDir()
	set := filepath.Join(root, "physical")
	if err := os.MkdirAll(filepath.Join(set, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(filepath.Join(set, "nested"), link); err != nil {
		t.Fatal(err)
	}
	p := link + "/../weights"
	if got := SetFolderOf(root, p); got != set {
		t.Fatalf("physical walk selected %q, want %q", got, set)
	}
}

func TestAdoptModelFolderKeepsAdmittedRootAcrossProtection(t *testing.T) {
	base := t.TempDir()
	root, replacement := filepath.Join(base, "root"), filepath.Join(base, "replacement")
	sentinel := filepath.Join(base, "sentinel")
	for _, p := range []string{filepath.Join(root, "set"), filepath.Join(replacement, "set"), sentinel} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	replacementFile := filepath.Join(replacement, "set", "unchanged")
	if err := os.WriteFile(replacementFile, []byte("owned replacement fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(replacementFile, 0o666); err != nil {
		t.Fatal(err)
	}
	swapped := false
	stop, ok := fileid.ObserveOpens(func(p string) {
		if swapped || p != sentinel {
			return
		}
		swapped = true
		if err := os.Rename(root, filepath.Join(base, "retired")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, root); err != nil {
			t.Fatal(err)
		}
	})
	if !ok {
		t.Fatal("open observer already in use")
	}
	defer stop()
	adopted, err := AdoptModelFolder(root, filepath.Join(root, "set"), []string{sentinel})
	if !swapped {
		t.Fatal("owned swap control did not execute")
	}
	if adopted || err == nil {
		t.Fatalf("replacement root reached protection: adopted=%v error=%v", adopted, err)
	}
	st, err := os.Stat(filepath.Join(root, "set", "unchanged"))
	if err != nil || st.Mode().Perm() != 0o666 {
		t.Fatalf("replacement fixture changed: stat=%v error=%v", st, err)
	}
}
