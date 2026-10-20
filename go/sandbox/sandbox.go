// Package sandbox runs an engine OA launches inside a Windows AppContainer
// with no capability but the facade's ContainerCapabilitySID, which no
// network or device access attaches to: one profile per engine kind, the file grants the
// engine needs applied once at install or update with a manifest and a
// snapshot of each granted path's access list, a launch check that refuses
// when they are not in place, and a removal that deletes the profile and
// every grant and proves each list is as it was (CONTRACT.md KIT-S1..KIT-S7;
// research/engine-sandbox/REPORT.md).
//
// The container is the engine's network containment: a capability-less
// AppContainer reaches no TCP, DNS or UDP, its children included, and needs
// no administrator. Process creation inside it is the facade's
// (supervision.Options.Container, FAC-P3); this package owns everything the
// container is given.
package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

// Access is what a grant lets the container do with a path and what it
// holds.
type Access string

const (
	// ReadExecute reads, lists and runs: an interpreter, a venv, an engine's
	// own script.
	ReadExecute Access = "rx"
	// Modify also writes and deletes: an engine's caches, temporary and
	// working folders.
	Modify Access = "rwd"
	// Attributes lets the container read one folder's attributes and wait
	// on it (SYNCHRONIZE and FILE_READ_ATTRIBUTES), and nothing it holds: the
	// entry is not inherited. Only Setup.GrantAncestorAttributes grants it,
	// on a granted path's ancestors (KIT-S7).
	Attributes Access = "sa"
	// ReadExecuteAttributes is ReadExecute plus FILE_WRITE_ATTRIBUTES: the
	// container may also set the timestamps and attribute bits of what it
	// reads, and still writes no content and deletes nothing.
	//
	// TEMPORARY EXCEPTION (TODO.md "Temporary exception: sandbox root
	// attributes"): Bun 1.3.14's module loader opens every module file with
	// FILE_WRITE_ATTRIBUTES, so a JavaScript package an application imports
	// inside the container needs it (live run 2026-10-06). Remove it when
	// Bun opens modules for reading only, released and pinned.
	ReadExecuteAttributes Access = "rxa"
)

// covers reports whether a grant of a gives everything b asks.
func (a Access) covers(b Access) bool {
	switch {
	case a == b:
		return true
	case b == Attributes:
		return a == Modify || a == ReadExecute || a == ReadExecuteAttributes
	case b == ReadExecute:
		return a == Modify || a == ReadExecuteAttributes
	case b == ReadExecuteAttributes:
		return a == Modify
	}
	return false
}

func (a Access) valid() bool {
	return a == ReadExecute || a == Modify || a == Attributes || a == ReadExecuteAttributes
}

// A Grant names one path and what the container may do with it. A folder's
// grant reaches everything it holds, now and later.
type Grant struct {
	Path   string `json:"path"`
	Access Access `json:"access"`
}

// Phase is where Setup stands with a component's sandbox.
type Phase string

const (
	// PhaseInstalling: Setup is installing or updating, or stopped before
	// it finished; no engine launches.
	PhaseInstalling Phase = "installing"
	// PhaseReady: every grant is applied; engines launch.
	PhaseReady Phase = "ready"
	// PhaseRemoving: Setup is removing the sandbox; no engine launches.
	PhaseRemoving Phase = "removing"
)

// An Entry is one grant as the manifest records it: the path, the access,
// whether it is a folder whose grant its contents inherit, the path's access
// list before the first grant (SDDL) and whether the grant was applied.
// The entry is written before the grant, so a crash between the two leaves
// a path removal still revokes.
type Entry struct {
	Path    string `json:"path"`
	ID      string `json:"id"`
	Access  Access `json:"access"`
	Tree    bool   `json:"tree"`
	Before  string `json:"before"`
	Applied bool   `json:"applied"`
}

// A Manifest is every effect a component's sandbox has on the machine: the
// profile, its SID and each grant (KIT-S2). Removal reads it and nothing
// else.
type Manifest struct {
	Format  int     `json:"format"`
	Profile string  `json:"profile"`
	SID     string  `json:"sid,omitempty"`
	Phase   Phase   `json:"phase"`
	Grants  []Entry `json:"grants"`
}

// manifestFormat is the manifest's layout version.
const manifestFormat = 2

// The refusals.
var (
	// ErrUnsupported: no platform but Windows has AppContainers.
	ErrUnsupported = errors.New("sandbox: AppContainers exist only on Windows")
	// ErrInUse: Setup cannot change a sandbox an engine of it runs in.
	ErrInUse = errors.New("sandbox: an engine runs in this sandbox; stop it before Setup changes it")
	// ErrBusy: a launch waits for Setup, which is installing, updating or
	// removing the sandbox, or stopped before it finished.
	ErrBusy = errors.New("sandbox: Setup is installing, updating or removing the engine's sandbox")
	// ErrNotInstalled: there is no sandbox to launch in.
	ErrNotInstalled = errors.New("sandbox: the engine's sandbox is not installed")
	// ErrNotGranted: a path the launch needs has no grant in place.
	ErrNotGranted = errors.New("sandbox: a path the engine needs is not granted to its sandbox")
)

// maxProfileName is CreateAppContainerProfile's limit.
const maxProfileName = 64

// profileNames assembles the current-account inventory. The platform reader
// reports absence only for a conclusively disappeared mapping; all other
// lookup errors must reach the caller before it can prove profile absence.
func profileNames(prefix string, entries []string, read func(string) (string, bool, error)) ([]string, error) {
	var out []string
	for _, entry := range entries {
		name, present, err := read(entry)
		if err != nil {
			return nil, fmt.Errorf("sandbox: read profile mapping %s: %w", entry, err)
		}
		if !present {
			continue
		}
		if name == "" {
			return nil, fmt.Errorf("sandbox: profile mapping %s has an empty Moniker", entry)
		}
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ProfileName is the profile of one engine kind:
// oa.engine.<provider>.<kind>, and .<label> after it for a second
// installation of the same kind (KIT-S1). Each part is lower-case letters,
// digits and hyphens.
func ProfileName(provider, kind, label string) (string, error) {
	parts := []string{provider, kind}
	if label != "" {
		parts = append(parts, label)
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return "", fmt.Errorf("sandbox: %q is not a profile name part: lower-case letters, digits and hyphens", p)
		}
	}
	name := "oa.engine." + strings.Join(parts, ".")
	if len(name) > maxProfileName {
		return "", fmt.Errorf("sandbox: the profile name %s is longer than %d characters", name, maxProfileName)
	}
	return name, nil
}

// ApplicationProfileName names an application's fixed-grant sandbox separately
// from engine installations. label identifies a second installed copy.
func ApplicationProfileName(application, label string) (string, error) {
	parts := []string{application}
	if label != "" {
		parts = append(parts, label)
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return "", fmt.Errorf("sandbox: invalid application profile part %q", p)
		}
	}
	name := "oa.app." + strings.Join(parts, ".")
	if len(name) > maxProfileName {
		return "", fmt.Errorf("sandbox: application profile exceeds %d characters", maxProfileName)
	}
	return name, nil
}

// ReadManifest reads the manifest at path.
func ReadManifest(path string) (*Manifest, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("sandbox: the manifest %q is not an absolute path", path)
	}
	if err := identity.LocalPath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: no manifest at %s", ErrNotInstalled, path)
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("sandbox: the manifest %s: %w", path, err)
	}
	if m.Format != manifestFormat {
		return nil, fmt.Errorf("sandbox: the manifest %s has format %d, not %d", path, m.Format, manifestFormat)
	}
	for _, e := range m.Grants {
		if !filepath.IsAbs(e.Path) || e.ID == "" || !e.Access.valid() {
			return nil, fmt.Errorf("sandbox: the manifest %s names the grant %q %q", path, e.Path, e.Access)
		}
		if err := identity.LocalPath(e.Path); err != nil {
			return nil, fmt.Errorf("sandbox: the manifest %s names the grant %q: %w", path, e.Path, err)
		}
	}
	return &m, nil
}

// writeManifest replaces the manifest at path whole: a reader sees the old
// one or the new one.
func writeManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	_, werr := temp.Write(append(data, '\n'))
	serr := temp.Sync()
	cerr := temp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		//unchecked: the write failure is the error returned
		os.Remove(temp.Name())
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		//unchecked: the rename failure is the error returned
		os.Remove(temp.Name())
		return err
	}
	return nil
}

// lockPath is the file Setup holds alone and each launch holds shared: a
// launch cannot start while Setup changes the sandbox, and Setup cannot
// change it while an engine runs (KIT-S3).
func lockPath(manifest string) string { return manifest + ".lock" }

// Ancestors is what Setup.GrantAncestorAttributes found above the granted
// paths (KIT-S7): the folders it granted Attributes, those the container
// already reaches (through its own SID, ALL APPLICATION PACKAGES or the
// facade's ContainerCapabilitySID), and those it could not grant because
// this account does not control their access list, such as a drive root.
type Ancestors struct {
	Granted, Reached, Unreached []string
}

// Removal is what removing a sandbox did: every path whose grant was
// revoked, those whose access list now differs from its snapshot (a change
// someone else made since), and whether the profile was deleted.
type Removal struct {
	Profile        string
	Revoked        []string
	Changed        []string
	ProfileDeleted bool
}
