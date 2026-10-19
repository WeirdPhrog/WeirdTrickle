//go:build integration && linux

package magitrickle

import (
	"errors"
	"net/netip"

	"magitrickle/utils/netfilterTools"
)

func (g *RuleSet) AddSubnet(prefix netip.Prefix, ttl netfilterTools.IPSetTimeout) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.addSubnet(prefix, ttl)
}

func (g *RuleSet) DelSubnet(prefix netip.Prefix) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.ipset.Del(prefix)
}

func (g *RuleSet) ListSubnets() (map[netip.Prefix]netfilterTools.IPSetTimeout, error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil, nil
	}
	return g.ipset.List()
}

// countFamilies counts IPv4 and IPv6 entries in a listing.
func countFamilies(subnets map[netip.Prefix]netfilterTools.IPSetTimeout) (v4, v6 int) {
	for prefix := range subnets {
		if prefix.Addr().Is4() {
			v4++
		} else {
			v6++
		}
	}
	return v4, v6
}

func (g *RuleSet) Sync() error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.sync()
}

func disableRuleSets(ruleSets []*RuleSet) error {
	var errs []error
	for _, ruleSet := range ruleSets {
		if err := ruleSet.Disable(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
