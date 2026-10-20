//go:build !windows

package sandbox

import (
	"os"
	"testing"

	"github.com/openabstractions/abstraction-provider-kit/go/internal/personfolders"
)

// TestMain runs this package's tests with the person's per-user folders
// inside a temporary folder (personfolders.Main); on Windows the TestMain
// of contained_windows_test.go does.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
