package netfilterTools

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"magitrickle/utils/iptables"

	"github.com/vishvananda/netlink"
)

type PortRemap struct {
	enabled atomic.Bool
	locker  sync.Mutex

	chainName string
	addresses []netlink.Addr
	from      uint16
	to        uint16
	nh        *Helper
}

func normalizeRemapAddresses(addresses []netlink.Addr) []netlink.Addr {
	byIP := make(map[string]netlink.Addr, len(addresses))
	for _, addr := range addresses {
		if addr.IPNet == nil {
			continue
		}
		ip := addr.IP.To4()
		if ip == nil {
			ip = addr.IP.To16()
		}
		if ip == nil {
			continue
		}
		ip = slices.Clone(ip)
		byIP[ip.String()] = netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(len(ip)*8, len(ip)*8)}}
	}
	next := make([]netlink.Addr, 0, len(byIP))
	for _, addr := range byIP {
		next = append(next, addr)
	}
	slices.SortFunc(next, func(a, b netlink.Addr) int { return slices.Compare(a.IP, b.IP) })
	return next
}

func sameRemapFamily(previous, next []netlink.Addr, proto iptables.Protocol) bool {
	family := func(addresses []netlink.Addr) []string {
		var result []string
		for _, addr := range addresses {
			if (proto == iptables.ProtocolIPv4 && len(addr.IP) == net.IPv4len) || (proto == iptables.ProtocolIPv6 && len(addr.IP) == net.IPv6len) {
				result = append(result, addr.IP.String())
			}
		}
		return result
	}
	return slices.Equal(family(previous), family(next))
}

// Replace live DNS destinations without unlinking PREROUTING. If either family
// fails, restore the previous desired rules so later hook commits cannot publish
// a partially prepared replacement. The event loop retries failed repairs.
func (r *PortRemap) UpdateAddresses(addresses []netlink.Addr) error {
	r.locker.Lock()
	defer r.locker.Unlock()
	previous := r.addresses
	next := normalizeRemapAddresses(addresses)
	var changed []*iptables.IPTables
	for _, ipt := range []*iptables.IPTables{r.nh.IPTables4, r.nh.IPTables6} {
		if ipt != nil && !sameRemapFamily(previous, next, ipt.Proto()) {
			changed = append(changed, ipt)
		}
	}
	r.addresses = next
	if !r.enabled.Load() {
		return nil
	}
	for _, ipt := range changed {
		if err := r.insertIPTablesRules(ipt); err != nil {
			r.addresses = previous
			errs := []error{err}
			for _, rollback := range changed {
				errs = append(errs, r.insertIPTablesRules(rollback))
			}
			return errors.Join(errs...)
		}
	}
	return nil
}

func (r *PortRemap) insertIPTablesRules(ipt *iptables.IPTables) error {
	if ipt == nil {
		return nil
	}

	err := ipt.RegisterChainOverride("nat", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to create chain: %w", err)
	}

	for _, addr := range r.addresses {
		if !((ipt.Proto() == iptables.ProtocolIPv4 && len(addr.IP) == net.IPv4len) || (ipt.Proto() == iptables.ProtocolIPv6 && len(addr.IP) == net.IPv6len)) {
			continue
		}

		for _, iptablesArgs := range [][]string{
			{"-p", "tcp", "-d", addr.IP.String(), "--dport", strconv.Itoa(int(r.from)), "-j", "DNAT", "--to-destination", fmt.Sprintf(":%d", r.to)},
			{"-p", "udp", "-d", addr.IP.String(), "--dport", strconv.Itoa(int(r.from)), "-j", "DNAT", "--to-destination", fmt.Sprintf(":%d", r.to)},
		} {
			err = ipt.Append("nat", r.chainName, iptablesArgs...)
			if err != nil {
				return fmt.Errorf("failed to append rule: %w", err)
			}
		}
	}

	err = ipt.Insert("nat", "PREROUTING", 1, "-j", r.chainName)
	if err != nil {
		return fmt.Errorf("failed to linking chain: %w", err)
	}

	err = ipt.Commit()
	if err != nil {
		return fmt.Errorf("failed to commit iptables rules: %w", err)
	}
	return nil
}

func (r *PortRemap) deleteIPTablesRules(ipt *iptables.IPTables) error {
	if ipt == nil {
		return nil
	}
	var errs []error

	err := ipt.RegisterChainDelete("nat", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to clear chain: %w", err))
	}

	err = ipt.Delete("nat", "PREROUTING", "-j", r.chainName)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to unlinking chain: %w", err))
	}

	err = ipt.Commit()
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to commit iptables rules: %w", err))
	}
	return errors.Join(errs...)
}

func (r *PortRemap) enable() error {
	if !r.enabled.CompareAndSwap(false, true) {
		return nil
	}

	err := r.insertIPTablesRules(r.nh.IPTables4)
	if err != nil {
		return err
	}

	err = r.insertIPTablesRules(r.nh.IPTables6)
	if err != nil {
		return err
	}

	return nil
}

func (r *PortRemap) Enable() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	err := r.enable()
	if err != nil {
		r.disable()
	}

	return err
}

func (r *PortRemap) disable() error {
	if !r.enabled.Load() {
		return nil
	}
	defer r.enabled.Store(false)

	var errs []error
	errs = append(errs, r.deleteIPTablesRules(r.nh.IPTables4))
	errs = append(errs, r.deleteIPTablesRules(r.nh.IPTables6))
	return errors.Join(errs...)
}

func (r *PortRemap) Disable() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	return r.disable()
}

func (nh *Helper) PortRemap(name string, from, to uint16, addr []netlink.Addr) *PortRemap {
	return &PortRemap{
		nh:        nh,
		chainName: nh.ChainPrefix + name,
		addresses: normalizeRemapAddresses(addr),
		from:      from,
		to:        to,
	}
}
