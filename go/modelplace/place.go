// Package modelplace finds a model set's pinned files where a person or
// another application keeps them, proves each by size and digest in place,
// and records where it lies; it copies nothing (CONTRACT.md KIT-M1..KIT-M4,
// VISION 2026-10-01: OA never copies a model for an engine).
package modelplace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/go/trustedpath"
	"github.com/openabstractions/abstraction-inference/go/providerhost/enginefiles"

	identity "github.com/openabstractions/abstraction-identity"
)

// A File is one pinned weight file of a model set: by its role, name, size
// and SHA-256, and for a publisher that records no SHA-256 also by the Git
// blob SHA-1 its commit binds.
type File struct {
	Role string
	Name string
	Size int64
	// SHA256 is the file's digest, lower-case hex: a Hugging Face LFS oid,
	// or one OA recorded from bytes whose Git blob matched.
	SHA256 string
	// GitBlobSHA1 is SHA-1 over "blob <size>\x00" and the content, the id
	// the publisher's Git commit names the file by; empty when the
	// publisher records a SHA-256.
	GitBlobSHA1 string
	// Source is where the publisher serves it, at Revision.
	Source, Revision string
	License          string
	// Locators are slash-separated paths, relative to a store or folder,
	// under which an application is known to keep this file. A search tries
	// them beside Name; every candidate is still proved by its digest.
	Locators []string
}

// A UsePolicy is a policy of permitted use a model's publisher attaches to
// its weights, beside their licence: its name, the licence that makes it
// binding and where its text is published.
type UsePolicy struct {
	Name, License, URL string
}

// LowerHex reports whether s is n lower-case hex digits.
func LowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidFiles refuses a pinned file list that cannot be placed as written: a
// name that is not one plain, sized name, a locator that is not a clean
// relative path, a digest that is not lower-case hex, or a role named twice.
func ValidFiles(set string, files []File) error {
	seen, roles := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		if f.Name == "" || strings.ContainsAny(f.Name, `/\:`) || f.Size < 1 || seen[identity.PathNameKey(f.Name)] {
			return fmt.Errorf("the model set %s's file %q is not one plain, sized name", set, f.Name)
		}
		seen[identity.PathNameKey(f.Name)] = true
		for _, l := range f.Locators {
			if l == "" || path.IsAbs(l) || path.Clean(l) != l || strings.HasPrefix(l, "../") || l == ".." || strings.ContainsAny(l, `\:`) {
				return fmt.Errorf("the model file %s has a locator %q that is not a clean relative path", f.Name, l)
			}
		}
		if !LowerHex(f.SHA256, 64) || f.GitBlobSHA1 != "" && !LowerHex(f.GitBlobSHA1, 40) {
			return fmt.Errorf("the model file %s needs a lower-case SHA-256", f.Name)
		}
		if f.Role == "" || roles[f.Role] {
			return fmt.Errorf("the model set %s names the role %q twice or not at all", set, f.Role)
		}
		roles[f.Role] = true
	}
	return nil
}

// A Search names the paths where a pinned file may lie, and the owners a
// file it finds may have to be trusted in place (identity ID-D2): a trusted
// file is proved by its metadata after its first full hash, any other in full
// at every launch (INF-F7).
type Search interface {
	Candidates(f File) []string
	Owners() trustedpath.Owners
}

// FolderSearch looks for a pinned file by its name in one folder, such as
// the folder a person downloaded the set to.
type FolderSearch string

// Owners of a folder a person names are the machine's and this account.
func (d FolderSearch) Owners() trustedpath.Owners {
	o := trustedpath.MachineOwners()
	o.Account = true
	return o
}

func (d FolderSearch) Candidates(f File) []string {
	out := []string{filepath.Join(string(d), f.Name)}
	for _, l := range f.Locators {
		out = append(out, filepath.Join(string(d), filepath.FromSlash(l)))
	}
	return out
}

// StoreFiles is what a store search reads from the stores an inventory
// source knows: their names, and the absolute path of the whole stored file
// an id names in one of them, false for an id that names none or a file it
// cannot hand over whole. The kit names no store provider: a provider that
// reads stores adapts its source to this (KIT-M1).
type StoreFiles interface {
	Stores() []string
	FilePath(store, id string) (string, bool)
}

// StoreSearch asks every store Files reads for a pinned file, by its digest,
// its name and its locators, and takes a whole stored file's absolute path.
type StoreSearch struct{ Files StoreFiles }

// Owners of a file in another application's store are the machine's alone:
// SYSTEM, Administrators and TrustedInstaller (ASUS's AICreator store).
func (s StoreSearch) Owners() trustedpath.Owners { return trustedpath.MachineOwners() }

func (s StoreSearch) Candidates(f File) []string {
	var out []string
	for _, store := range s.Files.Stores() {
		for _, id := range append([]string{"sha256:" + f.SHA256, f.Name}, f.Locators...) {
			if path, ok := s.Files.FilePath(store, id); ok && path != "" && filepath.IsAbs(path) {
				out = append(out, path)
			}
		}
	}
	return out
}

// Place finds each of files through searches, in order, and proves it where
// it lies by size, SHA-256 and, when pinned, Git blob SHA-1. It copies
// nothing. A candidate whose digest differs is refused and the search goes
// on; a file no search proves is the error, naming each refused candidate.
// The record at places then names where each file was found (KIT-M1).
func Place(ctx context.Context, files []File, searches []Search, places string) (map[string]string, error) {
	if !filepath.IsAbs(places) {
		return nil, errors.New("the places record must be an absolute path")
	}
	found := map[string]string{}
	owners := map[string]trustedpath.Owners{}
	for _, f := range files {
		var refused []string
		tried := map[string]bool{}
		for _, search := range searches {
			for _, p := range search.Candidates(f) {
				key := placeKey(p)
				// The places record is one name and path per line, tab
				// separated; a path holding either separator is never read.
				if tried[key] || !filepath.IsAbs(p) || strings.ContainsAny(p, "\t\r\n") {
					continue
				}
				tried[key] = true
				if err := Prove(ctx, f, p); err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						refused = append(refused, err.Error())
					}
					continue
				}
				found[f.Name] = filepath.Clean(p)
				owners[f.Name] = search.Owners()
				break
			}
			if found[f.Name] != "" {
				break
			}
		}
		if found[f.Name] == "" {
			if len(refused) > 0 {
				return nil, fmt.Errorf("no search holds the pinned %s: %s", f.Name, strings.Join(refused, "; "))
			}
			return nil, fmt.Errorf("no search holds the pinned %s", f.Name)
		}
	}
	var lines strings.Builder
	for _, f := range files {
		//unchecked: a strings.Builder write never returns an error
		lines.WriteString(f.Name + "\t" + found[f.Name] + "\t" + FormatOwners(owners[f.Name]) + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(places), 0o700); err != nil {
		return nil, err
	}
	data := []byte(lines.String())
	if err := cas.Change(places, func([]byte) ([]byte, error) { return data, nil }); err != nil {
		return nil, err
	}
	return found, nil
}

// FormatOwners writes owners as the places record keeps them: the SIDs,
// comma-separated, then ";account" when this account is trusted.
func FormatOwners(o trustedpath.Owners) string {
	s := strings.Join(o.SIDs, ",")
	if o.Account {
		s += ";account"
	}
	return s
}

// ParseOwners reads owners as FormatOwners wrote them. It admits the
// machine's owners (trustedpath.MachineOwners) and this account alone: a
// record naming any other SID, or one twice, is refused, so an edited record
// cannot widen whom the metadata shortcut trusts (KIT-M2).
func ParseOwners(s string) (trustedpath.Owners, bool) {
	var o trustedpath.Owners
	sids, account, _ := strings.Cut(s, ";")
	switch account {
	case "":
	case "account":
		o.Account = true
	default:
		return o, false
	}
	if sids != "" {
		machine := trustedpath.MachineOwners().SIDs
		for _, sid := range strings.Split(sids, ",") {
			if !slices.Contains(machine, sid) || slices.Contains(o.SIDs, sid) {
				return trustedpath.Owners{}, false
			}
			o.SIDs = append(o.SIDs, sid)
		}
	}
	return o, true
}

// Read reads a places record: where each of files lies and the owners it
// may have to be trusted in place. remedy is the command that writes the
// record, named in a refusal (KIT-M2).
func Read(places, set string, files []File, remedy string) (map[string]string, map[string]trustedpath.Owners, error) {
	data, err := os.ReadFile(places)
	if err != nil {
		return nil, nil, fmt.Errorf("no record of where the %s files lie (%w); place them with %s", set, err, remedy)
	}
	out, owned := map[string]string{}, map[string]trustedpath.Owners{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 {
			return nil, nil, fmt.Errorf("%s is no record of the %s files; place them again with %s", places, set, remedy)
		}
		name, p := fields[0], fields[1]
		o, ok := ParseOwners(fields[2])
		pinned := slices.ContainsFunc(files, func(f File) bool { return f.Name == name })
		if !ok || !pinned || !filepath.IsAbs(p) || out[name] != "" {
			return nil, nil, fmt.Errorf("%s is no record of the %s files", places, set)
		}
		out[name], owned[name] = p, o
	}
	for _, f := range files {
		if out[f.Name] == "" {
			return nil, nil, fmt.Errorf("%s does not say where %s lies", places, f.Name)
		}
	}
	return out, owned, nil
}

// Placed is each of files where its places record says it lies, with its
// owners, as enginefiles proves it in place (INF-F7).
func Placed(places, set string, files []File, remedy string) ([]enginefiles.Placed, error) {
	where, owners, err := Read(places, set, files, remedy)
	if err != nil {
		return nil, err
	}
	out := make([]enginefiles.Placed, 0, len(files))
	for _, f := range files {
		out = append(out, enginefiles.Placed{Path: where[f.Name], Size: f.Size, SHA256: f.SHA256, Owners: owners[f.Name]})
	}
	return out, nil
}

// Prove reads the file at p where it lies and refuses another size or
// digest than f's. A missing file wraps os.ErrNotExist.
func Prove(ctx context.Context, f File, p string) error {
	// A path through a junction or symbolic link could later name other
	// bytes; a launch refuses it (INF-F7), and so does placing.
	if err := enginefiles.CheckPlainPath(p); err != nil {
		return err
	}
	in, err := os.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return fmt.Errorf("%s is %d bytes, not the pinned %d", p, info.Size(), f.Size)
	}
	sum256 := sha256.New()
	blob := sha1.New()
	//unchecked: a hash.Hash write never returns an error
	io.WriteString(blob, "blob "+strconv.FormatInt(f.Size, 10)+"\x00")
	n, err := io.Copy(io.MultiWriter(sum256, blob), &contextReader{ctx: ctx, r: io.LimitReader(in, f.Size+1)})
	if err != nil {
		return err
	}
	if n != f.Size {
		return fmt.Errorf("%s changed size while it was read", p)
	}
	if got := hex.EncodeToString(sum256.Sum(nil)); got != f.SHA256 {
		return fmt.Errorf("%s has SHA-256 %s, not the pinned %s", p, got, f.SHA256)
	}
	if f.GitBlobSHA1 != "" {
		if got := hex.EncodeToString(blob.Sum(nil)); got != f.GitBlobSHA1 {
			return fmt.Errorf("%s has Git blob %s, not the pinned %s", p, got, f.GitBlobSHA1)
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// ModelsRoot is the folder OA keeps its own model sets in under a person's
// home, one folder per set (FOOTPRINT's OA-models).
func ModelsRoot(home string) string { return filepath.Join(home, "OA-models") }

// AdoptModelFolder makes dir a model folder of this account (identity
// ID-D3: owned by it, a protected DACL of this account, SYSTEM and
// Administrators, every file below carrying those entries inherited) when
// dir is a set folder directly under root, OA's own, and reports true. The
// root must pass privatedir.CheckModelsRoot: no drive root, home or known
// folder, no link on its path, resolving to itself. A file below dir with
// another name outside root and linkRoots (OA's job store, whose results
// LinkResult hard-links out, JOB-A17) refuses the folder unchanged. Any
// folder not directly under root is left exactly as it is and reported
// false. No file with a name outside root and linkRoots is changed, and
// every linkRoot that exists must pass privatedir.CheckModelsRoot (KIT-M3).
func AdoptModelFolder(root, dir string, linkRoots []string) (bool, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(dir) {
		return false, errors.New("the models root and the folder must be absolute paths")
	}
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	// dir must exist directly under root, by file identity.
	if parent, err := identity.SameFile(filepath.Dir(dir), root); err != nil || !parent {
		return false, nil
	}
	if same, err := identity.SameFile(dir, root); err != nil || same {
		return false, nil
	}
	if err := privatedir.CheckModelsRoot(root); err != nil {
		return false, err
	}
	if err := privatedir.ProtectModelFolder(dir, append([]string{root}, linkRoots...)); err != nil {
		return false, err
	}
	return true, nil
}

// SetFolderOf is the folder directly under root that holds p, or "" when p
// does not lie below root.
func SetFolderOf(root, p string) string {
	if p == "" {
		return ""
	}
	root = filepath.Clean(root)
	// Containment by file identity (identity.WithinRel).
	rel, inside, err := identity.WithinRel(p, root)
	if err != nil || !inside {
		return ""
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return filepath.Join(root, first)
}

// SetFolders adds to into each folder directly under root that a recorded set
// uses: the folder named for the set, and each folder holding a file its
// places record names. A set with no record adds its named folder alone
// (KIT-M3).
func SetFolders(root, set string, places string, files []File, remedy string, add func(dir string)) error {
	add(filepath.Join(root, set))
	if places == "" {
		return nil
	}
	where, _, err := Read(places, set, files, remedy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, p := range where {
		add(SetFolderOf(root, p))
	}
	return nil
}

// placeKey keys a path that may name one file in several spellings: its final
// path when it exists (identity.PlaceKey), else its cleaned spelling.
func placeKey(p string) string {
	if key, err := identity.PlaceKey(p); err == nil {
		return key
	}
	return filepath.Clean(p)
}
