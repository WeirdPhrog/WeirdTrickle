package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"

	"magitrickle/models"

	"github.com/rs/zerolog/log"
)

func ValidateFetchInterface(name string) error {
	if !models.IsValidInterfaceName(name) {
		return fmt.Errorf("invalid subscription fetch interface")
	}
	return nil
}

func fetchWithFallback(ctx context.Context, name string, fetch func(context.Context, string) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if name == "" {
		return fetch(ctx, "")
	}
	list, preferredErr := fetch(ctx, name)
	if preferredErr == nil {
		return list, nil
	}
	if errors.Is(preferredErr, errForbiddenFetchAddress) {
		return "", preferredErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Do not include the URL: subscription URLs may contain private access tokens.
	log.Warn().Str("interface", name).Msg("subscription download failed on preferred interface; retrying via system route")
	list, err := fetch(ctx, "")
	if err != nil {
		return "", errors.Join(fmt.Errorf("interface %s: %w", name, preferredErr), fmt.Errorf("system route: %w", err))
	}
	return list, nil
}

func fetchTransport(name string, check fetchAddressPolicy) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: fetchTimeout}
	if name != "" {
		var err error
		dialer, err = interfaceDialer(name)
		if err != nil {
			return nil, err
		}
	}
	control := dialer.Control
	dialer.Control = func(network, address string, conn syscall.RawConn) error {
		if err := check(address); err != nil {
			return err
		}
		if control != nil {
			return control(network, address, conn)
		}
		return nil
	}
	// Proxies could resolve unchecked destinations or access their localhost.
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return transport, nil
}

func interfaceDialer(name string) (*net.Dialer, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	if iface.Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("interface %s is down", name)
	}
	control, err := interfaceControl(name)
	if err != nil {
		return nil, err
	}
	dnsDialer := &net.Dialer{Timeout: fetchTimeout, Control: control}
	localDNSDialer := &net.Dialer{Timeout: fetchTimeout}
	localAddresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			// A router-local DNS proxy must remain reachable locally; it owns
			// routing of its upstream queries. Remote resolvers use the VPN.
			if isLocalResolver(address, localAddresses) {
				return localDNSDialer.DialContext(ctx, network, address)
			}
			return dnsDialer.DialContext(ctx, network, address)
		},
	}
	return &net.Dialer{Timeout: fetchTimeout, Control: control, Resolver: resolver}, nil
}

func isLocalResolver(address string, localAddresses []net.Addr) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip.IsLoopback() {
		return true
	}
	for _, address := range localAddresses {
		if network, ok := address.(*net.IPNet); ok && ip.Equal(network.IP) {
			return true
		}
	}
	return false
}
