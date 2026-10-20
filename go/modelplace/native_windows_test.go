package modelplace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/placetest"
)

// The observer runs before CreateFile. Abort the old whole-path open before
// it can traverse the owned junction to its inert UNC substitute.
func TestAdoptModelFolderRefusesShareBeforeTargetOpen(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "share")
	if err := placetest.ShareJunction(link); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(link, "absent")
	stop, ok := fileid.ObserveOpens(func(p string) {
		if strings.EqualFold(p, parent) || strings.HasPrefix(strings.ToLower(p), strings.ToLower(parent)+`\`) {
			panic("whole-path open would traverse the share junction")
		}
	})
	if !ok {
		t.Fatal("open observer already in use")
	}
	defer stop()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("unsafe pre-open boundary: %v", p)
		}
	}()
	if adopted, err := AdoptModelFolder(root, filepath.Join(parent, "child"), nil); adopted || err != nil {
		t.Fatalf("refused comparison: adopted=%v error=%v", adopted, err)
	}
}

func TestAdoptModelFolderPreservesStreamAndLocalLinkRefusal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "set")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if adopted, err := AdoptModelFolder(root+"::$DATA", dir, nil); adopted || err != nil {
		t.Fatalf("legacy stream comparison outcome changed: %v %v", adopted, err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := placetest.MountPoint(link, `\??\`+root, root); err != nil {
		t.Fatal(err)
	}
	if adopted, err := AdoptModelFolder(link, filepath.Join(link, "set"), nil); adopted || !errors.Is(err, privatedir.ErrNotModelFolder) {
		t.Fatalf("selected local root link must reach protection refusal: %v %v", adopted, err)
	}
}
