package modelplace

// Characterization (CQ-15a) of the default job store's per-system folder.
// It lives in its own file so the folder tests' path resolution stays out of
// a file that names a system.

import (
	"path/filepath"
	"runtime"
	"testing"

	config "github.com/openabstractions/abstraction-config/go"
)

func TestDefaultJobStoreUsesTheCanonicalDataFolder(t *testing.T) {
	home, local, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	for _, platform := range []string{"windows", "darwin", "linux", "freebsd"} {
		for _, values := range []map[string]string{
			{}, {"HOME": home, "LOCALAPPDATA": local},
			{"HOME": home, "LOCALAPPDATA": local, "XDG_DATA_HOME": xdg},
			{"HOME": "relative", "LOCALAPPDATA": "relative", "XDG_DATA_HOME": "relative"},
		} {
			getenv := func(key string) string { return values[key] }
			canonicalPlatform := platform
			if platform == "freebsd" {
				canonicalPlatform = "linux"
			}
			data, err := config.DataDirFor(canonicalPlatform, getenv, func() (string, error) { return values["HOME"], nil })
			want := ""
			if err == nil {
				want = filepath.Join(data, "runtime-v1", "jobs")
			}
			if got := defaultJobStoreFor(platform, getenv); got != want {
				t.Fatalf("%s: default store %q; want %q", platform, got, want)
			}
		}
	}
}

func TestCharacterizeDefaultJobStore(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if DefaultJobStore(env(nil)) != "" {
		t.Fatal("no environment gives a store")
	}
	var base string
	var vars map[string]string
	switch runtime.GOOS {
	case "windows":
		base = `C:\Users\p\AppData\Local`
		vars = map[string]string{"LOCALAPPDATA": base}
	case "darwin":
		vars = map[string]string{"HOME": "/Users/p"}
		base = "/Users/p/Library/Application Support"
	default:
		vars = map[string]string{"HOME": "/home/p"}
		base = "/home/p/.local/share"
		if got := DefaultJobStore(env(map[string]string{"XDG_DATA_HOME": "/x"})); got != filepath.Join("/x", "openabstractions", "runtime-v1", "jobs") {
			t.Fatalf("XDG: %q", got)
		}
		if DefaultJobStore(env(map[string]string{"XDG_DATA_HOME": "rel"})) != "" {
			t.Fatal("a relative XDG_DATA_HOME gives a store")
		}
	}
	if got := DefaultJobStore(env(vars)); got != filepath.Join(base, "openabstractions", "runtime-v1", "jobs") {
		t.Fatalf("job store %q", got)
	}
}
