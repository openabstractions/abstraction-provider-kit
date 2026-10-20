package modelbytes

import (
	"os"
	"testing"

	"github.com/openabstractions/abstraction-provider-kit/go/internal/personfolders"
)

// TestMain runs the tests with per-user folders in a temporary directory.
func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
