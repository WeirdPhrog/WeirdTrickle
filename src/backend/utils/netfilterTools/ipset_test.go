package netfilterTools

import (
	"net/netip"
	"testing"
)

func TestIPSetFamilySelection(t *testing.T) {
	set := &IPSet{ipsetName: "mt_x"}
	for prefix, want := range map[string]string{
		"192.0.2.0/24":         "mt_x_4",
		"2001:db8::1/128":      "mt_x_6",
		"::ffff:192.0.2.1/128": "mt_x_6",
	} {
		if got := set.setName(netip.MustParsePrefix(prefix)); got != want {
			t.Errorf("%s: set %q, want %q", prefix, got, want)
		}
	}
}

func TestIPSetEntryKeepsAddressLength(t *testing.T) {
	for prefix, length := range map[string]int{
		"192.0.2.0/24":         4,
		"2001:db8::/32":        16,
		"::ffff:192.0.2.1/128": 16,
	} {
		entry := ipsetEntry(netip.MustParsePrefix(prefix), nil)
		if len(entry.IP) != length || int(entry.CIDR) != netip.MustParsePrefix(prefix).Bits() {
			t.Errorf("%s: entry %v/%d", prefix, entry.IP, entry.CIDR)
		}
	}
}
