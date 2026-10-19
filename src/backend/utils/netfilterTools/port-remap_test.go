//go:build testing

package netfilterTools

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"magitrickle/utils/iptables"
)

type remapExecutable struct {
	*iptables.FakeIPTables
	commits  int
	failNext bool
}

func (e *remapExecutable) Restore(data []byte) error {
	e.commits++
	if e.failNext {
		e.failNext = false
		return errors.New("kernel rejected DNS rules")
	}
	return e.FakeIPTables.Restore(data)
}

func remapAddr(value string) netlink.Addr {
	ip := net.ParseIP(value)
	return netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}}
}

func TestDNSRemapUpdatesAddressesAndPreservesPreviousOnFailure(t *testing.T) {
	v4 := &remapExecutable{FakeIPTables: iptables.NewFakeIPTables(iptables.ProtocolIPv4)}
	v6 := &remapExecutable{FakeIPTables: iptables.NewFakeIPTables(iptables.ProtocolIPv6)}
	helper := &Helper{ChainPrefix: "MT_", IPTables4: iptables.NewIPTables(v4), IPTables6: iptables.NewIPTables(v6)}
	for _, ipt := range []*iptables.IPTables{helper.IPTables4, helper.IPTables6} {
		if err := ipt.RegisterChainPatch("nat", "PREROUTING"); err != nil {
			t.Fatal(err)
		}
	}
	initial := []netlink.Addr{remapAddr("192.0.2.1"), remapAddr("2001:db8::1")}
	r := helper.PortRemap("DNSOR", 53, 3553, initial)
	if err := r.Enable(); err != nil {
		t.Fatal(err)
	}
	v4Before, v6Before := v4.commits, v6.commits
	// Reordering, duplicate IPs and differing masks do not rebuild rules.
	if err := r.UpdateAddresses([]netlink.Addr{initial[1], initial[0], initial[0]}); err != nil {
		t.Fatal(err)
	}
	if v4.commits != v4Before || v6.commits != v6Before {
		t.Fatal("unchanged addresses caused commits")
	}
	next := []netlink.Addr{initial[0], remapAddr("2001:db8::2")}
	v6.failNext = true
	if err := r.UpdateAddresses(next); err == nil {
		t.Fatal("expected kernel failure")
	}
	if v4.commits != v4Before {
		t.Fatal("IPv6 change rebuilt healthy IPv4 rules")
	}
	for _, rule := range v6.GetRules("nat", "MT_DNSOR") {
		if !strings.Contains(strings.Join(rule, " "), "2001:db8::1") {
			t.Fatal("failed update lost previous rules")
		}
	}
	if err := r.UpdateAddresses(next); err != nil {
		t.Fatal(err)
	}
	for _, rule := range v6.GetRules("nat", "MT_DNSOR") {
		if !strings.Contains(strings.Join(rule, " "), "2001:db8::2") {
			t.Fatal("new IPv6 address was not intercepted")
		}
	}
	if err := r.UpdateAddresses(next[:1]); err != nil {
		t.Fatal(err)
	}
	if len(v6.GetRules("nat", "MT_DNSOR")) != 0 {
		t.Fatal("removed IPv6 address remained intercepted")
	}
	if len(v4.GetRules("nat", "MT_DNSOR")) != 2 {
		t.Fatal("IPv4 capture changed")
	}
	if len(v6.GetRules("nat", "PREROUTING")) != 1 {
		t.Fatal("remap hook lost or duplicated")
	}
}
