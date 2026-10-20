package testsupport

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestOutsideOptIn(t *testing.T) {
	for _, value := range []string{"", "1", "true", "example.test", ":443", "example.test:0", "example.test:65536", "example.test:https"} {
		calls := 0
		address, err := outsideControl(value, func(string, string, time.Duration) (net.Conn, error) {
			calls++
			return nil, errors.New("must not dial")
		})
		if calls != 0 || address != "" || (err == nil) != (value == "") {
			t.Fatalf("%q: address=%q err=%v calls=%d", value, address, err, calls)
		}
	}
	for _, value := range []string{"example.test:443", "192.0.2.1:443", "[2001:db8::1]:443"} {
		calls := 0
		_, err := outsideControl(value, func(network, address string, timeout time.Duration) (net.Conn, error) {
			calls++
			if network != "tcp" || address != value || timeout != 3*time.Second {
				t.Fatalf("control arguments: %s %s %s", network, address, timeout)
			}
			return nil, errors.New("fixture unreachable")
		})
		if calls != 1 || err == nil {
			t.Fatalf("%q: calls=%d err=%v", value, calls, err)
		}
	}
}

func TestOutsideNetworkOff(t *testing.T) {
	t.Setenv(OutsideAddressEnv, "")
	if got := OutsideNetwork(t); got != "" {
		t.Fatalf("disabled probe address %q", got)
	}
}

func TestOutsideControlClosesItsConnection(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	got, err := outsideControl("example.test:443", func(string, string, time.Duration) (net.Conn, error) {
		return client, nil
	})
	if err != nil || got != "example.test:443" {
		t.Fatalf("control: %q %v", got, err)
	}
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("control connection remained open")
	}
}
