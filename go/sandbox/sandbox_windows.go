//go:build windows

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	userenv              = windows.NewLazySystemDLL("userenv.dll")
	procCreateProfile    = userenv.NewProc("CreateAppContainerProfile")
	procDeleteProfile    = userenv.NewProc("DeleteAppContainerProfile")
	procDeriveSid        = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	procContainerFolder  = userenv.NewProc("GetAppContainerFolderPath")
	hresultAlreadyExists = uint32(0x800700B7)
)

// The access masks a grant writes, as specific file rights so an entry the
// system copies to what a folder holds reads the same.
const (
	maskReadExecute = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE
	maskModify      = maskReadExecute | windows.FILE_GENERIC_WRITE | windows.DELETE
	fileAllAccess   = 0x1F01FF
)

func mask(a Access) windows.ACCESS_MASK {
	switch a {
	case Modify:
		return maskModify
	case Attributes:
		return maskAttributes
	case ReadExecuteAttributes:
		return maskReadExecute | windows.FILE_WRITE_ATTRIBUTES
	}
	return maskReadExecute
}

// allApplicationPackages is the group every AppContainer is a member of.
const allApplicationPackages = "S-1-15-2-1"

// afterStep, set by tests only, runs after each step Setup records, so a
// test can stop an install where a crash would.
var afterStep func(step string) error

func step(name string) error {
	if afterStep != nil {
		return afterStep(name)
	}
	return nil
}

// createProfile creates the profile, or derives its SID when it exists
// already: one left by an install that stopped is this component's own.
func createProfile(name string) (*windows.SID, error) {
	if err := validateProfileName(name); err != nil {
		return nil, err
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	hr, _, _ := procCreateProfile.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(n)),
		0, 0, uintptr(unsafe.Pointer(&sid)))
	if uint32(hr) == hresultAlreadyExists {
		return deriveSID(name)
	}
	if hr != 0 {
		return nil, fmt.Errorf("sandbox: CreateAppContainerProfile %s: HRESULT 0x%08X", name, uint32(hr))
	}
	defer windows.FreeSid(sid)
	return sid.Copy()
}

// deriveSID is the SID the profile name has, whether or not it exists.
func deriveSID(name string) (*windows.SID, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var sid *windows.SID
	hr, _, _ := procDeriveSid.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&sid)))
	if hr != 0 {
		return nil, fmt.Errorf("sandbox: DeriveAppContainerSidFromAppContainerName %s: HRESULT 0x%08X", name, uint32(hr))
	}
	defer windows.FreeSid(sid)
	return sid.Copy()
}

// ProfileExists reports whether the profile whose SID is sid exists: its
// folder is known to the system.
func ProfileExists(sid string) bool {
	s, err := windows.UTF16PtrFromString(sid)
	if err != nil {
		return false
	}
	var p *uint16
	hr, _, _ := procContainerFolder.Call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&p)))
	if hr != 0 {
		return false
	}
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return true
}

// DeleteProfile deletes the profile name: its mapping and its folder. A
// profile that does not exist is no error.
func DeleteProfile(name string) error {
	if err := validateProfileName(name); err != nil {
		return err
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	if hr, _, _ := procDeleteProfile.Call(uintptr(unsafe.Pointer(n))); hr != 0 {
		return fmt.Errorf("sandbox: DeleteAppContainerProfile %s: HRESULT 0x%08X", name, uint32(hr))
	}
	return nil
}

func validateProfileName(name string) error {
	if strings.HasPrefix(name, "oa.app.") {
		parts := strings.Split(strings.TrimPrefix(name, "oa.app."), ".")
		if len(parts) < 1 || len(parts) > 2 {
			return fmt.Errorf("sandbox: %q is not one OA application profile", name)
		}
		label := ""
		if len(parts) == 2 {
			label = parts[1]
		}
		check, err := ApplicationProfileName(parts[0], label)
		if err != nil || check != name {
			return fmt.Errorf("sandbox: invalid application profile %q", name)
		}
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(name, "oa.engine."), ".")
	if !strings.HasPrefix(name, "oa.engine.") || len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("sandbox: %q is not one OA engine profile", name)
	}
	label := ""
	if len(parts) == 3 {
		label = parts[2]
	}
	check, err := ProfileName(parts[0], parts[1], label)
	if err != nil || check != name {
		return fmt.Errorf("sandbox: %q is not one OA engine profile: %v", name, err)
	}
	return nil
}

// ProfileSID derives the SID of an owner-chosen profile without creating it or
// granting anything. Persisted admission must still validate its trusted source.
func ProfileSID(profile string) (string, error) {
	if err := validateProfileName(profile); err != nil {
		return "", err
	}
	sid, err := deriveSID(profile)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

// validateManifestAuthority is the common boundary for every operation that
// trusts a persisted manifest. It binds the profile and SID, validates each
// saved ACL snapshot, and checks every extant grant against the same file ID
// Setup recorded. A missing grant is ambiguous: it may have been renamed
// with its AppContainer ACE still present elsewhere, so removal refuses it.
func validateManifestAuthority(m *Manifest) error {
	if err := validateProfileName(m.Profile); err != nil {
		return err
	}
	sid, err := deriveSID(m.Profile)
	if err != nil {
		return err
	}
	if m.SID != "" && m.SID != sid.String() || m.SID == "" && len(m.Grants) > 0 {
		return fmt.Errorf("sandbox: the manifest's profile and SID do not name one container")
	}
	if m.Phase != PhaseInstalling && m.Phase != PhaseReady && m.Phase != PhaseRemoving {
		return fmt.Errorf("sandbox: the manifest has an unknown phase %q", m.Phase)
	}
	for _, e := range m.Grants {
		if _, err := windows.SecurityDescriptorFromString(e.Before); err != nil {
			return fmt.Errorf("sandbox: the grant on %s has no valid original access list: %w", e.Path, err)
		}
		f, err := openACL(e.Path, false, e.ID)
		if err != nil {
			return fmt.Errorf("sandbox: recorded grant %s: %w", e.Path, err)
		}
		info, err := f.Stat()
		f.Close()
		// An Attributes entry is a folder's own, never inherited (KIT-S7).
		if err != nil || info.IsDir() != (e.Tree || e.Access == Attributes) || e.Access == Attributes && e.Tree {
			return fmt.Errorf("sandbox: recorded grant %s changed kind: %v", e.Path, err)
		}
	}
	return nil
}

// mappings is where the system records each profile of this account.
const mappings = `Software\Classes\Local Settings\Software\Microsoft\Windows\CurrentVersion\AppContainer\Mappings`

// Profiles is the name of every profile of this account that starts with
// prefix, as the system records it: a profile a crashed run left is found
// here (KIT-S5).
func Profiles(prefix string) ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, mappings, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer k.Close()
	sids, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	return profileNames(prefix, sids, func(sid string) (string, bool, error) {
		sk, err := registry.OpenKey(k, sid, registry.QUERY_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", true, err
		}
		moniker, _, err := sk.GetStringValue("Moniker")
		sk.Close()
		return moniker, true, err
	})
}

// checkPath refuses a grant path that is not absolute, not on a local drive
// (identity.LocalPath), absent, or a link or junction: a grant follows a
// link to whatever it names then, and a later revoke to whatever it names
// later. It reports whether the path is a folder.
func checkPath(p string) (bool, error) {
	info, err := pathInfo(p)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// pathInfo refuses a reparse point in any component. LocalPath checks the
// spelling and drive before Lstat opens even the first component.
func pathInfo(p string) (os.FileInfo, error) {
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("sandbox: %q is not an absolute path", p)
	}
	if err := identity.LocalPath(p); err != nil {
		return nil, err
	}
	var leaf os.FileInfo
	for at := filepath.Clean(p); ; at = filepath.Dir(at) {
		info, err := os.Lstat(at)
		if err != nil {
			return nil, err
		}
		if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return nil, fmt.Errorf("sandbox: %s is a link or a junction; grant the folder it names", at)
		}
		if leaf == nil {
			leaf = info
		}
		if filepath.Dir(at) == at {
			return leaf, nil
		}
	}
}

// CheckLocalPlacement validates an input before any setup or launch effect.
// A missing leaf is allowed, but every existing ancestor must be a local
// ordinary path rather than a junction or symlink.
func CheckLocalPlacement(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("sandbox: %q is not an absolute path", p)
	}
	if err := identity.LocalPath(p); err != nil {
		return err
	}
	for at := filepath.Clean(p); ; at = filepath.Dir(at) {
		info, err := os.Lstat(at)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("sandbox: %s is a link or a junction", at)
		}
		if filepath.Dir(at) == at {
			return nil
		}
	}
}

// openACL binds all ACL reads and writes to the same checked file object.
// Denying FILE_SHARE_DELETE keeps its name from being moved during the
// operation; FILE_FLAG_OPEN_REPARSE_POINT keeps a swapped leaf from following.
func openACL(path string, write bool, expectedID string) (*os.File, error) {
	want, err := pathInfo(path)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	if write {
		access |= windows.WRITE_DAC
	}
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	got, err := f.Stat()
	if err != nil || !os.SameFile(want, got) || got.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		f.Close()
		return nil, fmt.Errorf("sandbox: %s changed identity or became a link before its access list was opened: %v", path, err)
	}
	if expectedID != "" {
		id, err := identity.FileKeyOf(f)
		if err != nil || id != expectedID {
			f.Close()
			return nil, fmt.Errorf("sandbox: %s changed file identity since Setup: %s (%v)", path, id, err)
		}
	}
	return f, nil
}

// daclOf reads path's access list.
func daclOf(path string) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, error) {
	return daclOfID(path, "")
}

func daclOfID(path, expectedID string) (*windows.SECURITY_DESCRIPTOR, *windows.ACL, error) {
	f, err := openACL(path, false, expectedID)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox: open the access list of %s: %w", path, err)
	}
	defer f.Close()
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox: read the access list of %s: %w", path, err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox: the access list of %s: %w", path, err)
	}
	return sd, acl, nil
}

// snapshot is path's access list as SDDL: the record removal compares with.
func snapshot(path string) (string, error) {
	return snapshotID(path, "")
}

func snapshotID(path, expectedID string) (string, error) {
	sd, _, err := daclOfID(path, expectedID)
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

// withoutSID compares a saved access list with the final one after removal.
// A nested path can already have inherited this sandbox's parent grant when
// its own first snapshot is taken; that inherited entry belongs to Setup,
// not to a later change by someone else.
func withoutSID(sddl string, sid *windows.SID) (string, error) {
	if _, err := windows.SecurityDescriptorFromString(sddl); err != nil {
		return "", err
	}
	var out strings.Builder
	for i := 0; i < len(sddl); {
		if sddl[i] != '(' {
			out.WriteByte(sddl[i])
			i++
			continue
		}
		start, depth := i, 0
		for i < len(sddl) {
			if sddl[i] == '(' {
				depth++
			} else if sddl[i] == ')' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
			i++
		}
		if depth != 0 {
			return "", fmt.Errorf("sandbox: an access-list snapshot has an unclosed entry")
		}
		fields := strings.SplitN(sddl[start+1:i-1], ";", 7)
		if len(fields) < 6 || fields[5] != sid.String() {
			out.WriteString(sddl[start:i])
		}
	}
	return out.String(), nil
}

// changeACL grants sid access on path (inherited by what a folder holds), or
// revokes every entry for sid there with mode REVOKE_ACCESS; the system
// carries the change to what the folder holds.
func changeACL(path string, sid *windows.SID, access windows.ACCESS_MASK, tree bool, mode windows.ACCESS_MODE) error {
	return changeACLID(path, "", sid, access, tree, mode)
}

func changeACLID(path, expectedID string, sid *windows.SID, access windows.ACCESS_MASK, tree bool, mode windows.ACCESS_MODE) error {
	f, err := openACL(path, true, expectedID)
	if err != nil {
		return fmt.Errorf("sandbox: open the access list of %s: %w", path, err)
	}
	defer f.Close()
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	inherit := uint32(windows.NO_INHERITANCE)
	if tree {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: access, AccessMode: mode, Inheritance: inherit,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid)},
	}}, old)
	if err != nil {
		return fmt.Errorf("sandbox: build the access list of %s: %w", path, err)
	}
	if err := windows.SetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("sandbox: write the access list of %s: %w", path, err)
	}
	return nil
}

// ace is one entry of an access list.
type ace struct {
	kind, flags uint8
	mask        uint32
	sid         string
}

func entries(acl *windows.ACL) ([]ace, error) {
	if acl == nil {
		return nil, nil
	}
	out := make([]ace, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var a *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &a); err != nil {
			return nil, err
		}
		if a.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && a.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		out = append(out, ace{kind: a.Header.AceType, flags: a.Header.AceFlags, mask: specific(uint32(a.Mask)), sid: sid.String()})
	}
	return out, nil
}

// specific maps generic rights to the file rights they stand for.
func specific(m uint32) uint32 {
	out := m &^ (windows.GENERIC_READ | windows.GENERIC_WRITE | windows.GENERIC_EXECUTE | windows.GENERIC_ALL)
	if m&windows.GENERIC_READ != 0 {
		out |= windows.FILE_GENERIC_READ
	}
	if m&windows.GENERIC_WRITE != 0 {
		out |= windows.FILE_GENERIC_WRITE
	}
	if m&windows.GENERIC_EXECUTE != 0 {
		out |= windows.FILE_GENERIC_EXECUTE
	}
	if m&windows.GENERIC_ALL != 0 {
		out |= fileAllAccess
	}
	return out
}

// explicitGrant reports whether path carries this sandbox's own entry:
// explicit, applying to path itself, allowing at least access, and, on a
// folder, inherited by what it holds.
func explicitGrant(path, id, sid string, access Access, tree bool) (bool, error) {
	if access == Attributes {
		_, _, list, err := readSecurity(path, id)
		if err != nil {
			return false, err
		}
		return hasAttributeEntry(list, sid), nil
	}
	_, acl, err := daclOfID(path, id)
	if err != nil {
		return false, err
	}
	list, err := entries(acl)
	if err != nil {
		return false, err
	}
	want := uint32(mask(access))
	for _, a := range list {
		if a.kind != windows.ACCESS_ALLOWED_ACE_TYPE || a.sid != sid || a.flags&(windows.INHERITED_ACE|windows.INHERIT_ONLY_ACE) != 0 {
			continue
		}
		if tree && a.flags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
			continue
		}
		if a.mask&want == want {
			return true, nil
		}
	}
	return false, nil
}

// reaches reports whether path's own access list lets the container sid do
// access, through an entry for sid or ALL APPLICATION PACKAGES that applies
// to path, explicit or inherited, with no deny entry for either taking any
// of it away.
func reaches(path, sid string, access Access) (bool, error) {
	_, acl, err := daclOf(path)
	if err != nil {
		return false, err
	}
	list, err := entries(acl)
	if err != nil {
		return false, err
	}
	want := uint32(mask(access))
	var allowed uint32
	for _, a := range list {
		if a.flags&windows.INHERIT_ONLY_ACE != 0 || a.sid != sid && a.sid != allApplicationPackages {
			continue
		}
		if a.kind == windows.ACCESS_DENIED_ACE_TYPE && a.mask&want != 0 {
			return false, nil
		}
		if a.kind == windows.ACCESS_ALLOWED_ACE_TYPE {
			allowed |= a.mask
		}
	}
	return allowed&want == want, nil
}

// Holders is every path at or below root whose access list names sid: after
// a removal there is none (KIT-S4). Links and junctions below root are not
// followed.
func Holders(root, sid string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("sandbox: %s is a link or junction below a grant; remove it before removing the sandbox", p)
		}
		_, acl, err := daclOf(p)
		if err != nil {
			return err
		}
		list, err := entries(acl)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(list, func(a ace) bool { return a.sid == sid }) {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// openLock opens the sandbox's lock: alone for Setup, which may create it,
// shared for a launch, which only opens one Setup made.
func openLock(manifest string, alone bool) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(lockPath(manifest))
	if err != nil {
		return 0, err
	}
	access, share, disposition := uint32(windows.GENERIC_READ), uint32(windows.FILE_SHARE_READ), uint32(windows.OPEN_EXISTING)
	if alone {
		access, share, disposition = windows.GENERIC_READ|windows.GENERIC_WRITE, 0, windows.OPEN_ALWAYS
	}
	h, err := windows.CreateFile(p, access, share, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	switch {
	case err == nil:
		return h, nil
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION) && alone:
		return 0, ErrInUse
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return 0, ErrBusy
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return 0, fmt.Errorf("%w: no sandbox lock beside %s", ErrNotInstalled, manifest)
	}
	return 0, fmt.Errorf("sandbox: the lock beside %s: %w", manifest, err)
}

// checkManifestPath refuses a manifest path identity may not open.
func checkManifestPath(manifest string) error {
	if !filepath.IsAbs(manifest) {
		return fmt.Errorf("sandbox: the manifest %q is not an absolute path", manifest)
	}
	if err := identity.LocalPath(manifest); err != nil {
		return err
	}
	if _, err := pathInfo(filepath.Dir(manifest)); err != nil {
		return err
	}
	if _, err := os.Lstat(manifest); err == nil {
		_, err = pathInfo(manifest)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Setup is one install or update of a sandbox, holding its lock alone from
// Begin to Commit or Close (KIT-S2, KIT-S3).
type Setup struct {
	manifest string
	lock     windows.Handle
	m        *Manifest
	sid      *windows.SID
}

// Begin starts an install or update of the sandbox whose manifest is at
// manifest, for the profile name: it takes the lock alone (ErrInUse while
// an engine runs in it), records the phase installing before anything
// else, and creates the profile or adopts the one a stopped install left.
// The manifest's folder must exist.
func Begin(manifest, profile string) (*Setup, error) {
	if err := checkManifestPath(manifest); err != nil {
		return nil, err
	}
	if err := validateProfileName(profile); err != nil {
		return nil, err
	}
	lock, err := openLock(manifest, true)
	if err != nil {
		return nil, err
	}
	s := &Setup{manifest: manifest, lock: lock}
	fail := func(err error) (*Setup, error) {
		s.Close()
		return nil, err
	}
	m, err := ReadManifest(manifest)
	switch {
	case errors.Is(err, ErrNotInstalled):
		m = &Manifest{Format: manifestFormat, Profile: profile}
	case err != nil:
		return fail(err)
	case m.Profile != profile:
		return fail(fmt.Errorf("sandbox: %s is the sandbox of the profile %s, not %s", manifest, m.Profile, profile))
	}
	if !errors.Is(err, ErrNotInstalled) {
		if err := validateManifestAuthority(m); err != nil {
			return fail(err)
		}
	}
	s.m = m
	m.Phase = PhaseInstalling
	if err := writeManifest(manifest, m); err != nil {
		return fail(err)
	}
	if err := step("phase"); err != nil {
		return fail(err)
	}
	sid, err := createProfile(profile)
	if err != nil {
		return fail(err)
	}
	s.sid = sid
	m.SID = sid.String()
	if err := writeManifest(manifest, m); err != nil {
		return fail(err)
	}
	if err := step("profile"); err != nil {
		return fail(err)
	}
	return s, nil
}

// SID is the container's SID, for its launches and the pipes that admit it.
func (s *Setup) SID() string { return s.m.SID }

// Grant applies each grant once: a path new to the manifest is recorded
// with its access list's snapshot before it is changed; a path already
// granted is granted again, so files an update put in a granted folder
// inherit the grant. A grant only ever widens: a path granted Modify stays
// so when asked for ReadExecute.
func (s *Setup) Grant(grants ...Grant) error {
	return s.grant(grants, "")
}

// GrantBound applies one approved path only while its native file identity is
// the identity the resource owner approved. Every ACL handle rechecks that ID.
func (s *Setup) GrantBound(grant Grant, expectedID string) error {
	if expectedID == "" {
		return errors.New("sandbox: approved file identity required")
	}
	return s.grant([]Grant{grant}, expectedID)
}

func (s *Setup) grant(grants []Grant, expectedID string) error {
	for _, g := range grants {
		if !g.Access.valid() || g.Access == Attributes {
			return fmt.Errorf("sandbox: %q is no access Grant applies; GrantAncestorAttributes writes Attributes", g.Access)
		}
		tree, err := checkPath(g.Path)
		if err != nil {
			return err
		}
		id, err := identity.FileKey(g.Path)
		if err != nil {
			return err
		}
		if expectedID != "" && id != expectedID {
			return fmt.Errorf("sandbox: %s changed identity since approval", g.Path)
		}
		i := slices.IndexFunc(s.m.Grants, func(e Entry) bool {
			same, err := identity.SameFile(e.Path, g.Path)
			return err == nil && same
		})
		if i < 0 {
			_, acl, err := daclOfID(g.Path, id)
			if err != nil {
				return err
			}
			list, err := entries(acl)
			if err != nil {
				return err
			}
			parentOwned := slices.ContainsFunc(s.m.Grants, func(e Entry) bool {
				inside, err := identity.Within(g.Path, e.Path)
				return err == nil && inside && e.Tree
			})
			for _, a := range list {
				if a.sid == s.m.SID && (a.flags&windows.INHERITED_ACE == 0 || !parentOwned) {
					return fmt.Errorf("sandbox: %s already grants this container outside its recorded parent; its original access list cannot be restored", g.Path)
				}
			}
			before, err := snapshotID(g.Path, id)
			if err != nil {
				return err
			}
			s.m.Grants = append(s.m.Grants, Entry{Path: filepath.Clean(g.Path), ID: id, Access: g.Access, Tree: tree, Before: before})
			i = len(s.m.Grants) - 1
			if err := writeManifest(s.manifest, s.m); err != nil {
				return err
			}
			if err := step("recorded"); err != nil {
				return err
			}
		} else {
			if s.m.Grants[i].ID != id {
				return fmt.Errorf("sandbox: %s changed file identity since its first grant", g.Path)
			}
			if s.m.Grants[i].Access == Attributes {
				return fmt.Errorf("sandbox: %s holds an ancestor's Attributes entry; grant every path before GrantAncestorAttributes", g.Path)
			}
			if g.Access.covers(s.m.Grants[i].Access) {
				s.m.Grants[i].Access = g.Access
			}
		}
		e := &s.m.Grants[i]
		if err := changeACLID(e.Path, e.ID, s.sid, mask(e.Access), e.Tree, windows.GRANT_ACCESS); err != nil {
			return err
		}
		e.Applied = true
		if err := writeManifest(s.manifest, s.m); err != nil {
			return err
		}
		if err := step("applied"); err != nil {
			return err
		}
	}
	return nil
}

// Commit records the phase ready and lets launches in.
func (s *Setup) Commit() error {
	s.m.Phase = PhaseReady
	err := writeManifest(s.manifest, s.m)
	s.Close()
	return err
}

// Close lets go of the lock. Without Commit the phase stays installing,
// and every launch is refused until an install finishes or the sandbox is
// removed.
func (s *Setup) Close() {
	if s.lock != 0 {
		windows.CloseHandle(s.lock)
		s.lock = 0
	}
}

// Remove deletes the sandbox the manifest records: it takes the lock alone
// (ErrInUse while an engine runs in it), records the phase removing, revokes
// every entry for the container on each recorded path, applied or not,
// compares each path's access list with its snapshot, confirms no path at or
// below a recorded one still names the container, deletes the profile
// unless keepProfile, and deletes the manifest and its lock (KIT-S4).
func Remove(manifest string, keepProfile bool) (*Removal, error) {
	if err := checkManifestPath(manifest); err != nil {
		return nil, err
	}
	// Refuse a malformed or foreign authority before even creating a lock.
	preflight, err := ReadManifest(manifest)
	if err != nil {
		return nil, err
	}
	if err := validateManifestAuthority(preflight); err != nil {
		return nil, err
	}
	lock, err := openLock(manifest, true)
	if err != nil {
		return nil, err
	}
	m, err := ReadManifest(manifest)
	if err != nil {
		windows.CloseHandle(lock)
		return nil, err
	}
	if err := validateManifestAuthority(m); err != nil {
		windows.CloseHandle(lock)
		return nil, err
	}
	out := &Removal{Profile: m.Profile}
	m.Phase = PhaseRemoving
	if err := writeManifest(manifest, m); err != nil {
		windows.CloseHandle(lock)
		return nil, err
	}
	var sid *windows.SID
	if m.SID != "" {
		sid, err = windows.StringToSid(m.SID)
	} else {
		sid, err = deriveSID(m.Profile)
	}
	if err != nil {
		windows.CloseHandle(lock)
		return nil, err
	}
	var problems []error
	for i := len(m.Grants) - 1; i >= 0; i-- {
		e := m.Grants[i]
		if e.Access == Attributes {
			// An ancestor's entry is taken back without propagation, as it
			// was written (KIT-S7).
			if err := revokeAttributes(e, sid.String()); err != nil {
				problems = append(problems, err)
				continue
			}
			out.Revoked = append(out.Revoked, e.Path)
			continue
		}
		if err := changeACLID(e.Path, e.ID, sid, 0, e.Tree, windows.REVOKE_ACCESS); err != nil {
			problems = append(problems, err)
			continue
		}
		out.Revoked = append(out.Revoked, e.Path)
	}
	// A parent grant is inherited by a nested entry. Compare snapshots and
	// verify the whole tree only after every explicit grant is revoked.
	for _, e := range m.Grants {
		if e.Access == Attributes {
			// The entry was never inherited: the folder's own list is all
			// there is to compare and to search (KIT-S7).
			_, after, list, err := readSecurity(e.Path, e.ID)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			beforeComparable, beforeErr := withoutSID(e.Before, sid)
			afterComparable, afterErr := withoutSID(after, sid)
			if err := errors.Join(beforeErr, afterErr); err != nil {
				problems = append(problems, err)
			} else if beforeComparable != afterComparable {
				out.Changed = append(out.Changed, e.Path)
			}
			if namesSID(list, sid.String()) {
				problems = append(problems, fmt.Errorf("sandbox: %s still names the container", e.Path))
			}
			continue
		}
		if after, err := snapshotID(e.Path, e.ID); err != nil {
			problems = append(problems, err)
		} else {
			beforeComparable, beforeErr := withoutSID(e.Before, sid)
			afterComparable, afterErr := withoutSID(after, sid)
			if err := errors.Join(beforeErr, afterErr); err != nil {
				problems = append(problems, err)
			} else if beforeComparable != afterComparable {
				out.Changed = append(out.Changed, e.Path)
			}
		}
		holders, err := Holders(e.Path, sid.String())
		if err != nil {
			problems = append(problems, err)
		} else if len(holders) > 0 {
			problems = append(problems, fmt.Errorf("sandbox: %d paths below %s still name the container, first %s", len(holders), e.Path, holders[0]))
		}
	}
	if len(problems) > 0 {
		windows.CloseHandle(lock)
		return out, errors.Join(problems...)
	}
	if !keepProfile {
		if err := DeleteProfile(m.Profile); err != nil {
			windows.CloseHandle(lock)
			return out, err
		}
		if ProfileExists(sid.String()) {
			windows.CloseHandle(lock)
			return out, fmt.Errorf("sandbox: the profile %s is still there after its deletion", m.Profile)
		}
		out.ProfileDeleted = true
	}
	if err := os.Remove(manifest); err != nil {
		windows.CloseHandle(lock)
		return out, err
	}
	windows.CloseHandle(lock)
	//unchecked: a lock file without a manifest beside it admits no launch (ErrNotInstalled)
	os.Remove(lockPath(manifest))
	return out, nil
}

// A Launch is a checked sandbox one engine start runs in: the container's
// SID and a shared hold on its lock, kept until the engine has ended, so
// Setup does not change the sandbox under it (KIT-S3).
type Launch struct {
	Profile, SID string
	lock         windows.Handle
}

// Prepare checks the sandbox the manifest records before a launch (KIT-S3):
// Setup holds no lock on it and finished (ErrBusy), the profile exists
// with the recorded SID and every recorded grant is still on its path
// (ErrNotInstalled, ErrNotGranted), each path in needs lies at or below a
// grant that covers its access, and each carries that grant in its own
// access list, which a file moved into a granted folder after Setup does
// not (ErrNotGranted). Every path goes through identity's file identity.
func Prepare(manifest string, needs []Grant) (*Launch, error) {
	if err := checkManifestPath(manifest); err != nil {
		return nil, err
	}
	lock, err := openLock(manifest, false)
	if err != nil {
		return nil, err
	}
	l := &Launch{lock: lock}
	fail := func(err error) (*Launch, error) {
		l.Close()
		return nil, err
	}
	m, err := ReadManifest(manifest)
	if err != nil {
		return fail(err)
	}
	if err := validateManifestAuthority(m); err != nil {
		return fail(err)
	}
	if m.Phase != PhaseReady {
		return fail(fmt.Errorf("%w (%s)", ErrBusy, m.Phase))
	}
	sid, err := deriveSID(m.Profile)
	if err != nil {
		return fail(err)
	}
	if sid.String() != m.SID || !ProfileExists(m.SID) {
		return fail(fmt.Errorf("%w: the profile %s is missing; run Setup again", ErrNotInstalled, m.Profile))
	}
	for _, e := range m.Grants {
		ok, err := explicitGrant(e.Path, e.ID, m.SID, e.Access, e.Tree)
		if err != nil {
			return fail(fmt.Errorf("%w: %v", ErrNotGranted, err))
		}
		if !ok {
			return fail(fmt.Errorf("%w: the grant on %s is gone; run Setup again", ErrNotGranted, e.Path))
		}
	}
	for _, need := range needs {
		if !need.Access.valid() || !filepath.IsAbs(need.Path) {
			return fail(fmt.Errorf("sandbox: the need %q %q", need.Path, need.Access))
		}
		covered := slices.ContainsFunc(m.Grants, func(e Entry) bool {
			if !e.Access.covers(need.Access) {
				return false
			}
			if same, err := identity.SameFile(need.Path, e.Path); err == nil && same {
				return true
			}
			inside, err := identity.Within(need.Path, e.Path)
			return err == nil && inside && e.Tree
		})
		if !covered {
			return fail(fmt.Errorf("%w: %s (%s) lies under no grant of the profile %s", ErrNotGranted, need.Path, need.Access, m.Profile))
		}
		ok, err := reaches(need.Path, m.SID, need.Access)
		if err != nil {
			return fail(fmt.Errorf("%w: %v", ErrNotGranted, err))
		}
		if !ok {
			return fail(fmt.Errorf("%w: %s does not carry its folder's grant, as a file moved in after Setup does not; run Setup again", ErrNotGranted, need.Path))
		}
	}
	l.Profile, l.SID = m.Profile, m.SID
	return l, nil
}

// Close lets go of the launch's hold; Setup may change the sandbox once no
// launch holds it.
func (l *Launch) Close() {
	if l != nil && l.lock != 0 {
		windows.CloseHandle(l.lock)
		l.lock = 0
	}
}
