package subscriptions

import (
	"net/netip"
	"regexp"
	"strings"
)

var (
	domainCharRe   = regexp.MustCompile(`^[a-zA-Z0-9-.]+$`)
	wildcardCharRe = regexp.MustCompile(`^[a-zA-Z0-9\-.*?]+$`)
)

func isValidWildcard(pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, ".") || strings.HasSuffix(pattern, ".") {
		return false
	}
	if strings.Contains(pattern, "..") || strings.Contains(pattern, "**") {
		return false
	}
	return wildcardCharRe.MatchString(pattern)
}

func isValidDomain(pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, ".") || strings.HasSuffix(pattern, ".") {
		return false
	}
	if strings.Contains(pattern, "..") {
		return false
	}
	return domainCharRe.MatchString(pattern)
}

func isValidNamespace(pattern string) bool {
	return isValidDomain(pattern)
}

func isValidSubnet(pattern string) bool {
	addr, ok := parseSubnetAddress(pattern)
	return ok && addr.Is4()
}

func isValidSubnet6(pattern string) bool {
	addr, ok := parseSubnetAddress(pattern)
	return ok && addr.Is6()
}

func parseSubnetAddress(pattern string) (netip.Addr, bool) {
	if strings.Contains(pattern, "/") {
		prefix, err := netip.ParsePrefix(pattern)
		return prefix.Addr(), err == nil
	}
	addr, err := netip.ParseAddr(pattern)
	return addr, err == nil && addr.Zone() == ""
}
