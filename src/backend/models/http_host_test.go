package models

import "testing"

func TestHTTPAllowedHostNormalization(t *testing.T) {
	for value, expected := range map[string]string{
		"Router.Example.": "router.example", "пример.рф": "xn--e1afmkfd.xn--p1ai",
		"[::1]": "::1", "2001:db8::1": "2001:db8::1", "fe80::1%eth0": "fe80::1",
	} {
		actual, err := NormalizeHTTPHost(value)
		if err != nil || actual != expected {
			t.Fatalf("%s: %s %v", value, actual, err)
		}
	}
	for _, value := range []string{"", "*", "*.example.com", "http://router.example", "router.example:8080", "user@router.example", "router.example/path", "router.example?token", "router.example\n", "[router.example]", "[::1", "a..example", "-host.example"} {
		if _, err := NormalizeHTTPHost(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
