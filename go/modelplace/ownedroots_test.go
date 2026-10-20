package modelplace

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func dataEnv(base string) func(string) string {
	return func(name string) string {
		switch name {
		case "LOCALAPPDATA", "XDG_DATA_HOME", "HOME":
			return base
		}
		return ""
	}
}

// OA's own roots are the runtime's job store, resolved in one place; a
// --job-store flag replaces it and refuses an empty or relative folder.
func TestOwnedRootsNameTheJobStoreOnce(t *testing.T) {
	base := t.TempDir()
	getenv := dataEnv(base)
	if got := OwnedRoots(getenv); !slices.Equal(got, []string{DefaultJobStore(getenv)}) || got[0] == "" {
		t.Fatalf("owned roots %q, want the runtime's job store", got)
	}
	if got := OwnedRoots(func(string) string { return "" }); len(got) != 0 {
		t.Fatalf("an environment without a data folder names roots %q", got)
	}
	state, err := RuntimeStateFor("windows", getenv, func() (string, error) { return base, nil })
	if err != nil || JobStoreIn(state) != defaultJobStoreFor("windows", getenv) {
		t.Fatalf("runtime state %q (%v) holds no default job store %q", state, err, defaultJobStoreFor("windows", getenv))
	}

	parse := func(args ...string) (*JobStoreFlag, error) {
		var f JobStoreFlag
		fs := flag.NewFlagSet("models", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Var(&f, "job-store", "")
		return &f, fs.Parse(args)
	}
	if f, err := parse(); err != nil || !slices.Equal(f.OwnedRoots(getenv), OwnedRoots(getenv)) {
		t.Fatalf("absent flag: roots %q, %v", f.OwnedRoots(getenv), err)
	}
	other := filepath.Join(base, "isolated", "jobs")
	if f, err := parse("--job-store", other+string(filepath.Separator)); err != nil || !slices.Equal(f.OwnedRoots(getenv), []string{other}) {
		t.Fatalf("named job store: roots %q, %v", f.OwnedRoots(getenv), err)
	}
	for _, bad := range []string{"", "relative"} {
		if _, err := parse("--job-store", bad); err == nil {
			t.Fatalf("--job-store %q was accepted", bad)
		}
	}
}

// A set folder the job service staged with LinkResult, whose files' other
// names lie in the job store, is adopted with OwnedRoots; an extra name
// outside them, such as a link under Setup's state, refuses it unchanged.
func TestOwnedRootsAdmitTheJobStoresLinkAndNothingElse(t *testing.T) {
	for _, extra := range []bool{false, true} {
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		getenv := dataEnv(base)
		result := filepath.Join(DefaultJobStore(getenv), "results", "1794220690054-ed8aa54c0f9685d2e385")
		root := filepath.Join(base, "OA", "m")
		dir := filepath.Join(root, "set")
		staged := filepath.Join(dir, "added_tokens.json")
		setup := filepath.Join(base, "OA", "setup")
		for _, d := range []string{filepath.Dir(result), dir, setup} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		// The set folder starts writable by others, so an unchanged refusal
		// leaves it no model folder on Unix as well as on Windows.
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(result, []byte(`{"<think>": 151667}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(result, staged); err != nil {
			t.Skipf("this volume makes no hard links: %v", err)
		}
		if extra {
			if err := os.Link(result, filepath.Join(setup, "queued")); err != nil {
				t.Fatal(err)
			}
		}
		files := []File{{Name: "added_tokens.json"}}
		found := map[string]string{"added_tokens.json": staged}
		if err := AdoptSetFolders(root, nil, files, found, nil); !errors.Is(err, privatedir.ErrNotModelFolder) {
			t.Fatalf("extra=%v: without OA's roots the job store's name was admitted: %v", extra, err)
		}
		err = AdoptSetFolders(root, OwnedRoots(getenv), files, found, nil)
		if extra {
			if !errors.Is(err, privatedir.ErrNotModelFolder) || privatedir.CheckModelFolder(dir) == nil {
				t.Fatalf("a name under Setup's state was admitted: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("the job store's name refused the staged folder: %v", err)
		}
		if err := privatedir.CheckModelFolder(dir); err != nil {
			t.Fatalf("the staged folder is not a model folder: %v", err)
		}
	}
}
