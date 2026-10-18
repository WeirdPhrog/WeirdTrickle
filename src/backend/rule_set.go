package magitrickle

import (
	"errors"
	"fmt"
	"net"
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

	app           *App
	ipset         *netfilterTools.IPSet
	ipsetToLink   *netfilterTools.IPSetToLink
	permanentIPv4 map[netfilterTools.IPv4Subnet]bool
	permanentIPv6 map[netfilterTools.IPv6Subnet]bool
}

type ruleSetModel struct {
	group       *models.Group
	domainRules []*models.Rule
}

func (g *RuleSet) Enabled() bool {
	return g.enabled.Load()
}

func newRuleSet(spec rulesets.Spec, app *App) (*RuleSet, error) {
	g := &RuleSet{
		spec: spec,
		app:  app,
	}
	g.storeModel(spec.Model)
	return g, nil
}

func NewRuleSet(spec rulesets.Spec, app *App) (*RuleSet, error) {
	return newRuleSet(spec, app)
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
		case models.RuleTypeDomain, models.RuleTypeNamespace, models.RuleTypeWildcard, models.RuleTypeRegEx:
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
	if g.Enabled() && previous.Enable && model.Enable {
		// DNS additions use this same lock. Prepare replacement sets without
		// unlinking the existing packet classification or policy routes.
		g.storeModel(model)
		changed := !sameRules(previous.Rules, model.Rules)
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
		return nil
	}
	if running && previous.Enable != model.Enable {
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

func modelFromSpec(spec rulesets.Spec) *models.Group {
	if spec.Model != nil {
		return spec.Model
	}
	return &models.Group{ID: spec.ID, Name: spec.Name, Interface: spec.Interface, Enable: spec.Enable, Rules: spec.Rules}
}

// Compare configuration fields only; compiled regexes contain mutable locks.
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

func (g *RuleSet) addIPv4Subnet(subnet netfilterTools.IPv4Subnet, ttl netfilterTools.IPSetTimeout) error {
	if subnet.CIDR == 0 {
		subnet.CIDR = 32
	}
	if g.permanentIPv4[subnet] {
		ttl = nil
	}
	return g.ipset.AddIPv4Subnet(subnet, ttl)
}

func (g *RuleSet) AddIPv4Subnet(subnet netfilterTools.IPv4Subnet, ttl netfilterTools.IPSetTimeout) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}

	return g.addIPv4Subnet(subnet, ttl)
}

func (g *RuleSet) addIPv6Subnet(subnet netfilterTools.IPv6Subnet, ttl netfilterTools.IPSetTimeout) error {
	if subnet.CIDR == 0 {
		subnet.CIDR = 128
	}
	if g.permanentIPv6[subnet] {
		ttl = nil
	}
	return g.ipset.AddIPv6Subnet(subnet, ttl)
}

func (g *RuleSet) AddIPv6Subnet(subnet netfilterTools.IPv6Subnet, ttl netfilterTools.IPSetTimeout) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}

	return g.addIPv6Subnet(subnet, ttl)
}

func (g *RuleSet) delIPv4Subnet(subnet netfilterTools.IPv4Subnet) error {
	return g.ipset.DelIPv4Subnet(subnet)
}

func (g *RuleSet) DelIPv4Subnet(subnet netfilterTools.IPv4Subnet) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}

	return g.delIPv4Subnet(subnet)
}

func (g *RuleSet) delIPv6Subnet(subnet netfilterTools.IPv6Subnet) error {
	return g.ipset.DelIPv6Subnet(subnet)
}

func (g *RuleSet) DelIPv6Subnet(subnet netfilterTools.IPv6Subnet) error {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}

	return g.delIPv6Subnet(subnet)
}

func (g *RuleSet) listIPv4Subnets() (map[netfilterTools.IPv4Subnet]netfilterTools.IPSetTimeout, error) {
	return g.ipset.ListIPv4Subnets()
}

func (g *RuleSet) ListIPv4Subnets() (map[netfilterTools.IPv4Subnet]netfilterTools.IPSetTimeout, error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() {
		return nil, nil
	}

	if !g.ConfiguredEnabled() {
		return nil, nil
	}

	return g.listIPv4Subnets()
}

func (g *RuleSet) listIPv6Subnets() (map[netfilterTools.IPv6Subnet]netfilterTools.IPSetTimeout, error) {
	return g.ipset.ListIPv6Subnets()
}

func (g *RuleSet) ListIPv6Subnets() (map[netfilterTools.IPv6Subnet]netfilterTools.IPSetTimeout, error) {
	g.locker.Lock()
	defer g.locker.Unlock()
	if !g.Enabled() {
		return nil, nil
	}

	if !g.ConfiguredEnabled() {
		return nil, nil
	}

	return g.listIPv6Subnets()
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
	newIPv4SubnetList := make(map[netfilterTools.IPv4Subnet]netfilterTools.IPSetTimeout)
	newIPv6SubnetList := make(map[netfilterTools.IPv6Subnet]netfilterTools.IPSetTimeout)
	knownDomains := g.app.recordsCache.ListKnownDomains()
	for _, domain := range rules {
		if !domain.IsEnabled() {
			continue
		}
		switch domain.Type {
		case models.RuleTypeSubnet:
			ip, ipNet, err := net.ParseCIDR(domain.Rule)
			if err != nil {
				ip = net.ParseIP(domain.Rule)
				if ip == nil {
					continue
				}

				ip = ip.To4()
				if ip == nil {
					continue
				}

				ipNet = &net.IPNet{
					IP:   ip,
					Mask: net.CIDRMask(32, 32),
				}
			}

			ones, bits := ipNet.Mask.Size()
			if bits != 32 || ones > 32 {
				continue
			}

			var addr [4]byte
			copy(addr[:], ipNet.IP.Mask(ipNet.Mask).To4())
			cidr := uint8(ones)

			if addr == ([4]byte{}) && cidr == 0 {
				// TODO: Fix (remove dirty hack) after resolving https://github.com/vishvananda/netlink/issues/1091
				newIPv4SubnetList[netfilterTools.IPv4Subnet{
					Address: [4]byte{0x00},
					CIDR:    1,
				}] = nil
				newIPv4SubnetList[netfilterTools.IPv4Subnet{
					Address: [4]byte{0x80},
					CIDR:    1,
				}] = nil
			} else {
				newIPv4SubnetList[netfilterTools.IPv4Subnet{
					Address: addr,
					CIDR:    cidr,
				}] = nil
			}

		case models.RuleTypeSubnet6:
			ip, ipNet, err := net.ParseCIDR(domain.Rule)
			if err != nil {
				ip = net.ParseIP(domain.Rule)
				if ip == nil {
					continue
				}

				ip = ip.To16()
				if ip == nil {
					continue
				}

				ipNet = &net.IPNet{
					IP:   ip,
					Mask: net.CIDRMask(128, 128),
				}
			}

			ones, bits := ipNet.Mask.Size()
			if bits != 128 || ones > 128 {
				continue
			}

			var addr [16]byte
			copy(addr[:], ipNet.IP.Mask(ipNet.Mask).To16())
			cidr := uint8(ones)

			if addr == ([16]byte{}) && cidr == 0 {
				newIPv6SubnetList[netfilterTools.IPv6Subnet{
					Address: [16]byte{0x00},
					CIDR:    1,
				}] = nil
				newIPv6SubnetList[netfilterTools.IPv6Subnet{
					Address: [16]byte{0x80},
					CIDR:    1,
				}] = nil
			} else {
				newIPv6SubnetList[netfilterTools.IPv6Subnet{
					Address: addr,
					CIDR:    cidr,
				}] = nil
			}

		default:
			for _, domainName := range knownDomains {
				if !domain.IsMatch(domainName) {
					continue
				}
				domainAddresses := g.app.recordsCache.GetAddresses(domainName)
				for _, address := range domainAddresses {
					ttl := remainingTTL(address.Deadline, now)
					if ttl == 0 {
						continue
					}
					if len(address.Address) == net.IPv4len {
						subnet := netfilterTools.IPv4Subnet{Address: [4]byte(address.Address), CIDR: 32}
						if oldTTL, exists := newIPv4SubnetList[subnet]; !exists || (oldTTL != nil && ttl > *oldTTL) {
							newIPv4SubnetList[subnet] = &ttl
						}
					} else if len(address.Address) == net.IPv6len {
						subnet := netfilterTools.IPv6Subnet{Address: [16]byte(address.Address), CIDR: 128}
						if oldTTL, exists := newIPv6SubnetList[subnet]; !exists || (oldTTL != nil && ttl > *oldTTL) {
							newIPv6SubnetList[subnet] = &ttl
						}
					}
				}
			}
		}
	}

	if retainOld {
		old4, err := g.ipset.ListIPv4Subnets()
		if err != nil {
			return err
		}
		old6, err := g.ipset.ListIPv6Subnets()
		if err != nil {
			return err
		}
		retainSubnets(newIPv4SubnetList, old4)
		retainSubnets(newIPv6SubnetList, old6)
	}
	if err := g.ipset.Replace(newIPv4SubnetList, newIPv6SubnetList); err != nil {
		return fmt.Errorf("failed to replace routing set: %w", err)
	}
	g.permanentIPv4 = make(map[netfilterTools.IPv4Subnet]bool)
	g.permanentIPv6 = make(map[netfilterTools.IPv6Subnet]bool)
	for subnet, ttl := range newIPv4SubnetList {
		if ttl == nil && subnet.CIDR == 32 {
			g.permanentIPv4[subnet] = true
		}
	}
	for subnet, ttl := range newIPv6SubnetList {
		if ttl == nil && subnet.CIDR == 128 {
			g.permanentIPv6[subnet] = true
		}
	}
	return nil
}

func retainSubnets[K comparable](next, old map[K]netfilterTools.IPSetTimeout) {
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
	if !g.Enabled() || !g.ConfiguredEnabled() || !model.Enable || g.ipset == nil || g.ipsetToLink == nil {
		return g.updateModel(model)
	}
	g.locker.Lock()
	defer g.locker.Unlock()
	// Restore contents before releasing a suspended interface, and remove
	// additions retained during preparation even if the model never changed.
	if err := g.ipsetToLink.SetInterface(netfilterTools.Blackhole); err != nil {
		return err
	}
	g.storeModel(model)
	if err := g.sync(); err != nil {
		return err
	}
	return g.ipsetToLink.SetInterface(model.Interface)
}

func (g *RuleSet) Sync() error {
	g.locker.Lock()
	defer g.locker.Unlock()

	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}

	return g.sync()
}

func (g *RuleSet) LinkUpHook(event netlink.LinkUpdate) error {
	g.locker.Lock()
	defer g.locker.Unlock()

	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
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

	if !g.Enabled() {
		return nil
	}

	if !g.ConfiguredEnabled() {
		return nil
	}
	if g.ipsetToLink == nil {
		return fmt.Errorf("routing is not ready")
	}

	return g.ipsetToLink.AddrChangeHook(event)
}
