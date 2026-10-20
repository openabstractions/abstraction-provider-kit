//go:build windows

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

// maskAttributes is SYNCHRONIZE and FILE_READ_ATTRIBUTES: what a program
// needs to lstat a folder, and nothing more (KIT-S7).
const maskAttributes = windows.SYNCHRONIZE | windows.FILE_READ_ATTRIBUTES

// attributeACE is the one entry an Attributes grant writes: allowed, no
// inheritance flag, so what the folder holds never receives it.
func attributeACE(sid string) string {
	return fmt.Sprintf("(A;;0x%x;;;%s)", maskAttributes, sid)
}

// A securityFile is one path's security, read and written through a handle
// bound to the identity Setup recorded. WriteDACL replaces the access list of
// that object alone: SetKernelObjectSecurity is NtSetSecurityObject, which
// propagates nothing to what a folder holds. SetSecurityInfo,
// SetNamedSecurityInfo and icacls re-propagate inheritance through the whole
// tree below, which on a drive root or a profile folder rewrites every file.
type securityFile interface {
	// Read returns the owner's SID and the access list as SDDL ("D:...").
	Read() (owner, dacl string, err error)
	WriteDACL(dacl string) error
	Close() error
}

// openSecurity opens path's security; tests replace it with a fake writer.
var openSecurity = func(path, id string, write bool) (securityFile, error) {
	f, err := openACL(path, write, id)
	if err != nil {
		return nil, err
	}
	return handleSecurity{f}, nil
}

type handleSecurity struct{ f *os.File }

func (h handleSecurity) Read() (string, string, error) {
	handle := windows.Handle(h.f.Fd())
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", "", err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", "", err
	}
	dacl, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", "", err
	}
	ownerSID := ""
	if owner != nil {
		ownerSID = owner.String()
	}
	return ownerSID, dacl.String(), nil
}

// WriteDACL keeps the list's control flags: the kernel marks a DACL
// auto-inherited ("AI") only when the write asks for it with
// SE_DACL_AUTO_INHERIT_REQ, which keeps the object's inherited entries and
// takes the explicit ones from the new list, and protected ("P") only with
// PROTECTED_DACL_SECURITY_INFORMATION. Neither reaches any other object.
func (h handleSecurity) WriteDACL(dacl string) error {
	sd, err := windows.SecurityDescriptorFromString(dacl)
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_AUTO_INHERITED != 0 {
		if err := sd.SetControl(windows.SE_DACL_AUTO_INHERIT_REQ, windows.SE_DACL_AUTO_INHERIT_REQ); err != nil {
			return err
		}
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if control&windows.SE_DACL_PROTECTED != 0 {
		info = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	return windows.SetKernelObjectSecurity(windows.Handle(h.f.Fd()), info, sd)
}

func (h handleSecurity) Close() error { return h.f.Close() }

// aceSpans finds each top-level entry "(...)" of an SDDL string, a
// conditional entry's nested parentheses included.
func aceSpans(sddl string) ([][2]int, error) {
	var out [][2]int
	for i := 0; i < len(sddl); i++ {
		if sddl[i] != '(' {
			continue
		}
		start, depth := i, 0
		for ; i < len(sddl); i++ {
			if sddl[i] == '(' {
				depth++
			} else if sddl[i] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if depth != 0 {
			return nil, errors.New("sandbox: an access list has an unclosed entry")
		}
		out = append(out, [2]int{start, i + 1})
	}
	return out, nil
}

// aceFields splits one "(...)" entry into its SDDL fields: type, flags,
// rights, object, inherited object, SID and any condition.
func aceFields(entry string) []string {
	return strings.SplitN(entry[1:len(entry)-1], ";", 7)
}

// inheritedEntry reports whether an entry's flags hold ID, read as the
// two-letter tokens SDDL writes flags in.
func inheritedEntry(flags string) bool {
	for i := 0; i+1 < len(flags); i += 2 {
		if flags[i:i+2] == "ID" {
			return true
		}
	}
	return false
}

// withAttributeACE inserts the Attributes entry for sid after the explicit
// entries and before the inherited ones, the order Windows keeps.
func withAttributeACE(dacl, sid string) (string, error) {
	if !strings.HasPrefix(dacl, "D:") || strings.Contains(dacl, "NO_ACCESS_CONTROL") {
		return "", fmt.Errorf("sandbox: %q is no access list an entry can be added to", dacl)
	}
	spans, err := aceSpans(dacl)
	if err != nil {
		return "", err
	}
	at := len(dacl)
	for _, s := range spans {
		if f := aceFields(dacl[s[0]:s[1]]); len(f) >= 6 && inheritedEntry(f[1]) {
			at = s[0]
			break
		}
	}
	return dacl[:at] + attributeACE(sid) + dacl[at:], nil
}

// withoutExplicit removes sid's explicit allowed entries: every one, or with
// exact only the Attributes entry withAttributeACE writes.
func withoutExplicit(dacl, sid string, exact bool) (string, error) {
	spans, err := aceSpans(dacl)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	last := 0
	for _, s := range spans {
		f := aceFields(dacl[s[0]:s[1]])
		if len(f) < 6 || f[5] != sid || inheritedEntry(f[1]) || f[0] != "A" {
			continue
		}
		if exact {
			m, err := strconv.ParseUint(f[2], 0, 32)
			if f[1] != "" || err != nil || m != maskAttributes {
				continue
			}
		}
		out.WriteString(dacl[last:s[0]])
		last = s[1]
	}
	out.WriteString(dacl[last:])
	return out.String(), nil
}

// aclEntries parses an SDDL access list into entries.
func aclEntries(dacl string) ([]ace, error) {
	sd, err := windows.SecurityDescriptorFromString(dacl)
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	return entries(acl)
}

// allows reports whether the entries let one of sids do all of want: entries
// that apply to the object itself, with no deny entry for those SIDs taking
// any of it away. ALL APPLICATION PACKAGES and capability SIDs count for an
// AppContainer the way groups count for an account.
func allows(list []ace, want uint32, sids ...string) bool {
	var allowed uint32
	for _, a := range list {
		if a.flags&windows.INHERIT_ONLY_ACE != 0 || !slices.Contains(sids, a.sid) {
			continue
		}
		if a.kind == windows.ACCESS_DENIED_ACE_TYPE && a.mask&want != 0 {
			return false
		}
		if a.kind == windows.ACCESS_ALLOWED_ACE_TYPE {
			allowed |= a.mask
		}
	}
	return allowed&want == want
}

// accountControls reports whether the account itself, owner or named entry,
// may rewrite the access list. Rights through a group, Administrators
// included, do not count: a session never writes a list only an
// administrator or a shared group controls.
func accountControls(owner string, list []ace, user string) bool {
	if owner == user {
		return true
	}
	return allows(list, windows.WRITE_DAC, user)
}

// namesSID reports whether any entry names sid.
func namesSID(list []ace, sid string) bool {
	return slices.ContainsFunc(list, func(a ace) bool { return a.sid == sid })
}

// hasAttributeEntry reports whether sid holds an explicit entry that allows
// at least Attributes on the object itself.
func hasAttributeEntry(list []ace, sid string) bool {
	return slices.ContainsFunc(list, func(a ace) bool {
		return a.kind == windows.ACCESS_ALLOWED_ACE_TYPE && a.sid == sid &&
			a.flags&(windows.INHERITED_ACE|windows.INHERIT_ONLY_ACE) == 0 && a.mask&maskAttributes == maskAttributes
	})
}

// readSecurity reads path's owner and access list through one bound handle.
func readSecurity(path, id string) (string, string, []ace, error) {
	f, err := openSecurity(path, id, false)
	if err != nil {
		return "", "", nil, fmt.Errorf("sandbox: open the access list of %s: %w", path, err)
	}
	owner, dacl, err := f.Read()
	cerr := f.Close()
	if err = errors.Join(err, cerr); err != nil {
		return "", "", nil, fmt.Errorf("sandbox: read the access list of %s: %w", path, err)
	}
	list, err := aclEntries(dacl)
	if err != nil {
		return "", "", nil, fmt.Errorf("sandbox: the access list of %s: %w", path, err)
	}
	return owner, dacl, list, nil
}

// rewriteDACL changes path's access list with edit, through one handle bound
// to id, without propagation, and checks that the list Windows then holds is
// the one written. It reports whether anything changed.
func rewriteDACL(path, id string, edit func(string) (string, error)) (bool, error) {
	f, err := openSecurity(path, id, true)
	if err != nil {
		return false, fmt.Errorf("sandbox: open the access list of %s: %w", path, err)
	}
	defer f.Close()
	_, dacl, err := f.Read()
	if err != nil {
		return false, fmt.Errorf("sandbox: read the access list of %s: %w", path, err)
	}
	next, err := edit(dacl)
	if err != nil || next == dacl {
		return false, err
	}
	want, err := windows.SecurityDescriptorFromString(next)
	if err != nil {
		return false, fmt.Errorf("sandbox: the new access list of %s: %w", path, err)
	}
	if err := f.WriteDACL(next); err != nil {
		return false, fmt.Errorf("sandbox: write the access list of %s: %w", path, err)
	}
	_, got, err := f.Read()
	if err != nil {
		return true, fmt.Errorf("sandbox: read back the access list of %s: %w", path, err)
	}
	if got != want.String() {
		return true, fmt.Errorf("sandbox: %s holds %q after writing %q", path, got, want.String())
	}
	return true, nil
}

// ancestor is one folder above a granted path and its file identity.
type ancestor struct{ path, id string }

// ungrantedAncestors is every folder above a granted path that no folder
// grant of the manifest covers, each once by file identity, deepest first.
// With floor, only folders at or below floor count (a test's own tree).
func (s *Setup) ungrantedAncestors(floor string) ([]ancestor, error) {
	floorID := ""
	if floor != "" {
		var err error
		if floorID, err = identity.FileKey(floor); err != nil {
			return nil, err
		}
	}
	var trees []Entry
	for _, e := range s.m.Grants {
		if e.Tree && e.Access != Attributes {
			trees = append(trees, e)
		}
	}
	var out []ancestor
	seen := map[string]bool{}
	for _, e := range s.m.Grants {
		if e.Access == Attributes {
			continue
		}
		for at := filepath.Dir(e.Path); ; at = filepath.Dir(at) {
			if err := identity.LocalPath(at); err != nil {
				return nil, err
			}
			id, err := identity.FileKey(at)
			if err != nil {
				return nil, err
			}
			if floor != "" && id != floorID {
				inside, err := identity.Within(at, floor)
				if err != nil {
					return nil, err
				}
				if !inside {
					break
				}
			}
			covered := false
			for _, t := range trees {
				inside, err := identity.Within(at, t.Path)
				if err != nil {
					return nil, err
				}
				if t.ID == id || inside {
					covered = true
					break
				}
			}
			if !covered && !seen[id] {
				seen[id] = true
				out = append(out, ancestor{path: at, id: id})
			}
			if filepath.Dir(at) == at {
				break
			}
		}
	}
	return out, nil
}

// GrantAncestorAttributes lets the container read the attributes of each
// folder above a granted path that no folder grant covers, as a program that
// resolves its folder by lstat-ing every ancestor needs (KIT-S7). It runs
// after Grant. A folder the container already reaches is left alone; a
// folder whose access list this account controls is recorded with its
// snapshot, then given one non-inherited Attributes entry for the container
// without propagation; any other folder, such as a drive root, is reported in
// Unreached and left alone. Remove takes the entries back, after a crash too.
func (s *Setup) GrantAncestorAttributes() (*Ancestors, error) { return s.grantAncestors("") }

func (s *Setup) grantAncestors(floor string) (*Ancestors, error) {
	ancestors, err := s.ungrantedAncestors(floor)
	if err != nil {
		return nil, err
	}
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	user := token.User.Sid.String()
	out := &Ancestors{}
	for _, a := range ancestors {
		i := slices.IndexFunc(s.m.Grants, func(e Entry) bool { return e.ID == a.id })
		if i >= 0 && s.m.Grants[i].Access != Attributes {
			return nil, fmt.Errorf("sandbox: %s is granted and also above a grant", a.path)
		}
		if i < 0 {
			owner, dacl, list, err := readSecurity(a.path, a.id)
			if err != nil {
				return nil, err
			}
			switch {
			case allows(list, maskAttributes, s.m.SID, allApplicationPackages, supervision.ContainerCapabilitySID):
				out.Reached = append(out.Reached, a.path)
				continue
			case !accountControls(owner, list, user):
				out.Unreached = append(out.Unreached, a.path)
				continue
			case namesSID(list, s.m.SID):
				return nil, fmt.Errorf("sandbox: %s already names this container outside the manifest; its original access list cannot be restored", a.path)
			}
			s.m.Grants = append(s.m.Grants, Entry{Path: a.path, ID: a.id, Access: Attributes, Before: dacl})
			i = len(s.m.Grants) - 1
			if err := writeManifest(s.manifest, s.m); err != nil {
				return nil, err
			}
			if err := step("recorded"); err != nil {
				return nil, err
			}
		}
		e := &s.m.Grants[i]
		if _, err := rewriteDACL(e.Path, e.ID, func(dacl string) (string, error) {
			list, err := aclEntries(dacl)
			if err != nil || hasAttributeEntry(list, s.m.SID) {
				return dacl, err
			}
			return withAttributeACE(dacl, s.m.SID)
		}); err != nil {
			return nil, err
		}
		e.Applied = true
		if err := writeManifest(s.manifest, s.m); err != nil {
			return nil, err
		}
		if err := step("applied"); err != nil {
			return nil, err
		}
		out.Granted = append(out.Granted, e.Path)
	}
	return out, nil
}

// revokeAttributes removes every explicit entry for sid from one recorded
// ancestor, without propagation.
func revokeAttributes(e Entry, sid string) error {
	_, err := rewriteDACL(e.Path, e.ID, func(dacl string) (string, error) { return withoutExplicit(dacl, sid, false) })
	return err
}

// AttributeRoots are the two folders above every account's files whose
// access lists only an administrator changes: the system drive's root and
// the profiles folder.
//
// TEMPORARY EXCEPTION (TODO.md "Temporary exception: sandbox root
// attributes"): the per-machine installer gives the facade's
// ContainerCapabilitySID Attributes on these two, so every OA container can
// read their attributes. Remove these functions, the installer's custom
// actions and the capability once the application that needs it resolves
// its folders natively, released and pinned.
func AttributeRoots() ([]string, error) {
	windowsDir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return nil, err
	}
	root := filepath.VolumeName(windowsDir) + `\`
	profiles, err := windows.KnownFolderPath(windows.FOLDERID_UserProfiles, 0)
	if err != nil {
		return nil, err
	}
	out := []string{root, filepath.Clean(profiles)}
	for _, p := range out {
		if err := identity.LocalPath(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GrantRootAttributes adds one non-inherited Attributes entry for the
// facade's ContainerCapabilitySID to path's access list without propagation.
// An entry already there is no change. It needs an administrator for an
// AttributeRoots folder. TEMPORARY EXCEPTION; see AttributeRoots.
func GrantRootAttributes(path string) (bool, error) {
	return rewriteDACL(path, "", func(dacl string) (string, error) {
		list, err := aclEntries(dacl)
		if err != nil || hasAttributeEntry(list, supervision.ContainerCapabilitySID) {
			return dacl, err
		}
		return withAttributeACE(dacl, supervision.ContainerCapabilitySID)
	})
}

// RevokeRootAttributes removes exactly the entry GrantRootAttributes writes,
// by SID, rights and flags, without propagation; any other entry for the
// capability stays. TEMPORARY EXCEPTION; see AttributeRoots.
func RevokeRootAttributes(path string) (bool, error) {
	return rewriteDACL(path, "", func(dacl string) (string, error) {
		return withoutExplicit(dacl, supervision.ContainerCapabilitySID, true)
	})
}

// RootAttributesPresent reports whether path holds GrantRootAttributes'
// entry.
func RootAttributesPresent(path string) (bool, error) {
	_, _, list, err := readSecurity(path, "")
	if err != nil {
		return false, err
	}
	return hasAttributeEntry(list, supervision.ContainerCapabilitySID), nil
}
