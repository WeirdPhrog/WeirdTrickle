package magitrickle

import (
	"net"
	"testing"
	"time"

	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/utils/recordsCache"

	"github.com/miekg/dns"
	"github.com/vishvananda/netlink"
)

func TestUnrelatedDNSDoesNotWaitForRoutingUpdate(t *testing.T) {
	a := &App{config: constant.DefaultAppConfig, recordsCache: recordsCache.New()}
	a.publishConfigLocked()
	group := &models.Group{Enable: true, Rules: []*models.Rule{
		{Type: models.RuleTypeDomain, Rule: "vpn.example", Enable: true},
	}}
	if err := a.AddGroup(group); err != nil {
		t.Fatal(err)
	}
	a.stateMu.Lock() // Model a slow ipset replacement holding the write lock.
	defer a.stateMu.Unlock()
	done := make(chan error, 1)
	go func() {
		query := new(dns.Msg)
		query.SetQuestion("1.0.0.127.in-addr.arpa.", dns.TypePTR)
		if _, _, err := a.dnsRequestHook(nil, *query, "udp"); err != nil {
			done <- err
			return
		}
		query.SetQuestion("unrelated.example.", dns.TypeA)
		reply := new(dns.Msg)
		reply.SetReply(query)
		rr, _ := dns.NewRR("unrelated.example. 60 IN A 192.0.2.1")
		reply.Answer = []dns.RR{rr}
		_, err := a.dnsResponseHook(nil, *query, *reply, "udp")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated DNS waited for the routing lock")
	}
}

func TestDNSSnapshotIncludesPendingRulesAndDropsRolledBackRules(t *testing.T) {
	a := &App{}
	current := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeDomain, Rule: "old.example", Enable: true}}}
	if err := a.AddGroup(current); err != nil {
		t.Fatal(err)
	}
	next := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeNamespace, Rule: "new.example", Enable: true}}}
	a.stateMu.Lock()
	a.publishDNSRulesLocked([]rulesets.Spec{{Model: next}})
	for _, name := range []string{"old.example", "www.new.example"} {
		if !a.dnsRules.Load().matches([]string{name}) {
			t.Errorf("pending update would let %s bypass routing", name)
		}
	}
	done := make(chan struct{})
	go func() { a.dnsRuleSetSnapshot([]string{"www.new.example"}); close(done) }()
	select {
	case <-done:
		t.Error("a pending routing rule was bypassed")
	case <-time.After(20 * time.Millisecond):
	}
	a.publishDNSRulesLocked(nil) // Rolled back, leaving only the old model.
	a.stateMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("routing reader did not resume after update")
	}
	if a.dnsRules.Load().matches([]string{"www.new.example"}) {
		t.Fatal("rolled-back rules remained in the snapshot")
	}
}

func TestIncompleteRuleSetOnlyFailsMatchingDNSAndDoesNotPanic(t *testing.T) {
	g, _ := NewRuleSet(rulesets.Spec{Model: &models.Group{Enable: true, Rules: []*models.Rule{
		{Type: models.RuleTypeDomain, Rule: "vpn.example", Enable: true},
	}}}, nil)
	g.enabled.Store(true) // Failed disable: the routing link has already gone.
	ip := net.ParseIP("192.0.2.1").To4()
	if matched, err := g.addDNSAddress([]string{"unrelated.example"}, ip, 60); matched || err != nil {
		t.Fatalf("unrelated DNS affected: %t %v", matched, err)
	}
	if matched, err := g.addDNSAddress([]string{"vpn.example"}, ip, 60); !matched || err == nil {
		t.Fatalf("unroutable matching address must fail closed: %t %v", matched, err)
	}
	if err := g.LinkUpHook(netlink.LinkUpdate{}); err == nil {
		t.Fatal("incomplete link state should be reported")
	}
	if err := g.AddrChangeHook(netlink.AddrUpdate{}); err == nil {
		t.Fatal("incomplete address state should be reported")
	}
}
