//go:build integration && linux

package subscriptions

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// CI runs this in a disposable network namespace as root. Two real paths reach
// the same server: the preferred interface must override the lower-metric WAN.
func TestRealSubscriptionFetch(t *testing.T) {
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	ns := fmt.Sprintf("mt-fetch-%d", os.Getpid())
	run("netns", "add", ns)
	defer run("netns", "del", ns)
	run("link", "set", "lo", "up")
	run("-n", ns, "link", "set", "lo", "up")
	for i, name := range []string{"mt-wan", "mt-vpn"} {
		peer := name + "-p"
		run("link", "add", name, "type", "veth", "peer", "name", peer)
		run("link", "set", peer, "netns", ns)
		run("addr", "add", fmt.Sprintf("198.18.%d.1/30", i+1), "dev", name)
		run("-n", ns, "addr", "add", fmt.Sprintf("198.18.%d.2/30", i+1), "dev", peer)
		run("-6", "addr", "add", fmt.Sprintf("2001:db8:%d::1/64", i+1), "dev", name, "nodad")
		run("-n", ns, "-6", "addr", "add", fmt.Sprintf("2001:db8:%d::2/64", i+1), "dev", peer, "nodad")
		run("link", "set", name, "up")
		run("-n", ns, "link", "set", peer, "up")
		run("route", "add", "default", "via", fmt.Sprintf("198.18.%d.2", i+1), "dev", name, "metric", fmt.Sprint(10+i*100))
		run("-6", "route", "add", "default", "via", fmt.Sprintf("2001:db8:%d::2", i+1), "dev", name, "metric", fmt.Sprint(10+i*100))
	}
	run("-n", ns, "addr", "add", "198.19.0.1/32", "dev", "lo")
	run("-n", ns, "-6", "addr", "add", "2001:db8:99::1/128", "dev", "lo", "nodad")
	// Reverse-path filtering is unrelated to application socket binding.
	for _, cmd := range []*exec.Cmd{
		exec.Command("sysctl", "-qw", "net.ipv4.conf.all.rp_filter=0", "net.ipv4.conf.mt-wan.rp_filter=0", "net.ipv4.conf.mt-vpn.rp_filter=0"),
		exec.Command("ip", "netns", "exec", ns, "sysctl", "-qw", "net.ipv4.conf.all.rp_filter=0", "net.ipv4.conf.mt-wan-p.rp_filter=0", "net.ipv4.conf.mt-vpn-p.rp_filter=0"),
	} {
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sysctl: %v %s", err, out)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	server := exec.CommandContext(ctx, "ip", "netns", "exec", ns, os.Args[0], "-test.run", "^TestSubscriptionFetchServer$")
	server.Env = append(os.Environ(), "MT_FETCH_TEST_SERVER=1")
	server.Stderr = os.Stderr
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	ready := make(chan string, 1)
	go func() { scanner := bufio.NewScanner(stdout); scanner.Scan(); ready <- scanner.Text() }()
	select {
	case line := <-ready:
		if line != "ready" {
			t.Fatalf("server did not start: %s", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server startup timed out")
	}
	for _, family := range []struct{ host, wan, vpn string }{
		{"198.19.0.1", "198.18.1.1", "198.18.2.1"},
		{"[2001:db8:99::1]", "2001:db8:1::1", "2001:db8:2::1"},
	} {
		url := "http://" + family.host + ":18080/redirect"
		for _, path := range []struct{ name, source string }{{"", family.wan}, {"mt-vpn", family.vpn}} {
			body, err := fetchListAttemptWithPolicy(ctx, url, path.name, checkFetchAddress)
			if err != nil || body != path.source {
				t.Fatalf("%s via %q: body=%q err=%v", url, path.name, body, err)
			}
		}
	}
	// Exercise the production resolver's remote DNS dial path. Only redirect the
	// configured resolver address into this test namespace; never touch host DNS.
	dialer, err := interfaceDialer("mt-vpn")
	if err != nil {
		t.Fatal(err)
	}
	dnsDial := dialer.Resolver.Dial
	dialer.Resolver.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dnsDial(ctx, network, "198.19.0.1:53")
	}
	transport := &http.Transport{DialContext: dialer.DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, err := client.Get("http://subscription.test:18080/redirect")
	if err != nil {
		t.Fatal("DNS over selected interface:", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || (string(body) != "198.18.2.1" && string(body) != "2001:db8:2::1") {
		t.Fatalf("DNS fetch: %s %v", body, err)
	}
	// TCP DNS must use the same binding (e.g. after a truncated UDP response).
	conn, err := dnsDial(ctx, "tcp", "198.19.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(conn.LocalAddr().String(), "198.18.2.1:") {
		t.Fatal("TCP DNS escaped preferred interface")
	}
	conn.Close()
	run("link", "set", "mt-vpn", "down")
	list, err := FetchListWithInterface(ctx, "http://198.19.0.1:18080/redirect", "mt-vpn")
	if err != nil || list != "198.18.1.1" {
		t.Fatalf("down VPN did not fall back to WAN: %q %v", list, err)
	}
}

func TestSubscriptionFetchServer(t *testing.T) {
	if os.Getenv("MT_FETCH_TEST_SERVER") != "1" {
		t.Skip("helper process")
	}
	listener, err := net.Listen("tcp", "[::]:18080")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/list", http.StatusTemporaryRedirect)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		_, _ = io.WriteString(w, host)
	}))
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		if !strings.HasPrefix(w.RemoteAddr().String(), "198.18.2.1:") {
			response.Rcode = dns.RcodeRefused
		} else {
			for _, q := range request.Question {
				if q.Qtype == dns.TypeA {
					response.Answer = append(response.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("198.19.0.1")})
				}
				if q.Qtype == dns.TypeAAAA {
					response.Answer = append(response.Answer, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("2001:db8:99::1")})
				}
			}
		}
		_ = w.WriteMsg(response)
	})
	udp, err := net.ListenPacket("udp", "198.19.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "198.19.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	go (&dns.Server{PacketConn: udp, Handler: handler}).ActivateAndServe()
	go (&dns.Server{Listener: tcp, Handler: handler}).ActivateAndServe()
	fmt.Println("ready")
	select {}
}
