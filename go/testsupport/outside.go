// Package testsupport contains controls shared by provider containment tests.
package testsupport

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// OutsideAddressEnv explicitly opts a test into off-machine probes by naming
// the TCP host:port its uncontained control must first reach. Empty is off.
const OutsideAddressEnv = "OA_PROVIDER_KIT_OUTSIDE_ADDRESS"

func outsideAddress(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(value)
	n, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%s must be an explicit host:port", OutsideAddressEnv)
	}
	return value, nil
}

func outsideControl(value string, dial func(string, string, time.Duration) (net.Conn, error)) (string, error) {
	address, err := outsideAddress(value)
	if err != nil || address == "" {
		return address, err
	}
	c, err := dial("tcp", address, 3*time.Second)
	if err != nil {
		return "", fmt.Errorf("uncontained control cannot reach %s: %w", address, err)
	}
	return address, c.Close()
}

// OutsideNetwork runs the test's own control only after explicit opt-in. Its
// returned address is empty when all probes must remain on the machine.
func OutsideNetwork(t testing.TB) string {
	t.Helper()
	address, err := outsideControl(os.Getenv(OutsideAddressEnv), net.DialTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if address == "" {
		t.Logf("off-machine probes require %s=<host:port>", OutsideAddressEnv)
	}
	return address
}
