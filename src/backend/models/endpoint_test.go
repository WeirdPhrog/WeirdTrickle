package models

import (
	"net"
	"testing"
)

func TestConfiguredEndpointsSupportBareAndBracketedIPv6(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::", "[::]", "::1", "[::1]", "fe80::1%br0", "[fe80::1%br0]", ""} {
		t.Run(address, func(t *testing.T) {
			dnsAddress := (AppConfigDNSProxyServer{Address: address, Port: 3553}).HostPort()
			if _, err := net.ResolveUDPAddr("udp", dnsAddress); err != nil {
				t.Fatalf("invalid DNS endpoint %q: %v", dnsAddress, err)
			}
			if _, err := net.ResolveTCPAddr("tcp", dnsAddress); err != nil {
				t.Fatal(err)
			}
			httpAddress := (AppConfigHTTPWebServer{Address: address, Port: 8080}).HostPort()
			if _, err := net.ResolveTCPAddr("tcp", httpAddress); err != nil {
				t.Fatalf("invalid HTTP endpoint %q: %v", httpAddress, err)
			}
		})
	}
	for _, address := range []string{"router.local", "127.0.0.1", "::1", "[::1]"} {
		server := AppConfigDNSProxyServer{Address: address, Port: 53}
		host, port, err := net.SplitHostPort(server.HostPort())
		if err != nil || port != "53" || (host != address && "["+host+"]" != address) {
			t.Fatalf("upstream endpoint changed its host/port: %q, %v", server.HostPort(), err)
		}
	}
}
