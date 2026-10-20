package netfilterTools

import (
	"fmt"
	"slices"

	"magitrickle/utils/iptables"
)

// SetRoutingOrder preserves last-match priority when lists are reordered or a
// previously disabled set is enabled again. The table commit is atomic.
// Direct rule sets are not part of a tier; their exemptions live in ROUTE.
func (nh *Helper) SetRoutingOrder(keys []string, subscription bool) error {
	chain := nh.ChainPrefix + "GROUPS"
	if subscription {
		chain = nh.ChainPrefix + "SUBS"
	}
	for _, ipt := range []*iptables.IPTables{nh.IPTables4, nh.IPTables6} {
		if ipt == nil {
			continue
		}
		if err := ipt.RegisterChainOverride("mangle", chain); err != nil {
			return err
		}
		suffix := setSuffix(ipt)
		for _, key := range keys {
			if nh.isDirect(nh.IpsetPrefix + key) {
				continue
			}
			if err := ipt.Append("mangle", chain, "-m", "set", "--match-set", nh.IpsetPrefix+key+suffix, "dst", "-j", nh.ChainPrefix+key); err != nil {
				return err
			}
		}
		if err := ipt.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// InitRouting installs fixed routing tiers. MARK is not terminal: groups run
// last so an explicit group overrides subscriptions, regardless of when either
// rule set is enabled or refreshed. Ordering within each tier is unchanged.
// Call once after CleanIPTables, before enabling any rule sets.
func (nh *Helper) InitRouting() error {
	nh.directMu.Lock()
	defer nh.directMu.Unlock()
	direct := nh.directSetNamesLocked()
	for _, ipt := range []*iptables.IPTables{nh.IPTables4, nh.IPTables6} {
		if ipt == nil {
			continue
		}
		for _, suffix := range []string{"ROUTE", "SUBS", "GROUPS"} {
			if err := ipt.RegisterChainOverride("mangle", nh.ChainPrefix+suffix); err != nil {
				return err
			}
		}
		if err := nh.appendRouteRules(ipt, direct); err != nil {
			return err
		}
		if err := ipt.Append("mangle", "PREROUTING", "-j", nh.ChainPrefix+"ROUTE"); err != nil {
			return err
		}
		if err := ipt.Commit(); err != nil {
			return fmt.Errorf("failed to initialize routing chains: %w", err)
		}
	}
	return nil
}

// Direct sets are exempted before both tiers, so their traffic keeps the
// router's own marks and routing even if a subscription or group also matches.
func (nh *Helper) appendRouteRules(ipt *iptables.IPTables, direct []string) error {
	routing := nh.ChainPrefix + "ROUTE"
	// Reply packets keep their existing marks. Check once for all rule sets.
	if err := ipt.Append("mangle", routing, "-m", "conntrack", "--ctdir", "REPLY", "-j", "RETURN"); err != nil {
		return err
	}
	for _, name := range direct {
		if err := ipt.Append("mangle", routing, "-m", "set", "--match-set", name+setSuffix(ipt), "dst", "-j", "RETURN"); err != nil {
			return err
		}
	}
	for _, suffix := range []string{"SUBS", "GROUPS"} {
		if err := ipt.Append("mangle", routing, "-j", nh.ChainPrefix+suffix); err != nil {
			return err
		}
	}
	return nil
}

// setDirect adds or removes a direct exemption and rewrites ROUTE atomically
// per family. A failed rewrite restores the previous exemption list. ROUTE is
// rewritten even when the list is unchanged: after a partial failure one
// family may still differ, and an unchanged commit is a no-op.
func (nh *Helper) setDirect(ipsetName string, enabled bool) error {
	nh.directMu.Lock()
	defer nh.directMu.Unlock()
	previous := nh.directSets[ipsetName]
	if nh.directSets == nil {
		nh.directSets = make(map[string]bool)
	}
	setFlag := func(on bool) {
		if on {
			nh.directSets[ipsetName] = true
		} else {
			delete(nh.directSets, ipsetName)
		}
	}
	apply := func() error {
		direct := nh.directSetNamesLocked()
		for _, ipt := range []*iptables.IPTables{nh.IPTables4, nh.IPTables6} {
			if ipt == nil {
				continue
			}
			if err := ipt.RegisterChainOverride("mangle", nh.ChainPrefix+"ROUTE"); err != nil {
				return err
			}
			if err := nh.appendRouteRules(ipt, direct); err != nil {
				return err
			}
			if err := ipt.Commit(); err != nil {
				return err
			}
		}
		return nil
	}
	setFlag(enabled)
	if err := apply(); err != nil {
		setFlag(previous)
		if restoreErr := apply(); restoreErr != nil {
			return fmt.Errorf("%w (restoring routing exemptions: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

func (nh *Helper) isDirect(ipsetName string) bool {
	nh.directMu.Lock()
	defer nh.directMu.Unlock()
	return nh.directSets[ipsetName]
}

func (nh *Helper) directSetNamesLocked() []string {
	names := make([]string, 0, len(nh.directSets))
	for name := range nh.directSets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func setSuffix(ipt *iptables.IPTables) string {
	if ipt.Proto() == iptables.ProtocolIPv6 {
		return "_6"
	}
	return "_4"
}
