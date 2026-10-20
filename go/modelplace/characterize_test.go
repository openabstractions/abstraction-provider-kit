package modelplace

// Characterization tests (CQ-15a): they record how placement decides and
// refuses today, so the provider kit's move (W1-01) can prove it changed
// nothing. Every file lies in a test's temporary folder.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/go/trustedpath"
)

func plainDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCharacterizeLowerHex(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want bool
	}{
		{"0123456789abcdef", 16, true},
		{"0123456789ABCDEF", 16, false},
		{"abc", 4, false},
		{"abcg", 4, false},
		{"", 0, true},
	}
	for _, c := range cases {
		if got := LowerHex(c.s, c.n); got != c.want {
			t.Errorf("LowerHex(%q, %d) = %v, want %v", c.s, c.n, got, c.want)
		}
	}
}

func TestCharacterizeValidFilesRefusals(t *testing.T) {
	sum := strings.Repeat("a", 64)
	ok := File{Role: "main", Name: "m.gguf", Size: 1, SHA256: sum}
	if err := ValidFiles("s", []File{ok}); err != nil {
		t.Fatalf("a valid file was refused: %v", err)
	}
	if err := ValidFiles("s", nil); err != nil {
		t.Fatalf("an empty list was refused: %v", err)
	}
	with := func(edit func(*File)) []File { f := ok; edit(&f); return []File{f} }
	// Each refusal is pinned to the exact text people see, which also names
	// the check that refused it.
	const (
		plainName    = "the model set s's file %q is not one plain, sized name"
		cleanLocator = "the model file m.gguf has a locator %q that is not a clean relative path"
		lowerDigest  = "the model file m.gguf needs a lower-case SHA-256"
		roleTwice    = "the model set s names the role %q twice or not at all"
	)
	refused := []struct {
		name  string
		files []File
		want  string
	}{
		{"empty name", with(func(f *File) { f.Name = "" }), fmt.Sprintf(plainName, "")},
		{"slash name", with(func(f *File) { f.Name = "a/b" }), fmt.Sprintf(plainName, "a/b")},
		{"backslash name", with(func(f *File) { f.Name = `a\b` }), fmt.Sprintf(plainName, `a\b`)},
		{"colon name", with(func(f *File) { f.Name = "c:x" }), fmt.Sprintf(plainName, "c:x")},
		{"zero size", with(func(f *File) { f.Size = 0 }), fmt.Sprintf(plainName, "m.gguf")},
		{"upper digest", with(func(f *File) { f.SHA256 = strings.Repeat("A", 64) }), lowerDigest},
		{"short digest", with(func(f *File) { f.SHA256 = "ab" }), lowerDigest},
		{"bad blob", with(func(f *File) { f.GitBlobSHA1 = "xyz" }), lowerDigest},
		{"empty role", with(func(f *File) { f.Role = "" }), fmt.Sprintf(roleTwice, "")},
		{"absolute locator", with(func(f *File) { f.Locators = []string{"/a"} }), fmt.Sprintf(cleanLocator, "/a")},
		{"unclean locator", with(func(f *File) { f.Locators = []string{"a/./b"} }), fmt.Sprintf(cleanLocator, "a/./b")},
		{"parent locator", with(func(f *File) { f.Locators = []string{"../a"} }), fmt.Sprintf(cleanLocator, "../a")},
		{"dotdot locator", with(func(f *File) { f.Locators = []string{".."} }), fmt.Sprintf(cleanLocator, "..")},
		{"empty locator", with(func(f *File) { f.Locators = []string{""} }), fmt.Sprintf(cleanLocator, "")},
		{"backslash locator", with(func(f *File) { f.Locators = []string{`a\b`} }), fmt.Sprintf(cleanLocator, `a\b`)},
		{"duplicate name", []File{ok, func() File { f := ok; f.Role = "other"; return f }()}, fmt.Sprintf(plainName, "m.gguf")},
		{"duplicate role", []File{ok, func() File { f := ok; f.Name = "n.gguf"; return f }()}, fmt.Sprintf(roleTwice, "main")},
	}
	for _, c := range refused {
		if err := ValidFiles("s", c.files); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// A clean relative locator and a lower-case blob id are accepted.
	good := ok
	good.Locators = []string{"a/b/m.gguf"}
	good.GitBlobSHA1 = strings.Repeat("0", 40)
	if err := ValidFiles("s", []File{good}); err != nil {
		t.Fatalf("a locator and blob id were refused: %v", err)
	}
}

func TestCharacterizeSearchCandidatesAndOwners(t *testing.T) {
	dir := plainDir(t)
	f := File{Name: "m.gguf", Locators: []string{"x/m.gguf"}}
	got := FolderSearch(dir).Candidates(f)
	want := []string{filepath.Join(dir, "m.gguf"), filepath.Join(dir, "x", "m.gguf")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("folder candidates %v, want %v", got, want)
	}
	if !FolderSearch(dir).Owners().Account {
		t.Fatal("a folder search does not trust this account")
	}
	if (StoreSearch{}).Owners().Account {
		t.Fatal("a store search trusts this account")
	}
	if FormatOwners(StoreSearch{}.Owners()) != FormatOwners(trustedpath.MachineOwners()) {
		t.Fatal("a store search's owners are not the machine's")
	}
}

type fakeStores map[string]map[string]string

func (s fakeStores) Stores() []string {
	return []string{"one", "two"}
}
func (s fakeStores) FilePath(store, id string) (string, bool) {
	p, ok := s[store][id]
	return p, ok
}

func TestCharacterizeStoreSearchCandidates(t *testing.T) {
	dir := plainDir(t)
	f := File{Name: "m.gguf", SHA256: "ab", Locators: []string{"l/m.gguf"}}
	stores := fakeStores{
		"one": {"sha256:ab": filepath.Join(dir, "by-digest"), "m.gguf": "relative/path"},
		"two": {"m.gguf": filepath.Join(dir, "by-name"), "l/m.gguf": filepath.Join(dir, "by-locator")},
	}
	got := StoreSearch{Files: stores}.Candidates(f)
	// Stores in order; within each, digest, name, then locators; relative
	// paths are dropped.
	want := []string{filepath.Join(dir, "by-digest"), filepath.Join(dir, "by-name"), filepath.Join(dir, "by-locator")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("store candidates %v, want %v", got, want)
	}
}

func TestCharacterizeSearches(t *testing.T) {
	if _, err := Searches(nil, nil); err == nil || !strings.Contains(err.Error(), "--from") {
		t.Fatalf("no search: %v", err)
	}
	dir := plainDir(t)
	got, err := Searches([]string{dir}, fakeStores{})
	if err != nil || len(got) != 2 {
		t.Fatalf("searches %v %v", got, err)
	}
	if fs, ok := got[0].(FolderSearch); !ok || string(fs) != dir {
		t.Fatalf("first search %#v", got[0])
	}
	if _, ok := got[1].(StoreSearch); !ok {
		t.Fatalf("second search %#v", got[1])
	}
}

func TestCharacterizePlaceDecisions(t *testing.T) {
	first, second := plainDir(t), plainDir(t)
	f := pinned(t, second, "w.bin", "main", []byte("weights"))
	// A wrong copy in the first folder is refused and the search goes on.
	if err := os.WriteFile(filepath.Join(first, "w.bin"), []byte("WEIGHTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	places := filepath.Join(plainDir(t), "sub", "set.places")
	found, err := Place(context.Background(), []File{f}, []Search{FolderSearch(first), FolderSearch(second)}, places)
	if err != nil || found["w.bin"] != filepath.Join(second, "w.bin") {
		t.Fatalf("place %v %v", found, err)
	}
	data, err := os.ReadFile(places)
	if err != nil {
		t.Fatal(err)
	}
	wantLine := "w.bin\t" + filepath.Join(second, "w.bin") + "\t" + FormatOwners(FolderSearch(second).Owners()) + "\n"
	if string(data) != wantLine {
		t.Fatalf("record %q, want %q", data, wantLine)
	}

	// Relative records are refused before any search.
	if _, err := Place(context.Background(), []File{f}, nil, "rel.places"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative record: %v", err)
	}
	// Nothing found at all names the file and no refused candidate.
	if _, err := Place(context.Background(), []File{f}, []Search{FolderSearch(plainDir(t))}, places); err == nil ||
		err.Error() != "no search holds the pinned w.bin" {
		t.Fatalf("absent: %v", err)
	}
	// Only refused candidates name each refusal.
	_, err = Place(context.Background(), []File{f}, []Search{FolderSearch(first)}, places)
	if err == nil || !strings.HasPrefix(err.Error(), "no search holds the pinned w.bin: ") || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("refused: %v", err)
	}
}

func TestCharacterizeProve(t *testing.T) {
	dir := plainDir(t)
	data := []byte("content")
	f := pinned(t, dir, "c.bin", "main", data)
	p := filepath.Join(dir, "c.bin")
	if err := Prove(context.Background(), f, p); err != nil {
		t.Fatal(err)
	}
	if err := Prove(context.Background(), f, filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent: %v", err)
	}
	size := f
	size.Size++
	if err := Prove(context.Background(), size, p); err == nil || !strings.Contains(err.Error(), "bytes, not the pinned") {
		t.Fatalf("size: %v", err)
	}
	blob := sha1.New()
	blob.Write([]byte("blob 7\x00content"))
	withBlob := f
	withBlob.GitBlobSHA1 = hex.EncodeToString(blob.Sum(nil))
	if err := Prove(context.Background(), withBlob, p); err != nil {
		t.Fatalf("matching blob: %v", err)
	}
	withBlob.GitBlobSHA1 = strings.Repeat("0", 40)
	if err := Prove(context.Background(), withBlob, p); err == nil || !strings.Contains(err.Error(), "Git blob") {
		t.Fatalf("blob: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Prove(ctx, f, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if err := Prove(context.Background(), f, dir); err == nil {
		t.Fatal("a folder was proved")
	}
}

func TestCharacterizeOwnersRecord(t *testing.T) {
	machine := trustedpath.MachineOwners()
	both := machine
	both.Account = true
	for _, o := range []trustedpath.Owners{machine, both, {}, {Account: true}} {
		got, ok := ParseOwners(FormatOwners(o))
		if !ok || got.Account != o.Account || strings.Join(got.SIDs, ",") != strings.Join(o.SIDs, ",") {
			t.Fatalf("round trip of %+v: %+v %v", o, got, ok)
		}
	}
	if FormatOwners(trustedpath.Owners{Account: true}) != ";account" {
		t.Fatal("account-only owners")
	}
	for _, s := range []string{";other", "S-1-5-21-1-2-3-1001", ";account;x"} {
		if _, ok := ParseOwners(s); ok {
			t.Errorf("ParseOwners(%q) accepted", s)
		}
	}
	if len(machine.SIDs) > 0 {
		if _, ok := ParseOwners(machine.SIDs[0] + "," + machine.SIDs[0]); ok {
			t.Error("a SID named twice was accepted")
		}
	}
}

func TestCharacterizeReadRefusals(t *testing.T) {
	dir := plainDir(t)
	a := File{Name: "a.bin", Size: 1, SHA256: strings.Repeat("a", 64)}
	b := File{Name: "b.bin", Size: 1, SHA256: strings.Repeat("b", 64)}
	pa, pb := filepath.Join(dir, "a.bin"), filepath.Join(dir, "b.bin")
	write := func(text string) string {
		p := filepath.Join(plainDir(t), "set.places")
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("a.bin\t" + pa + "\t;account\nb.bin\t" + pb + "\t\n")
	where, owners, err := Read(good, "set", []File{a, b}, "fix")
	if err != nil || where["a.bin"] != pa || !owners["a.bin"].Account || owners["b.bin"].Account {
		t.Fatalf("read %v %v %v", where, owners, err)
	}
	// Each refusal is pinned to its exact text after the record's path; a
	// malformed line names the remedy, a line that names something else does
	// not.
	refused := []struct{ name, text, want string }{
		{"two fields", "a.bin\t" + pa + "\n", " is no record of the set files; place them again with fix"},
		{"bad owners", "a.bin\t" + pa + "\t;nobody\nb.bin\t" + pb + "\t\n", " is no record of the set files"},
		{"unpinned", "c.bin\t" + pa + "\t\n", " is no record of the set files"},
		{"relative", "a.bin\trel\t\nb.bin\t" + pb + "\t\n", " is no record of the set files"},
		{"twice", "a.bin\t" + pa + "\t\na.bin\t" + pa + "\t\nb.bin\t" + pb + "\t\n", " is no record of the set files"},
		{"missing file", "a.bin\t" + pa + "\t\n", " does not say where b.bin lies"},
	}
	for _, c := range refused {
		p := write(c.text)
		if _, _, err := Read(p, "set", []File{a, b}, "fix"); err == nil || err.Error() != p+c.want {
			t.Errorf("%s: %v, want %q", c.name, err, p+c.want)
		}
	}
	absent := filepath.Join(plainDir(t), "absent.places")
	if _, _, err := Read(absent, "set", []File{a}, "fix"); !errors.Is(err, os.ErrNotExist) ||
		!strings.HasPrefix(err.Error(), "no record of where the set files lie (") || !strings.HasSuffix(err.Error(), "); place them with fix") {
		t.Errorf("absent record: %v", err)
	}
	if _, _, err := Read(write("x\n"), "set", []File{a}, "the fix"); err == nil || !strings.Contains(err.Error(), "the fix") {
		t.Errorf("malformed record does not name the remedy: %v", err)
	}
	placed, err := Placed(good, "set", []File{b, a}, "fix")
	if err != nil || len(placed) != 2 || placed[0].Path != pb || placed[1].Path != pa || placed[0].Size != 1 || placed[1].SHA256 != a.SHA256 {
		t.Fatalf("placed %+v %v", placed, err)
	}
}

func TestCharacterizeFolders(t *testing.T) {
	if got := ModelsRoot(filepath.FromSlash("/h")); got != filepath.Join(filepath.FromSlash("/h"), "OA-models") {
		t.Fatalf("ModelsRoot %q", got)
	}
	root := plainDir(t)
	if SetFolderOf(root, "") != "" || SetFolderOf(root, plainDir(t)) != "" {
		t.Fatal("SetFolderOf outside root")
	}
	var added []string
	if err := SetFolders(root, "set", "", nil, "fix", func(d string) { added = append(added, d) }); err != nil ||
		len(added) != 1 || added[0] != filepath.Join(root, "set") {
		t.Fatalf("no record: %v %v", added, err)
	}
	added = nil
	if err := SetFolders(root, "set", filepath.Join(root, "absent.places"), nil, "fix", func(d string) { added = append(added, d) }); err != nil || len(added) != 1 {
		t.Fatalf("absent record: %v %v", added, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "other", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := File{Name: "f.bin", Size: 1, SHA256: strings.Repeat("a", 64)}
	rec := filepath.Join(plainDir(t), "set.places")
	if err := os.WriteFile(rec, []byte("f.bin\t"+filepath.Join(root, "other", "deep", "f.bin")+"\t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added = nil
	if err := SetFolders(root, "set", rec, []File{f}, "fix", func(d string) { added = append(added, d) }); err != nil ||
		len(added) != 2 || added[1] != filepath.Join(root, "other") {
		t.Fatalf("recorded: %v %v", added, err)
	}
	// An empty root adopts nothing and calls nothing.
	if err := AdoptSetFolders("", nil, []File{f}, map[string]string{"f.bin": "x"}, func(string) { t.Fatal("adopted") }); err != nil {
		t.Fatal(err)
	}
	// A folder not directly under root is left alone and reported false.
	if ok, err := AdoptModelFolder(root, filepath.Join(root, "other", "deep"), nil); ok || err != nil {
		t.Fatalf("deep folder: %v %v", ok, err)
	}
	if ok, err := AdoptModelFolder(root, root, nil); ok || err != nil {
		t.Fatalf("root itself: %v %v", ok, err)
	}
	if _, err := AdoptModelFolder("rel", root, nil); err == nil {
		t.Fatal("a relative root was accepted")
	}
}

// ProtectFolders refuses a root CheckModelsRoot refuses before it reads
// anything; a dry run lists every unrecorded folder as left alone, then each
// recorded folder as it stands, and changes nothing.
func TestCharacterizeProtectFolders(t *testing.T) {
	var out strings.Builder
	if n, err := ProtectFolders("rel", nil, nil, true, &out); n != 0 || !errors.Is(err, privatedir.ErrNotModelFolder) || out.Len() != 0 {
		t.Fatalf("relative root: %d %v %q", n, err, out.String())
	}
	root := plainDir(t)
	if err := privatedir.CheckModelsRoot(root); err != nil {
		t.Skipf("this test folder is no models root here: %v", err)
	}
	rec, other := filepath.Join(root, "rec"), filepath.Join(root, "other")
	for _, d := range []string{rec, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := func() string {
		if privatedir.CheckModelFolder(rec) == nil {
			return "already a model folder; protecting would rewrite the entries below it"
		}
		return "would be protected"
	}
	before := state()
	n, err := ProtectFolders(root, []string{rec}, nil, true, &out)
	want := other + ": not a recorded set; left as it is\n" + rec + ": " + before + "\n"
	if n != 0 || err != nil || out.String() != want {
		t.Fatalf("dry run: %d %v %q, want %q", n, err, out.String(), want)
	}
	if state() != before {
		t.Fatal("a dry run changed a recorded folder")
	}
}
