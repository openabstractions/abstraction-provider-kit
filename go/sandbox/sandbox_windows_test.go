package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

// profileSeq numbers the profiles of one test process.
var profileSeq atomic.Int32

const sandboxSecurityProbeOptIn = "OA_PROVIDER_KIT_SANDBOX_SECURITY_TESTS"

func requireSandboxSecurityProbeOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv(sandboxSecurityProbeOptIn) != "1" {
		t.Skipf("sandbox security probes are paused until the owner confirms Daybreak access is clear; set %s=1 only after that confirmation", sandboxSecurityProbeOptIn)
	}
}

// testProfile is a profile name of this test process alone; the profile is
// deleted when the test ends, whatever the test did with it.
func testProfile(t *testing.T) string {
	t.Helper()
	name, err := ProfileName("kit-test", strconv.Itoa(os.Getpid()), "t"+strconv.Itoa(int(profileSeq.Add(1))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := DeleteProfile(name); err != nil {
			t.Errorf("delete the test profile: %v", err)
		}
	})
	return name
}

// rig is one component: an engine folder to read, a state folder to write,
// a folder outside every grant, and the manifest beside them.
type rig struct {
	dir, manifest, engine, state, outside, profile string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{dir: dir, manifest: filepath.Join(dir, "sandbox.json"), engine: filepath.Join(dir, "engine"),
		state: filepath.Join(dir, "state"), outside: filepath.Join(dir, "outside"), profile: testProfile(t)}
	for _, f := range []string{filepath.Join(r.engine, "a.txt"), filepath.Join(r.engine, "sub", "b.txt"),
		filepath.Join(r.state, "cache", "c.txt"), filepath.Join(r.outside, "d.txt")} {
		write(t, f, "x")
	}
	return r
}

func TestGrantBoundChecksApprovedIdentityBeforeRecordingOrChangingACL(t *testing.T) {
	r := newRig(t)
	before := captureACL(r.engine)
	if before.err != nil {
		t.Fatal(before.err)
	}
	wrong, err := identity.FileKey(r.state)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.GrantBound(Grant{Path: r.engine, Access: Modify}, wrong); err == nil {
		t.Fatal("wrong approved identity was granted")
	}
	m, err := ReadManifest(r.manifest)
	if err != nil || len(m.Grants) != 0 {
		t.Fatalf("refused grant recorded effects: %+v %v", m, err)
	}
	after := captureACL(r.engine)
	if after.err != nil || after.id != before.id || after.sddl != before.sddl {
		t.Fatalf("refused grant changed ACL: %+v", after)
	}
	if err := s.GrantBound(Grant{Path: r.engine, Access: ReadExecute}, before.id); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(r.manifest, true); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) grants() []Grant {
	return []Grant{{Path: r.engine, Access: ReadExecute}, {Path: r.state, Access: Modify}}
}

// install runs one whole install or update.
func (r *rig) install(t *testing.T) {
	t.Helper()
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(r.grants()...); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
}

// aclDiagnostic retains a checked read before an ordinary lifecycle effect.
type aclDiagnostic struct {
	id, sddl string
	err      error
}

func captureACL(path string) aclDiagnostic {
	id, err := identity.FileKey(path)
	if err != nil {
		return aclDiagnostic{err: err}
	}
	sddl, err := snapshotID(path, id)
	return aclDiagnostic{id: id, sddl: sddl, err: err}
}

// logACLChange reads only the fixture's existing path, on failure. Raw SDDL
// retains descriptor control flags; parsed ACEs retain kind/flags/mask/SID.
func logACLChange(t *testing.T, label, path, id, before, sid string) {
	t.Helper()
	after, readErr := snapshotID(path, id)
	t.Logf("%s path=%s expected_id=%s before_raw=%q after_raw=%q read_error=%v", label, path, id, before, after, readErr)
	if readErr != nil {
		return
	}
	value, sidErr := windows.StringToSid(sid)
	if sidErr != nil {
		t.Logf("%s SID parse error: %v", label, sidErr)
		return
	}
	beforeComparable, beforeErr := withoutSID(before, value)
	afterComparable, afterErr := withoutSID(after, value)
	_, acl, aclErr := daclOfID(path, id)
	var list []ace
	if aclErr == nil {
		list, aclErr = entries(acl)
	}
	t.Logf("%s before_comparable=%q before_error=%v after_comparable=%q after_error=%v after_aces=%+v acl_error=%v", label, beforeComparable, beforeErr, afterComparable, afterErr, list, aclErr)
}

func logRemovalACL(t *testing.T, m *Manifest, captureErr error) {
	t.Helper()
	if captureErr != nil || m == nil {
		t.Logf("pre-removal manifest capture error: %v", captureErr)
		return
	}
	for _, e := range m.Grants {
		logACLChange(t, "post-revoke", e.Path, e.ID, e.Before, m.SID)
	}
}

// removeAll removes the sandbox and checks nothing of it is left: no path
// under the rig names the container, no access list differs from its
// snapshot, the profile and the manifest are gone (KIT-S4).
func (r *rig) removeAll(t *testing.T, sid string) {
	t.Helper()
	// Remove can delete the manifest while reporting Changed; retain its
	// original access lists and identities before that existing call.
	captured, captureErr := ReadManifest(r.manifest)
	out, err := Remove(r.manifest, false)
	if err != nil {
		logRemovalACL(t, captured, captureErr)
		t.Fatalf("remove: %v (%+v)", err, out)
	}
	if len(out.Changed) > 0 || !out.ProfileDeleted {
		logRemovalACL(t, captured, captureErr)
		t.Fatalf("removal %+v", out)
	}
	if holders, err := Holders(r.dir, sid); err != nil || len(holders) > 0 {
		t.Fatalf("paths still naming the container: %v, %v", holders, err)
	}
	if ProfileExists(sid) {
		t.Fatal("the profile is still there")
	}
	if left, err := Profiles(r.profile); err != nil || len(left) > 0 {
		t.Fatalf("the system still records %v (%v)", left, err)
	}
	for _, p := range []string{r.manifest, lockPath(r.manifest)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is left: %v", p, err)
		}
	}
}

// TestInstallLaunchRemove: an install records the profile and each grant
// with its snapshot; a launch needing granted paths is let in with the
// container's SID; a path outside the grants or wanting more access is
// refused; removal leaves no entry for the container and every access list
// as it was (KIT-S2, KIT-S3, KIT-S4).
func TestInstallLaunchRemove(t *testing.T) {
	r := newRig(t)
	before := map[string]string{}
	for _, g := range r.grants() {
		before[g.Path], _ = snapshot(g.Path)
	}
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if m.Phase != PhaseReady || m.Profile != r.profile || m.SID == "" || len(m.Grants) != 2 {
		t.Fatalf("manifest %+v", m)
	}
	for _, e := range m.Grants {
		if !e.Applied || !e.Tree || e.Before != before[e.Path] {
			t.Fatalf("entry %+v, before %q", e, before[e.Path])
		}
	}
	l, err := Prepare(r.manifest, []Grant{{Path: filepath.Join(r.engine, "sub", "b.txt"), Access: ReadExecute},
		{Path: filepath.Join(r.state, "cache"), Access: Modify}, {Path: r.engine, Access: ReadExecute}})
	if err != nil {
		t.Fatal(err)
	}
	if l.SID != m.SID || l.Profile != r.profile {
		t.Fatalf("launch %+v", l)
	}
	l.Close()
	for name, need := range map[string]Grant{
		"outside":        {Path: filepath.Join(r.outside, "d.txt"), Access: ReadExecute},
		"more than read": {Path: filepath.Join(r.engine, "a.txt"), Access: Modify},
	} {
		if l, err := Prepare(r.manifest, []Grant{need}); !errors.Is(err, ErrNotGranted) {
			l.Close()
			t.Fatalf("%s: %v", name, err)
		}
	}
	r.removeAll(t, m.SID)
}

// A component may grant its whole work folder and narrower state folders
// inside it. Removal must finish revoking all grants before it judges the
// inherited entries and the original access lists.
func TestRemoveNestedGrants(t *testing.T) {
	for _, parentFirst := range []bool{true, false} {
		name := "child-first"
		if parentFirst {
			name = "parent-first"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			nested := filepath.Join(r.state, "cache")
			before, err := snapshot(nested)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Begin(r.manifest, r.profile)
			if err != nil {
				t.Fatal(err)
			}
			grants := []Grant{{Path: r.state, Access: Modify}, {Path: nested, Access: Modify}}
			if !parentFirst {
				grants[0], grants[1] = grants[1], grants[0]
			}
			if err := s.Grant(grants...); err != nil {
				s.Close()
				t.Fatal(err)
			}
			if err := s.Commit(); err != nil {
				t.Fatal(err)
			}
			m, err := ReadManifest(r.manifest)
			if err != nil {
				t.Fatal(err)
			}
			removed, err := Remove(r.manifest, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(removed.Changed) != 0 || !removed.ProfileDeleted {
				logRemovalACL(t, m, nil)
				t.Fatalf("removal left changes: %+v", removed)
			}
			if after, err := snapshot(nested); err != nil || after != before {
				t.Fatalf("nested access list after removal %q (%v), before %q", after, err, before)
			}
			if holders, err := Holders(r.state, m.SID); err != nil || len(holders) > 0 {
				t.Fatalf("nested grant holders %v (%v)", holders, err)
			}
		})
	}
}

func TestGrantRefusesAPreexistingContainerACE(t *testing.T) {
	requireSandboxSecurityProbeOptIn(t)
	r := newRig(t)
	sid, err := deriveSID(r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := changeACL(r.engine, sid, mask(ReadExecute), true, windows.GRANT_ACCESS); err != nil {
		t.Fatal(err)
	}
	defer changeACL(r.engine, sid, 0, true, windows.REVOKE_ACCESS)
	before, err := snapshot(r.engine)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(Grant{Path: r.engine, Access: ReadExecute}); err == nil {
		s.Close()
		t.Fatal("Setup accepted an access list it cannot restore")
	}
	s.Close()
	if after, err := snapshot(r.engine); err != nil || after != before {
		t.Fatalf("a refused grant changed the original access list: %q (%v)", after, err)
	}
	if _, err := Remove(r.manifest, false); err != nil {
		t.Fatal(err)
	}
}

// TestAnUpdateGrantsWhatItMovedIn: a file moved into a granted folder keeps
// the access list it had, so a launch that needs it is refused until Setup
// runs again, which grants it; the snapshots stay the first install's
// (KIT-S2, KIT-S3).
func TestAnUpdateGrantsWhatItMovedIn(t *testing.T) {
	r := newRig(t)
	r.install(t)
	first, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(r.engine, "sub", "moved.pyd")
	write(t, filepath.Join(r.outside, "moved.pyd"), "new")
	if err := os.Rename(filepath.Join(r.outside, "moved.pyd"), moved); err != nil {
		t.Fatal(err)
	}
	paths := []string{r.engine, filepath.Dir(moved), moved}
	beforeUpdate := make(map[string]aclDiagnostic, len(paths))
	for _, path := range paths {
		beforeUpdate[path] = captureACL(path)
	}
	need := []Grant{{Path: moved, Access: ReadExecute}}
	if l, err := Prepare(r.manifest, need); !errors.Is(err, ErrNotGranted) {
		l.Close()
		t.Fatalf("a launch needing a file moved in after Setup: %v", err)
	}
	r.install(t)
	l, err := Prepare(r.manifest, need)
	if err != nil {
		for _, path := range paths {
			before := beforeUpdate[path]
			t.Logf("pre-update path=%s id=%s capture_error=%v", path, before.id, before.err)
			logACLChange(t, "post-update", path, before.id, before.sddl, first.SID)
		}
		t.Fatalf("after the update: %v", err)
	}
	l.Close()
	again, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Grants) != len(first.Grants) || again.Grants[0].Before != first.Grants[0].Before || again.Grants[1].Before != first.Grants[1].Before {
		t.Fatalf("the update changed the record: %+v, was %+v", again.Grants, first.Grants)
	}
	r.removeAll(t, again.SID)
}

// TestSetupAndLaunchesExcludeEachOther: no launch while Setup holds the
// sandbox, no Setup change or removal while a launch holds it, and two
// launches (two models at once) share it (KIT-S3).
func TestSetupAndLaunchesExcludeEachOther(t *testing.T) {
	r := newRig(t)
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(r.grants()...); err != nil {
		t.Fatal(err)
	}
	if l, err := Prepare(r.manifest, nil); !errors.Is(err, ErrBusy) {
		l.Close()
		t.Fatalf("a launch during the install: %v", err)
	}
	sid := s.SID()
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	one, err := Prepare(r.manifest, []Grant{{Path: r.engine, Access: ReadExecute}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Prepare(r.manifest, []Grant{{Path: r.state, Access: Modify}})
	if err != nil {
		t.Fatalf("a second launch beside the first: %v", err)
	}
	if s, err := Begin(r.manifest, r.profile); !errors.Is(err, ErrInUse) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("an update while engines run: %v", err)
	}
	if _, err := Remove(r.manifest, false); !errors.Is(err, ErrInUse) {
		t.Fatalf("a removal while engines run: %v", err)
	}
	one.Close()
	if _, err := Remove(r.manifest, false); !errors.Is(err, ErrInUse) {
		t.Fatalf("a removal while one engine runs: %v", err)
	}
	two.Close()
	r.removeAll(t, sid)
}

// TestAStoppedInstallIsRemoved: an install stopped after any step, as a
// crash stops it, leaves a manifest in the phase installing that refuses
// every launch, and a profile the system lists; removal revokes every
// recorded path, applied or not, and deletes the profile (KIT-S2, KIT-S5).
func TestAStoppedInstallIsRemoved(t *testing.T) {
	crash := errors.New("the install stopped here")
	for _, at := range []string{"phase", "profile", "recorded", "applied"} {
		t.Run(at, func(t *testing.T) {
			r := newRig(t)
			sid, err := deriveSID(r.profile)
			if err != nil {
				t.Fatal(err)
			}
			afterStep = func(step string) error {
				if step == at {
					return crash
				}
				return nil
			}
			s, err := Begin(r.manifest, r.profile)
			if err == nil {
				err = s.Grant(r.grants()...)
				s.Close()
			}
			afterStep = nil
			if !errors.Is(err, crash) {
				t.Fatalf("the install did not stop: %v", err)
			}
			if l, err := Prepare(r.manifest, nil); !errors.Is(err, ErrBusy) {
				l.Close()
				t.Fatalf("a launch after a stopped install: %v", err)
			}
			if at != "phase" {
				if left, err := Profiles(r.profile); err != nil || len(left) != 1 {
					t.Fatalf("the left profile is not listed: %v %v", left, err)
				}
			}
			r.removeAll(t, sid.String())
		})
	}
}

// TestAMissingGrantOrProfileRefusesTheLaunch: a grant revoked by hand, or a
// profile deleted by hand, refuses the launch by name; Setup again repairs
// the grant, and removal still completes without the profile (KIT-S3).
func TestAMissingGrantOrProfileRefusesTheLaunch(t *testing.T) {
	r := newRig(t)
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := windows.StringToSid(m.SID)
	if err := changeACL(r.engine, sid, 0, true, windows.REVOKE_ACCESS); err != nil {
		t.Fatal(err)
	}
	if l, err := Prepare(r.manifest, nil); !errors.Is(err, ErrNotGranted) {
		l.Close()
		t.Fatalf("a launch without its grant: %v", err)
	}
	r.install(t)
	l, err := Prepare(r.manifest, nil)
	if err != nil {
		t.Fatalf("after Setup again: %v", err)
	}
	l.Close()
	if err := DeleteProfile(r.profile); err != nil {
		t.Fatal(err)
	}
	if l, err := Prepare(r.manifest, nil); !errors.Is(err, ErrNotInstalled) {
		l.Close()
		t.Fatalf("a launch without its profile: %v", err)
	}
	r.removeAll(t, m.SID)
	if l, err := Prepare(r.manifest, nil); !errors.Is(err, ErrNotInstalled) {
		l.Close()
		t.Fatalf("a launch after removal: %v", err)
	}
}

// TestAGrantRefusesALink: a junction is never granted, since a grant and its
// revoke would follow it to whatever it names at the time.
func TestAGrantRefusesALink(t *testing.T) {
	requireSandboxSecurityProbeOptIn(t)
	r := newRig(t)
	link := filepath.Join(r.dir, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, r.outside).CombinedOutput(); err != nil {
		t.Skipf("no junction: %v %s", err, out)
	}
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Grant(Grant{Path: link, Access: ReadExecute})
	sid := s.SID()
	s.Close()
	if err == nil {
		t.Fatal("a junction was granted")
	}
	if holders, _ := Holders(r.outside, sid); len(holders) > 0 {
		t.Fatalf("the junction's target names the container: %v", holders)
	}
	//unchecked: the junction itself, removed non-recursively before the rig
	os.Remove(link)
	r.removeAll(t, sid)
}

// A grant through a junction in an ancestor would grant its target while
// the manifest records a name that could later point somewhere else.
func TestAGrantRefusesAnAncestorJunction(t *testing.T) {
	requireSandboxSecurityProbeOptIn(t)
	r := newRig(t)
	link := filepath.Join(r.dir, "alias")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, r.outside).CombinedOutput(); err != nil {
		t.Skipf("no junction: %v %s", err, out)
	}
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Grant(Grant{Path: filepath.Join(link, "d.txt"), Access: ReadExecute})
	sid := s.SID()
	s.Close()
	if err == nil {
		t.Fatal("a path through an ancestor junction was granted")
	}
	if holders, err := Holders(r.outside, sid); err != nil || len(holders) > 0 {
		t.Fatalf("the junction target names the container: %v (%v)", holders, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, sid)
}

// Manifest paths are data read back from disk. A UNC grant is rejected
// before Prepare or Remove can resolve it or change any access list.
func TestManifestRefusesANonlocalGrant(t *testing.T) {
	r := newRig(t)
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	original := *m
	m.Grants = append([]Entry(nil), m.Grants...)
	m.Grants[0].Path = `\\unreachable.example\share\engine`
	if err := writeManifest(r.manifest, m); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(r.manifest); !errors.Is(err, identity.ErrNotLocalPath) {
		t.Fatalf("manifest grant: %v", err)
	}
	if l, err := Prepare(r.manifest, nil); !errors.Is(err, identity.ErrNotLocalPath) {
		l.Close()
		t.Fatalf("launch grant: %v", err)
	}
	if _, err := Remove(r.manifest, false); !errors.Is(err, identity.ErrNotLocalPath) {
		t.Fatalf("removal grant: %v", err)
	}
	if err := writeManifest(r.manifest, &original); err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, original.SID)
}

// Replacement of a recorded folder by a junction refuses removal before
// it touches the new target or changes the manifest's phase.
func TestRemoveRefusesASwappedGrant(t *testing.T) {
	requireSandboxSecurityProbeOptIn(t)
	r := newRig(t)
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	outsideBefore, err := snapshot(r.outside)
	if err != nil {
		t.Fatal(err)
	}
	moved := r.engine + "-held"
	if err := os.Rename(r.engine, moved); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", r.engine, r.outside).CombinedOutput(); err != nil {
		os.Rename(moved, r.engine)
		t.Skipf("no junction: %v %s", err, out)
	}
	if _, err := Remove(r.manifest, false); err == nil {
		t.Fatal("removal followed a swapped grant")
	}
	if after, err := snapshot(r.outside); err != nil || after != outsideBefore {
		t.Fatalf("junction target access list changed: %q (%v), before %q", after, err, outsideBefore)
	}
	if current, err := ReadManifest(r.manifest); err != nil || current.Phase != PhaseReady {
		t.Fatalf("removal changed the manifest before refusing: %+v (%v)", current, err)
	}
	if err := os.Remove(r.engine); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, r.engine); err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, m.SID)
}

func TestRemoveRefusesAMissingGrantMovedOutsideTheTree(t *testing.T) {
	requireSandboxSecurityProbeOptIn(t)
	r := newRig(t)
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(r.outside, "moved-engine")
	if err := os.Rename(r.engine, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(r.manifest, false); err == nil {
		t.Fatal("removal forgot an applied grant moved outside its recorded tree")
	}
	if current, err := ReadManifest(r.manifest); err != nil || current.Phase != PhaseReady {
		t.Fatalf("removal changed phase before refusing a missing grant: %+v (%v)", current, err)
	}
	if !ProfileExists(m.SID) {
		t.Fatal("removal deleted the profile despite an unresolved grant")
	}
	if holders, err := Holders(moved, m.SID); err != nil || len(holders) == 0 {
		t.Fatalf("moved grant was not held: %v (%v)", holders, err)
	}
	if err := os.Rename(moved, r.engine); err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, m.SID)
}

// TestManifestRefusesAnotherProfile: a manifest belongs to its profile.
func TestManifestRefusesAnotherProfile(t *testing.T) {
	r := newRig(t)
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Begin(r.manifest, testProfile(t)); err == nil {
		s.Close()
		t.Fatal("a manifest was taken over by another profile")
	}
	r.removeAll(t, m.SID)
}

func TestManifestRefusesMismatchedProfileSIDBeforeRemoval(t *testing.T) {
	r := newRig(t)
	r.install(t)
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	original := *m
	m.Profile = testProfile(t)
	if err := writeManifest(r.manifest, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(r.manifest, false); err == nil {
		t.Fatal("removal accepted a profile/SID mismatch")
	}
	if current, err := ReadManifest(r.manifest); err != nil || current.Phase != PhaseReady {
		t.Fatalf("removal changed phase before refusal: %+v (%v)", current, err)
	}
	if err := writeManifest(r.manifest, &original); err != nil {
		t.Fatal(err)
	}
	r.removeAll(t, original.SID)
}
