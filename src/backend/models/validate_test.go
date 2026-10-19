package models

import "testing"

func TestNormalizeGroupColor(t *testing.T) {
	for input, want := range map[string]string{
		"#A1b2C3":  "#a1b2c3",
		"#ffffff":  "#ffffff",
		"":         "#ffffff",
		"#fff":     "#ffffff",
		"a1b2c3":   "#ffffff",
		"#a1b2c3 ": "#ffffff",
	} {
		if got := NormalizeGroupColor(input); got != want {
			t.Errorf("NormalizeGroupColor(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsValidInterfaceName(t *testing.T) {
	for _, name := range []string{"", "nwg0", "tun0", "ppp0", "lo", "eth0.100", "veth-1", "wg-vpn.1", "a_b", "123456789012345"} {
		if !IsValidInterfaceName(name) {
			t.Errorf("rejected %q", name)
		}
	}
	for _, name := range []string{".", "..", "1234567890123456", "eth0/1", "eth 0", " wg0", "wg0:1", "wg0\x00", "eth0\n"} {
		if IsValidInterfaceName(name) {
			t.Errorf("accepted %q", name)
		}
	}
}
