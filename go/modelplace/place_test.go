package modelplace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pinned(t *testing.T, dir, name, role string, data []byte) File {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return File{Role: role, Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

// KIT-M1, KIT-M2: a file is found where it lies, proved by its digest and
// recorded with its owners; the record reads back, and one naming an
// unpinned file, or missing one, is refused with the remedy named.
func TestPlacedFilesAreProvedAndRecorded(t *testing.T) {
	// Placing refuses a path through a link (INF-F7), and the temporary
	// folder may lie below one (macOS /tmp and /var).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := pinned(t, dir, "a.safetensors", "main", []byte("weights"))
	b := pinned(t, dir, "b.json", "tokenizer", []byte("{}"))
	files := []File{a, b}
	if err := ValidFiles("set", files); err != nil {
		t.Fatal(err)
	}
	places := filepath.Join(t.TempDir(), "set.places")
	found, err := Place(context.Background(), files, []Search{FolderSearch(dir)}, places)
	if err != nil || found[a.Name] != filepath.Join(dir, a.Name) {
		t.Fatalf("place %v %v", found, err)
	}
	placed, err := Placed(places, "set", files, "the remedy")
	if err != nil || len(placed) != 2 || placed[1].SHA256 != b.SHA256 || !placed[0].Owners.Account {
		t.Fatalf("placed %+v %v", placed, err)
	}
	wrong := a
	wrong.SHA256 = strings.Repeat("0", 64)
	if _, err := Place(context.Background(), []File{wrong}, []Search{FolderSearch(dir)}, places); err == nil {
		t.Fatal("a file of another digest was placed")
	}
	if _, _, err := Read(places, "set", []File{a}, "the remedy"); err == nil {
		t.Fatal("a record naming an unpinned file was read")
	}
	_, _, err = Read(filepath.Join(dir, "absent.places"), "set", files, "the remedy")
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "the remedy") {
		t.Fatalf("absent record: %v", err)
	}
	if SetFolderOf(dir, filepath.Join(dir, "x", "y")) != filepath.Join(dir, "x") || SetFolderOf(dir, dir) != "" {
		t.Fatal("SetFolderOf")
	}
}
