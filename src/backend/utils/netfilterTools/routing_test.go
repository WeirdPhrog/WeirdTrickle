//go:build testing

package netfilterTools

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"magitrickle/utils/iptables"
)

func TestRoutingPriority(t *testing.T) {
	for _, proto := range []iptables.Protocol{iptables.ProtocolIPv4, iptables.ProtocolIPv6} {
		for _, subscriptionFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("family=%d/subscriptionFirst=%t", proto, subscriptionFirst), func(t *testing.T) {
				fake := iptables.NewFakeIPTables(proto)
				ipt := iptables.NewIPTables(fake)
				nh := &Helper{ChainPrefix: "MT_", IpsetPrefix: "mt_"}
				if proto == iptables.ProtocolIPv4 {
					nh.IPTables4 = ipt
				} else {
					nh.IPTables6 = ipt
				}
				for table, chain := range map[string]string{"mangle": "PREROUTING", "filter": "FORWARD", "nat": "POSTROUTING"} {
					must(t, ipt.RegisterChainPatch(table, chain))
				}
				must(t, nh.InitRouting())
				group := nh.IPSetToLink("group", "vpn-group", nh.IPSet("group"), false)
				sub := nh.IPSetToLink("sub-test", "vpn-sub", nh.IPSet("sub-test"), true)
				group.mark, sub.mark = 101, 202
				order := []*IPSetToLink{group, sub}
				if subscriptionFirst {
					order = []*IPSetToLink{sub, group}
				}
				for _, r := range order {
					must(t, r.insertIPTablesRules(ipt))
				}
				suffix := "_4"
				if proto == iptables.ProtocolIPv6 {
					suffix = "_6"
				}
				sets := map[string]bool{"mt_group" + suffix: true, "mt_sub-test" + suffix: true}
				check := func(want uint32) {
					t.Helper()
					mark, connmark := routePacket(t, fake, sets, false, 77)
					if mark != want || connmark != want {
						t.Fatalf("packet/connection marks = %d/%d, want %d", mark, connmark, want)
					}
				}
				check(group.mark)
				// Subscription refresh and group interface edits must not change tiers.
				for _, r := range []*IPSetToLink{sub, group, sub} {
					must(t, r.deleteIPTablesRules(ipt))
					must(t, r.insertIPTablesRules(ipt))
					check(group.mark)
				}
				must(t, ipt.Commit()) // Netfilter hook / firewall reconstruction.
				check(group.mark)
				sets["mt_group"+suffix] = false // DNS address expires or stops matching.
				check(sub.mark)
				sets["mt_group"+suffix] = true
				must(t, group.deleteIPTablesRules(ipt)) // Disabled/deleted group.
				check(sub.mark)
				must(t, group.insertIPTablesRules(ipt))
				sets["mt_sub-test"+suffix] = false
				check(group.mark)
				sets["mt_group"+suffix] = false
				check(77) // Preserve unrelated marks when neither tier matches.
				sets["mt_group"+suffix], sets["mt_sub-test"+suffix] = true, true
				if mark, connmark := routePacket(t, fake, sets, true, 88); mark != 88 || connmark != 88 {
					t.Fatal("reply traffic was re-marked")
				}
				if got := fake.GetRules("mangle", "MT_ROUTE"); !reflect.DeepEqual(got, [][]string{{"-m", "conntrack", "--ctdir", "REPLY", "-j", "RETURN"}, {"-j", "MT_SUBS"}, {"-j", "MT_GROUPS"}}) {
					t.Fatalf("unexpected tier order: %v", got)
				}
				must(t, nh.CleanIPTables())
				for _, name := range []string{"MT_ROUTE", "MT_SUBS", "MT_GROUPS", group.chainName, sub.chainName} {
					if fake.ChainExists("mangle", name) {
						t.Fatalf("chain %s survived cleanup", name)
					}
				}
				must(t, nh.InitRouting())
				must(t, sub.insertIPTablesRules(ipt))
				must(t, group.insertIPTablesRules(ipt))
				check(group.mark)
			})
		}
	}
}

func TestRoutingMatchesAndSkips(t *testing.T) {
	for _, proto := range []iptables.Protocol{iptables.ProtocolIPv4, iptables.ProtocolIPv6} {
		t.Run(fmt.Sprint(proto), func(t *testing.T) {
			fake := iptables.NewFakeIPTables(proto)
			ipt := iptables.NewIPTables(fake)
			nh := &Helper{ChainPrefix: "MT_", IpsetPrefix: "mt_"}
			if proto == iptables.ProtocolIPv4 {
				nh.IPTables4 = ipt
			} else {
				nh.IPTables6 = ipt
			}
			for table, chain := range map[string]string{"mangle": "PREROUTING", "filter": "FORWARD", "nat": "POSTROUTING"} {
				must(t, ipt.RegisterChainPatch(table, chain))
			}
			must(t, nh.InitRouting())
			sub1 := nh.IPSetToLink("sub1", "vpn-sub1", nh.IPSet("sub1"), true)
			sub2 := nh.IPSetToLink("sub2", "vpn-sub2", nh.IPSet("sub2"), true)
			group1 := nh.IPSetToLink("group1", "vpn-group1", nh.IPSet("group1"), false)
			group2 := nh.IPSetToLink("group2", Blackhole, nh.IPSet("group2"), false)
			sub1.mark, sub2.mark, group1.mark, group2.mark = 101, 102, 201, 202
			// Interleaved installation must still give groups priority. Within
			// each tier the last matching rule set wins, including blackholes.
			for _, r := range []*IPSetToLink{group1, sub1, group2, sub2} {
				must(t, r.insertIPTablesRules(ipt))
			}
			rules := []*IPSetToLink{sub1, sub2, group1, group2}
			check := func(priority []*IPSetToLink) {
				t.Helper()
				for mask := 0; mask < 1<<len(rules); mask++ {
					sets := map[string]bool{}
					for i, r := range rules {
						sets[r.ipsetName(ipt)] = mask&(1<<i) != 0
					}
					for _, reply := range []bool{false, true} {
						// Different packet/connection marks expose accidental saves
						// on nonmatches, even if the packet already has our mark.
						for _, initial := range [][2]uint32{{0, 0}, {77, 88}, {group1.mark, 99}} {
							wantMark, wantConnmark := initial[0], initial[1]
							if !reply {
								for _, r := range priority {
									if sets[r.ipsetName(ipt)] {
										wantMark, wantConnmark = r.mark, r.mark
									}
								}
							}
							got := traceRouting(t, fake, sets, reply, initial[0], initial[1])
							if got.mark != wantMark || got.connmark != wantConnmark {
								t.Fatalf("mask=%d reply=%t initial=%v: marks=%d/%d, want %d/%d", mask, reply, initial, got.mark, got.connmark, wantMark, wantConnmark)
							}
							wantLookups := len(rules)
							if reply {
								wantLookups = 0
							}
							if got.setLookups != wantLookups || got.directionChecks != 1 {
								t.Fatalf("lookups/direction checks=%d/%d, want %d/1", got.setLookups, got.directionChecks, wantLookups)
							}
							for _, r := range rules {
								if got.visited[r.chainName] != (!reply && sets[r.ipsetName(ipt)]) {
									t.Fatalf("unexpected visit to rule set %s (reply=%t mask=%d)", r.chainName, reply, mask)
								}
							}
						}
					}
				}
			}
			check([]*IPSetToLink{sub1, sub2, group1, group2})
			must(t, group1.deleteIPTablesRules(ipt))
			must(t, group1.insertIPTablesRules(ipt))
			check([]*IPSetToLink{sub1, sub2, group2, group1})
			must(t, sub1.deleteIPTablesRules(ipt))
			must(t, sub1.insertIPTablesRules(ipt))
			check([]*IPSetToLink{sub2, sub1, group2, group1})
		})
	}
}

func TestDirectRoutingExemption(t *testing.T) {
	for _, proto := range []iptables.Protocol{iptables.ProtocolIPv4, iptables.ProtocolIPv6} {
		t.Run(fmt.Sprint(proto), func(t *testing.T) {
			fake := iptables.NewFakeIPTables(proto)
			ipt := iptables.NewIPTables(fake)
			nh := &Helper{ChainPrefix: "MT_", IpsetPrefix: "mt_"}
			if proto == iptables.ProtocolIPv4 {
				nh.IPTables4 = ipt
			} else {
				nh.IPTables6 = ipt
			}
			for table, chain := range map[string]string{"mangle": "PREROUTING", "filter": "FORWARD", "nat": "POSTROUTING"} {
				must(t, ipt.RegisterChainPatch(table, chain))
			}
			must(t, nh.InitRouting())
			sub := nh.IPSetToLink("sub", "vpn-sub", nh.IPSet("sub"), true)
			group := nh.IPSetToLink("group", "vpn-group", nh.IPSet("group"), false)
			direct := nh.IPSetToLink("direct", Direct, nh.IPSet("direct"), false)
			sub.mark, group.mark = 101, 201
			must(t, sub.insertIPTablesRules(ipt))
			must(t, group.insertIPTablesRules(ipt))
			must(t, direct.Enable())
			// Ordering a tier must not reference the direct set's (absent) chain.
			must(t, nh.SetRoutingOrder([]string{"direct", "group"}, false))
			if fake.ChainExists("mangle", direct.chainName) || fake.ChainExists("filter", direct.chainName) || fake.ChainExists("nat", direct.chainName) {
				t.Fatal("direct rule set created marking, filter or NAT rules")
			}
			for mask := 0; mask < 8; mask++ {
				sets := map[string]bool{
					sub.ipsetName(ipt):    mask&1 != 0,
					group.ipsetName(ipt):  mask&2 != 0,
					direct.ipsetName(ipt): mask&4 != 0,
				}
				want := uint32(77)
				switch {
				case sets[direct.ipsetName(ipt)]:
				case sets[group.ipsetName(ipt)]:
					want = group.mark
				case sets[sub.ipsetName(ipt)]:
					want = sub.mark
				}
				if mark, connmark := routePacket(t, fake, sets, false, 77); mark != want || connmark != want {
					t.Fatalf("mask=%d: marks=%d/%d, want %d", mask, mark, connmark, want)
				}
			}
			// Firewall reconstruction keeps the exemption.
			must(t, ipt.Commit())
			all := map[string]bool{sub.ipsetName(ipt): true, group.ipsetName(ipt): true, direct.ipsetName(ipt): true}
			if mark, _ := routePacket(t, fake, all, false, 77); mark != 77 {
				t.Fatalf("exemption lost after reconstruction: mark %d", mark)
			}
			// Suspending a direct set (failed rollback) withdraws its exemption, so
			// unverified addresses follow the other sets instead of bypassing VPN.
			must(t, direct.SetInterface(Blackhole))
			// A suspended set is still direct: tiers must not jump to its absent chain.
			must(t, nh.SetRoutingOrder([]string{"direct", "group"}, false))
			if mark, _ := routePacket(t, fake, all, false, 77); mark != group.mark {
				t.Fatalf("suspended direct set still exempts traffic: mark %d", mark)
			}
			must(t, direct.SetInterface(Direct))
			if mark, _ := routePacket(t, fake, all, false, 77); mark != 77 {
				t.Fatalf("resumed direct set does not exempt traffic: mark %d", mark)
			}
			if direct.SetInterface("vpn-group") == nil || group.SetInterface(Direct) == nil {
				t.Fatal("mode switch was applied in place")
			}
			must(t, direct.Disable())
			if mark, _ := routePacket(t, fake, all, false, 77); mark != group.mark {
				t.Fatalf("disabled direct set still exempts traffic: mark %d", mark)
			}
			if got := fake.GetRules("mangle", "MT_ROUTE"); !reflect.DeepEqual(got, [][]string{{"-m", "conntrack", "--ctdir", "REPLY", "-j", "RETURN"}, {"-j", "MT_SUBS"}, {"-j", "MT_GROUPS"}}) {
				t.Fatalf("unexpected ROUTE chain after disabling: %v", got)
			}
		})
	}
}

// failingRestore makes chosen iptables-restore calls fail.
type failingRestore struct {
	*iptables.FakeIPTables
	fail func() bool
}

func (f *failingRestore) Restore(data []byte) error {
	if f.fail() {
		return fmt.Errorf("injected restore failure")
	}
	return f.FakeIPTables.Restore(data)
}

// IPv4 applies, IPv6 fails and the IPv4 rollback fails as well: the next
// removal must still rewrite ROUTE instead of trusting the in-memory list.
func TestDirectExemptionRecoversFromPartialFailure(t *testing.T) {
	fake4 := iptables.NewFakeIPTables(iptables.ProtocolIPv4)
	fake6 := iptables.NewFakeIPTables(iptables.ProtocolIPv6)
	failing := false
	restores4 := 0
	ipt4 := iptables.NewIPTables(&failingRestore{fake4, func() bool { restores4++; return failing && restores4 > 1 }})
	ipt6 := iptables.NewIPTables(&failingRestore{fake6, func() bool { return failing }})
	nh := &Helper{ChainPrefix: "MT_", IpsetPrefix: "mt_", IPTables4: ipt4, IPTables6: ipt6}
	for _, ipt := range []*iptables.IPTables{ipt4, ipt6} {
		for table, chain := range map[string]string{"mangle": "PREROUTING", "filter": "FORWARD", "nat": "POSTROUTING"} {
			must(t, ipt.RegisterChainPatch(table, chain))
		}
	}
	must(t, nh.InitRouting())
	restores4 = 0
	failing = true
	direct := nh.IPSetToLink("direct", Direct, nh.IPSet("direct"), false)
	if direct.Enable() == nil {
		t.Fatal("injected failure was not reported")
	}
	if got := len(fake4.GetRules("mangle", "MT_ROUTE")); got != 4 {
		t.Fatalf("test setup: IPv4 should keep the stale exemption, ROUTE has %d rules", got)
	}
	failing = false
	must(t, direct.Disable())
	want := [][]string{{"-m", "conntrack", "--ctdir", "REPLY", "-j", "RETURN"}, {"-j", "MT_SUBS"}, {"-j", "MT_GROUPS"}}
	if got := fake4.GetRules("mangle", "MT_ROUTE"); !reflect.DeepEqual(got, want) {
		t.Fatalf("stale IPv4 exemption survived cleanup: %v", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func routePacket(t *testing.T, fake *iptables.FakeIPTables, sets map[string]bool, reply bool, initial uint32) (uint32, uint32) {
	got := traceRouting(t, fake, sets, reply, initial, initial)
	return got.mark, got.connmark
}

type routingTrace struct {
	mark, connmark              uint32
	setLookups, directionChecks int
	visited                     map[string]bool
}

// Interpret only the generated mangle rules, failing on unexpected targets.
func traceRouting(t *testing.T, fake *iptables.FakeIPTables, sets map[string]bool, reply bool, mark, connmark uint32) routingTrace {
	t.Helper()
	trace := routingTrace{mark: mark, connmark: connmark, visited: map[string]bool{}}
	var walk func(string)
	walk = func(chain string) {
		trace.visited[chain] = true
		for _, rule := range fake.GetRules("mangle", chain) {
			arg := func(flag string) string {
				for i := 0; i+1 < len(rule); i++ {
					if rule[i] == flag {
						return rule[i+1]
					}
				}
				return ""
			}
			if arg("--ctdir") == "REPLY" {
				trace.directionChecks++
				if !reply {
					continue
				}
			}
			if set := arg("--match-set"); set != "" {
				trace.setLookups++
				if !sets[set] {
					continue
				}
			}
			switch target := arg("-j"); target {
			case "RETURN":
				return
			case "MARK":
				value, err := strconv.ParseUint(arg("--set-mark"), 10, 32)
				must(t, err)
				trace.mark = uint32(value)
			case "CONNMARK":
				trace.connmark = trace.mark
			default:
				if !fake.ChainExists("mangle", target) {
					t.Fatalf("unknown target %q", target)
				}
				walk(target)
			}
		}
	}
	walk("PREROUTING")
	return trace
}
