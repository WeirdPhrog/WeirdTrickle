package subscriptions

import (
	"errors"
	"net"
	"net/netip"
)

var errForbiddenFetchAddress = errors.New("subscription target address is not allowed")

type fetchAddressPolicy func(string) error

// net.Dialer calls this after resolution, before each connect, including after
// redirects. It retains its IPv4/IPv6 fallback without resolving a checked name
// a second time. Router-local DNS resolvers are handled separately.
func checkFetchAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errForbiddenFetchAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errForbiddenFetchAddress
	}
	local, err := net.InterfaceAddrs()
	if err != nil {
		return errors.New("cannot determine router addresses for subscription download")
	}
	return validateFetchAddress(ip, local)
}

func validateFetchAddress(ip netip.Addr, local []net.Addr) error {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errForbiddenFetchAddress
	}
	if ip.Is4() && (ip.As4()[0] == 0 || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})) {
		return errForbiddenFetchAddress
	}
	for _, addr := range local {
		if prefix, ok := addr.(*net.IPNet); ok && prefix.IP.Equal(net.IP(ip.AsSlice())) {
			return errForbiddenFetchAddress
		}
	}
	// A separate LAN server can legitimately host a subscription.
	return nil
}

// Preserve cancellation/error identity without exposing URLs, redirect headers
// or response bodies through logs and API error messages.
type fetchError struct {
	message string
	cause   error
}

func (e *fetchError) Error() string { return e.message }
func (e *fetchError) Unwrap() error { return e.cause }
