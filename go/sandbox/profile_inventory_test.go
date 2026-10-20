package sandbox

import (
	"errors"
	"slices"
	"testing"
)

func TestProfileInventoryRefusesUnreadableEntries(t *testing.T) {
	refusal := errors.New("fixture mapping lookup refused")
	for _, kind := range []string{"open-refused", "moniker-refused", "moniker-missing", "moniker-empty"} {
		t.Run(kind, func(t *testing.T) {
			names, err := profileNames("oa.engine.pytorch.", []string{"found", kind}, func(entry string) (string, bool, error) {
				if entry == "found" {
					return "oa.engine.pytorch.torch", true, nil
				}
				if entry == "moniker-empty" {
					return "", true, nil
				}
				return "", true, refusal
			})
			if err == nil || (kind != "moniker-empty" && !errors.Is(err, refusal)) || names != nil {
				t.Fatal("unreadable mapping became absence or partial inventory", names, err)
			}
		})
	}
	for _, prefix := range []string{"", "oa.engine.pytorch."} {
		names, err := profileNames(prefix, []string{"gone", "b", "a", "foreign"}, func(entry string) (string, bool, error) {
			switch entry {
			case "gone":
				return "", false, nil // only a conclusively disappeared entry
			case "foreign":
				return "another.profile", true, nil
			default:
				return "oa.engine.pytorch." + entry, true, nil
			}
		})
		want := []string{"oa.engine.pytorch.a", "oa.engine.pytorch.b"}
		if prefix == "" {
			want = append([]string{"another.profile"}, want...)
		}
		if err != nil || !slices.Equal(names, want) {
			t.Fatal("inventory filter/sort/disappearance", names, err)
		}
	}
}
