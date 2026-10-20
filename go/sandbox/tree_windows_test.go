package sandbox

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"golang.org/x/sys/windows"
)

// A private parent supplies the legacy inherited entries that a normal
// Python-created development directory also supplies, independent of TEMP.
func privateTreeRig(t *testing.T) *rig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := privatedir.Make(dir); err != nil {
		t.Fatal(err)
	}
	r := &rig{dir: dir, manifest: filepath.Join(dir, "sandbox.json"), engine: filepath.Join(dir, "engine"),
		state: filepath.Join(dir, "state"), outside: filepath.Join(dir, "outside"), profile: testProfile(t)}
	for _, path := range []string{filepath.Join(r.engine, "a.txt"), filepath.Join(r.engine, "sub", "b.txt"),
		filepath.Join(r.state, "cache", "c.txt"), filepath.Join(r.outside, "d.txt")} {
		write(t, path, "inert")
	}
	return r
}

func crashTreeEditChild() {
	s, err := Begin(os.Getenv("OA_TREE_MANIFEST"), os.Getenv("OA_TREE_PROFILE"))
	if err != nil {
		os.Exit(71)
	}
	afterStep = func(at string) error {
		if at == "tree object applied" {
			os.Exit(73) // Native handles close; the durable capture must suffice.
		}
		return nil
	}
	if err := s.Grant(Grant{Path: os.Getenv("OA_TREE_ROOT"), Access: Modify}); err != nil {
		os.Exit(72)
	}
}

func stoppedTree(t *testing.T, r *rig) *manifestDisk {
	t.Helper()
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), "OA_SANDBOX_CHILD=tree-crash", "OA_TREE_MANIFEST="+r.manifest,
		"OA_TREE_PROFILE="+r.profile, "OA_TREE_ROOT="+r.state)
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("stopped parent edit: %v", err)
	}
	disk, err := readManifestDisk(r.manifest)
	if err != nil || disk.Format != 3 || disk.TreeEdit == nil || len(disk.TreeEdit.Objects) != 3 {
		t.Fatalf("crash lost its complete capture: %+v %v", disk, err)
	}
	return disk
}

func TestCapturedTreeSurvivesAProcessExitAfterTheParentWrite(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "foreign-change"}[foreign], func(t *testing.T) {
			r := privateTreeRig(t)
			path := filepath.Join(r.state, "cache")
			before, err := snapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			disk := stoppedTree(t, r)
			if object := disk.TreeEdit.Objects[1]; object.Path != path || object.ID == "" || object.Before != before {
				t.Fatalf("child descriptor was captured after the parent effect: %+v", object)
			}
			want := before
			if foreign {
				sid, err := deriveSID("oa.tree-external")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := rewriteDACL(path, "", func(d string) (string, error) { return withAttributeACE(d, sid.String()) }); err != nil {
					t.Fatal(err)
				}
				want, err = snapshot(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			out, err := Remove(r.manifest, false)
			if err != nil || !out.ProfileDeleted || foreign != slices.Contains(out.Changed, path) {
				t.Fatalf("capture recovery: %+v %v", out, err)
			}
			if actual, err := snapshot(path); err != nil || actual != want {
				t.Fatalf("child after recovery %q (%v), want %q", actual, err, want)
			}
		})
	}
}

func TestCapturedTreeIsDurableBeforeTheFirstEffect(t *testing.T) {
	r := privateTreeRig(t)
	path := filepath.Join(r.state, "cache")
	before, _ := snapshot(path)
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	stopped := errors.New("stop after durable capture")
	afterStep = func(at string) error {
		if at == "tree captured" {
			return stopped
		}
		return nil
	}
	err = s.Grant(Grant{Path: r.state, Access: Modify})
	afterStep = nil
	if !errors.Is(err, stopped) {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Commit(); err == nil {
		t.Fatal("unfinished capture became ready")
	}
	if actual, _ := snapshot(path); actual != before {
		t.Fatal("capture changed a child before its first ACL effect")
	}
	if _, err := Remove(r.manifest, false); err != nil {
		t.Fatal(err)
	}
}

func TestCapturedTreeRefusesAChangedIdentityBeforeRemoval(t *testing.T) {
	r := privateTreeRig(t)
	disk := stoppedTree(t, r)
	original, _ := os.ReadFile(r.manifest)
	disk.TreeEdit.Objects[1].ID = disk.TreeEdit.Objects[0].ID
	if err := writeManifestDisk(r.manifest, disk); err != nil {
		t.Fatal(err)
	}
	forged, _ := os.ReadFile(r.manifest)
	if _, err := Remove(r.manifest, false); err == nil {
		t.Fatal("changed captured identity reached removal")
	}
	if after, _ := os.ReadFile(r.manifest); !bytes.Equal(after, forged) {
		t.Fatal("refusal changed the captured journal")
	}
	if err := os.WriteFile(r.manifest, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(r.manifest, false); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyManifestCapturesOnlyItsCurrentRecoveryFacts(t *testing.T) {
	r := privateTreeRig(t)
	r.install(t)
	path := filepath.Join(r.state, "cache")
	if _, err := rewriteDACL(path, "", protectedExplicit); err != nil {
		t.Fatal(err)
	}
	before, err := snapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	m.Format = 2
	if err := writeManifest(r.manifest, m); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(r.manifest); err != nil {
		t.Fatal("legacy format refused", err)
	}
	sid, err := windows.StringToSid(m.SID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := withoutSID(before, sid)
	if err != nil {
		t.Fatal(err)
	}
	stopped := errors.New("observe legacy capture")
	afterStep = func(at string) error {
		if at == "tree captured" {
			return stopped
		}
		return nil
	}
	_, err = Remove(r.manifest, false)
	afterStep = nil
	if !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	disk, err := readManifestDisk(r.manifest)
	if err != nil || disk.Format != 3 || !disk.Legacy || disk.TreeEdit == nil {
		t.Fatalf("legacy capture lost its provenance: %+v %v", disk, err)
	}
	if _, err := Remove(r.manifest, false); err != nil {
		t.Fatal(err)
	}
	if after, err := snapshot(path); err != nil || after != want {
		t.Fatalf("legacy recovery invented an earlier descriptor: %q (%v), want %q", after, err, want)
	}
}
