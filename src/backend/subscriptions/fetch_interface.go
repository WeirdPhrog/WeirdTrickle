package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"

	"github.com/rs/zerolog/log"
)

var fetchInterfacePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

func ValidateFetchInterface(name string) error {
	// An absent/down interface is allowed: retain the choice across VPN restarts.
	if name != "" && (!fetchInterfacePattern.MatchString(name) || name == "." || name == "..") {
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

func fetchTransport(name string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if name == "" {
		return transport, nil
	}
	dialer, err := interfaceDialer(name)
	if err != nil {
		return nil, err
	}
	transport.Proxy = nil // An environment proxy must not bypass the selected path.
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
