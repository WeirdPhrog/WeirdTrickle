package magitrickle

import (
	"errors"
	"fmt"
	"net"
	"slices"

	"magitrickle/constant"

	"github.com/rs/zerolog/log"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// This is a request: SO_RCVBUF is still capped by net.core.rmem_max.
// Receive errors are handled by reconnecting and resynchronizing state.
const netlinkReceiveBufferSize = 1 << 20

func subscribeLinkUpdates() (chan netlink.LinkUpdate, chan struct{}, error) {
	linkUpdateChannel := make(chan netlink.LinkUpdate, 64)
	done := make(chan struct{})
	if err := netlink.LinkSubscribeWithOptions(linkUpdateChannel, done, netlink.LinkSubscribeOptions{
		ReceiveBufferSize: netlinkReceiveBufferSize,
		ErrorCallback: func(err error) {
			select {
			case <-done:
				return
			default:
				log.Warn().Err(err).Msg("netlink link receive error")
			}
		},
	}); err != nil {
		close(done)
		return nil, nil, fmt.Errorf("failed to subscribe to link updates: %w", err)
	}
	return linkUpdateChannel, done, nil
}

func subscribeAddrUpdates() (chan netlink.AddrUpdate, chan struct{}, error) {
	addrUpdateChannel := make(chan netlink.AddrUpdate, 64)
	done := make(chan struct{})
	if err := netlink.AddrSubscribeWithOptions(addrUpdateChannel, done, netlink.AddrSubscribeOptions{
		ReceiveBufferSize: netlinkReceiveBufferSize,
		ErrorCallback: func(err error) {
			select {
			case <-done:
				return
			default:
				log.Warn().Err(err).Msg("netlink address receive error")
			}
		},
	}); err != nil {
		close(done)
		return nil, nil, fmt.Errorf("failed to subscribe to addr updates: %w", err)
	}
	return addrUpdateChannel, done, nil
}

// handleLink обрабатывает события изменения состояния сетевых интерфейсов
func (a *App) handleLink(event netlink.LinkUpdate) error {
	if event.Link == nil {
		return fmt.Errorf("netlink link update has no interface")
	}
	var errs []error
	switch event.Header.Type {
	case unix.RTM_NEWLINK:
		linkAttrs := event.Link.Attrs()
		if linkAttrs.Flags&net.FlagUp == 0 {
			break
		}

		ifaceName := linkAttrs.Name
		if !slices.Contains(constant.IgnoredInterfaces, ifaceName) {
			log.Debug().
				Str("interface", ifaceName).
				Int("type", int(event.Header.Type)).
				Msg("interface up")
		}
		for _, group := range a.ruleSetSnapshot() {
			if group.RouteInterface() != ifaceName {
				continue
			}

			if err := group.LinkUpHook(event); err != nil {
				errs = append(errs, err)
				log.Error().
					Err(err).
					Str("group", group.IDValue().String()).
					Msg("error while handling interface up")
			}
		}

	case unix.RTM_DELLINK:
		log.Debug().
			Str("interface", event.Link.Attrs().Name).
			Int("type", int(event.Header.Type)).
			Msg("interface del")
	}
	return errors.Join(errs...)
}

// handleAddr обрабатывает события изменения IP-адресов сетевых интерфейсов
func (a *App) handleAddr(event netlink.AddrUpdate) error {
	if !event.NewAddr {
		return nil
	}

	iface, err := netlink.LinkByIndex(event.LinkIndex)
	if err != nil {
		log.Error().Err(err).Int("linkIndex", event.LinkIndex).Msg("failed to get interface for addr update")
		return err
	}

	ifaceName := iface.Attrs().Name
	if !slices.Contains(constant.IgnoredInterfaces, ifaceName) {
		log.Debug().
			Str("interface", ifaceName).
			Str("addr", event.LinkAddress.String()).
			Msg("interface address changed")
	}

	var errs []error
	for _, group := range a.ruleSetSnapshot() {
		if group.RouteInterface() != ifaceName {
			continue
		}

		if err := group.AddrChangeHook(event); err != nil {
			errs = append(errs, err)
			log.Error().
				Err(err).
				Str("group", group.IDValue().String()).
				Msg("error while handling interface addr change")
		}
	}
	return errors.Join(errs...)
}

func (a *App) lanAddresses(allowMissing bool) ([]netlink.Addr, error) {
	var addresses []netlink.Addr
	for _, name := range a.Config().Link {
		link, err := netlink.LinkByName(name)
		if err != nil {
			var missing netlink.LinkNotFoundError
			if allowMissing && errors.As(err, &missing) {
				continue
			}
			return nil, fmt.Errorf("failed to find LAN interface: %w", err)
		}
		addrs, err := netlink.AddrList(link, nl.FAMILY_ALL)
		if err != nil {
			return nil, fmt.Errorf("failed to list LAN addresses: %w", err)
		}
		addresses = append(addresses, addrs...)
	}
	return addresses, nil
}

func (a *App) refreshDNSRemap() error {
	if a.dnsOverrider == nil {
		return nil
	}
	addresses, err := a.lanAddresses(true)
	if err != nil {
		return err
	}
	return a.dnsOverrider.UpdateAddresses(addresses)
}

// Rebuild routes from current interface state after losing events. Do not
// rebuild healthy ipsets or release groups suspended by an unfinished rollback.
func (a *App) resyncNetworkState() error {
	errs := []error{a.refreshDNSRemap()}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	for _, group := range a.ruleSetSnapshotLocked() {
		if !group.Enabled() || !group.ConfiguredEnabled() || group.restoreState.Load() != nil || group.RouteInterface() == "blackhole" {
			continue
		}
		link, err := netlink.LinkByName(group.RouteInterface())
		if err != nil {
			var missing netlink.LinkNotFoundError
			if !errors.As(err, &missing) {
				errs = append(errs, err)
			}
			continue
		}
		if link.Attrs().Flags&net.FlagUp != 0 {
			errs = append(errs, group.LinkUpHook(netlink.LinkUpdate{Link: link}))
		}
	}
	return errors.Join(errs...)
}
