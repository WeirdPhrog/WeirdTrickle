//go:build integration && linux

package magitrickle

import (
	"testing"

	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/utils/intID"
	"magitrickle/utils/iptables"
	"magitrickle/utils/netfilterTools"
	"magitrickle/utils/recordsCache"

	"github.com/miekg/dns"
	"github.com/vishvananda/netlink"
)

func TestRealRuleSetSyncDNSBindings(t *testing.T) {
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	nh, err := netfilterTools.New("DN_", "dn_", false, false, 30000)
	check(err)
	for _, ipt := range []*iptables.IPTables{nh.IPTables4, nh.IPTables6} {
		for table, chain := range map[string]string{"mangle": "PREROUTING", "filter": "FORWARD", "nat": "POSTROUTING"} {
			check(ipt.RegisterChainPatch(table, chain))
		}
	}
	check(nh.InitRouting())
	defer nh.CleanIPTables()
	a := &App{config: constant.DefaultAppConfig, nfHelper: nh, recordsCache: recordsCache.New()}
	a.config.Netfilter.IPSet.AdditionalTTL = 0
	a.enabled.Store(true)
	check(a.AddGroup(&models.Group{ID: intID.ID{31}, Interface: "blackhole", Enable: true, Rules: []*models.Rule{{ID: intID.ID{32}, Type: models.RuleTypeDomain, Rule: "www.example", Enable: true}}}))
	g := a.userRuleSets[0]
	defer g.Disable()
	query := new(dns.Msg)
	query.SetQuestion("www.example.", dns.TypeHTTPS)
	reply := new(dns.Msg)
	reply.SetReply(query)
	binding, err := dns.NewRR("www.example. 120 IN HTTPS 1 target.example. ipv4hint=192.0.2.10 ipv6hint=2001:db8::10")
	check(err)
	reply.Answer = []dns.RR{binding}
	extra, err := dns.NewRR("target.example. 120 IN A 192.0.2.11")
	check(err)
	reply.Extra = []dns.RR{extra}
	response, err := a.dnsResponseHook(nil, *query, *reply, "udp")
	check(err)
	if response.Rcode != dns.RcodeSuccess {
		t.Fatal("HTTPS response failed")
	}
	assertSets := func() {
		t.Helper()
		v4, err := g.ListIPv4Subnets()
		check(err)
		v6, err := g.ListIPv6Subnets()
		check(err)
		if len(v4) != 2 || len(v6) != 1 {
			t.Fatalf("hints/additional addresses missing from kernel: %v %v", v4, v6)
		}
		for _, ttl := range v4 {
			if ttl == nil || *ttl < 100 {
				t.Fatalf("short DNS answer reduced existing timeout: %v", ttl)
			}
		}
	}
	assertSets()
	binding.Header().Ttl = 1
	_, err = a.dnsResponseHook(nil, *query, *reply, "udp")
	check(err)
	assertSets()
	check(g.Sync())
	assertSets()
	// Simulate a kernel/ipset failure, then ensure the client never receives
	// a successful DNS answer containing an address we failed to route.
	check(nh.CleanIPTables())
	check(netlink.IpsetDestroy("dn_" + g.RuntimeKey() + "_4"))
	failed, err := dns.NewRR("www.example. 120 IN A 192.0.2.99")
	check(err)
	reply.Answer, reply.Extra = []dns.RR{failed}, nil
	response, err = a.dnsResponseHook(nil, *query, *reply, "udp")
	check(err)
	if response == nil || response.Rcode != dns.RcodeServerFailure || len(response.Answer) != 0 {
		t.Fatalf("unroutable address was returned to the client: %v", response)
	}
}
