package models

import (
	"net"
	"strconv"
	"strings"
)

func hostPort(address string, port uint16) string {
	// Existing configurations use bracketed IPv6 literals; accept bare ones too.
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		address = address[1 : len(address)-1]
	}
	return net.JoinHostPort(address, strconv.Itoa(int(port)))
}

func (server AppConfigDNSProxyServer) HostPort() string {
	return hostPort(server.Address, server.Port)
}

func (server AppConfigHTTPWebServer) HostPort() string {
	return hostPort(server.Address, server.Port)
}
