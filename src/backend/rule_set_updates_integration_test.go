//go:build integration && linux

package magitrickle

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/utils/intID"
	"magitrickle/utils/iptables"
	"magitrickle/utils/netfilterTools"
	"magitrickle/utils/recordsCache"

	"golang.org/x/sys/unix"
)

type probeIPTables struct {
	iptables.Executable
	probe func()
}

func (p *probeIPTables) Save() ([]byte, error) {
	p.probe()
	return p.Executable.Save()
}

func (p *probeIPTables) Restore(data []byte) error {
	p.probe()
	err := p.Executable.Restore(data)
	p.probe()
	return err
}

// Probe real egress between every firewall transaction as well as after edits.
// The main table has a working WAN default, so a missing policy route or set
// produces an observable WAN packet instead of being hidden by no route.
func TestRealRuleSetSyncUpdates(t *testing.T) {
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	run("ip", "link", "set", "lo", "up")
	check(os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0600))
	check(os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0600))
	run("ip", "link", "add", "mtlan", "type", "veth", "peer", "name", "mtclient")
	defer exec.Command("ip", "link", "del", "mtlan").Run()
	for _, iface := range []string{"mtlan", "mtclient"} {
		run("ip", "link", "set", iface, "up")
	}
	run("ip", "addr", "add", "192.168.230.1/24", "dev", "mtlan")
	run("ip", "-6", "addr", "add", "fd00:230::1/64", "dev", "mtlan", "nodad")
	lan, err := net.InterfaceByName("mtlan")
	check(err)
	client, err := net.InterfaceByName("mtclient")
	check(err)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, 0)
	check(err)
	defer unix.Close(fd)
	for i, iface := range []string{"mtwan", "mtvpn", "mtother"} {
		run("ip", "link", "add", iface, "type", "dummy")
		defer exec.Command("ip", "link", "del", iface).Run()
		run("ip", "link", "set", iface, "up")
		run("ip", "addr", "add", fmt.Sprintf("192.0.%d.2/24", i+2), "dev", iface)
		run("ip", "-6", "addr", "add", fmt.Sprintf("2001:db8:%d::2/64", i+2), "dev", iface, "nodad")
	}
	run("ip", "route", "add", "default", "dev", "mtwan")
	run("ip", "-6", "route", "add", "default", "dev", "mtwan")
	families := []struct{ command, network, address string }{
		{"iptables", "udp4", "198.18.0.1"}, {"ip6tables", "udp6", "2001:db8:ffff::1"},
	}
	for _, f := range families {
		// Counters live in separate chains so app commits never reset them.
		for _, iface := range []string{"mtwan", "mtvpn", "mtother"} {
			run(f.command, "-t", "mangle", "-N", "EGRESS_"+iface)
			run(f.command, "-t", "mangle", "-A", "EGRESS_"+iface, "-j", "DROP")
			run(f.command, "-t", "mangle", "-A", "POSTROUTING", "-d", f.address, "-o", iface, "-j", "EGRESS_"+iface)
		}
	}
	sequence := uint16(10000)
	probe := func() {
		for _, f := range families {
			sequence++
			packet := forwardingProbe(client.HardwareAddr, lan.HardwareAddr, net.ParseIP(f.address), sequence)
			check(unix.Sendto(fd, packet, 0, &unix.SockaddrLinklayer{Ifindex: client.Index}))
		}
		// Let ingress/forward processing complete before the next table update.
		time.Sleep(time.Millisecond)
	}
	probing := false
	wrap := func(e iptables.Executable) *iptables.IPTables {
		return iptables.NewIPTables(&probeIPTables{Executable: e, probe: func() {
			if probing {
				probe()
			}
		}})
	}
	nh := &netfilterTools.Helper{ChainPrefix: "UP_", IpsetPrefix: "up_", StartIdx: 20000,
		IPTables4: wrap(iptables.NewRealIPTables()), IPTables6: wrap(iptables.NewRealIP6Tables())}
	for _, ipt := range []*iptables.IPTables{nh.IPTables4, nh.IPTables6} {
		for table, chain := range map[string]string{"mangle": "PREROUTING", "filter": "FORWARD", "nat": "POSTROUTING"} {
			check(ipt.RegisterChainPatch(table, chain))
		}
	}
	check(nh.InitRouting())
	defer nh.CleanIPTables()
	a := &App{config: constant.DefaultAppConfig, nfHelper: nh, recordsCache: recordsCache.New()}
	a.enabled.Store(true)
	defer func() {
		probing = false
		disableRuleSets(a.userRuleSets)
		disableRuleSets(a.subscriptionRuleSets)
	}()
	sub := &models.Subscription{ID: intID.ID{11}, Interface: "mtvpn", Enable: true, Rules: []*models.SubscriptionRule{
		{ID: intID.ID{12}, Type: models.RuleTypeSubnet, Rule: "198.18.0.0/15", Enable: true},
		{ID: intID.ID{13}, Type: models.RuleTypeSubnet6, Rule: "2001:db8::/32", Enable: true},
	}}
	check(a.AddSubscription(sub))
	original := a.subscriptionRuleSets[0]
	oldSet, oldLink := original.ipset, original.ipsetToLink
	count := func(command, iface string) uint64 {
		t.Helper()
		out := run(command, "-t", "mangle", "-L", "EGRESS_"+iface, "-nvx")
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) > 2 && f[2] == "DROP" {
				n, err := strconv.ParseUint(f[0], 10, 64)
				check(err)
				return n
			}
		}
		t.Fatalf("missing counter: %s", out)
		return 0
	}
	assertEgress := func(iface string) {
		t.Helper()
		for _, f := range families {
			before := count(f.command, iface)
			probe()
			if count(f.command, iface) <= before {
				t.Fatalf("%s: packet did not use %s", f.network, iface)
			}
			if n := count(f.command, "mtwan"); n != 0 {
				t.Fatalf("%s: %d packets leaked to WAN", f.network, n)
			}
		}
	}
	probing = true
	assertEgress("mtvpn")
	check(a.AddSubscription(&models.Subscription{ID: intID.ID{14}, Interface: "mtother", Enable: true}))
	updated := *sub
	updated.Rules = append(append([]*models.SubscriptionRule(nil), sub.Rules...), &models.SubscriptionRule{ID: intID.ID{15}, Type: models.RuleTypeSubnet, Rule: "203.0.113.0/24", Enable: true})
	check(a.ReplaceSubscriptions([]*models.Subscription{&updated, a.subscriptions[1]}))
	if a.subscriptionRuleSets[0] != original || original.ipset != oldSet || original.ipsetToLink != oldLink {
		t.Fatal("subscription refresh destroyed live routing")
	}
	assertEgress("mtvpn")
	group := &models.Group{ID: intID.ID{21}, Interface: "mtother", Enable: true, Rules: []*models.Rule{
		{ID: intID.ID{22}, Type: models.RuleTypeSubnet, Rule: "198.18.0.1/32", Enable: true},
		{ID: intID.ID{23}, Type: models.RuleTypeSubnet6, Rule: "2001:db8:ffff::1/128", Enable: true},
	}}
	check(a.AddGroup(group))
	g := a.userRuleSets[0]
	oldSet, oldLink = g.ipset, g.ipsetToLink
	assertEgress("mtother")
	changed := *group
	changed.Interface = "mtvpn"
	check(a.UpdateGroup(&changed))
	if g.ipset != oldSet || g.ipsetToLink != oldLink {
		t.Fatal("interface edit destroyed live routing")
	}
	assertEgress("mtvpn")
	check(a.ReplaceGroups([]*models.Group{group}))
	assertEgress("mtother")
	// Edit membership and interface together, then transfer addresses between
	// two existing groups in both directions without falling into subscriptions.
	combined := *group
	combined.Interface = "mtvpn"
	combined.Rules = append(append([]*models.Rule(nil), group.Rules...), &models.Rule{ID: intID.ID{25}, Type: models.RuleTypeSubnet, Rule: "203.0.113.1/32", Enable: true})
	check(a.UpdateGroup(&combined))
	assertEgress("mtvpn")
	check(a.UpdateGroup(group))
	assertEgress("mtother")
	receiver := &models.Group{ID: intID.ID{24}, Interface: "mtother", Enable: true}
	check(a.AddGroup(receiver))
	empty, received := *group, *receiver
	empty.Rules, received.Rules = nil, group.Rules
	check(a.ReplaceGroups([]*models.Group{&empty, &received}))
	assertEgress("mtother")
	check(a.ReplaceGroups([]*models.Group{group, receiver}))
	assertEgress("mtother")
	invalid := *group
	invalid.Interface = "interface-name-too-long-for-kernel"
	if err := a.UpdateGroup(&invalid); err == nil {
		t.Fatal("invalid interface unexpectedly accepted")
	}
	assertEgress("mtother")
	// A bad replacement must preserve both old address families.
	if err := g.ipset.Replace(map[netfilterTools.IPv4Subnet]netfilterTools.IPSetTimeout{{Address: [4]byte{198, 18, 0, 1}, CIDR: 32}: nil}, map[netfilterTools.IPv6Subnet]netfilterTools.IPSetTimeout{{CIDR: 255}: nil}); err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	assertEgress("mtother")
	// Missing/down/deleted VPN and explicit blackhole must not fall through
	// to the subscription VPN or the working system WAN default.
	assertBlocked := func() {
		t.Helper()
		for _, f := range families {
			for _, iface := range []string{"mtwan", "mtvpn", "mtother"} {
				before := count(f.command, iface)
				probe()
				if count(f.command, iface) != before {
					t.Fatalf("%s: blocked traffic escaped through %s", f.network, iface)
				}
			}
		}
	}
	for _, iface := range []string{"missing-vpn", "blackhole", "mtother"} {
		changed.Interface = iface
		check(a.UpdateGroup(&changed))
		if iface == "mtother" {
			run("ip", "link", "set", "mtother", "down")
		}
		assertBlocked()
	}
	run("ip", "link", "del", "mtother")
	assertBlocked()
	// Prove that the probe detects the working fallback route when routing is
	// deliberately disabled; zero WAN counters above must not be a dead probe.
	probing = false
	check(disableRuleSets(a.userRuleSets))
	check(disableRuleSets(a.subscriptionRuleSets))
	probe()
	for _, f := range families {
		if count(f.command, "mtwan") == 0 {
			t.Fatalf("%s: WAN control probe did not arrive", f.network)
		}
	}
}

func forwardingProbe(sourceMAC, destMAC net.HardwareAddr, dest net.IP, port uint16) []byte {
	checksum := func(data []byte) uint16 {
		var sum uint32
		for len(data) >= 2 {
			sum += uint32(binary.BigEndian.Uint16(data))
			data = data[2:]
		}
		if len(data) != 0 {
			sum += uint32(data[0]) << 8
		}
		for sum>>16 != 0 {
			sum = sum&0xffff + sum>>16
		}
		return ^uint16(sum)
	}
	udp := make([]byte, 10)
	binary.BigEndian.PutUint16(udp, port)
	binary.BigEndian.PutUint16(udp[2:], 53535)
	binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
	udp[8], udp[9] = 'm', 't'
	ethernet := make([]byte, 14)
	copy(ethernet, destMAC)
	copy(ethernet[6:], sourceMAC)
	var ip []byte
	if dest.To4() != nil {
		binary.BigEndian.PutUint16(ethernet[12:], unix.ETH_P_IP)
		ip = make([]byte, 20)
		ip[0], ip[8], ip[9] = 0x45, 64, 17
		binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)+len(udp)))
		copy(ip[12:], net.IP{192, 168, 230, 2})
		copy(ip[16:], dest.To4())
		binary.BigEndian.PutUint16(ip[10:], checksum(ip))
	} else {
		binary.BigEndian.PutUint16(ethernet[12:], unix.ETH_P_IPV6)
		ip = make([]byte, 40)
		ip[0], ip[6], ip[7] = 0x60, 17, 64
		binary.BigEndian.PutUint16(ip[4:], uint16(len(udp)))
		copy(ip[8:], net.ParseIP("fd00:230::2").To16())
		copy(ip[24:], dest.To16())
		pseudo := append(append([]byte(nil), ip[8:]...), 0, 0, 0, byte(len(udp)), 0, 0, 0, 17)
		value := checksum(append(pseudo, udp...))
		if value == 0 {
			value = 0xffff
		}
		binary.BigEndian.PutUint16(udp[6:], value)
	}
	return append(append(ethernet, ip...), udp...)
}
