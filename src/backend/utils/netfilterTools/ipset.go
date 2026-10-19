package netfilterTools

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

type IPSetTimeout *uint32

var zeroTimeout = IPSetTimeout(new(uint32))

type IPSet struct {
	enabled atomic.Bool
	locker  sync.Mutex

	ipsetName string
	deadlines map[netip.Prefix]time.Time
	nextSweep time.Time
}

// setName selects the kernel set by address family. A 16-byte address,
// including an IPv4-mapped one, belongs to the IPv6 set.
func (r *IPSet) setName(prefix netip.Prefix) string {
	if prefix.Addr().Is4() {
		return r.ipsetName + "_4"
	}
	return r.ipsetName + "_6"
}

func ipsetEntry(prefix netip.Prefix, timeout IPSetTimeout) *netlink.IPSetEntry {
	return &netlink.IPSetEntry{IP: prefix.Addr().AsSlice(), CIDR: uint8(prefix.Bits()), Timeout: timeout}
}

func (r *IPSet) longerTimeout(prefix netip.Prefix, timeout IPSetTimeout) bool {
	now := time.Now()
	if !now.Before(r.nextSweep) {
		for prefix, deadline := range r.deadlines {
			if !now.Before(deadline) {
				delete(r.deadlines, prefix)
			}
		}
		r.nextSweep = now.Add(time.Minute)
	}
	return timeout != nil && *timeout != 0 && r.deadlines[prefix].After(now.Add(time.Duration(*timeout)*time.Second))
}

func (r *IPSet) rememberTimeout(prefix netip.Prefix, timeout IPSetTimeout, addedAt time.Time) {
	if timeout == nil || *timeout == 0 {
		delete(r.deadlines, prefix)
		return
	}
	if r.deadlines == nil {
		r.deadlines = make(map[netip.Prefix]time.Time)
	}
	r.deadlines[prefix] = addedAt.Add(time.Duration(*timeout) * time.Second)
}

func (r *IPSet) Add(prefix netip.Prefix, timeout IPSetTimeout) error {
	r.locker.Lock()
	defer r.locker.Unlock()

	if !r.enabled.Load() {
		return nil
	}
	if r.longerTimeout(prefix, timeout) {
		return nil
	}

	if timeout == nil {
		timeout = zeroTimeout
	}

	addedAt := time.Now()
	entry := ipsetEntry(prefix, timeout)
	entry.Replace = true
	if err := netlink.IpsetAdd(r.setName(prefix), entry); err != nil {
		return fmt.Errorf("failed to add address: %w", err)
	}

	r.rememberTimeout(prefix, timeout, addedAt)
	return nil
}

func (r *IPSet) Del(prefix netip.Prefix) error {
	r.locker.Lock()
	defer r.locker.Unlock()

	if !r.enabled.Load() {
		return nil
	}

	err := netlink.IpsetDel(r.setName(prefix), ipsetEntry(prefix, nil))
	if err != nil && !errors.Is(err, nl.IPSetError(nl.IPSET_ERR_EXIST)) {
		return fmt.Errorf("failed to delete address: %w", err)
	}

	delete(r.deadlines, prefix)
	return nil
}

// List returns the entries of both address families.
func (r *IPSet) List() (map[netip.Prefix]IPSetTimeout, error) {
	r.locker.Lock()
	defer r.locker.Unlock()

	if !r.enabled.Load() {
		return nil, nil
	}

	addresses := make(map[netip.Prefix]IPSetTimeout)
	for _, family := range []struct {
		suffix string
		length int
	}{{"_4", net.IPv4len}, {"_6", net.IPv6len}} {
		list, err := netlink.IpsetList(r.ipsetName + family.suffix)
		if err != nil {
			return nil, err
		}
		for _, entry := range list.Entries {
			if len(entry.IP) != family.length {
				continue
			}
			addr, _ := netip.AddrFromSlice(entry.IP)
			bits := int(entry.CIDR)
			if bits == 0 {
				bits = addr.BitLen() // CIDR 0 has always denoted a host entry.
			}
			prefix := netip.PrefixFrom(addr, bits)
			if entry.Timeout != nil && *entry.Timeout == 0 {
				addresses[prefix] = nil
			} else {
				addresses[prefix] = entry.Timeout
			}
		}
	}

	return addresses, nil
}

// Replace prepares both families before publishing either. An invalid entry or
// a full set must leave the live routing set intact, not partially emptied.
func (r *IPSet) Replace(entries map[netip.Prefix]IPSetTimeout) error {
	r.locker.Lock()
	defer r.locker.Unlock()
	if !r.enabled.Load() {
		return fmt.Errorf("ipset is disabled")
	}
	count4 := 0
	for prefix := range entries {
		if !prefix.IsValid() {
			return fmt.Errorf("invalid routing prefix %s", prefix)
		}
		if prefix.Addr().Is4() {
			count4++
		}
	}
	tmp4, tmp6 := r.ipsetName+"_t4", r.ipsetName+"_t6"
	preparedAt := time.Now()
	for _, name := range []string{tmp4, tmp6} {
		if err := netlink.IpsetDestroy(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	defer netlink.IpsetDestroy(tmp4)
	defer netlink.IpsetDestroy(tmp6)
	create := func(name string, family uint8, count int) error {
		// maxelem is a limit, not an up-front allocation. Leave room for DNS
		// hosts and allow subscriptions larger than ipset's 65536 default.
		timeout := uint32(300)
		return netlink.IpsetCreate(name, "hash:net", netlink.IpsetCreateOptions{
			Timeout: &timeout, Family: family,
			MaxElements: uint32(max(65536, count+count/4)),
		})
	}
	if err := create(tmp4, unix.AF_INET, count4); err != nil {
		return err
	}
	if err := create(tmp6, unix.AF_INET6, len(entries)-count4); err != nil {
		return err
	}
	for prefix, ttl := range entries {
		if ttl == nil {
			ttl = zeroTimeout
		}
		name, family := tmp6, "IPv6"
		if prefix.Addr().Is4() {
			name, family = tmp4, "IPv4"
		}
		if err := netlink.IpsetAdd(name, ipsetEntry(prefix, ttl)); err != nil {
			return fmt.Errorf("prepare %s set: %w", family, err)
		}
	}
	if err := netlink.IpsetSwap(tmp4, r.ipsetName+"_4"); err != nil {
		return err
	}
	if err := netlink.IpsetSwap(tmp6, r.ipsetName+"_6"); err != nil {
		return errors.Join(err, netlink.IpsetSwap(tmp4, r.ipsetName+"_4"))
	}
	r.deadlines = make(map[netip.Prefix]time.Time)
	for prefix, ttl := range entries {
		if ttl != nil && *ttl != 0 {
			r.deadlines[prefix] = preparedAt.Add(time.Duration(*ttl) * time.Second)
		}
	}
	return nil
}

func (r *IPSet) ipsetCreate() error {
	err := netlink.IpsetCreate(r.ipsetName+"_4", "hash:net", netlink.IpsetCreateOptions{
		Timeout: func(i uint32) *uint32 { return &i }(300),
		Family:  unix.AF_INET,
	})
	if err != nil {
		return fmt.Errorf("failed to create ipset: %w", err)
	}

	err = netlink.IpsetCreate(r.ipsetName+"_6", "hash:net", netlink.IpsetCreateOptions{
		Timeout: func(i uint32) *uint32 { return &i }(300),
		Family:  unix.AF_INET6,
	})
	if err != nil {
		return fmt.Errorf("failed to create ipset: %w", err)
	}

	return nil
}

func (r *IPSet) ipsetDestroy() error {
	var errs []error
	err := netlink.IpsetDestroy(r.ipsetName + "_4")
	if err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	err = netlink.IpsetDestroy(r.ipsetName + "_6")
	if err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	if errs != nil {
		return fmt.Errorf("failed to destroy ipsets: %w", errors.Join(errs...))
	}
	return nil
}

func (r *IPSet) enable() error {
	if !r.enabled.CompareAndSwap(false, true) {
		return nil
	}

	err := r.ipsetDestroy()
	if err != nil {
		return err
	}

	err = r.ipsetCreate()
	if err != nil {
		return err
	}
	r.deadlines = nil

	return nil
}

func (r *IPSet) Enable() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	err := r.enable()
	if err != nil {
		r.disable()
	}

	return err
}

func (r *IPSet) disable() error {
	if !r.enabled.Load() {
		return nil
	}
	if err := r.ipsetDestroy(); err != nil {
		return err
	}
	r.enabled.Store(false)
	r.deadlines = nil
	return nil
}

func (r *IPSet) Disable() error {
	r.locker.Lock()
	defer r.locker.Unlock()

	return r.disable()
}

func (nh *Helper) IPSet(name string) *IPSet {
	return &IPSet{
		ipsetName: nh.IpsetPrefix + name,
	}
}
