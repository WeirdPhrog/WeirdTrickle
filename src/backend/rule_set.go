package magitrickle

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"magitrickle/models"
	"magitrickle/rulesets"
	"magitrickle/utils/intID"
	"magitrickle/utils/netfilterTools"

	"github.com/vishvananda/netlink"
)

type RuleSet struct {
	spec  rulesets.Spec
	model atomic.Pointer[ruleSetModel]

	enabled atomic.Bool
	locker  sync.Mutex

	app          *App
	ipset        *netfilterTools.IPSet
	ipsetToLink  *netfilterTools.IPSetToLink
	permanent    map[netip.Prefix]bool
	restoreState atomic.Pointer[routingRestoreState]
}

type ruleSetModel struct {
	group       *models.Group
	domainRules []*models.Rule
}

func (g *RuleSet) Enabled() bool {
	return g.enabled.Load()
}

func newRuleSet(spec rulesets.Spec, app *App) *RuleSet {
	g := &RuleSet{
		spec: spec,
		app:  app,
	}
	g.storeModel(spec.Model)
	return g
}

func (g *RuleSet) Model() *models.Group {
	// Published models and their rules are immutable. Updates replace the model.
	return g.model.Load().group
}

func (g *RuleSet) storeModel(group *models.Group) {
	if group == nil {
		group = modelFromSpec(g.spec)
	}
	model := &ruleSetModel{group: group}
	for _, rule := range group.Rules {
		if !rule.Enable {
			continue
		}
		switch rule.Type {
		case models.RuleTypeDomain, models.RuleTypeNamespace, models.RuleTypeWildcard:
			model.domainRules = append(model.domainRules, rule)
		}
	}
	// Publish the model and its DNS subset together, including on rollback.
	g.model.Store(model)
}

func (g *RuleSet) domainRules() []*models.Rule {
	model := g.model.Load()
	if !model.group.Enable {
		return nil
	}
	return model.domainRules
}

func (g *RuleSet) IDValue() intID.ID {
	if model := g.Model(); model != nil {
		return model.ID
	}
	return g.spec.ID
}

func (g *RuleSet) RuntimeKey() string {
	return g.spec.RuntimeKey
}

func (g *RuleSet) DisplayName() string {
	if model := g.Model(); model != nil {
		return model.Name
	}
	return g.spec.Name
}

func (g *RuleSet) RouteInterface() string {
	if model := g.Model(); model != nil {
		return model.Interface
	}
	return g.spec.Interface
}

func (g *RuleSet) RuleModels() []*models.Rule {
	if model := g.Model(); model != nil {
		return model.Rules
	}
	return g.spec.Rules
}

func (g *RuleSet) ConfiguredEnabled() bool {
	if model := g.Model(); model != nil {
		return model.Enable
	}
	return g.spec.Enable
}

func (g *RuleSet) updateModel(model *models.Group) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	previous := g.Model()
	running := g.Enabled()
	if running && previous.Enable && (g.ipset == nil || g.ipsetToLink == nil) {
		if err := g.disable(); err != nil {
			return err
		}
	}
	// Direct routing has no mark or table to keep, so switching to or from it
	// recreates the routing link instead of changing the interface in place.
	modeChanged := isDirect(previous.Interface) != isDirect(model.Interface)
	if g.Enabled() && previous.Enable && model.Enable && !modeChanged {
		// DNS additions use this same lock. Prepare replacement sets without
		// unlinking the existing packet classification or policy routes.
		g.storeModel(model)
		changed := g.restoreState.Load() != nil || !sameRules(previous.Rules, model.Rules)
		if changed {
			if err := g.sync(); err != nil {
				g.storeModel(previous)
				return err
			}
		}
		if err := g.ipsetToLink.SetInterface(model.Interface); err != nil {
			g.storeModel(previous)
			if changed {
				return errors.Join(err, g.sync())
			}
			return err
		}
		g.restoreState.Store(nil)
		return nil
	}
	if running && (previous.Enable != model.Enable || modeChanged) {
		if err := g.disable(); err != nil {
			return err
		}
	}
	g.storeModel(model)
	apply := func() error {
		if !running {
			return nil
		}
		if err := g.enable(); err != nil {
			return err
		}
		return nil
	}
	if err := apply(); err != nil {
		cleanupErr := g.disable()
		g.storeModel(previous)
		return errors.Join(err, cleanupErr, apply())
	}
	return nil
}

func isDirect(iface string) bool {
	return iface == netfilterTools.Direct
}

func modelFromSpec(spec rulesets.Spec) *models.Group {
	if spec.Model != nil {
		return spec.Model
	}
	return &models.Group{ID: spec.ID, Name: spec.Name, Interface: spec.Interface, Enable: spec.Enable, Rules: spec.Rules}
}

// Compare rule values; equivalent request snapshots may have different pointers.
func sameRules(a, b []*models.Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x := range a {
		y := b[i]
		if x.ID != y.ID || x.Type != y.Type || x.Rule != y.Rule || x.Enable != y.Enable {
			return false
		}
	}
	return true
}

func (g *RuleSet) addSubnet(prefix netip.Prefix, ttl netfilterTools.IPSetTimeout) error {
	if g.permanent[prefix] {
		ttl = nil
	}
	return g.ipset.Add(prefix, ttl)
}

func (g *RuleSet) enable() error {
	if !g.enabled.CompareAndSwap(false, true) {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}

	ipset := g.app.nfHelper.IPSet(g.RuntimeKey())
	ipsetToLink := g.app.nfHelper.IPSetToLink(g.RuntimeKey(), g.RouteInterface(), ipset, g.spec.Subscription)
	if err := ipsetToLink.ClearIfDisabled(); err != nil {
		return fmt.Errorf("failed to clear iptables: %w", err)
	}

	g.ipset = ipset
	if err := ipset.Enable(); err != nil {
		return fmt.Errorf("failed to initialize ipset: %w", err)
	}

	// Fill the set before linking it into packet classification.
	if err := g.sync(); err != nil {
		return err
	}
	g.ipsetToLink = ipsetToLink
	if err := ipsetToLink.Enable(); err != nil {
		return fmt.Errorf("failed to link ipset to interface: %w", err)
	}

	return nil
}

func (g *RuleSet) Enable() error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if err := g.enable(); err != nil {
		_ = g.disable()
		return err
	}
	return nil
}

func (g *RuleSet) disable() error {
	if !g.Enabled() {
		return nil
	}
	if g.ipsetToLink != nil {
		if err := g.ipsetToLink.Disable(); err != nil {
			return fmt.Errorf("failed to unlink ipset from interface: %w", err)
		}
		g.ipsetToLink = nil
	}
	if g.ipset != nil {
		if err := g.ipset.Disable(); err != nil {
			return fmt.Errorf("failed to destroy ipset: %w", err)
		}
		g.ipset = nil
	}
	g.enabled.Store(false)
	g.restoreState.Store(nil)
	return nil
}

func (g *RuleSet) Disable() error {
	g.locker.Lock()
	defer g.locker.Unlock()
	return g.disable()
}

func (g *RuleSet) sync() error {
	return g.syncRules(g.RuleModels(), false)
}

func (g *RuleSet) syncRules(rules []*models.Rule, retainOld bool) error {
	now := time.Now()
	next := make(map[netip.Prefix]netfilterTools.IPSetTimeout)
	knownDomains := g.app.recordsCache.ListKnownDomains()
	for _, domain := range rules {
		if !domain.IsEnabled() {
			continue
		}
		switch domain.Type {
		case models.RuleTypeSubnet, models.RuleTypeSubnet6:
			for _, prefix := range subnetPrefixes(domain.Rule, domain.Type == models.RuleTypeSubnet6) {
				next[prefix] = nil
			}

		default:
			for _, domainName := range knownDomains {
				if !domain.IsMatch(domainName) {
					continue
				}
				for _, address := range g.app.recordsCache.GetAddresses(domainName) {
					ttl := remainingTTL(address.Deadline, now)
					if ttl == 0 {
						continue
					}
					addr, ok := netip.AddrFromSlice(address.Address)
					if !ok {
						continue
					}
					host := netip.PrefixFrom(addr, addr.BitLen())
					if oldTTL, exists := next[host]; !exists || (oldTTL != nil && ttl > *oldTTL) {
						next[host] = &ttl
					}
				}
			}
		}
	}

	if retainOld {
		old, err := g.ipset.List()
		if err != nil {
			return err
		}
		retainSubnets(next, old)
	}
	if err := g.ipset.Replace(next); err != nil {
		return fmt.Errorf("failed to replace routing set: %w", err)
	}
	g.permanent = make(map[netip.Prefix]bool)
	for prefix, ttl := range next {
		if ttl == nil && prefix.IsSingleIP() {
			g.permanent[prefix] = true
		}
	}
	return nil
}

// subnetPrefixes parses a subnet/subnet6 rule with net.ParseCIDR semantics; a
// plain address is a host route. Rules of the other family are ignored.
func subnetPrefixes(rule string, ipv6 bool) []netip.Prefix {
	bits := 32
	if ipv6 {
		bits = 128
	}
	_, ipNet, err := net.ParseCIDR(rule)
	if err != nil {
		ip := net.ParseIP(rule)
		if ip == nil {
			return nil
		}
		ipNet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
	}
	ones, maskBits := ipNet.Mask.Size()
	if maskBits != bits {
		return nil
	}
	masked := ipNet.IP.Mask(ipNet.Mask)
	if ipv6 {
		masked = masked.To16()
	} else {
		masked = masked.To4()
	}
	addr, ok := netip.AddrFromSlice(masked)
	if !ok {
		return nil
	}
	if ones == 0 {
		// TODO: Fix (remove dirty hack) after resolving https://github.com/vishvananda/netlink/issues/1091
		upper := make([]byte, len(masked))
		upper[0] = 0x80
		upperAddr, _ := netip.AddrFromSlice(upper)
		return []netip.Prefix{netip.PrefixFrom(addr, 1), netip.PrefixFrom(upperAddr, 1)}
	}
	return []netip.Prefix{netip.PrefixFrom(addr, ones)}
}

func retainSubnets(next, old map[netip.Prefix]netfilterTools.IPSetTimeout) {
	for subnet, ttl := range old {
		current, exists := next[subnet]
		if !exists || ttl == nil || (current != nil && *ttl > *current) {
			next[subnet] = ttl
		}
	}
}

// Prepare additions for every edited set before any set removes addresses.
// This covers a domain/IP being moved between two existing groups in one edit.
func (g *RuleSet) prepareUpdate(model *models.Group, retainOld bool) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() || !model.Enable {
		return nil
	}
	if g.ipset == nil || g.ipsetToLink == nil {
		return fmt.Errorf("routing is not ready")
	}
	if g.RouteInterface() != model.Interface {
		if err := g.ipsetToLink.SetInterface(netfilterTools.Blackhole); err != nil {
			return err
		}
	}
	if retainOld && !sameRules(g.RuleModels(), model.Rules) {
		return g.syncRules(model.Rules, true)
	}
	return nil
}

func (g *RuleSet) restoreModel(model *models.Group) error {
	if !g.Enabled() || !g.ConfiguredEnabled() || !model.Enable || g.ipset == nil || g.ipsetToLink == nil ||
		isDirect(g.RouteInterface()) != isDirect(model.Interface) {
		return g.updateModel(model)
	}
	g.locker.Lock()
	defer g.locker.Unlock()
	return g.restoreRuntimeLocked(model)
}

func (g *RuleSet) restoreRuntimeLocked(model *models.Group) (err error) {
	// Restore contents before releasing a suspended interface, and remove
	// additions retained during preparation even if the model never changed.
	defer func() {
		if err != nil {
			g.recordRestoreFailure(err, time.Now())
		} else {
			g.restoreState.Store(nil)
		}
	}()
	g.storeModel(model)
	if err := g.ipsetToLink.SetInterface(netfilterTools.Blackhole); err != nil {
		return err
	}
	if err := g.sync(); err != nil {
		return err
	}
	if err := g.ipsetToLink.SetInterface(model.Interface); err != nil {
		return err
	}
	return nil
}

func (g *RuleSet) retryRestore(now time.Time) error {
	state := g.restoreState.Load()
	if state == nil || now.Before(state.next) {
		return nil
	}
	g.locker.Lock()
	defer g.locker.Unlock()
	state = g.restoreState.Load()
	if state == nil || now.Before(state.next) {
		return nil
	}
	if !g.Enabled() || !g.ConfiguredEnabled() {
		g.restoreState.Store(nil)
		return nil
	}
	return g.restoreRuntimeLocked(g.Model())
}

func (g *RuleSet) LinkUpHook(event netlink.LinkUpdate) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	if g.ipsetToLink == nil {
		return fmt.Errorf("routing is not ready")
	}
	return g.ipsetToLink.LinkUpHook(event)
}

func (g *RuleSet) AddrChangeHook(event netlink.AddrUpdate) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() || !g.ConfiguredEnabled() {
		return nil
	}
	if g.ipsetToLink == nil {
		return fmt.Errorf("routing is not ready")
	}
	return g.ipsetToLink.AddrChangeHook(event)
}
