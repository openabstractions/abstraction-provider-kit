package sdcpp

import (
	"os"
	"testing"

	"github.com/openabstractions/abstraction-provider-kit/go/internal/personfolders"
)

func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }
