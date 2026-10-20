package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

func userSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

func normalSDDL(t *testing.T, sddl string) string {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

// fakeSecurity stands in for the ancestors' security: each path's owner and
// access list, and every write in order.
type fakeSecurity struct {
	mu     sync.Mutex
	owner  map[string]string
	dacl   map[string]string
	writes []string
}

type fakeFile struct {
	s     *fakeSecurity
	path  string
	write bool
}

func (f fakeFile) Read() (string, string, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	d, ok := f.s.dacl[f.path]
	if !ok {
		return "", "", fmt.Errorf("fake: no security for %s", f.path)
	}
	return f.s.owner[f.path], d, nil
}

func (f fakeFile) WriteDACL(dacl string) error {
	if !f.write {
		return errors.New("fake: opened for reading")
	}
	sd, err := windows.SecurityDescriptorFromString(dacl)
	if err != nil {
		return err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.dacl[f.path] = sd.String()
	f.s.writes = append(f.s.writes, f.path)
	return nil
}

func (fakeFile) Close() error { return nil }

// useFake routes every Attributes read and write through s for the test.
func useFake(t *testing.T, s *fakeSecurity) {
	t.Helper()
	real := openSecurity
	openSecurity = func(path, id string, write bool) (securityFile, error) {
		if id != "" {
			got, err := identity.FileKey(path)
			if err != nil || got != id {
				return nil, fmt.Errorf("fake: %s is not %s (%v)", path, id, err)
			}
		}
		return fakeFile{s: s, path: filepath.Clean(path), write: write}, nil
	}
	t.Cleanup(func() { openSecurity = real })
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// ancestorRig is a rig with a project three folders deep and the fake
// security of the folders above it: the rig's root controlled by an
// administrator only, p reached through ALL APPLICATION PACKAGES, p\q
// this account's.
type ancestorRig struct {
	*rig
	project, p, q string
	fake          *fakeSecurity
	qBefore       string
}

func newAncestorRig(t *testing.T) *ancestorRig {
	t.Helper()
	r := &ancestorRig{rig: newRig(t)}
	r.p = filepath.Join(r.dir, "p")
	r.q = filepath.Join(r.p, "q")
	r.project = filepath.Join(r.q, "project")
	mkdirs(t, r.project)
	user := userSID(t)
	r.qBefore = normalSDDL(t, "D:AI(A;OICI;FA;;;"+user+")(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)")
	r.fake = &fakeSecurity{
		owner: map[string]string{r.dir: "S-1-5-18", r.p: "S-1-5-18", r.q: "S-1-5-32-544"},
		dacl: map[string]string{
			r.dir: normalSDDL(t, "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;0x1200a9;;;BU)"),
			r.p:   normalSDDL(t, "D:PAI(A;OICI;FA;;;SY)(A;;0x1200a9;;;AC)"),
			r.q:   r.qBefore,
		},
	}
	useFake(t, r.fake)
	return r
}

func (r *ancestorRig) begin(t *testing.T) *Setup {
	t.Helper()
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(Grant{Path: r.engine, Access: ReadExecute}, Grant{Path: r.project, Access: Modify}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s
}

// TestAncestorAttributesWithAFakeWriter: the folders above the grants are
// judged once each by file identity; one this account controls is recorded
// with its snapshot and gets one non-inherited Attributes entry before the
// inherited ones; one the container reaches and one only an administrator
// controls are left alone; nothing below an ancestor is written; a launch
// checks the entry; removal restores the list exactly (KIT-S7).
func TestAncestorAttributesWithAFakeWriter(t *testing.T) {
	r := newAncestorRig(t)
	s := r.begin(t)
	got, err := s.grantAncestors(r.dir)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	sid := s.SID()
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	want := &Ancestors{Granted: []string{r.q}, Reached: []string{r.p}, Unreached: []string{r.dir}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ancestors %+v, want %+v", got, want)
	}
	if !slices.Equal(r.fake.writes, []string{r.q}) {
		t.Fatalf("writes %v, want one write to %s", r.fake.writes, r.q)
	}
	user := userSID(t)
	wantDACL := normalSDDL(t, "D:AI(A;OICI;FA;;;"+user+")(A;;0x100080;;;"+sid+")(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)")
	if r.fake.dacl[r.q] != wantDACL {
		t.Fatalf("written %q, want %q", r.fake.dacl[r.q], wantDACL)
	}
	m, err := ReadManifest(r.manifest)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(m.Grants, func(e Entry) bool { return e.Access == Attributes })
	if i < 0 || m.Grants[i].Path != r.q || m.Grants[i].Tree || !m.Grants[i].Applied || m.Grants[i].Before != r.qBefore || len(m.Grants) != 3 {
		t.Fatalf("manifest %+v", m.Grants)
	}
	l, err := Prepare(r.manifest, []Grant{{Path: r.project, Access: Modify}})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	// An entry taken away by hand refuses the launch, as any grant does.
	r.fake.dacl[r.q] = r.qBefore
	if l, err := Prepare(r.manifest, nil); !errors.Is(err, ErrNotGranted) {
		l.Close()
		t.Fatalf("a launch without the ancestor's entry: %v", err)
	}
	r.fake.dacl[r.q] = wantDACL
	r.removeAll(t, sid)
	if r.fake.dacl[r.q] != r.qBefore || !slices.Equal(r.fake.writes, []string{r.q, r.q}) {
		t.Fatalf("after removal %q, writes %v", r.fake.dacl[r.q], r.fake.writes)
	}
}

// TestAncestorAttributesStoppedInstall: a session stopped after recording
// an ancestor, or after writing its entry, as a crash stops it, leaves a
// manifest Remove takes back whole (KIT-S5, KIT-S7).
func TestAncestorAttributesStoppedInstall(t *testing.T) {
	crash := errors.New("the session stopped here")
	for _, at := range []string{"recorded", "applied"} {
		t.Run(at, func(t *testing.T) {
			r := newAncestorRig(t)
			s := r.begin(t)
			sid := s.SID()
			afterStep = func(step string) error {
				if step == at {
					return crash
				}
				return nil
			}
			_, err := s.grantAncestors(r.dir)
			afterStep = nil
			s.Close()
			if !errors.Is(err, crash) {
				t.Fatalf("the session did not stop: %v", err)
			}
			r.removeAll(t, sid)
			if r.fake.dacl[r.q] != r.qBefore {
				t.Fatalf("after removal %q, want %q", r.fake.dacl[r.q], r.qBefore)
			}
		})
	}
}

// An ancestor that already names the container outside the manifest, with
// less than Attributes, cannot be restored, so nothing is recorded or
// written for it. One that already gives Attributes is reached as it is.
func TestAncestorAttributesRefuseAForeignEntry(t *testing.T) {
	r := newAncestorRig(t)
	s := r.begin(t)
	defer s.Close()
	foreign := normalSDDL(t, strings.Replace(r.qBefore, "(A;OICIID;FA;;;SY)", "(A;;0x1;;;"+s.SID()+")(A;OICIID;FA;;;SY)", 1))
	if foreign == r.qBefore {
		t.Fatal("the fixture has no inherited SYSTEM entry")
	}
	r.fake.dacl[r.q] = foreign
	if _, err := s.grantAncestors(r.dir); err == nil {
		t.Fatal("an ancestor naming the container already was taken over")
	}
	if len(r.fake.writes) != 0 {
		t.Fatalf("writes %v", r.fake.writes)
	}
	r.fake.dacl[r.q] = r.qBefore
	s.Close()
	if _, err := Remove(r.manifest, false); err != nil {
		t.Fatal(err)
	}
}

func TestAttributeEntryText(t *testing.T) {
	const sid = "S-1-15-2-1-2-3-4-5-6-7"
	for _, c := range []struct{ in, want string }{
		{"D:P", "D:P(A;;0x100080;;;" + sid + ")"},
		{"D:PAI(D;;FA;;;WD)(A;;FA;;;SY)(A;OICIID;FA;;;BA)", "D:PAI(D;;FA;;;WD)(A;;FA;;;SY)(A;;0x100080;;;" + sid + ")(A;OICIID;FA;;;BA)"},
		{"D:AI(A;OICIID;FA;;;BA)", "D:AI(A;;0x100080;;;" + sid + ")(A;OICIID;FA;;;BA)"},
		{"D:(XA;;FR;;;WD;(Member_of {SID(BA)}))", "D:(XA;;FR;;;WD;(Member_of {SID(BA)}))(A;;0x100080;;;" + sid + ")"},
	} {
		got, err := withAttributeACE(c.in, sid)
		if err != nil || got != c.want {
			t.Fatalf("withAttributeACE(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		back, err := withoutExplicit(got, sid, true)
		if err != nil || back != c.in {
			t.Fatalf("withoutExplicit(%q) = %q, %v", got, back, err)
		}
	}
	if _, err := withAttributeACE("D:NO_ACCESS_CONTROL", sid); err == nil {
		t.Fatal("an entry was added to a null access list")
	}
	// The exact revoke leaves every other entry for the SID; the session's
	// revoke takes every explicit one and never an inherited one.
	other := "D:(A;;FA;;;" + sid + ")(A;;0x100080;;;" + sid + ")(A;OICIID;FR;;;" + sid + ")"
	if got, _ := withoutExplicit(other, sid, true); got != "D:(A;;FA;;;"+sid+")(A;OICIID;FR;;;"+sid+")" {
		t.Fatalf("exact revoke: %q", got)
	}
	if got, _ := withoutExplicit(other, sid, false); got != "D:(A;OICIID;FR;;;"+sid+")" {
		t.Fatalf("explicit revoke: %q", got)
	}
	for flags, want := range map[string]bool{"": false, "OICI": false, "OICIID": true, "ID": true, "CIIO": false, "IOID": true} {
		if inheritedEntry(flags) != want {
			t.Fatalf("inheritedEntry(%q) != %v", flags, want)
		}
	}
}

func TestAttributeJudgements(t *testing.T) {
	user := userSID(t)
	const container = "S-1-15-2-1-2-3-4-5-6-7"
	list := func(sddl string) []ace {
		l, err := aclEntries(sddl)
		if err != nil {
			t.Fatalf("%s: %v", sddl, err)
		}
		return l
	}
	for name, c := range map[string]struct {
		owner, dacl      string
		reached, control bool
	}{
		"drive root":     {"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464", "D:PAI(A;;LC;;;AU)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;0x1200a9;;;BU)", false, false},
		"profiles":       {"S-1-5-18", "D:PAI(A;;0x1200a9;;;WD)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", false, false},
		"profile folder": {"S-1-5-18", "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user + ")", false, true},
		"owned":          {user, "D:AI(A;OICIID;FA;;;SY)", false, true},
		"denied":         {"S-1-5-18", "D:(D;;WD;;;" + user + ")(A;;FA;;;" + user + ")", false, false},
		"packages":       {"S-1-5-18", "D:(A;;0x1200a9;;;AC)", true, false},
		"capability":     {"S-1-5-18", "D:(A;;0x100080;;;" + supervision.ContainerCapabilitySID + ")", true, false},
		"own entry":      {"S-1-5-18", "D:(A;;0x100080;;;" + container + ")", true, false},
		"inherit only":   {"S-1-5-18", "D:(A;OICIIO;FA;;;AC)", false, false},
		"denied package": {"S-1-5-18", "D:(D;;0x80;;;" + container + ")(A;;FA;;;AC)", false, false},
	} {
		l := list(c.dacl)
		if got := allows(l, maskAttributes, container, allApplicationPackages, supervision.ContainerCapabilitySID); got != c.reached {
			t.Fatalf("%s: reached %v", name, got)
		}
		if got := accountControls(c.owner, l, user); got != c.control {
			t.Fatalf("%s: controls %v", name, got)
		}
	}
}

// TestAncestorAttributesOnARealTree: on a real folder tree, each ancestor
// gets one non-inherited entry through the non-propagating writer, a
// sibling whose inherited entries are stale keeps them stale, and removal
// returns every list to its snapshot. A control shows that SetSecurityInfo
// on the same parent re-propagates into that sibling (KIT-S7).
func TestAncestorAttributesOnARealTree(t *testing.T) {
	r := newRig(t)
	a := filepath.Join(r.dir, "a")
	b := filepath.Join(a, "b")
	project := filepath.Join(b, "c")
	sibling := filepath.Join(a, "sibling")
	mkdirs(t, project, sibling)
	write(t, filepath.Join(a, "file.txt"), "x")
	// Make the sibling's inherited entries stale: drop the first inherited
	// one without propagation. A propagating write on a restores it.
	siblingID, err := identity.FileKey(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rewriteDACL(sibling, siblingID, func(dacl string) (string, error) {
		spans, err := aceSpans(dacl)
		if err != nil {
			return "", err
		}
		for _, s := range spans {
			if inheritedEntry(aceFields(dacl[s[0]:s[1]])[1]) {
				return dacl[:s[0]] + dacl[s[1]:], nil
			}
		}
		return "", errors.New("the sibling inherits nothing")
	}); err != nil {
		t.Fatal(err)
	}
	untouched := map[string]string{}
	for _, p := range []string{sibling, filepath.Join(a, "file.txt")} {
		if untouched[p], err = snapshot(p); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string]string{}
	for _, p := range []string{r.dir, a, b} {
		if before[p], err = snapshot(p); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(Grant{Path: project, Access: Modify}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	got, err := s.grantAncestors(r.dir)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	sid := s.SID()
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Granted, []string{b, a, r.dir}) || len(got.Reached) != 0 || len(got.Unreached) != 0 {
		t.Fatalf("ancestors %+v", got)
	}
	for _, p := range got.Granted {
		_, acl, err := daclOf(p)
		if err != nil {
			t.Fatal(err)
		}
		list, err := entries(acl)
		if err != nil {
			t.Fatal(err)
		}
		mine := slices.DeleteFunc(slices.Clone(list), func(x ace) bool { return x.sid != sid })
		if len(mine) != 1 || mine[0].kind != windows.ACCESS_ALLOWED_ACE_TYPE || mine[0].flags != 0 || mine[0].mask != maskAttributes {
			t.Fatalf("%s holds %+v for the container", p, mine)
		}
	}
	for p, want := range untouched {
		if after, err := snapshot(p); err != nil || after != want {
			t.Fatalf("%s changed below an ancestor: %q (%v), was %q", p, after, err, want)
		}
	}
	l, err := Prepare(r.manifest, []Grant{{Path: project, Access: Modify}})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	r.removeAll(t, sid)
	for p, want := range before {
		if after, err := snapshot(p); err != nil || after != want {
			t.Fatalf("%s after removal %q (%v), was %q", p, after, err, want)
		}
	}
	for p, want := range untouched {
		if after, err := snapshot(p); err != nil || after != want {
			t.Fatalf("%s changed by removal: %q (%v), was %q", p, after, err, want)
		}
	}
	// Control: the propagating writer the kit uses for folder grants rewrites
	// the stale sibling when it changes the parent alone.
	guests, err := windows.StringToSid("S-1-5-32-546")
	if err != nil {
		t.Fatal(err)
	}
	if err := changeACL(a, guests, maskAttributes, false, windows.GRANT_ACCESS); err != nil {
		t.Fatal(err)
	}
	if after, err := snapshot(sibling); err != nil || after == untouched[sibling] {
		t.Fatalf("SetSecurityInfo left the stale sibling alone (%v); the sentinel proves nothing", err)
	}
}

// TestReadExecuteAttributesAccess: rxa is read-execute plus
// FILE_WRITE_ATTRIBUTES and nothing more, between ReadExecute and Modify.
func TestReadExecuteAttributesAccess(t *testing.T) {
	if got, want := uint32(mask(ReadExecuteAttributes)), uint32(maskReadExecute|windows.FILE_WRITE_ATTRIBUTES); got != want {
		t.Fatalf("mask %#x, want %#x", got, want)
	}
	if m := uint32(mask(ReadExecuteAttributes)); m&(windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|windows.DELETE) != 0 {
		t.Fatalf("rxa grants content writes or deletion: %#x", m)
	}
	for _, c := range []struct {
		a, b Access
		want bool
	}{
		{ReadExecuteAttributes, ReadExecute, true}, {ReadExecuteAttributes, Attributes, true},
		{ReadExecuteAttributes, Modify, false}, {Modify, ReadExecuteAttributes, true},
		{ReadExecute, ReadExecuteAttributes, false}, {ReadExecuteAttributes, ReadExecuteAttributes, true},
	} {
		if got := c.a.covers(c.b); got != c.want {
			t.Fatalf("%s covers %s = %v", c.a, c.b, got)
		}
	}
}

// TestReadExecuteAttributesGrantWithAFakeWriter: a package granted rxa
// covers the folders below it, so a project nested in it owes Attributes only
// above the package; what the package holds inherits read-execute plus write
// attributes, which a launch checks; removal restores it (KIT-S2, KIT-S7).
func TestReadExecuteAttributesGrantWithAFakeWriter(t *testing.T) {
	r := newAncestorRig(t)
	pkg := r.q
	nested := filepath.Join(pkg, "node_modules", "inner")
	module := filepath.Join(nested, "index.js")
	write(t, module, "export {}")
	before, err := snapshot(module)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Begin(r.manifest, r.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(Grant{Path: pkg, Access: ReadExecuteAttributes}, Grant{Path: nested, Access: Modify}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	got, err := s.grantAncestors(r.dir)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	sid := s.SID()
	if err := s.Commit(); err != nil {
		t.Fatal(err)
	}
	// q is the package itself; p is reached; the rig's root is unreached; the
	// node_modules folder between the package and the nested grant is covered.
	if len(got.Granted) != 0 || fmt.Sprint(got.Reached) != fmt.Sprint([]string{r.p}) || fmt.Sprint(got.Unreached) != fmt.Sprint([]string{r.dir}) {
		t.Fatalf("ancestors %+v", got)
	}
	if len(r.fake.writes) != 0 {
		t.Fatalf("writes %v", r.fake.writes)
	}
	_, acl, err := daclOf(module)
	if err != nil {
		t.Fatal(err)
	}
	list, err := entries(acl)
	if err != nil {
		t.Fatal(err)
	}
	var inherited uint32
	for _, a := range list {
		if a.sid == sid && a.flags&windows.INHERITED_ACE != 0 {
			inherited |= a.mask
		}
	}
	if want := uint32(mask(ReadExecuteAttributes)); inherited&want != want {
		t.Fatalf("the module inherits %#x, want %#x", inherited, want)
	}
	l, err := Prepare(r.manifest, []Grant{{Path: pkg, Access: ReadExecuteAttributes}, {Path: module, Access: ReadExecute}})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if l, err := Prepare(r.manifest, []Grant{{Path: pkg, Access: Modify}}); !errors.Is(err, ErrNotGranted) {
		l.Close()
		t.Fatalf("Modify under an rxa grant: %v", err)
	}
	r.removeAll(t, sid)
	if after, err := snapshot(module); err != nil || after != before {
		t.Fatalf("module after removal %q (%v), was %q", after, err, before)
	}
}

// TestRootAttributesOnAFolder:the installer's entry for the facade's
// capability is written once without propagation, read back, and removed
// exactly, by SID, rights and flags; another entry for the same SID stays
// (KIT-S7). A temporary folder stands in for a root.
func TestRootAttributesOnAFolder(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	child := filepath.Join(root, "child")
	mkdirs(t, child)
	before, err := snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	childBefore, err := snapshot(child)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		changed, err := GrantRootAttributes(root)
		if err != nil || changed != want {
			t.Fatalf("grant %d: changed %v, %v", i, changed, err)
		}
	}
	if present, err := RootAttributesPresent(root); err != nil || !present {
		t.Fatalf("present %v, %v", present, err)
	}
	if after, err := snapshot(child); err != nil || after != childBefore {
		t.Fatalf("the child changed: %q (%v)", after, err)
	}
	// Another entry for the capability, which the revoke must not take.
	other := "(A;;CC;;;" + supervision.ContainerCapabilitySID + ")"
	if _, err := rewriteDACL(root, "", func(dacl string) (string, error) {
		return strings.Replace(dacl, "(A;;0x100080;", other+"(A;;0x100080;", 1), nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		changed, err := RevokeRootAttributes(root)
		if err != nil || changed != want {
			t.Fatalf("revoke %d: changed %v, %v", i, changed, err)
		}
	}
	if present, err := RootAttributesPresent(root); err != nil || present {
		t.Fatalf("present after revoke %v, %v", present, err)
	}
	after, err := snapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after, other) || strings.Replace(after, other, "", 1) != before {
		t.Fatalf("after revoke %q, before %q", after, before)
	}
	roots, err := AttributeRoots()
	if err != nil || len(roots) != 2 || filepath.Dir(roots[0]) != roots[0] || filepath.VolumeName(roots[1]) != filepath.VolumeName(roots[0]) {
		t.Fatalf("roots %v, %v", roots, err)
	}
}
