//go:build !windows

package sandbox

// No platform but Windows has AppContainers; every call is refused
// (KIT-S6). An engine on another platform runs with no OS-level network
// containment from this package.

func ProfileExists(string) bool                { return false }
func CheckLocalPlacement(string) error         { return ErrUnsupported }
func DeleteProfile(string) error               { return ErrUnsupported }
func Profiles(string) ([]string, error)        { return nil, ErrUnsupported }
func Holders(string, string) ([]string, error) { return nil, ErrUnsupported }
func Begin(string, string) (*Setup, error)     { return nil, ErrUnsupported }
func Remove(string, bool) (*Removal, error)    { return nil, ErrUnsupported }
func Prepare(string, []Grant) (*Launch, error) { return nil, ErrUnsupported }
func (*Setup) SID() string                     { return "" }
func (*Setup) Grant(...Grant) error            { return ErrUnsupported }
func (*Setup) GrantBound(Grant, string) error  { return ErrUnsupported }
func ProfileSID(string) (string, error)        { return "", ErrUnsupported }
func (*Setup) Commit() error                   { return ErrUnsupported }
func (*Setup) GrantAncestorAttributes() (*Ancestors, error) {
	return nil, ErrUnsupported
}
func AttributeRoots() ([]string, error)          { return nil, ErrUnsupported }
func GrantRootAttributes(string) (bool, error)   { return false, ErrUnsupported }
func RevokeRootAttributes(string) (bool, error)  { return false, ErrUnsupported }
func RootAttributesPresent(string) (bool, error) { return false, ErrUnsupported }
func (*Setup) Close()                            {}
func (*Launch) Close()                           {}

// Setup is an install of a sandbox; none exists here.
type Setup struct{}

// A Launch is a checked sandbox; none exists here.
type Launch struct{ Profile, SID string }
