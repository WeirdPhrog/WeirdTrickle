package models

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
)

// Configured hosts contain a name or IP, without a scheme, path, port or wildcard.
func NormalizeHTTPHost(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("invalid HTTP allowed host")
	}
	bracketed := strings.HasPrefix(value, "[") || strings.HasSuffix(value, "]")
	if bracketed {
		if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
			return "", fmt.Errorf("invalid HTTP allowed host")
		}
		value = value[1 : len(value)-1]
	}
	if ip, err := netip.ParseAddr(value); err == nil {
		return ip.Unmap().WithZone("").String(), nil
	}
	if bracketed {
		return "", fmt.Errorf("invalid HTTP allowed host")
	}
	value, err := idna.Lookup.ToASCII(strings.TrimSuffix(value, "."))
	if err != nil || len(value) == 0 || len(value) > 253 {
		return "", fmt.Errorf("invalid HTTP allowed host")
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid HTTP allowed host")
		}
		for _, c := range strings.ToLower(label) {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("invalid HTTP allowed host")
			}
		}
	}
	return strings.ToLower(value), nil
}
