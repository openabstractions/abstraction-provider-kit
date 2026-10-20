package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/windows"
)

type heldTree struct {
	plan  *treeEdit
	files []*os.File
}

func (h *heldTree) close() {
	for _, f := range h.files {
		f.Close()
	}
}

func editManifestTree(path string, disk *manifestDisk, sid *windows.SID, mode string, held *heldTree) ([]string, error) {
	var err error
	if held == nil {
		held, err = pinTree(disk.TreeEdit)
		if err != nil {
			return nil, err
		}
	}
	defer held.close()
	previousFormat, previousLegacy, previousMode := disk.Format, disk.Legacy, held.plan.Mode
	if disk.Format == 2 && len(disk.Grants) != 0 {
		disk.Legacy = true
	}
	disk.Format = manifestFormat
	held.plan.Mode = mode
	disk.TreeEdit = held.plan
	if err := writeManifestDisk(path, disk); err != nil {
		disk.Format, disk.Legacy, held.plan.Mode = previousFormat, previousLegacy, previousMode
		return nil, err
	}
	if err := step("tree captured"); err != nil {
		return nil, err
	}
	changed, err := held.apply(sid, mode)
	if err != nil {
		return changed, err
	}
	disk.TreeEdit = nil
	if err := writeManifestDisk(path, disk); err != nil {
		disk.TreeEdit = held.plan
		return changed, err
	}
	return changed, nil
}

func snapshotFile(f *os.File) (string, error) {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

// captureTree pins every selected name and captures every original descriptor
// before a parent write can change inheritance on its children.
func captureTree(path, expectedID string, access Access, tree bool, mode string) (_ *heldTree, err error) {
	h := &heldTree{plan: &treeEdit{Root: path, Access: access, Mode: mode}}
	defer func() {
		if err != nil {
			h.close()
		}
	}()
	capture := func(p string, directory bool) error {
		id, err := identity.FileKey(p)
		if err != nil {
			return err
		}
		if p == path && expectedID != "" && id != expectedID {
			return errors.New("sandbox: grant root changed identity")
		}
		f, err := openACL(p, true, id)
		if err != nil {
			return err
		}
		h.files = append(h.files, f)
		before, err := snapshotFile(f)
		if err != nil {
			return err
		}
		h.plan.Objects = append(h.plan.Objects, treeObject{Path: p, ID: id, Directory: directory, Before: before})
		return nil
	}
	if !tree {
		err = capture(path, false)
	} else {
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
				return fmt.Errorf("sandbox: %s is a link or junction below a grant", p)
			}
			return capture(p, d.IsDir())
		})
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

func pinTree(plan *treeEdit) (_ *heldTree, err error) {
	h := &heldTree{plan: plan}
	defer func() {
		if err != nil {
			h.close()
		}
	}()
	for _, object := range plan.Objects {
		f, err := openACL(object.Path, true, object.ID)
		if err != nil {
			return nil, err
		}
		h.files = append(h.files, f)
		info, err := f.Stat()
		if err != nil || info.IsDir() != object.Directory {
			return nil, fmt.Errorf("sandbox: captured tree object changed kind: %s (%v)", object.Path, err)
		}
	}
	return h, nil
}

func validateTreeEdit(disk *manifestDisk) error {
	plan := disk.TreeEdit
	if plan == nil {
		return nil
	}
	if disk.Format != manifestFormat || plan.Mode != "grant" && plan.Mode != "revoke" || len(plan.Objects) == 0 {
		return errors.New("sandbox: invalid captured tree edit")
	}
	var root *Entry
	for i := range disk.Grants {
		if disk.Grants[i].Path == plan.Root {
			root = &disk.Grants[i]
			break
		}
	}
	if root == nil || root.Access == Attributes || root.Access != plan.Access || plan.Objects[0].Path != root.Path || plan.Objects[0].ID != root.ID {
		return errors.New("sandbox: captured tree edit is not its recorded grant")
	}
	seen := map[string]bool{}
	for _, object := range plan.Objects {
		inside, err := identity.Within(object.Path, root.Path)
		if err != nil || !filepath.IsAbs(object.Path) || object.ID == "" || seen[object.Path] || object.Path != root.Path && (!root.Tree || !inside) {
			return fmt.Errorf("sandbox: captured object is outside its grant: %s (%v)", object.Path, err)
		}
		if _, err := windows.SecurityDescriptorFromString(object.Before); err != nil {
			return fmt.Errorf("sandbox: captured object has no valid security descriptor: %s", object.Path)
		}
		seen[object.Path] = true
	}
	h, err := pinTree(plan)
	if err == nil {
		h.close()
	}
	return err
}

// protectedExplicit is the exact inheritance rewrite observed when Windows
// changes a legacy parent's inheritable ACL. It changes no SID or right. This
// recognizes an operation's own rewrite; final comparisons stay byte-exact.
func protectedExplicit(dacl string) (string, error) {
	spans, err := aceSpans(dacl)
	if err != nil || !strings.HasPrefix(dacl, "D:") || strings.Contains(dacl, "NO_ACCESS_CONTROL") {
		return "", errors.New("sandbox: cannot recognize captured inheritance")
	}
	at := len(dacl)
	if len(spans) != 0 {
		at = spans[0][0]
	}
	prefix := dacl[:at]
	if !strings.Contains(prefix, "P") {
		prefix = "D:P" + prefix[2:]
	}
	var out strings.Builder
	out.WriteString(prefix)
	for _, span := range spans {
		fields := aceFields(dacl[span[0]:span[1]])
		if len(fields) < 6 {
			return "", errors.New("sandbox: malformed captured entry")
		}
		fields[1] = strings.ReplaceAll(fields[1], "ID", "")
		out.WriteString("(" + strings.Join(fields, ";") + ")")
	}
	sd, err := windows.SecurityDescriptorFromString(out.String())
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

func grantDACL(dacl string, sid *windows.SID, access windows.ACCESS_MASK, directory bool) (string, error) {
	list, err := aclEntries(dacl)
	if err != nil {
		return "", err
	}
	wanted := uint32(access)
	for _, a := range list {
		if a.sid == sid.String() && a.kind == windows.ACCESS_ALLOWED_ACE_TYPE && a.flags&windows.INHERIT_ONLY_ACE == 0 {
			wanted |= a.mask
		}
	}
	kept, err := withoutExplicit(dacl, sid.String(), false)
	if err != nil {
		return "", err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	return withAllowedACE(kept, fmt.Sprintf("(A;%s;0x%x;;;%s)", flags, wanted, sid.String()))
}

func (h *heldTree) apply(sid *windows.SID, mode string) ([]string, error) {
	var changed []string
	wants := make([]string, len(h.files))
	for i, object := range h.plan.Objects {
		saved, err := windows.SecurityDescriptorFromString(object.Before)
		if err != nil {
			return changed, err
		}
		before, err := daclSDDL(saved)
		if err != nil {
			return changed, err
		}
		_, current, err := (handleSecurity{h.files[i]}).Read()
		if err != nil {
			return changed, err
		}
		a, err := withoutSID(before, sid)
		if err != nil {
			return changed, err
		}
		b, err := withoutSID(current, sid)
		if err != nil {
			return changed, err
		}
		normalized, err := protectedExplicit(a)
		if err != nil {
			return changed, err
		}
		if b != a && b != normalized {
			if mode == "grant" {
				return changed, fmt.Errorf("sandbox: %s changed after its tree was captured", object.Path)
			}
			// Preserve a foreign edit rather than restoring stale permissions.
			before = current
			changed = append(changed, object.Path)
		}
		var next string
		if mode == "revoke" {
			next, err = withoutSID(before, sid)
		} else if mode == "grant" {
			next, err = grantDACL(before, sid, mask(h.plan.Access), object.Directory)
		} else {
			err = errors.New("sandbox: unsupported captured edit")
		}
		if err != nil {
			return changed, err
		}
		want, err := windows.SecurityDescriptorFromString(next)
		if err != nil {
			return changed, err
		}
		wants[i] = want.String()
		if err := (handleSecurity{h.files[i]}).WriteDACL(next); err != nil {
			return changed, err
		}
		if mode == "grant" {
			if err := step("tree object applied"); err != nil {
				return changed, err
			}
		}
	}
	for i, f := range h.files {
		_, actual, err := (handleSecurity{f}).Read()
		if err != nil || actual != wants[i] {
			return changed, fmt.Errorf("sandbox: captured tree readback %s: got %q, want %q (%v)", h.plan.Objects[i].Path, actual, wants[i], err)
		}
	}
	return changed, nil
}
