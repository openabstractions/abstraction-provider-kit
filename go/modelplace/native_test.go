package modelplace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSetFolderEntryAndMissingParents(t *testing.T) {
	root := t.TempDir()
	set := filepath.Join(root, "set")
	if err := os.Mkdir(set, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(set, "weights")
	if err := os.WriteFile(file, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(set, "hardlink")
	if err := os.Link(file, alias); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, alias, filepath.Join(set, "missing", "weights"), filepath.Join(root, "new", "missing", "weights")} {
		want := set
		if p == filepath.Join(root, "new", "missing", "weights") {
			want = filepath.Join(root, "new")
		}
		if got := SetFolderOf(root, p); got != want {
			t.Errorf("SetFolderOf(%q)=%q want %q", p, got, want)
		}
	}
	if got := SetFolderOf(root, root); got != "" {
		t.Fatalf("root itself selected %q", got)
	}
	if ok, err := AdoptModelFolder(root, filepath.Join(root, "absent"), nil); ok || err != nil {
		t.Fatalf("missing comparison: %v %v", ok, err)
	}
	if ok, err := AdoptModelFolder(set, file, nil); ok || err == nil {
		t.Fatalf("selected ordinary file must reach protection refusal: %v %v", ok, err)
	}
}
