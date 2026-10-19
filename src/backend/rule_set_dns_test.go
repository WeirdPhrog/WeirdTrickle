package magitrickle

import (
	"fmt"
	"sync"
	"testing"

	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/utils/intID"
)

func TestDNSRulesTrackGroupEditsAndExcludeSubnetRules(t *testing.T) {
	domain := &models.Rule{ID: intID.ID{1}, Type: models.RuleTypeDomain, Rule: "example.com", Enable: true}
	group := &models.Group{Enable: true, Rules: []*models.Rule{
		{Type: models.RuleTypeSubnet, Rule: "192.0.2.0/24", Enable: true},
		domain,
		{Type: models.RuleTypeSubnet6, Rule: "2001:db8::/32", Enable: true},
		{Type: models.RuleTypeNamespace, Rule: "disabled.example", Enable: false},
	}}
	g := newRuleSet(rulesets.Spec{Model: group}, nil)
	if got := g.domainRules(); len(got) != 1 || got[0] != domain {
		t.Fatalf("unexpected DNS rules: %v", got)
	}
	if len(g.RuleModels()) != 4 {
		t.Fatal("filtering DNS rules removed configured IP rules")
	}
	updated := *group
	updated.Rules = []*models.Rule{{Type: models.RuleTypeDomain, Rule: "new.example", Enable: true}}
	if err := g.updateModel(&updated); err != nil {
		t.Fatal(err)
	}
	if got := g.domainRules(); len(got) != 1 || !got[0].IsMatch("new.example") || got[0].IsMatch("example.com") {
		t.Fatal("DNS rules did not follow the edited model")
	}
	disabled := updated
	disabled.Enable = false
	if err := g.updateModel(&disabled); err != nil {
		t.Fatal(err)
	}
	if len(g.domainRules()) != 0 {
		t.Fatal("disabled group still participates in DNS matching")
	}
	if err := g.updateModel(group); err != nil {
		t.Fatal(err)
	}
	if got := g.domainRules(); len(got) != 1 || got[0] != domain {
		t.Fatal("restoring the original model did not restore its DNS rules")
	}
}

func TestDNSRulesConcurrentModelPublication(t *testing.T) {
	first := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeDomain, Rule: "first.example", Enable: true}}}
	second := &models.Group{Enable: true, Rules: []*models.Rule{{Type: models.RuleTypeSubnet, Rule: "192.0.2.0/24", Enable: true}}}
	g := newRuleSet(rulesets.Spec{Model: first}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				for _, rule := range g.domainRules() {
					if !rule.IsMatch("first.example") {
						t.Error("non-domain rule reached the DNS matcher")
					}
				}
				g.Model()
			}
		}()
	}
	for n := 0; n < 100; n++ {
		if err := g.updateModel(second); err != nil {
			t.Fatal(err)
		}
		if err := g.updateModel(first); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func BenchmarkDNSRuleScan(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		rules := make([]*models.Rule, count+1)
		for i := 0; i < count; i++ {
			rules[i] = &models.Rule{Type: models.RuleTypeSubnet, Rule: "192.0.2.0/24", Enable: true}
		}
		rules[count] = &models.Rule{Type: models.RuleTypeDomain, Rule: "example.com", Enable: true}
		g := newRuleSet(rulesets.Spec{Subscription: true, Enable: true, Rules: rules}, nil)
		for _, filtered := range []bool{false, true} {
			b.Run(fmt.Sprintf("subnets=%d/filtered=%t", count, filtered), func(b *testing.B) {
				b.ReportAllocs()
				for n := 0; n < b.N; n++ {
					rules := g.RuleModels()
					if filtered {
						rules = g.domainRules()
					}
					for _, rule := range rules {
						if rule.IsEnabled() && rule.IsMatch("example.com") {
							break
						}
					}
				}
			})
		}
	}
}
