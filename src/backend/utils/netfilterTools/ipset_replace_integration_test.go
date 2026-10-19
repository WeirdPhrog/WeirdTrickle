//go:build integration && linux

package netfilterTools

import (
	"net/netip"
	"testing"
)

func TestRealRoutingLargeIPSetReplacement(t *testing.T) {
	set := (&Helper{IpsetPrefix: "large_"}).IPSet("sub")
	if err := set.Enable(); err != nil {
		t.Fatal(err)
	}
	defer set.Disable()
	addresses := make(map[netip.Prefix]IPSetTimeout, 65537)
	for i := 0; i < 65537; i++ {
		addresses[netip.PrefixFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 32)] = nil
	}
	if err := set.Replace(addresses); err != nil {
		t.Fatal(err)
	}
	stored, err := set.List()
	if err != nil || len(stored) != len(addresses) {
		t.Fatalf("large subscription truncated: %d/%d: %v", len(stored), len(addresses), err)
	}
	if err := set.Replace(map[netip.Prefix]IPSetTimeout{netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 0, 2, 1}), 255): nil}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	stored, err = set.List()
	if err != nil || len(stored) != len(addresses) {
		t.Fatalf("failed replacement damaged the active set: %d: %v", len(stored), err)
	}
}
