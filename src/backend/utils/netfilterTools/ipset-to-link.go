package netfilterTools

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"magitrickle/utils/iptables"

	"github.com/rs/zerolog/log"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

const Blackhole = "blackhole"

// Direct is the interface value of a rule set whose traffic bypasses
// WeirdTrickle: it is exempted before both tiers and keeps the router's own
// marks and routes, with no mark, table, filter or NAT rules of its own.
const Direct = "direct"

type IPSetToLink struct {
	enabled atomic.Bool
	locker  sync.Mutex

	direct       bool // fixed at creation; changing mode recreates the link
	chainName    string
	routingChain string
	ifaceName    string
	startIdx     uint32
	ipset        *IPSet
	nh           *Helper
	mark         uint32
	table        int
	ip4Rule      *netlink.Rule
	ip6Rule      *netlink.Rule
	ip4Route     [2]*netlink.Route
	ip6Route     [2]*netlink.Route
}

func (r *IPSetToLink) ipsetName(ipt *iptables.IPTables) string {
	if ipt.Proto() == iptables.ProtocolIPv4 {
		return r.ipset.ipsetName + "_4"
	}
	return r.ipset.ipsetName + "_6"
}

func (r *IPSetToLink) insertIPTablesRules(ipt *iptables.IPTables) error {
	if ipt == nil {
		return nil
	}

	ipsetName := r.ipsetName(ipt)

	/*
		Filter Forward
	*/

	err := ipt.RegisterChainOverride("filter", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to create chain: %w", err)
	}

	if r.ifaceName != Blackhole {
		err = ipt.Append("filter", r.chainName, "-o", r.ifaceName, "-m", "set", "--match-set", ipsetName, "dst", "-j", "ACCEPT")
		if err != nil {
			return fmt.Errorf("failed to fix protect for IPv4: %w", err)
		}
	}

	err = ipt.Append("filter", "FORWARD", "-j", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to append rule to PREROUTING: %w", err)
	}

	/*
		Mangle Prerouting
	*/

	err = ipt.RegisterChainOverride("mangle", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to create chain: %w", err)
	}

	markStr := strconv.Itoa(int(r.mark))
	// The tier checks membership before entering this chain, so MARK and
	// CONNMARK share one lookup. Fall through to preserve last-match priority.
	for _, iptablesArgs := range [][]string{
		{"-j", "MARK", "--set-mark", markStr},
		{"-j", "CONNMARK", "--save-mark"}, // Required for Keenetic routing; keep saving on every match.
	} {
		err = ipt.Append("mangle", r.chainName, iptablesArgs...)
		if err != nil {
			return fmt.Errorf("failed to append rule: %w", err)
		}
	}

	err = ipt.Append("mangle", r.routingChain, "-m", "set", "--match-set", ipsetName, "dst", "-j", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to append rule to PREROUTING: %w", err)
	}

	/*
		NAT Postrouting
	*/

	err = ipt.RegisterChainOverride("nat", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to create chain: %w", err)
	}

	err = ipt.Append("nat", r.chainName, "-m", "set", "--match-set", ipsetName, "dst", "-j", "MASQUERADE")
	if err != nil {
		return fmt.Errorf("failed to create rule: %w", err)
	}

	err = ipt.Append("nat", "POSTROUTING", "-j", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to append rule to POSTROUTING: %w", err)
	}

	err = ipt.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit iptables rules: %w", err)
	}
	return nil
}

func (r *IPSetToLink) deleteIPTablesRules(ipt *iptables.IPTables) error {
	if ipt == nil {
		return nil
	}
	var errs []error

	/*
		Filter Forward
	*/

	err := ipt.RegisterChainDelete("filter", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to clear chain: %w", err))
	}

	err = ipt.Delete("filter", "FORWARD", "-j", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to unlinking chain: %w", err))
	}

	/*
		Mangle Prerouting
	*/

	err = ipt.RegisterChainDelete("mangle", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to delete chain: %w", err))
	}

	err = ipt.Delete("mangle", r.routingChain, "-m", "set", "--match-set", r.ipsetName(ipt), "dst", "-j", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to unlinking chain: %w", err))
	}

	/*
		NAT Postrouting
	*/

	err = ipt.RegisterChainDelete("nat", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to delete chain: %w", err))
	}

	err = ipt.Delete("nat", "POSTROUTING", "-j", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to unlinking chain: %w", err))
	}

	err = ipt.Commit()
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to commit iptables rules: %w", err))
	}
	return errors.Join(errs...)
}

func (r *IPSetToLink) insertIPRule() error {
	if r.nh.IPTables4 != nil {
		rule := netlink.NewRule()
		rule.Mark = r.mark
		rule.Table = r.table
		rule.Family = nl.FAMILY_V4
		_ = netlink.RuleDel(rule)
		err := netlink.RuleAdd(rule)
		if err != nil {
			return fmt.Errorf("error while mapping marked packages to table: %w", err)
		}
		r.ip4Rule = rule
	}

	if r.nh.IPTables6 != nil {
		rule := netlink.NewRule()
		rule.Mark = r.mark
		rule.Table = r.table
		rule.Family = nl.FAMILY_V6
		_ = netlink.RuleDel(rule)
		err := netlink.RuleAdd(rule)
		if err != nil {
			return fmt.Errorf("error while mapping marked packages to table: %w", err)
		}
		r.ip6Rule = rule
	}

	return nil
}

func (r *IPSetToLink) deleteIPRule() error {
	var errs []error

	if r.ip4Rule != nil {
		err := netlink.RuleDel(r.ip4Rule)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, fmt.Errorf("error while deleting rule: %w", err))
		} else {
			r.ip4Rule = nil
		}
	}

	if r.ip6Rule != nil {
		err := netlink.RuleDel(r.ip6Rule)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, fmt.Errorf("error while deleting rule: %w", err))
		} else {
			r.ip6Rule = nil
		}
	}

	return errors.Join(errs...)
}

func (r *IPSetToLink) insertIPRoute() error {
	if r.nh.IPTables4 != nil {
		route := &netlink.Route{
			Priority: 20,
			Dst:      &net.IPNet{IP: []byte{0, 0, 0, 0}, Mask: []byte{0, 0, 0, 0}},
			Table:    r.table,
			Type:     unix.RTN_BLACKHOLE,
			Family:   nl.FAMILY_V4,
		}
		if err := netlink.RouteAdd(route); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("error while adding ipv4 blackhole route: %w", err)
		}
		r.ip4Route[0] = route
	}

	if r.nh.IPTables6 != nil {
		route := &netlink.Route{
			Priority: 20,
			Dst:      &net.IPNet{IP: []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, Mask: []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
			Table:    r.table,
			Type:     unix.RTN_BLACKHOLE,
			Family:   nl.FAMILY_V6,
		}
		if err := netlink.RouteAdd(route); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("error while adding ipv6 blackhole route: %w", err)
		}
		r.ip6Route[0] = route
	}

	if r.ifaceName == Blackhole {
		return nil
	}

	iface, err := netlink.LinkByName(r.ifaceName)
	if err != nil {
		if errors.As(err, &netlink.LinkNotFoundError{}) {
			log.Warn().Str("iface", r.ifaceName).Msg("interface not found, it can be catched later")
			return nil
		}
		return fmt.Errorf("error while getting interface: %w", err)
	}
	if iface.Attrs().Flags&net.FlagUp == 0 {
		log.Warn().Str("iface", r.ifaceName).Msg("interface is down")
		return nil
	}

	var errs []error
	if r.nh.IPTables4 != nil {
		r.ip4Route[1], err = r.updateIfaceRoute(iface, nl.FAMILY_V4, r.ip4Route[1])
		errs = append(errs, err)
	}
	if r.nh.IPTables6 != nil {
		r.ip6Route[1], err = r.updateIfaceRoute(iface, nl.FAMILY_V6, r.ip6Route[1])
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (r *IPSetToLink) updateIfaceRoute(iface netlink.Link, family int, current *netlink.Route) (*netlink.Route, error) {
	ipLen := net.IPv4len
	if family == nl.FAMILY_V6 {
		ipLen = net.IPv6len
	}

	route := &netlink.Route{
		Priority:  10,
		LinkIndex: iface.Attrs().Index,
		Table:     r.table,
		Family:    family,
		Dst:       &net.IPNet{IP: make(net.IP, ipLen), Mask: make(net.IPMask, ipLen)},
	}

	if iface.Attrs().Flags&net.FlagPointToPoint == 0 {
		gateway, err := getGwFromIface(iface, family)
		if err != nil {
			log.Warn().Str("iface", r.ifaceName).Err(err).Int("family", family).Msg("gateway not found")
		} else {
			route.Gw = gateway
		}
	}

	// An unchanged gateway does not imply the route still exists: link/address
	// changes can remove it, and a recreated interface gets a new index.
	if err := netlink.RouteReplace(route); err != nil {
		if errors.Is(err, unix.ENODEV) {
			log.Warn().Str("iface", r.ifaceName).Int("family", family).Msg("interface not ready for this IP family, skipping route")
			return current, nil
		}
		return current, fmt.Errorf("error replacing iface route: %w", err)
	}
	return route, nil
}

func getGwFromIface(iface netlink.Link, family int) (net.IP, error) {
	routes, err := netlink.RouteListFiltered(family, &netlink.Route{
		LinkIndex: iface.Attrs().Index,
	}, netlink.RT_FILTER_OIF)
	if err != nil {
		return nil, err
	}
	if gateway := defaultGateway(routes); gateway != nil {
		return gateway, nil
	}
	return nil, fmt.Errorf("no gateway found for interface %s", iface.Attrs().Name)
}

func defaultGateway(routes []netlink.Route) net.IP {
	var best *netlink.Route
	for i := range routes {
		route := &routes[i]
		if route.Gw == nil {
			continue
		}
		if route.Dst != nil {
			if ones, _ := route.Dst.Mask.Size(); ones != 0 {
				continue
			}
		}
		if best == nil || route.Priority < best.Priority {
			best = route
		}
	}
	if best != nil {
		return best.Gw
	}
	return nil
}

func (r *IPSetToLink) deleteIPRoute() error {
	errs := make([]error, 0)

	for i := 1; i >= 0; i-- {
		if r.ip4Route[i] == nil {
			continue
		}
		err := netlink.RouteDel(r.ip4Route[i])
		if err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("error while deleting route: %w", err))
			continue
		}
		r.ip4Route[i] = nil
	}

	for i := 1; i >= 0; i-- {
		if r.ip6Route[i] == nil {
			continue
		}
		err := netlink.RouteDel(r.ip6Route[i])
		if err != nil && !errors.Is(err, unix.ESRCH) {
			errs = append(errs, fmt.Errorf("error while deleting route: %w", err))
			continue
		}
		r.ip6Route[i] = nil
	}

	return errors.Join(errs...)
}

func (r *IPSetToLink) getUnusedMarkAndTable() (idx uint32, err error) {
	// Find unused mark and table
	markMap := make(map[uint32]struct{})
	tableMap := map[int]struct{}{0: {}, 253: {}, 254: {}, 255: {}}

	rules, err := netlink.RuleList(nl.FAMILY_ALL)
	if err != nil {
		return 0, fmt.Errorf("error while getting rules: %w", err)
	}
	for _, rule := range rules {
		markMap[rule.Mark] = struct{}{}
		tableMap[rule.Table] = struct{}{}
	}

	routes, err := netlink.RouteListFiltered(nl.FAMILY_ALL, &netlink.Route{}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return 0, fmt.Errorf("error while getting routes: %w", err)
	}
	for _, route := range routes {
		tableMap[route.Table] = struct{}{}
	}

	for idx = r.startIdx; idx < 0x7ffffffe; idx++ {
		_, tableExists := tableMap[int(idx)]
		_, markExists := markMap[idx]
		if !tableExists && !markExists {
			break
		}
	}

	return idx, nil
}

func (r *IPSetToLink) enable() error {
	if !r.enabled.CompareAndSwap(false, true) {
		return nil
	}
	if r.direct {
		return r.nh.setDirect(r.ipset.ipsetName, directActive)
	}

	var err error
	idx, err := r.getUnusedMarkAndTable()
	if err != nil {
		return err
	}
	r.mark, r.table = idx, int(idx)

	// Install the terminal blackhole route before making this table reachable.
	err = r.insertIPRoute()
	if err != nil {
		return err
	}

	err = r.insertIPRule()
	if err != nil {
		return err
	}

	err = r.insertIPTablesRules(r.nh.IPTables4)
	if err != nil {
		return err
	}

	err = r.insertIPTablesRules(r.nh.IPTables6)
	if err != nil {
		return err
	}

	return nil
}

func (r *IPSetToLink) Enable() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	err := r.enable()
	if err != nil {
		r.disable()
	} else {
		log.Debug().
			Int("table", r.table).
			Int("mark", int(r.mark)).
			Msg("using ip table and mark")
	}

	return err
}

func (r *IPSetToLink) disable() error {
	if !r.enabled.Load() {
		return nil
	}
	if r.direct {
		if err := r.nh.setDirect(r.ipset.ipsetName, directAbsent); err != nil {
			return err
		}
		r.enabled.Store(false)
		return nil
	}
	// Keep policy routes (including blackholes) while packets can still be
	// marked. If unlinking fails, retain them and allow a later cleanup retry.
	if err := errors.Join(r.deleteIPTablesRules(r.nh.IPTables4), r.deleteIPTablesRules(r.nh.IPTables6)); err != nil {
		return err
	}
	if err := r.deleteIPRule(); err != nil {
		return err
	}
	if err := r.deleteIPRoute(); err != nil {
		return err
	}
	r.enabled.Store(false)
	return nil
}

// SetInterface keeps the mark, table and live ipset. During the transition the
// blackhole default remains installed, so there is no fall-through to WAN.
func (r *IPSetToLink) SetInterface(name string) error {
	r.locker.Lock()
	defer r.locker.Unlock()
	if r.ifaceName == name {
		return nil
	}
	if r.direct {
		// A direct set has no table to blackhole. Suspending withdraws its
		// exemption instead, so unverified addresses follow the remaining rule
		// sets rather than bypassing VPN; Direct restores the exemption.
		if name != Blackhole && name != Direct {
			return fmt.Errorf("switching between direct and %s routing requires a new routing link", name)
		}
		if r.enabled.Load() {
			state := directSuspended
			if name == Direct {
				state = directActive
			}
			if err := r.nh.setDirect(r.ipset.ipsetName, state); err != nil {
				return err
			}
		}
		r.ifaceName = name
		return nil
	}
	if name == Direct {
		return fmt.Errorf("switching between %s and direct routing requires a new routing link", r.ifaceName)
	}
	previous := r.ifaceName
	apply := func(name string) error {
		// Remove only the active routes; the fallback must survive a missing,
		// down or IPv4-only replacement VPN.
		for _, routes := range []*[2]*netlink.Route{&r.ip4Route, &r.ip6Route} {
			if routes[1] != nil {
				if err := netlink.RouteDel(routes[1]); err != nil && !errors.Is(err, unix.ESRCH) {
					return err
				}
				routes[1] = nil
			}
		}
		r.ifaceName = name
		if err := r.insertIPRoute(); err != nil {
			return err
		}
		for _, ipt := range []*iptables.IPTables{r.nh.IPTables4, r.nh.IPTables6} {
			if ipt == nil {
				continue
			}
			if err := ipt.RegisterChainOverride("filter", r.chainName); err != nil {
				return err
			}
			if name != Blackhole {
				if err := ipt.Append("filter", r.chainName, "-o", name, "-m", "set", "--match-set", r.ipsetName(ipt), "dst", "-j", "ACCEPT"); err != nil {
					return err
				}
			}
			if err := ipt.Commit(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := apply(name); err != nil {
		return errors.Join(err, apply(previous))
	}
	return nil
}

func (r *IPSetToLink) Disable() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	return r.disable()
}

func (r *IPSetToLink) ClearIfDisabled() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	if r.enabled.Load() {
		return nil
	}

	var errs []error
	errs = append(errs, r.deleteIPRoute())
	errs = append(errs, r.deleteIPRule())
	errs = append(errs, r.deleteIPTablesRules(r.nh.IPTables4))
	errs = append(errs, r.deleteIPTablesRules(r.nh.IPTables6))
	return errors.Join(errs...)
}

func (r *IPSetToLink) LinkUpHook(event netlink.LinkUpdate) error {
	r.locker.Lock()
	defer r.locker.Unlock()

	if !r.enabled.Load() || r.direct || event.Link.Attrs().Name != r.ifaceName {
		return nil
	}

	var errs []error
	errs = append(errs, r.insertIPRoute())
	return errors.Join(errs...)
}

func (r *IPSetToLink) AddrChangeHook(event netlink.AddrUpdate) error {
	r.locker.Lock()
	defer r.locker.Unlock()

	if !r.enabled.Load() || r.direct || r.ifaceName == Blackhole {
		return nil
	}

	iface, err := netlink.LinkByName(r.ifaceName)
	if err != nil {
		return fmt.Errorf("error while getting interface: %w", err)
	}
	if iface.Attrs().Index != event.LinkIndex {
		return nil
	}

	var errs []error
	if r.nh.IPTables4 != nil {
		r.ip4Route[1], err = r.updateIfaceRoute(iface, nl.FAMILY_V4, r.ip4Route[1])
		errs = append(errs, err)
	}
	if r.nh.IPTables6 != nil {
		r.ip6Route[1], err = r.updateIfaceRoute(iface, nl.FAMILY_V6, r.ip6Route[1])
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (nh *Helper) IPSetToLink(name string, ifaceName string, ipset *IPSet, subscription bool) *IPSetToLink {
	routingChain := nh.ChainPrefix + "GROUPS"
	if subscription {
		routingChain = nh.ChainPrefix + "SUBS"
	}
	return &IPSetToLink{
		direct:       ifaceName == Direct,
		routingChain: routingChain,
		nh:           nh,
		chainName:    nh.ChainPrefix + name,
		ifaceName:    ifaceName,
		ipset:        ipset,
		startIdx:     nh.StartIdx,
	}
}
