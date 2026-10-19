//go:build integration && linux

package magitrickle

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"magitrickle/constant"
	"magitrickle/utils/iptables"
	"magitrickle/utils/netfilterTools"
)

// Called only by the existing isolated-namespace CI job, for legacy and nft.
func TestRealRuleSetSyncDNSRemapAddresses(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	lan := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "mtdns"}}
	check(netlink.LinkAdd(lan))
	defer netlink.LinkDel(lan)
	check(netlink.LinkSetUp(lan))
	add := func(value string) *netlink.Addr {
		t.Helper()
		address, err := netlink.ParseAddr(value)
		check(err)
		address.Flags = unix.IFA_F_NODAD
		check(netlink.AddrAdd(lan, address))
		return address
	}
	add("192.0.2.10/24")
	old6 := add("2001:db8:5353::1/64")
	nh, err := netfilterTools.New("DN_", "dn_", false, false, 25000)
	check(err)
	defer nh.CleanIPTables()
	for _, ipt := range []*iptables.IPTables{nh.IPTables4, nh.IPTables6} {
		check(ipt.RegisterChainPatch("nat", "PREROUTING"))
	}
	a := &App{config: constant.DefaultAppConfig, nfHelper: nh}
	a.config.Link = []string{"mtdns"}
	a.publishConfigLocked()
	addresses, err := a.lanAddresses()
	check(err)
	a.dnsOverrider = nh.PortRemap("DNSOR", 53, 3553, addresses)
	check(a.dnsOverrider.Enable())
	defer a.dnsOverrider.Disable()
	readRules := func(command string) string {
		t.Helper()
		out, err := exec.Command(command, "-t", "nat", "-S", "DN_DNSOR").CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", command, err, out)
		}
		return string(out)
	}
	add("192.0.2.11/24")
	add("2001:db8:5353::2/64")
	check(a.refreshDNSRemap())
	for _, expect := range []struct{ command, ip string }{{"iptables", "192.0.2.11"}, {"ip6tables", "2001:db8:5353::2"}} {
		if strings.Count(readRules(expect.command), "-d "+expect.ip+"/") != 2 {
			t.Fatalf("TCP/UDP DNS capture missing for %s", expect.ip)
		}
	}
	check(netlink.AddrDel(lan, old6))
	check(a.refreshDNSRemap())
	if strings.Contains(readRules("ip6tables"), "2001:db8:5353::1/") {
		t.Fatal("removed LAN address remained in DNS capture")
	}
	check(netlink.LinkDel(lan))
	check(a.refreshDNSRemap())
	for _, command := range []string{"iptables", "ip6tables"} {
		if strings.Contains(readRules(command), "-A ") {
			t.Fatal("deleted LAN retained DNS capture destinations")
		}
	}
}
