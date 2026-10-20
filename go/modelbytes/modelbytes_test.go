package modelbytes

import (
	"strings"
	"testing"
)

// KIT-MB1: the lender spells a URI the receivers' parser reads back as it
// was, and DigestHex takes only storage's canonical digest.
func TestURISpellsWhatParseReads(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	for _, u := range []URI{
		{Handle: 1234, Length: 1},
		{Handle: 8, Offset: 65537, Length: 1 << 40, SHA256: hex, Lease: 12, Memory: true},
	} {
		got, err := Parse(u.String())
		if err != nil || got != u {
			t.Errorf("%s read back as %+v, %v", u, got, err)
		}
	}
	if h, err := DigestHex("sha256:" + hex); err != nil || h != hex {
		t.Errorf("DigestHex: %q %v", h, err)
	}
	for _, bad := range []string{hex, "sha256:" + strings.ToUpper(hex), "sha256:abc"} {
		if _, err := DigestHex(bad); err == nil {
			t.Errorf("DigestHex accepted %q", bad)
		}
	}
	if IsURI(`C:\models\m.gguf`) || !IsURI("fd:3?offset=0&length=1") {
		t.Error("IsURI misreads a path or a URI")
	}
}
