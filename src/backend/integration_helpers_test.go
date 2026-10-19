//go:build integration && linux

package magitrickle

import (
	"errors"

	"magitrickle/utils/netfilterTools"
)

func (g *RuleSet) AddIPv4Subnet(subnet netfilterTools.IPv4Subnet, ttl netfilterTools.IPSetTimeout) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.addIPv4Subnet(subnet, ttl)
}

func (g *RuleSet) AddIPv6Subnet(subnet netfilterTools.IPv6Subnet, ttl netfilterTools.IPSetTimeout) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.addIPv6Subnet(subnet, ttl)
}

func (g *RuleSet) DelIPv4Subnet(subnet netfilterTools.IPv4Subnet) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.ipset.DelIPv4Subnet(subnet)
}

func (g *RuleSet) DelIPv6Subnet(subnet netfilterTools.IPv6Subnet) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	return g.ipset.DelIPv6Subnet(subnet)
}

func (g *RuleSet) ListIPv4Subnets() (map[netfilterTools.IPv4Subnet]netfilterTools.IPSetTimeout, error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil, nil
	}
	return g.ipset.ListIPv4Subnets()
}

func (g *RuleSet) ListIPv6Subnets() (map[netfilterTools.IPv6Subnet]netfilterTools.IPSetTimeout, error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil, nil
	}
	return g.ipset.ListIPv6Subnets()
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
