package sandbox

import (
	"strings"
	"testing"
)

// TestProfileName: one profile per engine kind, oa.engine.<provider>.<kind>,
// with a label for a second installation; a part outside lower-case letters,
// digits and hyphens, and a name past 64 characters, are refused (KIT-S1).
func TestProfileName(t *testing.T) {
	for _, c := range []struct{ provider, kind, label, want string }{
		{"pytorch", "torch", "", "oa.engine.pytorch.torch"},
		{"pytorch", "torch", "3f9a2c1b0d4e", "oa.engine.pytorch.torch.3f9a2c1b0d4e"},
		{"modelhost", "llama-cpp", "", "oa.engine.modelhost.llama-cpp"},
	} {
		got, err := ProfileName(c.provider, c.kind, c.label)
		if err != nil || got != c.want {
			t.Fatalf("ProfileName(%q, %q, %q) = %q, %v", c.provider, c.kind, c.label, got, err)
		}
	}
	for _, c := range [][3]string{
		{"", "torch", ""}, {"pytorch", "", ""}, {"PyTorch", "torch", ""}, {"py.torch", "torch", ""},
		{"pytorch", "torch", "a b"}, {"pytorch", "torch", "x/y"}, {"pytorch", "torch", strings.Repeat("a", 41)},
	} {
		if got, err := ProfileName(c[0], c[1], c[2]); err == nil {
			t.Fatalf("ProfileName(%q, %q, %q) = %q, want a refusal", c[0], c[1], c[2], got)
		}
	}
}

// TestAccessCovers: Modify covers ReadExecute, never the other way.
func TestAccessCovers(t *testing.T) {
	if !Modify.covers(ReadExecute) || !ReadExecute.covers(ReadExecute) || ReadExecute.covers(Modify) {
		t.Fatal("access cover is wrong")
	}
}

func TestApplicationProfileNamesRemainSeparateFromEngines(t *testing.T) {
	got, err := ApplicationProfileName("opencode", "copy-1")
	if err != nil || got != "oa.app.opencode.copy-1" {
		t.Fatalf("application profile = %q, %v", got, err)
	}
	for _, name := range []string{"", "OpenCode", "opencode.x", "opencode/x"} {
		if _, err := ApplicationProfileName(name, ""); err == nil {
			t.Fatalf("accepted invalid application name %q", name)
		}
	}
}
