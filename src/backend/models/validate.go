package models

import (
	"regexp"
	"strings"
)

var (
	groupColorPattern    = regexp.MustCompile(`(?i)^#[0-9a-f]{6}$`)
	interfaceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)
)

// NormalizeGroupColor returns a lower-case #rrggbb color, or white if invalid.
func NormalizeGroupColor(color string) string {
	if !groupColorPattern.MatchString(color) {
		return "#ffffff"
	}
	return strings.ToLower(color)
}

// IsValidInterfaceName accepts an empty name or a safe Linux interface name.
// An absent/down interface is allowed: retain the choice across VPN restarts.
func IsValidInterfaceName(name string) bool {
	return name == "" || (interfaceNamePattern.MatchString(name) && name != "." && name != "..")
}
