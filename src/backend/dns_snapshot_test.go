package magitrickle

import (
	"net"
	"testing"
	"time"

	"magitrickle/constant"
	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/subscriptions"
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
	next := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeNamespace, Rule: "new.example", Enable: true}}}
	sub := subscriptions.BuildRuntimeRuleSets([]*models.Subscription{{
		Enable: true, Interface: "vpn-test", Rules: []*models.SubscriptionRule{
			{Type: models.RuleTypeNamespace, Rule: "new.example", Enable: true},
		},
	}})[0]
	for name, spec := range map[string]rulesets.Spec{"group": {Model: next}, "subscription": sub} {
		t.Run(name, func(t *testing.T) { testPendingDNSSnapshotRollback(t, spec) })
	}
}

func TestConfigDNSPrefilterKeepsBothClassesDuringNestedReconciliation(t *testing.T) {
	a := &App{}
	old := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeNamespace, Rule: "old-config.example", Enable: true}}}
	if err := a.AddGroup(old); err != nil {
		t.Fatal(err)
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	group := rulesets.Spec{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeNamespace, Rule: "new-group.example", Enable: true}}}
	sub := subscriptions.BuildRuntimeRuleSets([]*models.Subscription{{Enable: true, Interface: "vpn", Rules: []*models.SubscriptionRule{{Type: models.RuleTypeNamespace, Rule: "new-sub.example", Enable: true}}}})[0]
	a.beginConfigDNSRulesLocked([]rulesets.Spec{group, sub})
	a.userRuleSets[0].storeModel(modelFromSpec(group)) // First stage committed.
	for _, pending := range [][]rulesets.Spec{{sub}, nil, {group}, nil} {
		a.publishDNSRulesLocked(pending)
		if !a.dnsRules.Load().matches([]string{"new-group.example"}) || !a.dnsRules.Load().matches([]string{"new-sub.example"}) || !a.dnsRules.Load().matches([]string{"old-config.example"}) {
			t.Fatal("nested update dropped domains from the full config transaction")
		}
	}
	a.userRuleSets[0].storeModel(old)
	a.configDNSRules = nil
	a.publishDNSRulesLocked(nil)
	if a.dnsRules.Load().matches([]string{"new-group.example", "new-sub.example"}) {
		t.Fatal("rolled-back rules remained in the prefilter")
	}
}

func testPendingDNSSnapshotRollback(t *testing.T, pending rulesets.Spec) {
	a := &App{}
	current := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeDomain, Rule: "old.example", Enable: true}}}
	if err := a.AddGroup(current); err != nil {
		t.Fatal(err)
	}
	a.stateMu.Lock()
	a.publishDNSRulesLocked([]rulesets.Spec{pending})
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

func TestPendingSubscriptionDNSWaitsAndFailsClosedIfRoutingIsNotReady(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "add"
		if refresh {
			name = "refresh"
		}
		t.Run(name, func(t *testing.T) {
			a := &App{config: constant.DefaultAppConfig, recordsCache: recordsCache.New()}
			a.publishConfigLocked()
			sub := &models.Subscription{Enable: true, Interface: "vpn-test", Rules: []*models.SubscriptionRule{
				{Type: models.RuleTypeDomain, Rule: "old.example", Enable: true},
			}}
			if refresh {
				current, err := NewRuleSet(subscriptions.BuildRuntimeRuleSets([]*models.Subscription{sub})[0], a)
				if err != nil {
					t.Fatal(err)
				}
				a.subscriptionRuleSets = []*RuleSet{current}
			}
			sub.Rules = []*models.SubscriptionRule{{Type: models.RuleTypeDomain, Rule: "new.example", Enable: true}}
			pending := subscriptions.BuildRuntimeRuleSets([]*models.Subscription{sub})
			if pending[0].Model != nil || !pending[0].Subscription || !pending[0].Enable {
				t.Fatal("test must exercise an enabled subscription spec without Model")
			}
			next, err := NewRuleSet(pending[0], a)
			if err != nil {
				t.Fatal(err)
			}
			// An incomplete runtime must produce SERVFAIL after the update, never
			// release a usable address just because the pending spec has no Model.
			next.enabled.Store(true)
			a.stateMu.Lock()
			a.publishDNSRulesLocked(pending)
			if !a.dnsRules.Load().matches([]string{"new.example"}) {
				t.Error("pending subscription domain is missing from the DNS filter")
			}
			if refresh && !a.dnsRules.Load().matches([]string{"old.example"}) {
				t.Error("previous subscription domain was dropped before commit")
			}
			done := make(chan *dns.Msg, 1)
			go func() {
				query := new(dns.Msg)
				query.SetQuestion("new.example.", dns.TypeA)
				reply := new(dns.Msg)
				reply.SetReply(query)
				reply.Answer = []dns.RR{&dns.A{
					Hdr: dns.RR_Header{Name: "new.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP("192.0.2.1").To4(),
				}}
				response, err := a.dnsResponseHook(nil, *query, *reply, "udp")
				if err != nil {
					t.Error(err)
				}
				done <- response
			}()
			var response *dns.Msg
			early := false
			select {
			case response = <-done:
				early = true
				t.Error("DNS answer escaped while subscription routing was being applied")
			case <-time.After(20 * time.Millisecond):
			}
			a.subscriptionRuleSets = []*RuleSet{next}
			a.publishDNSRulesLocked(nil)
			a.stateMu.Unlock()
			if !early {
				select {
				case response = <-done:
				case <-time.After(time.Second):
					t.Fatal("DNS did not resume after the subscription update")
				}
			}
			if response == nil || response.Rcode != dns.RcodeServerFailure || len(response.Answer) != 0 {
				t.Fatalf("incomplete subscription routing must withhold DNS addresses: %v", response)
			}
			if a.dnsRules.Load().matches([]string{"old.example"}) {
				t.Error("previous subscription domain remained after commit")
			}
		})
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
